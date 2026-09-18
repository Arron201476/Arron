package revision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

const maxSidecarRevisionResponseBytes = 8 << 20

// SidecarService keeps revision transactions in Go while delegating every
// model and patch decision to an OpenAI Agents SDK runner.
type SidecarService struct {
	store   *businessruntime.Store
	baseURL string
	token   string
	client  *http.Client
}

func NewSidecar(
	store *businessruntime.Store,
	baseURL string,
	token string,
	timeout time.Duration,
) (*SidecarService, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("revision sidecar URL must be absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("revision sidecar URL must use HTTP or HTTPS")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("revision sidecar token is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("revision sidecar timeout must be positive")
	}
	return &SidecarService{
		store: store, baseURL: strings.TrimRight(parsed.String(), "/"),
		token: token, client: &http.Client{Timeout: timeout},
	}, nil
}

func (s *SidecarService) Available() bool {
	return s != nil && s.store != nil && s.client != nil
}

func (s *SidecarService) Run(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	if !s.Available() {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.poll(ctx, logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *SidecarService) poll(ctx context.Context, logger *slog.Logger) {
	if err := s.store.RecoverExpiredRevisionAttempts(ctx, s.client.Timeout+30*time.Second); err != nil {
		logger.Error("recover expired SDK revision attempts", "error", err)
		return
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		logger.Error("list projects for SDK revision worker", "error", err)
		return
	}
	for _, project := range projects {
		requests, listErr := s.store.ListRevisionRequests(ctx, project.ProjectID)
		if listErr != nil {
			logger.Error("list SDK revision requests", "project_id", project.ProjectID, "error", listErr)
			continue
		}
		for _, request := range requests {
			if request.Status != "queued" && request.Status != "waiting_safe_checkpoint" {
				continue
			}
			if _, executeErr := s.Execute(ctx, request.RevisionRequestID); executeErr != nil {
				var domain *businessruntime.DomainError
				if errors.As(executeErr, &domain) && (domain.Code == "REVISION_WAITING_SAFE_CHECKPOINT" || domain.Code == "REVISION_QUEUE_ORDER_CONFLICT") {
					break
				}
				logger.Error("execute SDK revision request", "revision_request_id", request.RevisionRequestID, "error", executeErr)
				if _, failErr := s.store.FailUnauthorizedQueuedRevision(ctx, request.RevisionRequestID, request.Version); failErr != nil {
					logger.Error("finalize unauthorized revision", "revision_request_id", request.RevisionRequestID, "error", failErr)
				}
			}
			break
		}
	}
}

func (s *SidecarService) Execute(ctx context.Context, requestID string) (result businessruntime.RevisionRequest, err error) {
	if !s.Available() {
		return businessruntime.RevisionRequest{}, fmt.Errorf("REVISION_ADAPTER_UNAVAILABLE")
	}
	request, err := s.store.GetRevisionRequest(ctx, requestID)
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	if (request.Status != "queued" && request.Status != "waiting_safe_checkpoint") || request.BaseVersionID == nil {
		return request, nil
	}
	principal, err := s.store.RevisionExecutionPrincipal(ctx, requestID)
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	if _, active := businessruntime.AgentActivityFromContext(ctx); !active {
		ctx = identity.WithPrincipal(ctx, principal)
	}
	resolution, err := s.store.GetTargetResolution(ctx, request.TargetResolutionID)
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	base, err := s.store.GetArtifactVersion(ctx, *request.BaseVersionID)
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	var artifactPayload map[string]any
	if err := json.Unmarshal(base.Payload, &artifactPayload); err != nil {
		return businessruntime.RevisionRequest{}, fmt.Errorf("decode base Artifact: %w", err)
	}
	contextPayload, err := json.Marshal(map[string]any{
		"pack_type": "sdk_revision", "pack_version": "1.0.0",
		"revision_request_id": request.RevisionRequestID,
		"instruction":         request.Instruction, "operation": request.Operation,
		"target": resolution, "base_artifact_version_id": base.ArtifactVersionID,
		"artifact_schema":  map[string]string{"schema_id": base.SchemaID, "schema_version": base.SchemaVersion},
		"artifact_payload": artifactPayload,
	})
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	attempt, err := s.store.BeginRevisionAttempt(ctx, businessruntime.BeginRevisionAttemptCommand{
		RevisionRequestID: request.RevisionRequestID,
		ExpectedVersion:   request.Version,
		ContextPayload:    contextPayload,
		ContextHash:       fmt.Sprintf("%x", sha256.Sum256(contextPayload)),
		AdapterID:         "openai_agents_sdk_apply_patch",
		AdapterVersion:    "1.0.0",
	})
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	defer func() {
		if err != nil {
			failureCode := "SDK_REVISION_EXECUTION_FAILED"
			var domain *businessruntime.DomainError
			if errors.As(err, &domain) {
				switch domain.Code {
				case "REVISION_EXECUTION_OWNER_REQUIRED", "REVISION_EXECUTION_OWNER_INVALID", "AUTHENTICATION_REQUIRED", "WORKSPACE_ACCESS_DENIED", "ROLE_FORBIDDEN", "PROJECT_NOT_FOUND", "IDENTITY_INVALID":
					failureCode = domain.Code
				}
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if cleanupErr := s.store.FailRevisionAttempt(cleanup, businessruntime.FailRevisionAttemptCommand{
				RevisionRequestID: request.RevisionRequestID,
				RevisionAttemptID: attempt.RevisionAttemptID,
				FailureCode:       failureCode,
			}); cleanupErr != nil {
				err = fmt.Errorf("%w; finalize revision failure: %v", err, cleanupErr)
			}
		}
	}()

	payload := map[string]any{
		"revision_request_id": request.RevisionRequestID,
		"revision_attempt_id": attempt.RevisionAttemptID,
		"instruction":         request.Instruction,
		"target":              resolution,
		"artifact_schema":     map[string]string{"schema_id": base.SchemaID, "schema_version": base.SchemaVersion},
		"artifact_payload":    artifactPayload,
	}
	var frozenContext struct {
		ArtifactValidation *businessruntime.ArtifactEditContract `json:"artifact_validation"`
	}
	if err := json.Unmarshal(attempt.ContextPayload, &frozenContext); err != nil {
		return businessruntime.RevisionRequest{}, fmt.Errorf("decode frozen revision contract: %w", err)
	}
	if frozenContext.ArtifactValidation != nil {
		payload["artifact_validation"] = frozenContext.ArtifactValidation
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/internal/v1/revisions/execute", bytes.NewReader(encoded))
	if err != nil {
		return businessruntime.RevisionRequest{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+s.token)
	response, err := s.client.Do(httpRequest)
	if err != nil {
		return businessruntime.RevisionRequest{}, fmt.Errorf("call SDK revision sidecar: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxSidecarRevisionResponseBytes))
		return businessruntime.RevisionRequest{}, fmt.Errorf("SDK revision sidecar returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var output struct {
		Outcome         string          `json:"outcome"`
		ProposalPayload json.RawMessage `json:"proposal_payload"`
		ProposalSummary string          `json:"proposal_summary"`
		ProviderID      string          `json:"provider_id"`
		TraceRef        string          `json:"trace_ref"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxSidecarRevisionResponseBytes)).Decode(&output); err != nil {
		return businessruntime.RevisionRequest{}, fmt.Errorf("decode SDK revision response: %w", err)
	}
	if output.Outcome == "no_change" {
		result, err = s.store.CompleteRevisionNoChange(ctx, businessruntime.CompleteRevisionNoChangeCommand{
			RevisionRequestID: request.RevisionRequestID,
			RevisionAttemptID: attempt.RevisionAttemptID,
			Summary:           output.ProposalSummary,
			ProviderID:        output.ProviderID,
			TraceRef:          output.TraceRef,
		})
		return result, err
	}
	result, err = s.store.CompleteRevisionAttempt(ctx, businessruntime.CompleteRevisionAttemptCommand{
		RevisionRequestID: request.RevisionRequestID,
		RevisionAttemptID: attempt.RevisionAttemptID,
		ProposalPayload:   output.ProposalPayload,
		ProposalSummary:   output.ProposalSummary,
		ProviderID:        output.ProviderID,
		TraceRef:          output.TraceRef,
	})
	return result, err
}
