package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

type Server struct {
	shell                 *shell.Core
	runtime               *businessruntime.Store
	revisions             RevisionExecutor
	agentTools            *agenttool.Registry
	logger                *slog.Logger
	mux                   *http.ServeMux
	agentTurns            *agentTurnManager
	agentRollout          AgentRolloutPolicy
	auth                  Authenticator
	sessionMu             sync.Mutex
	sessions              map[string]browserSession
	now                   func() time.Time
	nativeWorkspaceEngine businessruntime.NativeWorkspaceEngine
}

type RevisionExecutor interface {
	Execute(context.Context, string) (businessruntime.RevisionRequest, error)
}

func New(core *shell.Core, logger *slog.Logger) *Server {
	return NewWithRuntime(core, nil, logger)
}

func NewWithRuntime(core *shell.Core, runtimeStore *businessruntime.Store, logger *slog.Logger) *Server {
	return NewWithRuntimeAndRevision(core, runtimeStore, nil, logger)
}

func NewWithRuntimeAndRevision(core *shell.Core, runtimeStore *businessruntime.Store, revisions RevisionExecutor, logger *slog.Logger) *Server {
	return NewWithRuntimeRevisionAndTools(core, runtimeStore, revisions, nil, logger)
}

func NewWithRuntimeRevisionAndTools(
	core *shell.Core,
	runtimeStore *businessruntime.Store,
	revisions RevisionExecutor,
	agentTools *agenttool.Registry,
	logger *slog.Logger,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if runtimeStore != nil && agentTools != nil {
		runtimeStore.SetAgentToolRegistry(agentTools)
	}
	server := &Server{
		shell:        core,
		runtime:      runtimeStore,
		revisions:    revisions,
		agentTools:   agentTools,
		agentRollout: defaultAgentRolloutPolicy(),
		logger:       logger,
		mux:          http.NewServeMux(),
		auth:         NewLocalAuthenticator(identity.DefaultLocalPrincipal()),
		sessions:     map[string]browserSession{},
		now:          func() time.Time { return time.Now().UTC() },
	}
	server.routes()
	return server
}

func (s *Server) ConfigureAgentRollout(policy AgentRolloutPolicy) {
	s.agentRollout = policy
}

func (s *Server) Handler() http.Handler {
	return s.securityMiddleware(s.mux)
}

func (s *Server) ConfigureAuthentication(ctx context.Context, authenticator Authenticator) error {
	if authenticator == nil {
		return errors.New("authenticator is required")
	}
	if s.runtime != nil {
		for _, principal := range authenticator.Principals() {
			if err := s.runtime.BootstrapPrincipal(ctx, principal); err != nil {
				return err
			}
		}
	}
	s.auth = authenticator
	return nil
}

// StartAgentTurnManager enables the durable asynchronous message contract.
// Constructors keep it explicit so isolated handler tests can opt into the
// background lifecycle they exercise.
func (s *Server) StartAgentTurnManager(ctx context.Context) error {
	if s.runtime == nil || s.agentTurns != nil {
		return nil
	}
	if err := s.runtime.RecoverInterruptedAgentTurns(ctx); err != nil {
		return err
	}
	s.agentTurns = newAgentTurnManager(s.runtime, s.shell, s.logger)
	go s.agentTurns.run(ctx)
	return nil
}

func (s *Server) WaitAgentTurnManager(ctx context.Context) error {
	if s.agentTurns == nil {
		return nil
	}
	select {
	case <-s.agentTurns.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("POST /api/v1/auth/session", s.createAuthSession)
	s.mux.HandleFunc("DELETE /api/v1/auth/session", s.deleteAuthSession)
	s.mux.HandleFunc("GET /api/v1/auth/me", s.getCurrentPrincipal)
	s.mux.HandleFunc("GET /api/v1/workspace-projection", s.getWorkspaceProjection)
	s.mux.HandleFunc("/api/v1/capabilities", s.handleCapabilities)
	s.mux.HandleFunc("/api/v1/capabilities/", s.handleCapability)
	if s.agentTools != nil {
		s.mux.HandleFunc("GET /api/v1/agent-tools", s.listAgentTools)
		s.mux.HandleFunc("GET /internal/v1/agent-tools/catalog", s.getInternalAgentToolCatalog)
		if s.runtime != nil {
			s.mux.HandleFunc("GET /api/v1/agent-tools/configuration", s.getAgentToolConfiguration)
			s.mux.HandleFunc("PATCH /api/v1/agent-tools/configuration", s.updateAgentToolConfiguration)
			s.mux.HandleFunc("GET /api/v1/mcp-connections", s.listMCPConnections)
			s.mux.HandleFunc("PUT /api/v1/mcp-connections", s.updateMCPConnection)
			s.mux.HandleFunc("POST /internal/v1/mcp-connections/resolve", s.resolveMCPCredentials)
		}
	}
	if s.runtime != nil {
		s.nativeWorkspaceRoutes()
		s.mux.HandleFunc("GET /api/v1/agent-instructions", s.getAgentInstructions)
		s.mux.HandleFunc("GET /api/v1/projects/{project_id}/memory", s.getAgentMemory)
		s.mux.HandleFunc("PUT /api/v1/projects/{project_id}/memory", s.updateAgentMemory)
		s.mux.HandleFunc("GET /api/v1/projects/{project_id}/memory/generations/{generation_id}", s.getAgentMemoryGeneration)
		s.mux.HandleFunc("GET /api/v1/projects/{project_id}/memory/generations", s.listAgentMemoryGenerations)
		s.mux.HandleFunc("POST /api/v1/projects/{project_id}/memory/generations", s.queueAgentMemoryGeneration)
		s.mux.HandleFunc("GET /api/v1/projects/{project_id}/memory/sources", s.listAgentMemorySources)
		s.mux.HandleFunc("GET /api/v1/projects/{project_id}/memory/preferences", s.getAgentMemoryPreferences)
		s.mux.HandleFunc("PUT /api/v1/projects/{project_id}/memory/preferences", s.updateAgentMemoryPreferences)
		s.mux.HandleFunc("POST /api/v1/projects/{project_id}/memory/generations/{generation_id}/control", s.controlAgentMemoryGeneration)
		s.mux.HandleFunc("PUT /api/v1/agent-instructions", s.updateAgentInstructions)
		s.mux.HandleFunc("POST /internal/v1/agent-instructions/snapshot", s.resolveAgentInstructionSnapshot)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/snapshot", s.resolveAgentMemorySnapshot)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/rollouts", s.saveAgentMemoryRollout)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/archive-policy", s.resolveAgentMemoryArchivePolicy)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/archive", s.archiveAgentMemoryRollout)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/archive-receipt", s.readAgentMemoryArchiveReceipt)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/archive-recover", s.recoverAgentMemoryArchive)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/rollouts/read", s.readAgentMemoryRollout)
		s.mux.HandleFunc("POST /internal/v1/agent-memory/generations/{operation}", s.memoryGenerationWorker)
		s.mux.HandleFunc("GET /api/v1/agent-tool-calls/{agent_tool_call_id}/instruction-proposal", s.getAgentInstructionProposal)
		s.mux.HandleFunc("GET /api/v1/agent-tool-calls/{agent_tool_call_id}/memory-proposal", s.getAgentMemoryProposal)
		s.mux.HandleFunc("GET /api/v1/agent-tool-calls/{agent_tool_call_id}/memory-tool-proposal", s.getAgentMemoryToolProposal)
		s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/instructions", s.applyAgentInstructionTool)
		s.runtimeRoutes()
	}
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "仅支持 GET。")
		return
	}
	readiness := s.shell.Readiness()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok",
		"data": map[string]any{
			"agent_core_available":       readiness.AgentCoreAvailable,
			"generic_chat_available":     readiness.GenericChatAvailable,
			"domain_capability_count":    readiness.DomainCapabilityCount,
			"available_capability_count": readiness.AvailableCapabilityCount,
			"runtime_available":          s.runtime != nil,
			"agent_rollout":              s.agentRollout.Status(),
		},
	})
}

func (s *Server) handleCapabilities(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/v1/capabilities" {
		writeError(writer, http.StatusNotFound, "RESOURCE_NOT_FOUND", "请求的资源不存在。")
		return
	}
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "仅支持 GET。")
		return
	}
	registry, err := s.registryForRequest(request.Context(), "")
	if err != nil {
		writeRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"data": map[string]any{
			"items":       registry.PublicList(),
			"diagnostics": registry.SkillDiagnostics(),
		},
	})
}

func (s *Server) handleCapability(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "仅支持 GET。")
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/capabilities/")
	if id == "" || strings.Contains(id, "/") {
		writeError(writer, http.StatusNotFound, "RESOURCE_NOT_FOUND", "请求的能力不存在。")
		return
	}
	registry, err := s.registryForRequest(request.Context(), "")
	if err != nil {
		writeRuntimeError(writer, err)
		return
	}
	definition, ok, err := registry.PublicDefinitionWithSchemas(id)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "CAPABILITY_SCHEMA_UNAVAILABLE", "能力配置 Schema 当前不可用。")
		return
	}
	if !ok {
		writeError(writer, http.StatusNotFound, "CAPABILITY_NOT_FOUND", "请求的能力不存在。")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": definition})
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func writeRuntimeError(writer http.ResponseWriter, err error) {
	var domain *businessruntime.DomainError
	if !errors.As(err, &domain) {
		writeError(writer, http.StatusInternalServerError, "INTERNAL_ERROR", "服务内部错误。")
		return
	}
	status := http.StatusBadRequest
	switch {
	case domain.Code == "AUTHENTICATION_REQUIRED":
		status = http.StatusUnauthorized
	case strings.HasSuffix(domain.Code, "_NOT_FOUND"):
		status = http.StatusNotFound
	case strings.Contains(domain.Code, "CONFLICT"),
		domain.Code == "AGENT_MEMORY_ARCHIVE_NOT_READY",
		domain.Code == "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED",
		domain.Code == "REVISION_EXECUTION_OWNER_REQUIRED",
		domain.Code == "REVISION_EXECUTION_OWNER_INVALID",
		domain.Code == "WORKFLOW_TRANSITION_UNMATCHED",
		domain.Code == "WORKFLOW_TRANSITION_AMBIGUOUS",
		domain.Code == "WORKFLOW_FAILURE_TRANSITION_CHANGED",
		domain.Code == "WORKFLOW_FAILURE_NOT_SETTLED",
		domain.Code == "WORKFLOW_FAILURE_REQUIRES_REVIEW",
		domain.Code == "SKILL_VERSION_IMMUTABLE",
		domain.Code == "SKILL_DISCOVERY_CHANGED",
		domain.Code == "SKILL_DRAFT_CHANGED",
		domain.Code == "SKILL_INSTALL_ALREADY_COMMITTED",
		domain.Code == "AGENT_TOOL_CONFIGURATION_CHANGED",
		domain.Code == "EXECUTION_CONTROL_STALE",
		domain.Code == "EXECUTION_CONTROL_UNAVAILABLE",
		domain.Code == "SDK_TOOL_REPLAY_RISK",
		domain.Code == "SDK_TOOL_OUTCOME_UNRESOLVED",
		domain.Code == "AGENT_TOOL_OUTCOME_REVIEW_CONFLICT",
		domain.Code == "AGENT_TOOL_OUTCOME_REVIEW_CORRUPT",
		domain.Code == "SKILL_ID_CHANGED",
		domain.Code == "SKILL_UPGRADE_ID_MISMATCH",
		domain.Code == "SKILL_CAPABILITY_COLLISION",
		domain.Code == "APPROVAL_ALREADY_RESOLVED",
		domain.Code == "APPROVAL_SUBJECT_CHANGED",
		domain.Code == "IMPACT_REVIEW_REQUIRED",
		domain.Code == "IMPACT_REVIEW_EXPIRED",
		domain.Code == "IMPACT_REVIEW_CONFLICT",
		domain.Code == "REGENERATION_PLAN_CONFLICT",
		domain.Code == "FINAL_SELECTION_PREVIEW_EXPIRED",
		domain.Code == "QUALITY_REVIEW_STATUS_CONFLICT",
		domain.Code == "QUALITY_REVIEW_INPUT_CHANGED",
		domain.Code == "PROPOSED_ACTION_STALE",
		domain.Code == "CONFIRMATION_STALE",
		domain.Code == "CONFIRMATION_ACTION_MISMATCH",
		domain.Code == "CONFIRMATION_SNAPSHOT_MISMATCH",
		domain.Code == "RUN_ACTIVE_MESSAGE_LOCKED",
		domain.Code == "DELETE_PREVIEW_EXPIRED",
		domain.Code == "ASSET_DELETE_BLOCKED_BY_RUN",
		domain.Code == "PROJECT_DELETE_BLOCKED_BY_RUN":
		status = http.StatusConflict
	case domain.Code == "AGENT_TOOL_APPROVAL_REQUIRED",
		domain.Code == "AGENT_TOOL_APPROVAL_ALREADY_RESOLVED",
		domain.Code == "AGENT_TOOL_APPROVAL_SUBJECT_CHANGED",
		domain.Code == "AGENT_TOOL_APPROVAL_RESUME_CONFLICT",
		domain.Code == "SCRIPT_SANDBOX_POLICY_VERSION_CONFLICT",
		domain.Code == "SKILL_SCRIPT_CONFIRMATION_REQUIRED",
		domain.Code == "SKILL_SCRIPT_EXECUTION_IN_PROGRESS":
		status = http.StatusConflict
	case domain.Code == "AGENT_TURN_CANCELLED",
		domain.Code == "AGENT_TURN_TERMINAL",
		domain.Code == "AGENT_TURN_STATE_INVALID",
		domain.Code == "AGENT_TURN_CONTEXT_MISMATCH":
		status = http.StatusConflict
	case domain.Code == "ASSET_SOURCE_EXPIRED",
		domain.Code == "PROJECT_FILE_DELETED",
		domain.Code == "ASSET_SOURCE_DELETED",
		domain.Code == "EXPORT_EXPIRED":
		status = http.StatusGone
	case domain.Code == "CAPABILITY_UNAVAILABLE",
		domain.Code == "CAPABILITY_VERSION_UNAVAILABLE",
		domain.Code == "SKILL_EXECUTION_MODE_UNAVAILABLE",
		domain.Code == "AGENT_TOOL_REGISTRY_UNAVAILABLE",
		domain.Code == "AGENT_TOOL_UNAVAILABLE",
		domain.Code == "MCP_CREDENTIAL_STORAGE_UNAVAILABLE",
		domain.Code == "MCP_CREDENTIAL_UNAVAILABLE",
		domain.Code == "SCRIPT_SANDBOX_UNAVAILABLE",
		domain.Code == "SCRIPT_SANDBOX_NOT_CONFIGURED",
		domain.Code == "SKILL_SCRIPT_POLICY_DISABLED":
		status = http.StatusServiceUnavailable
	case domain.Code == "SKILL_ARCHIVE_TOO_LARGE",
		domain.Code == "SKILL_PACKAGE_FILE_TOO_LARGE",
		domain.Code == "SKILL_PACKAGE_EXPANDED_TOO_LARGE",
		domain.Code == "SKILL_PACKAGE_TOO_MANY_FILES",
		domain.Code == "AGENT_TOOL_ARGUMENTS_TOO_LARGE",
		domain.Code == "AGENT_TOOL_RESULT_TOO_LARGE":
		status = http.StatusRequestEntityTooLarge
	case domain.Code == "SKILL_ARCHIVE_REQUIRED":
		status = http.StatusUnsupportedMediaType
	case domain.Code == "WORKSPACE_ACCESS_DENIED", domain.Code == "ROLE_FORBIDDEN", domain.Code == "AGENT_ACTIVITY_REQUIRED", domain.Code == "AGENT_ACTIVITY_FORBIDDEN":
		status = http.StatusForbidden
	case domain.Code == "WORKSPACE_QUOTA_EXCEEDED", domain.Code == "PROJECT_FILE_QUOTA_EXCEEDED", domain.Code == "AGENT_SUBTASK_LIMIT":
		status = http.StatusTooManyRequests
	case strings.HasPrefix(domain.Code, "SKILL_PACKAGE_"),
		strings.HasPrefix(domain.Code, "SKILL_ARCHIVE_"),
		strings.HasPrefix(domain.Code, "SKILL_SCRIPT_"),
		domain.Code == "MCP_SECRET_REF_REQUIRED":
		status = http.StatusUnprocessableEntity
	case domain.Code == "OUTPUT_SCHEMA_VALIDATION_FAILED", domain.Code == "AGENT_SUBTASK_RESULT_INVALID",
		domain.Code == "BATCH_COVERAGE_INVALID",
		domain.Code == "BATCH_MERGE_INCOMPLETE":
		status = http.StatusUnprocessableEntity
	}
	writeError(writer, status, domain.Code, domain.Message)
}
