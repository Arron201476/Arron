package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
	"novel2script-agent/backend/internal/llm"
	"novel2script-agent/backend/internal/mainagent"
	"novel2script-agent/backend/internal/worker"
)

func main() {
	if err := runServer(); err != nil {
		log.Fatal(err)
	}
}

func runServer() error {
	llmConfig := llm.ConfigFromEnv()
	llmClient := llm.NewOpenAICompatibleClient(llmConfig)
	contentWorker := worker.NewLLMWorker(llmClient, llmConfig.GenerationModel)
	if err := worker.ValidateDesignReferences(); err != nil {
		return err
	}
	runtimeWorker := agentruntime.ContentWorker(contentWorker)
	controlAgent := mainagent.NewWithFallback(llmClient, llmConfig.ControlModel, llmConfig.ControlFallbackModel)
	mainAgent := mainagent.Decider(controlAgent)
	runtimeName := selectedRuntimeName()
	if runtimeName == "eino" {
		einoWorker, err := worker.NewEinoWorker(contentWorker)
		if err != nil {
			return err
		}
		runtimeWorker = einoWorker
		einoMainAgent, err := mainagent.NewEinoAgent(controlAgent)
		if err != nil {
			return err
		}
		mainAgent = einoMainAgent
	} else if runtimeName != "native" {
		return fmt.Errorf("unsupported N2S_RUNTIME %q; use native or eino", runtimeName)
	}
	runtime := agentruntime.NewRuntimeWithStateAndName(runtimeWorker, runtimeStatePath(), runtimeName)
	shutdownRequests := make(chan struct{}, 1)
	workspace, err := openWorkspaceStore(workspaceStorePath())
	if err != nil {
		return err
	}
	defer workspace.close()
	projects, messages, files, err := workspace.load()
	if err != nil {
		return err
	}
	server := &apiServer{
		runtime:       runtime,
		mainAgent:     mainAgent,
		sourceReader:  contentWorker,
		store:         workspace,
		projects:      projects,
		messages:      messages,
		files:         files,
		runtimeName:   runtimeName,
		shutdownToken: strings.TrimSpace(os.Getenv("N2S_SHUTDOWN_TOKEN")),
		shutdown: func() {
			select {
			case shutdownRequests <- struct{}{}:
			default:
			}
		},
	}
	if err := server.restoreProjectsFromRuntime(); err != nil {
		return err
	}

	handler := newAPIHandler(server)

	addr := serverAddress()
	log.Printf("agent backend listening on http://%s", addr)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      4 * time.Minute,
		IdleTimeout:       90 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.ListenAndServe()
	}()
	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-shutdownSignal.Done():
		log.Printf("agent backend shutting down")
	case <-shutdownRequests:
		log.Printf("agent backend received local shutdown request")
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	if err := runtime.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shutdown runtime: %w", err)
	}
	return nil
}

func selectedRuntimeName() string {
	if runtimeName := strings.ToLower(strings.TrimSpace(os.Getenv("N2S_RUNTIME"))); runtimeName != "" {
		return runtimeName
	}
	return "eino"
}

func serverAddress() string {
	if addr := strings.TrimSpace(os.Getenv("N2S_ADDR")); addr != "" {
		return addr
	}
	return "127.0.0.1:8831"
}

func runtimeStatePath() string {
	return filepath.Join(stateDirectory(), "runtime.db")
}

type apiServer struct {
	runtime       *agentruntime.Runtime
	mainAgent     mainagent.Decider
	sourceReader  sourceReader
	store         *workspaceStore
	mu            sync.Mutex
	projects      map[string]projectDTO
	messages      map[string][]messageDTO
	files         map[string]fileDTO
	runtimeName   string
	shutdownToken string
	shutdown      func()
}

type sourceReader interface {
	AnalyzeSources(context.Context, string, []worker.SourceDocument) (string, error)
}

type projectStatus string

const (
	projectIdle            projectStatus = "idle"
	projectRunning         projectStatus = "running"
	projectWaitingApproval projectStatus = "waiting_approval"
	projectPaused          projectStatus = "paused"
	projectFailed          projectStatus = "failed"
	projectCompleted       projectStatus = "completed"
)

type projectDTO struct {
	ProjectID              string            `json:"project_id"`
	Title                  string            `json:"title"`
	SourceMode             agent.SourceMode  `json:"source_mode"`
	Status                 projectStatus     `json:"status"`
	ActiveRunID            string            `json:"active_run_id,omitempty"`
	CurrentFocusArtifactID string            `json:"current_focus_artifact_id,omitempty"`
	ActiveArtifacts        map[string]string `json:"active_artifacts,omitempty"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type messageDTO struct {
	MessageID        string         `json:"message_id"`
	ProjectID        string         `json:"project_id"`
	RunID            string         `json:"run_id,omitempty"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	Attachments      []string       `json:"attachments,omitempty"`
	SelectionContext map[string]any `json:"selection_context,omitempty"`
	Intent           string         `json:"intent,omitempty"`
	DecisionContext  map[string]any `json:"decision_context,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

type fileDTO struct {
	FileID        string    `json:"file_id"`
	ProjectID     string    `json:"project_id"`
	Filename      string    `json:"filename"`
	MimeType      string    `json:"mime_type"`
	SizeBytes     int64     `json:"size_bytes"`
	Status        string    `json:"status"`
	TextPreview   string    `json:"text_preview,omitempty"`
	TextContent   string    `json:"-"`
	ContentBase64 string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

type createProjectRequest struct {
	Title      string           `json:"title,omitempty"`
	SourceMode agent.SourceMode `json:"source_mode,omitempty"`
}

type projectMessageRequest struct {
	Content          string                  `json:"content"`
	DisplayContent   string                  `json:"display_content,omitempty"`
	SourceModeHint   agent.SourceMode        `json:"source_mode_hint,omitempty"`
	FileIDs          []string                `json:"file_ids,omitempty"`
	SelectionContext map[string]any          `json:"selection_context,omitempty"`
	ClientContext    map[string]any          `json:"client_context,omitempty"`
	GenerationConfig *agent.GenerationConfig `json:"generation_config,omitempty"`
}

type projectFileRequest struct {
	FileName      string `json:"file_name"`
	MimeType      string `json:"mime_type,omitempty"`
	Size          int64  `json:"size,omitempty"`
	TextContent   string `json:"text_content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

type approvalResolveRequest struct {
	Action        string         `json:"action"`
	Instruction   string         `json:"instruction,omitempty"`
	ClientContext map[string]any `json:"client_context,omitempty"`
}

type artifactUpdateRequest struct {
	BaseVersion int            `json:"base_version"`
	Payload     map[string]any `json:"payload"`
}

func (s *apiServer) health(w http.ResponseWriter, r *http.Request) {
	storeStatus := "ok"
	if s.store == nil {
		storeStatus = "memory"
	} else if err := s.store.health(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"runtime": "main_agent",
			"store":   "error",
			"error":   "workspace storage is unavailable",
		})
		return
	}
	if err := s.runtime.PersistenceError(); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "RUNTIME_PERSISTENCE_FAILED", "运行状态无法安全保存", true, true, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"runtime": s.runtimeName,
		"store":   storeStatus,
	})
}

func (s *apiServer) listProjects(w http.ResponseWriter, r *http.Request) {
	if err := s.restoreProjectsFromRuntime(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	projects := make([]projectDTO, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *apiServer) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	project, err := s.newProject(req.Title, req.SourceMode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": project})
}

func (s *apiServer) getProject(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	project, ok := s.projectByID(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	if s.runtime != nil && strings.TrimSpace(project.ActiveRunID) != "" {
		if run, exists := s.runtime.GetRun(project.ActiveRunID); exists {
			var err error
			project, err = s.updateProjectFromRun(run)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project})
}

func (s *apiServer) deleteProject(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("project_id"))
	if _, ok := s.projectByID(projectID); !ok {
		writeAPIError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "作品不存在或已被删除。", false, false, nil)
		return
	}
	if err := s.runtime.DeleteProject(projectID); err != nil {
		if errors.Is(err, agentruntime.ErrProjectBusy) {
			writeAPIError(w, http.StatusConflict, "PROJECT_BUSY", "作品仍有内容正在生成或修改，请先暂停或等待完成后再删除。", true, false, nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED", "作品运行数据删除失败，请稍后重试。", true, true, nil)
		return
	}
	if s.store != nil {
		if err := s.store.deleteProject(projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeAPIError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "作品不存在或已被删除。", false, false, nil)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	s.mu.Lock()
	delete(s.projects, projectID)
	delete(s.messages, projectID)
	for fileID, file := range s.files {
		if file.ProjectID == projectID {
			delete(s.files, fileID)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "project_id": projectID})
}

func (s *apiServer) listProjectMessages(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	if _, ok := s.projectByID(projectID); !ok {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	s.mu.Lock()
	messages := append([]messageDTO{}, s.messages[projectID]...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *apiServer) listProjectFiles(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	if _, ok := s.projectByID(projectID); !ok {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	files := s.filesForProject(projectID)
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *apiServer) uploadProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	project, ok := s.projectByID(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	var req projectFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.FileName) == "" {
		writeError(w, http.StatusBadRequest, errors.New("file_name is required"))
		return
	}
	if strings.TrimSpace(req.TextContent) == "" && strings.TrimSpace(req.ContentBase64) == "" {
		writeError(w, http.StatusBadRequest, errors.New("text_content or content_base64 is required"))
		return
	}
	file, err := s.storeFile(project.ProjectID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"file": file})
}

func (s *apiServer) deleteFile(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("file_id")
	ok, err := s.removeFile(fileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("file not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "file_id": fileID})
}

func (s *apiServer) projectMessage(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")
	var req projectMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Content) == "" && len(req.FileIDs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("content or file_ids are required"))
		return
	}

	project, ok := s.projectByID(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	sourceMode := req.SourceModeHint
	if sourceMode == "" || sourceMode == agent.SourceModeUnknown {
		sourceMode = project.SourceMode
	}
	attachments, err := s.attachmentsForFileIDs(project.ProjectID, req.FileIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	selectedArtifactID, selectedText := selectionValues(req.SelectionContext)
	agentResp, status, err := s.executeAgentMessage(r.Context(), mainagent.MessageRequest{
		ProjectID:          project.ProjectID,
		Message:            req.Content,
		SourceMode:         sourceMode,
		RunID:              project.ActiveRunID,
		SelectedArtifactID: selectedArtifactID,
		SelectedText:       selectedText,
		SelectionContext:   req.SelectionContext,
		Attachments:        attachments,
		GenerationConfig:   req.GenerationConfig,
	})
	if err != nil {
		writeError(w, status, err)
		return
	}

	userMessage, err := s.appendMessage(messageDTO{
		ProjectID:        project.ProjectID,
		RunID:            runIDFromAgentResponse(agentResp),
		Role:             "user",
		Content:          displayOrRequestContent(req.DisplayContent, req.Content),
		Attachments:      req.FileIDs,
		SelectionContext: req.SelectionContext,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	agentMessage, err := s.appendMessage(messageDTO{
		ProjectID:       project.ProjectID,
		RunID:           runIDFromAgentResponse(agentResp),
		Role:            "agent",
		Content:         agentResp.AgentMessage,
		Intent:          string(agentResp.Decision.Intent),
		DecisionContext: decisionAuditContext(agentResp.Decision),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	project, err = s.updateProjectFromAgentResponse(project.ProjectID, agentResp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_message":     userMessage,
		"agent_message":    agentMessage,
		"decision":         agentResp.Decision,
		"project":          project,
		"run":              agentResp.Run,
		"approval_request": agentResp.ApprovalRequest,
		"events":           agentResp.Events,
		"artifacts":        agentResp.Artifacts,
	})
}

func decisionAuditContext(decision mainagent.Decision) map[string]any {
	audit := map[string]any{
		"intent": decision.Intent, "next_action": decision.NextAction, "reason": decision.Reason,
		"runtime": decision.Runtime, "orchestrator": decision.Orchestrator, "source_mode": decision.SourceMode,
		"requires_generation_config": decision.RequiresGenerationConfig,
	}
	if decision.GenerationConfig != nil {
		audit["generation_config"] = decision.GenerationConfig
	}
	if len(decision.TargetFileIDs) > 0 {
		audit["target_file_ids"] = append([]string(nil), decision.TargetFileIDs...)
	}
	if decision.RevisionIntent != "" {
		audit["revision_intent"] = decision.RevisionIntent
	}
	if decision.RevisionTarget != nil {
		audit["revision_target"] = decision.RevisionTarget
	}
	if decision.Trace != nil {
		audit["trace"] = decision.Trace
	}
	if decision.RevisionContext != nil {
		context := map[string]any{
			"user_request":             decision.RevisionContext.UserRequest,
			"apply_policy":             decision.RevisionContext.ApplyPolicy,
			"downstream_refresh_scope": decision.RevisionContext.DownstreamRefreshScope,
		}
		if decision.RevisionContext.FocusedContext != nil {
			context["focused_context"] = decision.RevisionContext.FocusedContext
		}
		audit["revision_context"] = context
	}
	return audit
}

func (s *apiServer) resolveApproval(w http.ResponseWriter, r *http.Request) {
	approvalID := r.PathValue("approval_request_id")
	var req approvalResolveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	approval, ok := s.runtime.Approval(approvalID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("approval request not found"))
		return
	}
	runBefore, ok := s.runtime.GetRun(approval.RunID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	userMessage, err := s.appendMessage(messageDTO{
		ProjectID: runBefore.ProjectID, RunID: approval.RunID, Role: "user", Content: approvalActionUserText(req.Action),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	decision := "approve"
	if req.Action == "pause" || req.Action == "revise_instruction" || req.Action == "reject" || req.Action == "keep_downstream" || req.Action == "regenerate_downstream" {
		decision = req.Action
	}
	resp, err := s.runtime.ContinueRunAsyncContext(r.Context(), approval.RunID, agent.ContinueRunRequest{
		Decision: decision,
		Note:     req.Instruction,
	})
	if err != nil {
		agentMessage, _ := s.appendMessage(messageDTO{
			ProjectID: runBefore.ProjectID, RunID: approval.RunID, Role: "agent",
			Content: "确认操作未能完成：" + safeModelError(err), Intent: string(mainagent.IntentExplainState),
		})
		run, _ := s.runtime.GetRun(approval.RunID)
		project, projectErr := s.updateProjectFromRun(run)
		if projectErr != nil {
			writeError(w, http.StatusInternalServerError, projectErr)
			return
		}
		currentApproval, stillPending := s.runtime.CurrentApproval(approval.RunID)
		if !stillPending && approval.Status == "pending" {
			currentApproval = approval
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code": "APPROVAL_ACTION_FAILED", "message": safeModelError(err),
				"recoverable": true, "retryable": true,
			},
			"approval_request": currentApproval,
			"project":          project,
			"run":              run,
			"events":           s.runtime.Events(approval.RunID),
			"artifacts":        s.runtime.Artifacts(approval.RunID),
			"agent_message": map[string]any{
				"message_id": agentMessage.MessageID, "role": "agent", "content": agentMessage.Content, "created_at": agentMessage.CreatedAt,
			},
			"user_message": userMessage,
		})
		return
	}
	project, err := s.updateProjectFromRun(resp.Run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	currentApproval, _ := s.runtime.CurrentApproval(resp.Run.RunID)
	agentMessage, err := s.appendMessage(messageDTO{
		ProjectID: resp.Run.ProjectID, RunID: resp.Run.RunID, Role: "agent",
		Content: approvalActionAgentText(req.Action), Intent: approvalActionIntent(req.Action),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"approval_request": currentApproval,
		"project":          project,
		"run":              resp.Run,
		"events":           resp.Events,
		"artifacts":        resp.Artifacts,
		"user_message":     userMessage,
		"agent_message":    agentMessage,
	})
}

func approvalActionUserText(action string) string {
	switch action {
	case "pause":
		return "暂停"
	case "keep_downstream":
		return "保留现有后续内容"
	case "regenerate_downstream":
		return "重新生成受影响内容"
	default:
		return "确认继续"
	}
}

func approvalActionAgentText(action string) string {
	switch action {
	case "pause":
		return "已暂停当前流程，你可以继续补充修改要求。"
	case "keep_downstream":
		return "已保留现有后续内容，本次修改已完成。"
	case "regenerate_downstream":
		return "已确认重新生成受影响的后续内容。旧内容会保留到对应新版本生成完成。"
	default:
		return "已收到确认，我会继续推进后续生成。"
	}
}

func approvalActionIntent(action string) string {
	if action == "pause" {
		return string(mainagent.IntentPauseRun)
	}
	return string(mainagent.IntentApproveCheckpoint)
}

func (s *apiServer) executeAgentMessage(ctx context.Context, req mainagent.MessageRequest) (mainagent.MessageResponse, int, error) {
	effectiveMessage, err := messageWithAttachments(req)
	if err != nil {
		return mainagent.MessageResponse{}, http.StatusBadRequest, err
	}
	if strings.TrimSpace(effectiveMessage) == "" {
		return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("message or attachments are required")
	}
	if strings.TrimSpace(req.Message) == "" && len(req.Attachments) > 0 {
		return mainagent.MessageResponse{
			Decision: mainagent.Decision{
				Intent:           mainagent.IntentChatIdle,
				Confidence:       1,
				NextAction:       mainagent.ActionReply,
				SourceMode:       req.SourceMode,
				AgentReply:       attachmentOnlyReply(),
				RequiresApproval: false,
				Reason:           "attachments_only_no_instruction",
				Runtime:          "rule",
			},
			AgentMessage: attachmentOnlyReply(),
		}, http.StatusOK, nil
	}

	decisionReq := req
	decisionReq.Message = messageForDecision(req, effectiveMessage)
	decisionReq.GenerationConfig = generationConfigWithDetectedMarkers(req.GenerationConfig, sourceTextAvailableForMarkerDetection(req), preserveExistingEpisodeMarksRequested(req))
	if (decisionReq.SourceMode == "" || decisionReq.SourceMode == agent.SourceModeAuto || decisionReq.SourceMode == agent.SourceModeUnknown) && hasStrongNovelSourceEvidence(effectiveMessage) {
		decisionReq.SourceMode = agent.SourceModeNovel
	}
	agentCtx := s.assembleAgentContext(decisionReq)

	decision := s.mainAgent.Decide(ctx, agentCtx)
	decision.GenerationConfig = generationConfigWithDetectedMarkers(decision.GenerationConfig, sourceTextAvailableForMarkerDetection(req), preserveExistingEpisodeMarksRequested(req))
	resp := mainagent.MessageResponse{
		Decision:     decision,
		AgentMessage: decision.AgentReply,
	}

	switch decision.NextAction {
	case mainagent.ActionStartRun:
		targetFileIDs, selectionIssue := s.generationTargetFileIDs(req, decision.TargetFileIDs)
		if selectionIssue != "" {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.AgentReply = selectionIssue
			resp.AgentMessage = selectionIssue
			break
		}
		generationInput, sourceFiles, err := s.generationSourceInput(req, effectiveMessage, targetFileIDs)
		if err != nil {
			return mainagent.MessageResponse{}, http.StatusBadRequest, err
		}
		generationConfig, configErr := validatedGenerationConfigForSource(effectiveGenerationConfig(req.GenerationConfig, decision.GenerationConfig), generationInput)
		if configErr != nil {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.AgentReply = configErr.Error()
			resp.AgentMessage = configErr.Error()
			break
		}
		startResp, err := s.runtime.StartRunAsyncContext(ctx, agent.StartRunRequest{
			ProjectID:        req.ProjectID,
			UserMessage:      strings.TrimSpace(req.Message),
			SourceText:       generationInput,
			SourceFiles:      sourceFiles,
			SourceMode:       resolveStartRunSourceMode(req, decision, generationInput),
			GenerationConfig: generationConfig,
		})
		if err != nil {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.Warning = safeModelError(err)
			resp.AgentMessage = "已经识别到生成任务，但内容模型没有成功生成产物：" + safeModelError(err)
			return resp, http.StatusServiceUnavailable, err
		}
		resp.Run = &startResp.Run
		resp.Events = startResp.Events
		resp.Artifacts = startResp.Artifacts
		resp.ApprovalRequest = startResp.ApprovalRequest
	case mainagent.ActionInspectSource:
		documents, issue := s.sourceDocumentsForAnalysis(req, decision)
		if issue != "" {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.AgentReply = issue
			resp.AgentMessage = issue
			break
		}
		if s.sourceReader == nil {
			return mainagent.MessageResponse{}, http.StatusServiceUnavailable, errors.New("source analysis model is not configured")
		}
		analysis, err := s.sourceReader.AnalyzeSources(ctx, req.Message, documents)
		if err != nil {
			return mainagent.MessageResponse{}, http.StatusServiceUnavailable, err
		}
		resp.Decision.AgentReply = analysis
		resp.AgentMessage = analysis
	case mainagent.ActionApproveRun:
		if req.RunID == "" {
			return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("run_id is required for approve_run")
		}
		continueResp, err := s.runtime.ContinueRunAsyncContext(ctx, req.RunID, agent.ContinueRunRequest{
			Decision: "approve",
			Note:     req.Message,
		})
		if err != nil {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.Warning = safeModelError(err)
			resp.AgentMessage = "确认操作已识别，但后续生成没有成功：" + safeModelError(err)
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				resp.Run = &run
				resp.Events = s.runtime.Events(req.RunID)
				resp.Artifacts = s.runtime.Artifacts(req.RunID)
				if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
					resp.ApprovalRequest = approval
				}
			}
			return resp, http.StatusConflict, err
		}
		resp.Run = &continueResp.Run
		resp.Events = continueResp.Events
		resp.Artifacts = continueResp.Artifacts
	case mainagent.ActionPauseRun:
		if req.RunID == "" {
			return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("run_id is required for pause_run")
		}
		currentRun, ok := s.runtime.GetRun(req.RunID)
		if !ok {
			return mainagent.MessageResponse{}, http.StatusNotFound, errors.New("run not found")
		}
		if currentRun.Status == agent.RunRunning {
			pausedRun, events, err := s.runtime.PauseRunningRun(req.RunID, req.Message)
			if err != nil {
				return mainagent.MessageResponse{}, http.StatusConflict, err
			}
			resp.Run = &pausedRun
			resp.Events = events
			resp.Artifacts = s.runtime.Artifacts(req.RunID)
		} else {
			continueResp, err := s.runtime.ContinueRunAsyncContext(ctx, req.RunID, agent.ContinueRunRequest{
				Decision: "pause",
				Note:     req.Message,
			})
			if err != nil {
				return mainagent.MessageResponse{}, http.StatusConflict, err
			}
			resp.Run = &continueResp.Run
			resp.Events = continueResp.Events
			resp.Artifacts = continueResp.Artifacts
		}
	case mainagent.ActionResumeRun:
		if req.RunID == "" {
			return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("run_id is required for resume_run")
		}
		resumedRun, events, err := s.runtime.ResumePausedRun(req.RunID)
		if err != nil {
			return mainagent.MessageResponse{}, http.StatusConflict, err
		}
		resp.Run = &resumedRun
		resp.Events = events
		resp.Artifacts = s.runtime.Artifacts(req.RunID)
	case mainagent.ActionRerunStep:
		if req.RunID == "" {
			return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("run_id is required for rerun_step")
		}
		stepID := strings.TrimSpace(decision.TargetArtifact)
		if stepID == "" {
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				stepID = run.CurrentStepID
			}
		}
		rerunResp, err := s.runtime.RerunStep(req.RunID, stepID, agent.RerunStepRequest{
			Reason: req.Message,
		})
		if err != nil {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.Warning = safeModelError(err)
			resp.AgentMessage = "重跑失败任务时出错：" + safeModelError(err)
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				resp.Run = &run
				resp.Events = s.runtime.Events(req.RunID)
				resp.Artifacts = s.runtime.Artifacts(req.RunID)
				if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
					resp.ApprovalRequest = approval
				}
			}
			return resp, http.StatusConflict, err
		}
		resp.Run = &rerunResp.Run
		resp.Events = rerunResp.Events
		resp.Artifacts = rerunResp.Artifacts
	case mainagent.ActionReviseCheckpoint:
		if req.RunID == "" {
			return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("run_id is required for revise_checkpoint")
		}
		revisionContext := buildRevisionContextPack(req, decision, agentCtx, s.runtime.Artifacts(req.RunID))
		resp.Decision.RevisionContext = revisionContext
		if revisionContext != nil && revisionContext.RevisionIntent == mainagent.RevisionIntentReplaceSourceInput && revisionContext.Target.ArtifactType == "source_input" {
			payload, replacementIssue := sourceReplacementPayload(revisionContext, req.Attachments)
			if replacementIssue != "" {
				resp.Decision.NextAction = mainagent.ActionReply
				resp.Decision.Intent = mainagent.IntentExplainState
				resp.Decision.Reason = "source_replacement_requires_attachment"
				resp.AgentMessage = replacementIssue
				if run, ok := s.runtime.GetRun(req.RunID); ok {
					resp.Run = &run
					resp.Events = s.runtime.Events(req.RunID)
					resp.Artifacts = s.runtime.Artifacts(req.RunID)
				}
				return resp, http.StatusOK, nil
			}
			updated, events, artifacts, ok := s.runtime.UpdateArtifact(revisionContext.Target.ArtifactID, payload)
			if !ok {
				return mainagent.MessageResponse{}, http.StatusBadRequest, errors.New("source input is no longer current; refresh and try again")
			}
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.Reason = "source_input_replaced"
			if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
				resp.ApprovalRequest = approval
				resp.AgentMessage = fmt.Sprintf("已替换输入材料并保存为 v%d。现有后续内容仍保留，请选择保留后续内容，或用新材料重新生成受影响的后续内容。", updated.Version)
			} else {
				resp.AgentMessage = fmt.Sprintf("已替换输入材料并保存为 v%d。当前还没有后续内容，后续生成会直接使用这份新材料。", updated.Version)
			}
			resp.Events = events
			resp.Artifacts = artifacts
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				resp.Run = &run
			}
			return resp, http.StatusOK, nil
		}
		if issue := validateRevisionContextPack(revisionContext); issue != "" {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.Intent = mainagent.IntentExplainState
			resp.Decision.Reason = "revision_plan_validation_failed"
			resp.AgentMessage = issue
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				resp.Run = &run
				resp.Events = s.runtime.Events(req.RunID)
				resp.Artifacts = s.runtime.Artifacts(req.RunID)
				if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
					resp.ApprovalRequest = approval
				}
			}
			return resp, http.StatusOK, nil
		}
		reviseResp, err := s.runtime.ReviseCheckpoint(req.RunID, formatRevisionInstruction(req.Message, revisionContext))
		if err != nil {
			resp.Decision.NextAction = mainagent.ActionReply
			resp.Decision.Warning = safeModelError(err)
			resp.AgentMessage = "修改当前确认点时出错：" + safeModelError(err)
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				resp.Run = &run
				resp.Events = s.runtime.Events(req.RunID)
				resp.Artifacts = s.runtime.Artifacts(req.RunID)
				if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
					resp.ApprovalRequest = approval
				}
			}
			return resp, http.StatusOK, nil
		}
		resp.Run = &reviseResp.Run
		resp.Events = reviseResp.Events
		resp.Artifacts = reviseResp.Artifacts
	default:
		if req.RunID != "" {
			if run, ok := s.runtime.GetRun(req.RunID); ok {
				resp.Run = &run
				resp.Events = s.runtime.Events(req.RunID)
				resp.Artifacts = s.runtime.Artifacts(req.RunID)
				if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
					resp.ApprovalRequest = approval
				}
			}
		}
	}

	if resp.Decision.Trace != nil {
		resp.Decision.Trace.ExecutedAction = resp.Decision.NextAction
	}
	return resp, http.StatusOK, nil
}

func validateRevisionContextPack(pack *mainagent.RevisionContextPack) string {
	if pack == nil {
		return "我还不能定位要修改的产物，请先选择具体产物或正文内容。"
	}
	target := pack.Target
	artifactType := strings.TrimSpace(target.ArtifactType)
	fieldPath := strings.TrimSpace(target.FieldPath)
	if artifactType == "" || strings.TrimSpace(target.ArtifactID) == "" {
		return "修改目标缺少有效产物，请刷新后重新选择要修改的内容。"
	}
	allowedRoots := map[string]map[string]bool{
		"source_input":     {"generation_config": true, "notes": true, "text": true, "source_text": true, "attachments": true, "files": true},
		"story_bible":      {"story_overview": true, "story_goal": true, "summary": true, "core_conflict": true, "characters": true, "relationships": true, "major_plotline": true, "must_keep_facts": true, "adaptation_risks": true, "world_rules": true, "short_drama_assets": true, "climax_map": true, "emotional_payoff": true, "foreshadowing_and_payoff": true, "generation_config": true, "risk_notes": true, "source_evidence": true, "source_trace": true, "source_structure": true},
		"episode_split":    {"generation_config": true, "episodes": true, "coverage_check": true, "global_risks": true, "source_trace": true, "source_volume_assessment": true, "split_strategy": true, "target_episode_count": true, "actual_episode_count": true},
		"material_bank":    {"generation_config": true, "input_type_tags": true, "user_supplied_facts": true, "conflict_materials": true, "emotional_drives": true, "payoff_candidates": true, "hook_candidates": true, "visual_scene_candidates": true, "discard_or_later": true, "inferred_candidates": true, "gaps_and_questions": true, "volume_fit_notes": true, "most_promising_direction": true, "risks": true, "source_trace": true},
		"story_seed":       {"generation_config": true, "logline": true, "core_premise": true, "genre_tags": true, "protagonist": true, "main_characters": true, "relationship_engine": true, "central_conflict": true, "world_rules": true, "main_plotline": true, "payoff_chain": true, "hook_engine": true, "generated_additions": true, "development_notes": true, "volume_plan_notes": true, "risks": true, "source_trace": true},
		"series_blueprint": {"generation_config": true, "resolved_episode_count": true, "recommended_episode_count": true, "episode_count_reason": true, "series_promise": true, "phase_plan": true, "payoff_distribution": true, "hook_distribution": true, "first_major_climax_plan": true, "pacing_density_plan": true, "character_progression": true, "relationship_progression": true, "continuity_rules": true, "generated_additions": true, "fit_risks": true, "source_trace": true},
		"episode_cards":    {"generation_config": true, "episodes": true, "continuity_delta": true, "next_action": true, "global_risks": true, "risk_notes": true, "source_evidence": true},
	}
	intent := pack.RevisionIntent
	if artifactType == "script_context" || artifactType == "scripts" {
		return "该产物是内部或聚合结果，不能直接修改；请修改对应的上游产物或具体分集剧本。"
	}
	if artifactType == "script_unit" {
		switch intent {
		case mainagent.RevisionIntentPatchScriptSpan:
			if pack.FocusedContext == nil || strings.TrimSpace(pack.FocusedContext.SelectedText) == "" || (strings.TrimSpace(target.NodeID) == "" && strings.TrimSpace(target.SceneID) == "") {
				return "剧本选区缺少正文或节点定位，请重新选中要修改的句子。"
			}
		case mainagent.RevisionIntentRegenerateScriptScene:
			if strings.TrimSpace(target.SceneID) == "" {
				return "请先指定要重写的场景。"
			}
		case mainagent.RevisionIntentRegenerateScriptEpisode:
			if strings.TrimSpace(target.EpisodeID) == "" {
				return "请先指定要重写第几集。"
			}
		default:
			return "当前剧本修改粒度与目标不一致，请重新选择句子、场景或分集。"
		}
		return ""
	}
	if _, ok := allowedRoots[artifactType]; !ok {
		return "当前产物类型尚未定义可执行的修改粒度。"
	}
	localArtifactIntents := map[mainagent.RevisionIntent]bool{
		mainagent.RevisionIntentPatchArtifactField:      true,
		mainagent.RevisionIntentPatchArtifactEntity:     true,
		mainagent.RevisionIntentPatchArtifactCollection: true,
		mainagent.RevisionIntentPatchArtifactSection:    true,
		mainagent.RevisionIntentRegenerateArtifact:      true,
		mainagent.RevisionIntentAddRequirement:          true,
	}
	allowedIntents := map[string]map[mainagent.RevisionIntent]bool{
		"source_input": {
			mainagent.RevisionIntentPatchArtifactField: true,
			mainagent.RevisionIntentReplaceSourceInput: true,
		},
		"story_bible":      localArtifactIntents,
		"episode_split":    localArtifactIntents,
		"material_bank":    localArtifactIntents,
		"story_seed":       localArtifactIntents,
		"series_blueprint": localArtifactIntents,
		"episode_cards":    localArtifactIntents,
	}
	if !allowedIntents[artifactType][intent] {
		return "当前修改粒度不符合该产物的修改合同，请重新定位具体字段、实体或整份产物。"
	}
	switch intent {
	case mainagent.RevisionIntentPatchArtifactField, mainagent.RevisionIntentPatchArtifactEntity, mainagent.RevisionIntentPatchArtifactCollection, mainagent.RevisionIntentPatchArtifactSection:
		if fieldPath == "" && !(intent == mainagent.RevisionIntentPatchArtifactEntity && strings.TrimSpace(target.EpisodeID) != "") {
			return "修改计划缺少目标字段或区块，请先确认具体修改位置。"
		}
		if strings.HasPrefix(fieldPath, "generation_config.") && strings.TrimSpace(target.EpisodeID) != "" {
			return "当前目标同时指向单集和全局生成配置，请先确认修改范围。"
		}
		root := fieldPath
		if root == "" && strings.TrimSpace(target.EpisodeID) != "" {
			root = "episodes"
		}
		if index := strings.IndexAny(root, ".[ "); index >= 0 {
			root = root[:index]
		}
		root = strings.TrimSuffix(root, "[")
		rootAllowed := allowedRoots[artifactType][root]
		if !rootAllowed && pack.TargetArtifact != nil {
			_, rootAllowed = pack.TargetArtifact.Payload[root]
		}
		if !rootAllowed {
			return "修改目标无法映射到当前产物结构，请重新确认要修改的字段。"
		}
		if intent == mainagent.RevisionIntentPatchArtifactCollection {
			if pack.TargetArtifact == nil {
				return "列表结构修改缺少当前产物快照，请刷新后重试。"
			}
			value, exists := pack.TargetArtifact.Payload[root]
			if _, list := value.([]any); !exists || !list {
				return "结构修改目标不是列表，不能执行拆分、合并、增删或移动。"
			}
		}
		if artifactType == "source_input" && intent == mainagent.RevisionIntentPatchArtifactField && root != "notes" {
			return "输入材料正文不能由内容模型局部改写；请上传新的材料并明确选择替换输入。"
		}
	case mainagent.RevisionIntentRegenerateArtifact, mainagent.RevisionIntentReplaceSourceInput, mainagent.RevisionIntentAddRequirement:
		return ""
	default:
		return "当前修改意图不符合该产物支持的修改粒度。"
	}
	return ""
}

func sourceReplacementPayload(pack *mainagent.RevisionContextPack, attachments []mainagent.FileAttachment) (map[string]any, string) {
	if pack == nil || pack.TargetArtifact == nil || len(attachments) == 0 {
		return nil, "请先上传新的原文或素材文件，再明确发送“用这个文件替换输入材料”。"
	}
	parts := make([]string, 0, len(attachments))
	attachmentIndex := make([]any, 0, len(attachments))
	for _, attachment := range attachments {
		content, err := attachmentText(attachment)
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		name := strings.TrimSpace(attachment.FileName)
		if name == "" {
			name = "untitled"
		}
		parts = append(parts, fmt.Sprintf("【附件：%s】\n%s", name, strings.TrimSpace(content)))
		attachmentIndex = append(attachmentIndex, map[string]any{"file_name": name, "mime_type": attachment.MimeType, "size": attachment.Size})
	}
	if len(parts) == 0 {
		return nil, "新附件中没有可读取的文本内容，暂时不能替换输入材料。"
	}
	payload := map[string]any{}
	data, err := json.Marshal(pack.TargetArtifact.Payload)
	if err == nil {
		_ = json.Unmarshal(data, &payload)
	}
	payload["text"] = strings.Join(parts, "\n\n")
	payload["attachments"] = attachmentIndex
	if pack.SourceMode != "" {
		payload["source_mode"] = pack.SourceMode
	}
	return payload, ""
}

func (s *apiServer) assembleAgentContext(req mainagent.MessageRequest) mainagent.Context {
	ctx := mainagent.Context{
		Request:       req,
		ArtifactIndex: map[string]string{},
	}

	if project, ok := s.projectByID(req.ProjectID); ok {
		ctx.Project = &mainagent.ProjectContext{
			ProjectID:   project.ProjectID,
			Title:       project.Title,
			Status:      string(project.Status),
			SourceMode:  project.SourceMode,
			ActiveRunID: project.ActiveRunID,
			Files:       s.projectFileIndex(project.ProjectID),
		}
		ctx.Conversation = s.conversationContext(project.ProjectID, 8)
	}

	if strings.TrimSpace(req.RunID) == "" {
		ctx.FocusedContext = focusedContextFromRequest(req, nil)
		return ctx
	}

	run, ok := s.runtime.GetRun(req.RunID)
	if !ok {
		ctx.FocusedContext = focusedContextFromRequest(req, nil)
		return ctx
	}

	ctx.Run = &run
	events := s.runtime.Events(req.RunID)
	artifacts := s.runtime.Artifacts(req.RunID)
	ctx.EventDigest = eventDigest(events, 12)
	ctx.Artifacts = artifactDigests(artifacts)
	ctx.ArtifactIndex = artifactIndex(artifacts)
	if approval, ok := s.runtime.CurrentApproval(req.RunID); ok {
		ctx.ApprovalRequest = approval
	}
	ctx.CurrentStepContext = currentStepContext(run, artifacts)
	ctx.FocusedContext = focusedContextFromRequest(req, artifacts)
	ctx.UpstreamContext = upstreamContext(run.SourceMode, ctx.CurrentStepContext, artifacts)
	return ctx
}

func (s *apiServer) conversationContext(projectID string, limit int) *mainagent.ConversationContext {
	if strings.TrimSpace(projectID) == "" {
		return nil
	}
	s.mu.Lock()
	messages := append([]messageDTO(nil), s.messages[projectID]...)
	s.mu.Unlock()
	if limit <= 0 {
		limit = 8
	}
	var latestSelected *messageDTO
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "agent" && selectionConsumingMessageIntent(messages[index].Intent) {
			break
		}
		if len(messages[index].SelectionContext) > 0 {
			selected := messages[index]
			latestSelected = &selected
			break
		}
	}
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	if latestSelected != nil {
		included := false
		for _, message := range messages {
			if message.MessageID == latestSelected.MessageID {
				included = true
				break
			}
		}
		if !included {
			messages = append([]messageDTO{*latestSelected}, messages...)
		}
	}
	turns := make([]mainagent.ConversationTurn, 0, len(messages))
	for _, message := range messages {
		turns = append(turns, mainagent.ConversationTurn{
			Role:             message.Role,
			Content:          truncateText(message.Content, 800),
			RunID:            message.RunID,
			Intent:           message.Intent,
			AttachmentIDs:    append([]string(nil), message.Attachments...),
			SelectionContext: compactSelectionContext(message.SelectionContext),
			CreatedAt:        message.CreatedAt,
		})
	}
	return &mainagent.ConversationContext{RecentTurns: turns}
}

func selectionConsumingMessageIntent(intent string) bool {
	switch mainagent.Intent(strings.TrimSpace(intent)) {
	case mainagent.IntentReviseCheckpoint, mainagent.IntentApproveCheckpoint, mainagent.IntentGenerateFromNovel, mainagent.IntentGenerateFromMaterial, mainagent.IntentResumeRun, mainagent.IntentRerunStep:
		return true
	default:
		return false
	}
}

func eventDigest(events []agent.RunEvent, limit int) []mainagent.EventDigest {
	if limit <= 0 {
		limit = 12
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	digest := make([]mainagent.EventDigest, 0, len(events))
	for _, event := range events {
		digest = append(digest, mainagent.EventDigest{
			Type:         event.Type,
			StepID:       event.StepID,
			Message:      truncateText(event.Message, 240),
			ArtifactRefs: append([]string(nil), event.ArtifactRefs...),
			Payload:      compactMap(event.Payload, 2),
			CreatedAt:    event.CreatedAt,
		})
	}
	return digest
}

func artifactDigests(artifacts []agent.Artifact) []mainagent.ArtifactDigest {
	digests := make([]mainagent.ArtifactDigest, 0, len(artifacts))
	for _, artifact := range artifacts {
		digests = append(digests, mainagent.ArtifactDigest{
			ArtifactID:   artifact.ArtifactID,
			ArtifactType: artifact.ArtifactType,
			Status:       artifact.Status,
			Version:      artifact.Version,
		})
	}
	return digests
}

func artifactIndex(artifacts []agent.Artifact) map[string]string {
	index := map[string]string{}
	for _, artifact := range artifacts {
		index[artifact.ArtifactType] = artifact.ArtifactID
		if artifact.ArtifactType == "script_unit" {
			if episodeID := stringValue(artifact.Payload, "episode_id", "episode"); episodeID != "" {
				index["script_unit:"+episodeID] = artifact.ArtifactID
			}
		}
	}
	return index
}

func currentStepContext(run agent.Run, artifacts []agent.Artifact) *mainagent.CurrentStepContext {
	stepID := strings.TrimSpace(run.CurrentStepID)
	artifactType := currentArtifactType(run)
	var current *agent.Artifact
	if artifactType != "" {
		current = latestArtifactByType(artifacts, artifactType)
	}
	if current == nil && len(artifacts) > 0 {
		artifact := artifacts[len(artifacts)-1]
		current = &artifact
		artifactType = artifact.ArtifactType
	}
	if stepID == "" && artifactType == "" && current == nil {
		return nil
	}
	context := &mainagent.CurrentStepContext{
		StepID:       stepID,
		StepLabel:    stepLabel(stepID, artifactType),
		ArtifactType: artifactType,
		ReadPolicy:   readPolicyForArtifact(artifactType),
	}
	if current != nil {
		context.ArtifactID = current.ArtifactID
		context.ArtifactType = current.ArtifactType
		context.Status = current.Status
		context.Version = current.Version
		context.PayloadExcerpt = compactMap(current.Payload, 3)
		context.PayloadSummary = payloadSummary(current.Payload)
	}
	return context
}

func focusedContextFromRequest(req mainagent.MessageRequest, artifacts []agent.Artifact) *mainagent.FocusedContext {
	artifactID := strings.TrimSpace(req.SelectedArtifactID)
	selectedText := strings.TrimSpace(req.SelectedText)
	selection := req.SelectionContext
	if artifactID == "" {
		artifactID = firstString(selection, "artifact_id", "artifactId", "selected_artifact_id", "selectedArtifactId")
	}
	if selectedText == "" {
		selectedText = firstString(selection, "selected_text", "selectedText", "selection_text", "text")
	}
	if artifactID == "" && selectedText == "" {
		return nil
	}
	context := &mainagent.FocusedContext{
		ArtifactID:      artifactID,
		ArtifactType:    firstString(selection, "artifact_type", "artifactType"),
		SelectedText:    truncateText(selectedText, 12000),
		FieldPath:       firstString(selection, "field_path", "fieldPath"),
		EpisodeID:       firstString(selection, "episode_id", "episodeId"),
		SceneID:         firstString(selection, "scene_id", "sceneId"),
		NodeID:          firstString(selection, "node_id", "nodeId", "line_id", "lineId"),
		StartSceneID:    firstString(selection, "start_scene_id", "startSceneId"),
		EndSceneID:      firstString(selection, "end_scene_id", "endSceneId"),
		StartLineID:     firstString(selection, "start_line_id", "startLineId"),
		EndLineID:       firstString(selection, "end_line_id", "endLineId"),
		LineIDs:         firstStringList(selection, "line_ids", "lineIds"),
		SelectionScope:  firstString(selection, "selection_scope", "selectionScope"),
		SelectionStart:  firstInt(selection, "selection_start", "selectionStart"),
		SelectionEnd:    firstInt(selection, "selection_end", "selectionEnd"),
		BeforeText:      truncateText(firstString(selection, "before_context", "beforeText", "before_text"), 600),
		AfterText:       truncateText(firstString(selection, "after_context", "afterText", "after_text"), 600),
		SelectionSource: firstString(selection, "selection_source", "selectionSource"),
	}
	if context.SelectionSource == "" {
		context.SelectionSource = "user_selection"
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactID != artifactID {
			continue
		}
		context.ArtifactType = artifact.ArtifactType
		if context.EpisodeID == "" {
			context.EpisodeID = stringValue(artifact.Payload, "episode_id", "episode")
		}
		if context.SceneID == "" {
			context.SceneID = stringValue(artifact.Payload, "scene_id", "scene")
		}
		if context.NodeID == "" {
			context.NodeID = stringValue(artifact.Payload, "node_id", "node")
		}
		enrichFocusedScriptContext(context, artifact)
		break
	}
	return context
}

func enrichFocusedScriptContext(context *mainagent.FocusedContext, artifact agent.Artifact) {
	if context == nil || artifact.ArtifactType != "script_unit" || (context.BeforeText != "" && context.AfterText != "") {
		return
	}
	scenes, _ := artifact.Payload["scenes"].([]any)
	for _, value := range scenes {
		scene, _ := value.(map[string]any)
		if scene == nil || (context.SceneID != "" && strings.TrimSpace(fmt.Sprint(scene["scene_id"])) != context.SceneID) {
			continue
		}
		blocks, _ := scene["blocks"].([]any)
		start := scriptBlockIndex(context.StartLineID, context.NodeID, blocks)
		if start < 0 {
			continue
		}
		end := scriptBlockIndex(context.EndLineID, context.NodeID, blocks)
		if end < start {
			end = start
		}
		if context.BeforeText == "" {
			context.BeforeText = scriptBlockContext(blocks, maxInt(0, start-3), start)
		}
		if context.AfterText == "" {
			context.AfterText = scriptBlockContext(blocks, end+1, minInt(len(blocks), end+4))
		}
		return
	}
}

func scriptBlockIndex(primary string, fallback string, blocks []any) int {
	lineID := strings.TrimSpace(primary)
	if lineID == "" {
		lineID = strings.TrimSpace(fallback)
	}
	for index, value := range blocks {
		block, _ := value.(map[string]any)
		if block != nil && strings.TrimSpace(fmt.Sprint(block["line_id"])) == lineID {
			return index
		}
	}
	position := strings.LastIndex(lineID, "-line-")
	if position < 0 {
		return -1
	}
	number, err := strconv.Atoi(lineID[position+len("-line-"):])
	if err != nil || number < 1 || number > len(blocks) {
		return -1
	}
	return number - 1
}

func scriptBlockContext(blocks []any, start int, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(blocks) {
		end = len(blocks)
	}
	lines := make([]string, 0, end-start)
	for _, value := range blocks[start:end] {
		block, _ := value.(map[string]any)
		if block == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(block["text"]))
		if text == "" || text == "<nil>" {
			continue
		}
		speaker := strings.TrimSpace(fmt.Sprint(block["speaker"]))
		if speaker != "" && speaker != "<nil>" {
			text = speaker + "：" + text
		}
		lines = append(lines, text)
	}
	return truncateText(strings.Join(lines, "\n"), 600)
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func upstreamContext(sourceMode agent.SourceMode, current *mainagent.CurrentStepContext, artifacts []agent.Artifact) *mainagent.UpstreamContext {
	required := requiredArtifacts(sourceMode, current)
	if len(required) == 0 {
		return nil
	}
	summaries := map[string]string{}
	for _, artifactType := range required {
		if artifact := latestArtifactByType(artifacts, artifactType); artifact != nil {
			summaries[artifactType] = payloadSummary(artifact.Payload)
		}
	}
	return &mainagent.UpstreamContext{
		RequiredArtifacts: required,
		Summaries:         summaries,
	}
}

func selectionValues(selection map[string]any) (string, string) {
	return firstString(selection, "artifact_id", "selected_artifact_id", "artifactId"),
		firstString(selection, "selected_text", "selection_text", "text", "selectedText")
}

func buildRevisionContextPack(req mainagent.MessageRequest, decision mainagent.Decision, ctx mainagent.Context, artifacts []agent.Artifact) *mainagent.RevisionContextPack {
	target := mainagent.RevisionTarget{}
	if decision.RevisionTarget != nil {
		target = *decision.RevisionTarget
	}
	if target.ArtifactType == "" {
		target.ArtifactType = strings.TrimSpace(decision.TargetArtifact)
	}
	focusedContext := ctx.FocusedContext
	focusedContextRecovered := false
	if focusedContext == nil && (decision.Reason == "forced_revision_from_conversation_continuation" || decision.RevisionIntent == mainagent.RevisionIntentPatchScriptSpan) {
		focusedContext = focusedContextFromConversation(ctx.Conversation, target, artifacts)
		focusedContextRecovered = focusedContext != nil
	}
	if focusedContext != nil {
		focusedMatchesTarget := target.ArtifactType == "" || focusedContext.ArtifactType == "" || target.ArtifactType == focusedContext.ArtifactType
		if target.ArtifactID == "" && focusedMatchesTarget {
			target.ArtifactID = focusedContext.ArtifactID
		}
		if target.ArtifactType == "" {
			target.ArtifactType = focusedContext.ArtifactType
		}
		if target.FieldPath == "" && focusedMatchesTarget {
			target.FieldPath = focusedContext.FieldPath
		}
		if target.EpisodeID == "" && focusedMatchesTarget {
			target.EpisodeID = focusedContext.EpisodeID
		}
		if target.SceneID == "" && focusedMatchesTarget {
			target.SceneID = focusedContext.SceneID
		}
		if target.NodeID == "" && focusedMatchesTarget {
			target.NodeID = focusedContext.NodeID
		}
		if target.StartSceneID == "" && focusedMatchesTarget {
			target.StartSceneID = focusedContext.StartSceneID
		}
		if target.EndSceneID == "" && focusedMatchesTarget {
			target.EndSceneID = focusedContext.EndSceneID
		}
		if target.StartLineID == "" && focusedMatchesTarget {
			target.StartLineID = focusedContext.StartLineID
		}
		if target.EndLineID == "" && focusedMatchesTarget {
			target.EndLineID = focusedContext.EndLineID
		}
		if len(target.LineIDs) == 0 && focusedMatchesTarget {
			target.LineIDs = append([]string(nil), focusedContext.LineIDs...)
		}
	}
	if ctx.CurrentStepContext != nil {
		currentMatchesTarget := target.ArtifactType == "" || ctx.CurrentStepContext.ArtifactType == "" || target.ArtifactType == ctx.CurrentStepContext.ArtifactType
		if target.ArtifactID == "" && currentMatchesTarget {
			target.ArtifactID = ctx.CurrentStepContext.ArtifactID
		}
		if target.ArtifactType == "" {
			target.ArtifactType = ctx.CurrentStepContext.ArtifactType
		}
	}

	targetArtifact := resolveRevisionTargetArtifact(target, artifacts)
	if targetArtifact != nil {
		target.ArtifactID = targetArtifact.ArtifactID
		target.ArtifactType = targetArtifact.ArtifactType
	}

	sourceMode := decision.SourceMode
	if sourceMode != agent.SourceModeNovel && sourceMode != agent.SourceModeNonNovel && ctx.Run != nil {
		sourceMode = ctx.Run.SourceMode
	}
	userRequest := strings.TrimSpace(req.Message)
	if (decision.Reason == "forced_revision_from_conversation_continuation" || focusedContextRecovered) && strings.TrimSpace(decision.AgentReply) != "" {
		userRequest = strings.TrimSpace(decision.AgentReply)
	}
	if decision.Reason == "forced_revision_from_recent_selection" || decision.Reason == "forced_revision_retry_after_control_failure" || decision.Reason == "forced_revision_from_conversation_continuation" {
		if prior := recentSelectedRevisionInstruction(ctx.Conversation, target); prior != "" {
			userRequest = prior
		}
	}
	pack := &mainagent.RevisionContextPack{
		RevisionIntent:         decision.RevisionIntent,
		UserRequest:            userRequest,
		SourceMode:             sourceMode,
		Target:                 target,
		CurrentStepContext:     ctx.CurrentStepContext,
		FocusedContext:         focusedContext,
		RecentEvents:           append([]mainagent.EventDigest(nil), ctx.EventDigest...),
		DownstreamRefreshScope: downstreamRefreshScope(target.ArtifactType, sourceMode),
		ApplyPolicy:            revisionApplyPolicy(decision.RevisionIntent),
	}
	if ctx.Conversation != nil {
		pack.RecentTurns = append([]mainagent.ConversationTurn(nil), ctx.Conversation.RecentTurns...)
	}
	if ctx.Run != nil {
		pack.GenerationConfig = generationConfigFromRun(*ctx.Run)
	}
	if targetArtifact != nil {
		pack.TargetArtifact = artifactSnapshot(*targetArtifact, true)
	}
	for _, artifactType := range requiredRevisionArtifacts(sourceMode, target.ArtifactType) {
		if artifact := latestArtifactByType(artifacts, artifactType); artifact != nil {
			pack.RequiredUpstream = append(pack.RequiredUpstream, *artifactSnapshot(*artifact, false))
		}
	}
	return pack
}

func recentSelectedRevisionInstruction(conversation *mainagent.ConversationContext, target mainagent.RevisionTarget) string {
	if conversation == nil {
		return ""
	}
	turns := conversation.RecentTurns
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		if turn.Role != "user" || len(turn.SelectionContext) == 0 {
			continue
		}
		artifactID := firstString(turn.SelectionContext, "artifact_id", "selected_artifact_id", "artifactId")
		nodeID := firstString(turn.SelectionContext, "node_id", "line_id", "nodeId", "lineId")
		if target.ArtifactID != "" && artifactID != target.ArtifactID {
			continue
		}
		if target.NodeID != "" && nodeID != "" && nodeID != target.NodeID {
			continue
		}
		return strings.TrimSpace(turn.Content)
	}
	return ""
}

func focusedContextFromConversation(conversation *mainagent.ConversationContext, target mainagent.RevisionTarget, artifacts []agent.Artifact) *mainagent.FocusedContext {
	if conversation == nil {
		return nil
	}
	for index := len(conversation.RecentTurns) - 1; index >= 0; index-- {
		turn := conversation.RecentTurns[index]
		if turn.Role == "agent" && selectionConsumingMessageIntent(turn.Intent) {
			break
		}
		selection := turn.SelectionContext
		if len(selection) == 0 {
			continue
		}
		focused := focusedContextFromRequest(mainagent.MessageRequest{SelectionContext: selection}, artifacts)
		if focused == nil || strings.TrimSpace(focused.SelectedText) == "" {
			continue
		}
		if target.ArtifactID != "" && focused.ArtifactID != "" && target.ArtifactID != focused.ArtifactID {
			continue
		}
		if target.ArtifactType != "" && focused.ArtifactType != "" && target.ArtifactType != focused.ArtifactType {
			continue
		}
		if target.EpisodeID != "" && focused.EpisodeID != "" && target.EpisodeID != focused.EpisodeID {
			continue
		}
		if target.SceneID != "" && focused.SceneID != "" && target.SceneID != focused.SceneID {
			continue
		}
		if target.NodeID != "" && focused.NodeID != "" && target.NodeID != focused.NodeID {
			continue
		}
		if target.FieldPath != "" && focused.FieldPath != "" && target.FieldPath != focused.FieldPath {
			continue
		}
		return focused
	}
	return nil
}

func resolveRevisionTargetArtifact(target mainagent.RevisionTarget, artifacts []agent.Artifact) *agent.Artifact {
	if target.ArtifactID != "" {
		for index := len(artifacts) - 1; index >= 0; index-- {
			if artifacts[index].ArtifactID == target.ArtifactID &&
				(target.ArtifactType == "" || artifacts[index].ArtifactType == target.ArtifactType) &&
				artifacts[index].Status != agent.ArtifactSuperseded && artifacts[index].Status != agent.ArtifactInvalidated {
				artifact := artifacts[index]
				return &artifact
			}
		}
	}
	if target.ArtifactType != "" {
		for index := len(artifacts) - 1; index >= 0; index-- {
			artifact := artifacts[index]
			if artifact.ArtifactType != target.ArtifactType || artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
				continue
			}
			if target.ArtifactType == "script_unit" && strings.TrimSpace(target.EpisodeID) != "" && strings.TrimSpace(fmt.Sprint(artifact.Payload["episode_id"])) != strings.TrimSpace(target.EpisodeID) {
				continue
			}
			return &artifact
		}
	}
	return nil
}

func artifactSnapshot(artifact agent.Artifact, includeFullPayload bool) *mainagent.ArtifactSnapshot {
	payload := compactMap(artifact.Payload, 4)
	if includeFullPayload {
		payload = artifact.Payload
	}
	return &mainagent.ArtifactSnapshot{
		ArtifactID:     artifact.ArtifactID,
		ArtifactType:   artifact.ArtifactType,
		Status:         artifact.Status,
		Version:        artifact.Version,
		Payload:        payload,
		PayloadSummary: payloadSummary(artifact.Payload),
	}
}

func generationConfigFromRun(run agent.Run) map[string]any {
	if len(run.Metadata) == 0 {
		return nil
	}
	if config, ok := run.Metadata["generation_config"].(map[string]any); ok {
		return compactMap(config, 3)
	}
	return nil
}

func downstreamRefreshScope(artifactType string, sourceMode agent.SourceMode) []string {
	switch artifactType {
	case "source_input", "source_context":
		if sourceMode == agent.SourceModeNovel {
			return []string{"source_input", "story_bible", "episode_split", "episode_cards", "script_context", "script_unit", "scripts"}
		}
		return []string{"source_input", "material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}
	case "story_bible":
		return []string{"story_bible", "episode_split", "episode_cards", "script_context", "script_unit", "scripts"}
	case "episode_split", "source_chunks":
		return []string{"episode_split", "episode_cards", "script_context", "script_unit", "scripts"}
	case "material_bank":
		return []string{"material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}
	case "story_seed":
		return []string{"story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}
	case "series_blueprint":
		return []string{"series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}
	case "episode_cards":
		return []string{"episode_cards", "script_context", "script_unit", "scripts"}
	case "script_context":
		return []string{"script_context", "script_unit", "scripts"}
	case "script_unit", "scripts":
		return []string{"script_unit", "scripts"}
	default:
		return nil
	}
}

func revisionApplyPolicy(intent mainagent.RevisionIntent) string {
	switch intent {
	case mainagent.RevisionIntentPatchArtifactField, mainagent.RevisionIntentPatchArtifactEntity, mainagent.RevisionIntentPatchArtifactSection, mainagent.RevisionIntentPatchScriptSpan:
		return "patch_target_only_then_refresh_middle_panel"
	case mainagent.RevisionIntentRegenerateArtifact, mainagent.RevisionIntentRegenerateScriptEpisode, mainagent.RevisionIntentRegenerateScriptScene, mainagent.RevisionIntentRegenerateScriptRange:
		return "regenerate_target_scope_then_refresh_affected_artifacts"
	case mainagent.RevisionIntentRerunFailedTask:
		return "rerun_failed_task_then_continue_remaining_tasks"
	default:
		return "apply_user_requirement_to_current_checkpoint"
	}
}

func formatRevisionInstruction(userMessage string, pack *mainagent.RevisionContextPack) string {
	payload, err := json.Marshal(pack)
	if err != nil {
		return strings.TrimSpace(userMessage)
	}
	return "USER_REVISION_REQUEST:\n" + strings.TrimSpace(userMessage) + "\n\nREVISION_CONTEXT_PACK_JSON:\n" + string(payload)
}

func effectiveGenerationConfig(requested *agent.GenerationConfig, decided *agent.GenerationConfig) *agent.GenerationConfig {
	if requested != nil && !requested.Empty() {
		return requested
	}
	if decided != nil && !decided.Empty() {
		return decided
	}
	return nil
}

func sourceTextAvailableForMarkerDetection(req mainagent.MessageRequest) string {
	parts := make([]string, 0, len(req.Attachments))
	for _, attachment := range req.Attachments {
		if text := strings.TrimSpace(attachment.TextContent); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func generationConfigWithDetectedMarkers(config *agent.GenerationConfig, sourceText string, preserveSelected bool) *agent.GenerationConfig {
	markers := agent.DetectEpisodeMarkers(sourceText)
	next := agent.GenerationConfig{}
	if config != nil {
		next = *config
	}
	next.PreserveExistingEpisodeMarks = preserveSelected
	next.ExistingEpisodeMarkersDetected = len(markers) > 0
	next.DetectedEpisodeCount = len(markers)
	if preserveSelected && len(markers) > 0 {
		next.TargetEpisodeCount = len(markers)
	}
	if next.Empty() {
		return nil
	}
	return &next
}

func preserveExistingEpisodeMarksRequested(req mainagent.MessageRequest) bool {
	if req.GenerationConfig != nil {
		return req.GenerationConfig.PreserveExistingEpisodeMarks
	}
	message := strings.ToLower(strings.TrimSpace(req.Message))
	return containsAny(message, "保留原有分集", "保留原分集", "沿用原分集", "按原分集", "保持原分集", "preserve existing episode")
}

func validatedGenerationConfigForSource(config *agent.GenerationConfig, sourceText string) (*agent.GenerationConfig, error) {
	if config == nil {
		return nil, nil
	}
	next := *config
	markers := agent.DetectEpisodeMarkers(sourceText)
	next.ExistingEpisodeMarkersDetected = len(markers) > 0
	next.DetectedEpisodeCount = len(markers)
	if next.PreserveExistingEpisodeMarks {
		if len(markers) == 0 {
			return nil, fmt.Errorf("没有检测到连续可靠的原分集标记，请取消“保留原有分集”并填写目标集数。")
		}
		next.TargetEpisodeCount = len(markers)
	}
	return &next, nil
}

func currentArtifactType(run agent.Run) string {
	if value, ok := run.NextAction["artifact_type"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	if value, ok := run.NextAction["target_artifact"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return artifactTypeForStep(run.CurrentStepID)
}

func artifactTypeForStep(stepID string) string {
	stepID = strings.TrimSpace(stepID)
	if strings.HasPrefix(stepID, "step_approval_") {
		return strings.TrimPrefix(stepID, "step_approval_")
	}
	switch stepID {
	case "save_input", "step_save_input", "step_ingest_source":
		return "source_input"
	case "story_bible", "step_story_bible", "step_build_story_bible":
		return "story_bible"
	case "episode_split", "step_episode_split", "step_split_episodes":
		return "episode_split"
	case "episode_cards", "step_episode_cards", "step_plan_episode_cards":
		return "episode_cards"
	case "material_bank", "step_material_bank", "step_build_material_bank":
		return "material_bank"
	case "story_seed", "step_story_seed", "step_build_story_seed":
		return "story_seed"
	case "series_blueprint", "step_series_blueprint", "step_build_series_blueprint":
		return "series_blueprint"
	case "script_context", "step_script_context", "step_build_script_context":
		return "script_context"
	case "script_unit", "step_script_unit", "step_generate_script_unit":
		return "script_unit"
	case "scripts", "step_scripts", "step_build_scripts":
		return "scripts"
	default:
		return ""
	}
}

func stepLabel(stepID string, artifactType string) string {
	if artifactType != "" {
		return artifactType
	}
	if stepID != "" {
		return stepID
	}
	return "current_step"
}

func readPolicyForArtifact(artifactType string) string {
	switch artifactType {
	case "script_unit":
		return "read selected episode and nearby scenes; use full script only when global rewrite is requested"
	case "episode_cards":
		return "read every episode card field before approving or generating scripts"
	case "story_bible", "story_seed", "series_blueprint", "material_bank", "episode_split", "source_input":
		return "read the whole artifact summary and editable fields before the next downstream step"
	default:
		return "read current artifact excerpt and recent events before deciding"
	}
}

func requiredArtifacts(sourceMode agent.SourceMode, current *mainagent.CurrentStepContext) []string {
	if current == nil {
		return nil
	}
	switch current.ArtifactType {
	case "episode_cards":
		if sourceMode == agent.SourceModeNovel {
			return []string{"story_bible", "episode_split"}
		}
		return []string{"material_bank", "story_seed", "series_blueprint"}
	case "script_unit":
		if sourceMode == agent.SourceModeNovel {
			return []string{"story_bible", "episode_split", "episode_cards"}
		}
		return []string{"material_bank", "story_seed", "series_blueprint", "episode_cards"}
	case "series_blueprint":
		return []string{"material_bank", "story_seed"}
	case "story_seed":
		return []string{"material_bank"}
	case "episode_split", "source_chunks":
		return []string{"story_bible"}
	default:
		return nil
	}
}

func latestArtifactByType(artifacts []agent.Artifact, artifactType string) *agent.Artifact {
	for index := len(artifacts) - 1; index >= 0; index-- {
		if artifacts[index].ArtifactType == artifactType && artifacts[index].Status != agent.ArtifactSuperseded && artifacts[index].Status != agent.ArtifactInvalidated {
			artifact := artifacts[index]
			return &artifact
		}
	}
	return nil
}

func requiredRevisionArtifacts(sourceMode agent.SourceMode, artifactType string) []string {
	switch artifactType {
	case "source_input":
		return nil
	case "story_bible":
		return []string{"source_input"}
	case "episode_split":
		return []string{"source_input", "story_bible"}
	case "material_bank":
		return []string{"source_input"}
	case "story_seed":
		return []string{"source_input", "material_bank"}
	case "series_blueprint":
		return []string{"material_bank", "story_seed"}
	case "episode_cards":
		if sourceMode == agent.SourceModeNovel {
			return []string{"story_bible", "episode_split"}
		}
		return []string{"material_bank", "story_seed", "series_blueprint"}
	case "script_context":
		if sourceMode == agent.SourceModeNovel {
			return []string{"story_bible", "episode_split", "episode_cards"}
		}
		return []string{"material_bank", "story_seed", "series_blueprint", "episode_cards"}
	case "script_unit", "scripts":
		if sourceMode == agent.SourceModeNovel {
			return []string{"story_bible", "episode_split", "episode_cards", "script_context"}
		}
		return []string{"material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context"}
	default:
		return nil
	}
}

func compactMap(input map[string]any, depth int) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := map[string]any{}
	count := 0
	for key, value := range input {
		if count >= 16 {
			output["_truncated"] = true
			break
		}
		output[key] = compactValue(value, depth)
		count++
	}
	return output
}

func compactSelectionContext(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = compactValue(value, 2)
	}
	return output
}

func compactValue(value any, depth int) any {
	if depth <= 0 {
		return valueSummary(value)
	}
	switch typed := value.(type) {
	case string:
		return truncateText(typed, 1200)
	case []string:
		limit := len(typed)
		if limit > 12 {
			limit = 12
		}
		output := make([]string, 0, limit)
		for index := 0; index < limit; index++ {
			output = append(output, truncateText(typed[index], 360))
		}
		return output
	case []any:
		limit := len(typed)
		if limit > 8 {
			limit = 8
		}
		output := make([]any, 0, limit)
		for index := 0; index < limit; index++ {
			output = append(output, compactValue(typed[index], depth-1))
		}
		return output
	case []map[string]any:
		limit := len(typed)
		if limit > 8 {
			limit = 8
		}
		output := make([]any, 0, limit)
		for index := 0; index < limit; index++ {
			output = append(output, compactMap(typed[index], depth-1))
		}
		return output
	case map[string]any:
		return compactMap(typed, depth-1)
	default:
		return typed
	}
}

func valueSummary(value any) string {
	switch typed := value.(type) {
	case string:
		return truncateText(typed, 240)
	case []any:
		return fmt.Sprintf("array(len=%d)", len(typed))
	case map[string]any:
		return fmt.Sprintf("object(keys=%d)", len(typed))
	default:
		return fmt.Sprint(typed)
	}
}

func payloadSummary(payload map[string]any) string {
	if len(payload) == 0 {
		return "empty payload"
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
		if len(keys) >= 12 {
			break
		}
	}
	return "fields: " + strings.Join(keys, ", ")
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringify(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstStringList(values map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		var result []string
		switch typed := value.(type) {
		case []string:
			result = append(result, typed...)
		case []any:
			for _, item := range typed {
				if text := stringify(item); text != "" {
					result = append(result, text)
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return nil
}

func firstInt(values map[string]any, keys ...string) int {
	for _, key := range keys {
		switch typed := values[key].(type) {
		case int:
			return typed
		case int64:
			return int(typed)
		case float64:
			return int(typed)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func stringValue(values map[string]any, keys ...string) string {
	return firstString(values, keys...)
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case int, int64, float64, bool:
		return strings.TrimSpace(fmt.Sprint(typed))
	default:
		return ""
	}
}

func truncateText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n...[truncated]"
}

func (s *apiServer) newProject(title string, sourceMode agent.SourceMode) (projectDTO, error) {
	if strings.TrimSpace(title) == "" {
		title = "未命名作品"
	}
	if sourceMode == "" {
		sourceMode = agent.SourceModeAuto
	}
	projectID, err := newWorkspaceID("project")
	if err != nil {
		return projectDTO{}, err
	}
	now := time.Now().UTC()
	project := projectDTO{
		ProjectID:       projectID,
		Title:           title,
		SourceMode:      sourceMode,
		Status:          projectIdle,
		ActiveArtifacts: map[string]string{},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if s.store != nil {
		if err := s.store.upsertProject(project); err != nil {
			return projectDTO{}, err
		}
	}
	s.mu.Lock()
	s.projects[project.ProjectID] = project
	s.mu.Unlock()
	return project, nil
}

func (s *apiServer) ensureProject(projectID string, title string, sourceMode agent.SourceMode) (projectDTO, error) {
	if project, ok := s.projectByID(projectID); ok {
		return project, nil
	}
	if strings.TrimSpace(projectID) == "" {
		return s.newProject(title, sourceMode)
	}
	if run, ok := s.runtime.LatestRunForProject(projectID); ok {
		if strings.TrimSpace(title) == "" {
			title = "已恢复作品"
		}
		project := projectDTO{
			ProjectID:       projectID,
			Title:           title,
			SourceMode:      run.SourceMode,
			Status:          projectStatusFromRun(run.Status),
			ActiveRunID:     run.RunID,
			ActiveArtifacts: map[string]string{},
			CreatedAt:       run.StartedAt,
			UpdatedAt:       run.StartedAt,
		}
		project.SourceMode = run.SourceMode
		project.ActiveRunID = run.RunID
		project.Status = projectStatusFromRun(run.Status)
		for _, artifact := range s.runtime.Artifacts(run.RunID) {
			if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
				continue
			}
			project.ActiveArtifacts[artifact.ArtifactType] = artifact.ArtifactID
			project.CurrentFocusArtifactID = artifact.ArtifactID
		}
		project.UpdatedAt = time.Now().UTC()
		if s.store != nil {
			if err := s.store.upsertProject(project); err != nil {
				return projectDTO{}, err
			}
		}
		s.mu.Lock()
		s.projects[project.ProjectID] = project
		s.mu.Unlock()
		return project, nil
	}
	return projectDTO{}, fmt.Errorf("project not found: %s", projectID)
}

func (s *apiServer) projectByID(projectID string) (projectDTO, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	return project, ok
}

func (s *apiServer) appendMessage(message messageDTO) (messageDTO, error) {
	messageID, err := newWorkspaceID("message")
	if err != nil {
		return messageDTO{}, err
	}
	message.MessageID = messageID
	message.CreatedAt = time.Now().UTC()
	if s.store != nil {
		if err := s.store.insertMessage(message); err != nil {
			return messageDTO{}, err
		}
	}
	s.mu.Lock()
	s.messages[message.ProjectID] = append(s.messages[message.ProjectID], message)
	s.mu.Unlock()
	return message, nil
}

func (s *apiServer) storeFile(projectID string, req projectFileRequest) (fileDTO, error) {
	fileID, err := newWorkspaceID("file")
	if err != nil {
		return fileDTO{}, err
	}
	now := time.Now().UTC()
	size := req.Size
	if size <= 0 && strings.TrimSpace(req.TextContent) != "" {
		size = int64(len([]byte(req.TextContent)))
	}
	file := fileDTO{
		FileID:        fileID,
		ProjectID:     projectID,
		Filename:      req.FileName,
		MimeType:      req.MimeType,
		SizeBytes:     size,
		Status:        "uploaded",
		TextContent:   req.TextContent,
		ContentBase64: req.ContentBase64,
		TextPreview:   textPreview(req.TextContent),
		CreatedAt:     now,
	}
	if s.store != nil {
		if err := s.store.insertFile(file); err != nil {
			return fileDTO{}, err
		}
	}
	s.mu.Lock()
	s.files[file.FileID] = file
	s.mu.Unlock()
	return file, nil
}

func (s *apiServer) filesForProject(projectID string) []fileDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := []fileDTO{}
	for _, file := range s.files {
		if file.ProjectID == projectID {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].CreatedAt.Before(files[j].CreatedAt)
	})
	return files
}

func (s *apiServer) fileByID(fileID string) (fileDTO, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[fileID]
	return file, ok
}

func (s *apiServer) removeFile(fileID string) (bool, error) {
	s.mu.Lock()
	if _, ok := s.files[fileID]; !ok {
		s.mu.Unlock()
		return false, nil
	}
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.deleteFile(fileID); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	delete(s.files, fileID)
	s.mu.Unlock()
	return true, nil
}

func (s *apiServer) attachmentsForFileIDs(projectID string, fileIDs []string) ([]mainagent.FileAttachment, error) {
	attachments := make([]mainagent.FileAttachment, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		file, ok := s.fileByID(fileID)
		if !ok {
			return nil, fmt.Errorf("file not found: %s", fileID)
		}
		if file.ProjectID != projectID {
			return nil, fmt.Errorf("file does not belong to project: %s", fileID)
		}
		attachments = append(attachments, mainagent.FileAttachment{
			FileID:        file.FileID,
			FileName:      file.Filename,
			MimeType:      file.MimeType,
			Size:          file.SizeBytes,
			TextContent:   file.TextContent,
			ContentBase64: file.ContentBase64,
		})
	}
	return attachments, nil
}

func (s *apiServer) projectFileIndex(projectID string) []mainagent.ProjectFile {
	files := s.filesForProject(projectID)
	index := make([]mainagent.ProjectFile, 0, len(files))
	for _, file := range files {
		index = append(index, mainagent.ProjectFile{
			FileID: file.FileID, FileName: file.Filename, MimeType: file.MimeType,
			Size: file.SizeBytes,
		})
	}
	return index
}

func (s *apiServer) sourceDocumentsForAnalysis(req mainagent.MessageRequest, decision mainagent.Decision) ([]worker.SourceDocument, string) {
	targetIDs := append([]string(nil), decision.TargetFileIDs...)
	if len(targetIDs) == 0 && len(req.Attachments) > 0 {
		for _, attachment := range req.Attachments {
			if strings.TrimSpace(attachment.FileID) != "" {
				targetIDs = append(targetIDs, attachment.FileID)
			}
		}
	}
	projectFiles := s.filesForProject(req.ProjectID)
	if len(targetIDs) == 0 {
		if len(projectFiles) == 0 {
			return nil, "当前作品还没有可读取的附件。"
		}
		if len(projectFiles) > 1 {
			return nil, "当前作品有多个附件，请说明要分析哪个文件。"
		}
		targetIDs = []string{projectFiles[0].FileID}
	}

	documents := make([]worker.SourceDocument, 0, len(targetIDs))
	seen := map[string]bool{}
	for _, fileID := range targetIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID == "" || seen[fileID] {
			continue
		}
		seen[fileID] = true
		file, ok := s.fileByID(fileID)
		if !ok || file.ProjectID != req.ProjectID {
			return nil, "指定的附件已不存在或不属于当前作品，请重新选择。"
		}
		text, err := attachmentText(mainagent.FileAttachment{
			FileID: file.FileID, FileName: file.Filename, MimeType: file.MimeType, Size: file.SizeBytes,
			TextContent: file.TextContent, ContentBase64: file.ContentBase64,
		})
		if err != nil || strings.TrimSpace(text) == "" {
			return nil, "附件“" + file.Filename + "”没有可读取的文本内容。"
		}
		documents = append(documents, worker.SourceDocument{FileID: file.FileID, FileName: file.Filename, MimeType: file.MimeType, Text: text})
	}
	if len(documents) == 0 {
		return nil, "当前没有唯一可读取的附件。"
	}
	return documents, ""
}

func (s *apiServer) updateProjectFromAgentResponse(projectID string, resp mainagent.MessageResponse) (projectDTO, error) {
	if resp.Run != nil {
		return s.updateProjectFromRun(*resp.Run)
	}
	project, err := s.ensureProject(projectID, "", agent.SourceModeAuto)
	if err != nil {
		return projectDTO{}, err
	}
	project.UpdatedAt = time.Now().UTC()
	if s.store != nil {
		if err := s.store.upsertProject(project); err != nil {
			return projectDTO{}, err
		}
	}
	s.mu.Lock()
	s.projects[project.ProjectID] = project
	s.mu.Unlock()
	return project, nil
}

func (s *apiServer) updateProjectFromRun(run agent.Run) (projectDTO, error) {
	project, err := s.ensureProject(run.ProjectID, "", run.SourceMode)
	if err != nil {
		return projectDTO{}, err
	}
	project.SourceMode = run.SourceMode
	project.ActiveRunID = run.RunID
	project.Status = projectStatusFromRun(run.Status)
	project.ActiveArtifacts = map[string]string{}
	updatedAt := run.StartedAt
	if run.EndedAt != nil && run.EndedAt.After(updatedAt) {
		updatedAt = *run.EndedAt
	}
	for _, artifact := range s.runtime.Artifacts(run.RunID) {
		if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
			continue
		}
		project.ActiveArtifacts[artifact.ArtifactType] = artifact.ArtifactID
		project.CurrentFocusArtifactID = artifact.ArtifactID
		if artifact.UpdatedAt.After(updatedAt) {
			updatedAt = artifact.UpdatedAt
		}
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	project.UpdatedAt = updatedAt
	if s.store != nil {
		if err := s.store.upsertProject(project); err != nil {
			return projectDTO{}, err
		}
	}
	s.mu.Lock()
	s.projects[project.ProjectID] = project
	s.mu.Unlock()
	return project, nil
}

func (s *apiServer) restoreProjectsFromRuntime() error {
	projectIDs := make(map[string]struct{})
	for _, run := range s.runtime.Runs() {
		projectIDs[run.ProjectID] = struct{}{}
	}
	for projectID := range projectIDs {
		if latest, ok := s.runtime.LatestRunForProject(projectID); ok {
			if project, exists := s.projectByID(projectID); exists && project.Title == "已恢复作品" {
				project.Title = restoredProjectTitle(latest)
				if s.store != nil {
					if err := s.store.upsertProject(project); err != nil {
						return err
					}
				}
				s.mu.Lock()
				s.projects[projectID] = project
				s.mu.Unlock()
			}
			if _, err := s.updateProjectFromRun(latest); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	projects := make([]projectDTO, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	s.mu.Unlock()
	for _, project := range projects {
		if strings.TrimSpace(project.ActiveRunID) == "" {
			continue
		}
		if _, exists := s.runtime.GetRun(project.ActiveRunID); exists {
			continue
		}
		project.ActiveRunID = ""
		project.Status = projectIdle
		project.CurrentFocusArtifactID = ""
		project.ActiveArtifacts = map[string]string{}
		project.UpdatedAt = time.Now().UTC()
		if s.store != nil {
			if err := s.store.upsertProject(project); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.projects[project.ProjectID] = project
		s.mu.Unlock()
	}
	return nil
}

func restoredProjectTitle(run agent.Run) string {
	raw, _ := run.Metadata["user_message"].(string)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "【附件：") {
			continue
		}
		runes := []rune(line)
		if len(runes) > 22 {
			line = string(runes[:22]) + "..."
		}
		return line
	}
	return "已恢复作品"
}

func projectStatusFromRun(status agent.RunStatus) projectStatus {
	switch status {
	case agent.RunRunning:
		return projectRunning
	case agent.RunWaitingApproval:
		return projectWaitingApproval
	case agent.RunPaused:
		return projectPaused
	case agent.RunCompleted:
		return projectCompleted
	case agent.RunFailed, agent.RunCancelled:
		return projectFailed
	default:
		return projectIdle
	}
}

func runIDFromAgentResponse(resp mainagent.MessageResponse) string {
	if resp.Run == nil {
		return ""
	}
	return resp.Run.RunID
}

func textPreview(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 140 {
		return string(runes[:140]) + "..."
	}
	return text
}

func safeModelError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.ReplaceAll(message, "\n", " ")
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "client.timeout exceeded"),
		strings.Contains(lower, "timeout awaiting response headers"),
		strings.Contains(lower, "gateway timeout"),
		strings.Contains(lower, "status=504"),
		strings.Contains(lower, "status_code=504"):
		return "模型服务超时，请稍后重试。"
	case strings.Contains(lower, "status=429"), strings.Contains(lower, "status_code=429"), strings.Contains(lower, "too many requests"):
		return "模型服务繁忙，请稍后重试。"
	case containsSensitiveServiceDetail(lower):
		return "服务暂时无法完成请求，请稍后重试。"
	}
	runes := []rune(message)
	if len(runes) > 360 {
		return string(runes[:360]) + "..."
	}
	return message
}

func containsSensitiveServiceDetail(message string) bool {
	markers := []string{
		"bearer ", "authorization", "api key", "api_key", "database", "sqlite", "sqlstate",
		"connection string", "dsn=", "stack trace", "runtime/panic", "\\users\\", "/home/",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func attachmentOnlyReply() string {
	return "\u5df2\u6536\u5230\u9644\u4ef6\u3002\u9700\u8981\u751f\u6210\u5267\u672c\u65f6\uff0c\u8bf7\u8865\u5145\u660e\u786e\u6307\u4ee4\uff0c\u4f8b\u5982\u201c\u6839\u636e\u8fd9\u4e2a\u9644\u4ef6\u751f\u6210\u77ed\u5267\u201d\u3002"
}

func displayOrRequestContent(display string, request string) string {
	if value := strings.TrimSpace(display); value != "" {
		return value
	}
	return request
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func messageWithAttachments(req mainagent.MessageRequest) (string, error) {
	parts := []string{}
	if strings.TrimSpace(req.Message) != "" {
		parts = append(parts, strings.TrimSpace(req.Message))
	}
	for _, attachment := range req.Attachments {
		text, err := attachmentText(attachment)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		name := attachment.FileName
		if name == "" {
			name = "untitled"
		}
		parts = append(parts, fmt.Sprintf("\u3010\u9644\u4ef6\uff1a%s\u3011\n%s", name, strings.TrimSpace(text)))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (s *apiServer) generationSourceInput(req mainagent.MessageRequest, effectiveMessage string, targetFileIDs []string) (string, []agent.SourceFile, error) {
	attachments := append([]mainagent.FileAttachment(nil), req.Attachments...)
	if len(attachments) == 0 && len(targetFileIDs) > 0 {
		var err error
		attachments, err = s.attachmentsForFileIDs(req.ProjectID, targetFileIDs)
		if err != nil {
			return "", nil, err
		}
	}
	if len(attachments) == 0 {
		return strings.TrimSpace(effectiveMessage), nil, nil
	}
	parts := make([]string, 0, len(attachments))
	files := make([]agent.SourceFile, 0, len(attachments))
	for _, attachment := range attachments {
		content, err := attachmentText(attachment)
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(content) != "" {
			parts = append(parts, strings.TrimSpace(content))
		}
		files = append(files, agent.SourceFile{FileID: attachment.FileID, FileName: attachment.FileName, MimeType: attachment.MimeType, Size: attachment.Size})
	}
	if len(parts) == 0 {
		return strings.TrimSpace(effectiveMessage), files, nil
	}
	return strings.Join(parts, "\n\n"), files, nil
}

func (s *apiServer) generationTargetFileIDs(req mainagent.MessageRequest, targetFileIDs []string) ([]string, string) {
	if len(req.Attachments) > 0 || len(targetFileIDs) > 0 || !referencesStoredFile(req.Message) {
		return targetFileIDs, ""
	}
	files := s.filesForProject(req.ProjectID)
	if len(files) == 1 {
		return []string{files[0].FileID}, ""
	}
	if len(files) > 1 {
		return nil, "当前作品有多个附件，请说明要用哪个文件生成，避免混入无关材料。"
	}
	return nil, ""
}

func referencesStoredFile(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return containsAny(message, "附件", "文件", "刚才上传", "上传的", "这份材料", "这个材料", "已有材料", "project file", "uploaded file", "attachment")
}

func messageWithAttachmentNames(req mainagent.MessageRequest, includePreview bool) string {
	parts := []string{}
	if strings.TrimSpace(req.Message) != "" {
		parts = append(parts, strings.TrimSpace(req.Message))
	}
	for _, attachment := range req.Attachments {
		name := attachment.FileName
		if name == "" {
			name = "untitled"
		}
		line := fmt.Sprintf("[\u9644\u4ef6] %s (%d bytes)", name, attachment.Size)
		if includePreview {
			if excerpt := truncateText(attachment.TextContent, 1200); excerpt != "" {
				line += "\n[\u9644\u4ef6\u5185\u5bb9\u6458\u5f55]\n" + excerpt
			}
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

func messageForDecision(req mainagent.MessageRequest, effectiveMessage string) string {
	withNames := messageWithAttachmentNames(req, sourceContentIntent(req.Message))
	if strings.TrimSpace(req.Message) != "" {
		return withNames
	}
	if strings.TrimSpace(withNames) != "" {
		return strings.TrimSpace(withNames)
	}
	return strings.TrimSpace(effectiveMessage)
}

func sourceContentIntent(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return containsAny(message, "分析", "总结", "概括", "评价", "解释", "阅读", "查看", "生成", "改编", "写成", "拆成", "转成", "inspect", "analyze", "summarize", "generate", "adapt")
}

func resolveStartRunSourceMode(req mainagent.MessageRequest, decision mainagent.Decision, generationInput string) agent.SourceMode {
	// Explicit workspace selection wins. Evidence is used only while the mode is still automatic.
	if req.SourceMode == agent.SourceModeNovel || req.SourceMode == agent.SourceModeNonNovel {
		return req.SourceMode
	}
	if hasStrongNovelSourceEvidence(generationInput) {
		return agent.SourceModeNovel
	}
	if decision.SourceMode == agent.SourceModeNovel || decision.SourceMode == agent.SourceModeNonNovel {
		return decision.SourceMode
	}
	return agent.SourceModeAuto
}

func hasStrongNovelSourceEvidence(text string) bool {
	normalized := strings.TrimSpace(text)
	runeCount := len([]rune(normalized))
	if runeCount < 240 {
		return false
	}

	score := 0
	if strings.Contains(normalized, "【附件：") {
		score++
	}
	if hasChapterLikeLine(normalized) {
		score += 2
	}
	if quoteCount(normalized) >= 4 {
		score += 2
	} else if quoteCount(normalized) >= 2 {
		score++
	}
	if paragraphCount(normalized) >= 8 {
		score++
	}
	if containsAny(normalized, "他说", "她说", "我说", "低声", "抬头", "转身", "看着", "笑了", "皱眉", "心里") {
		score++
	}
	if containsAny(normalized, "第一天", "直到", "邻居", "警察", "小区", "房租", "晨练", "眼圈", "冷冷一笑") {
		score++
	}
	if runeCount >= 1000 {
		score++
	}
	if runeCount >= 3000 {
		score++
	}
	return score >= 4
}

func hasChapterLikeLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len([]rune(line)) <= 8 && (strings.HasPrefix(line, "第") || isSmallPositiveInteger(line)) {
			return true
		}
	}
	return false
}

func isSmallPositiveInteger(text string) bool {
	if text == "" || len(text) > 3 {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}
	return text != "0"
}

func quoteCount(text string) int {
	count := 0
	for _, token := range []string{"“", "”", "「", "」", "『", "』"} {
		count += strings.Count(text, token)
	}
	return count
}

func paragraphCount(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func attachmentText(attachment mainagent.FileAttachment) (string, error) {
	if strings.TrimSpace(attachment.TextContent) != "" {
		return attachment.TextContent, nil
	}
	if attachment.ContentBase64 == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(attachment.ContentBase64)
	if err != nil {
		return "", fmt.Errorf("attachment %s is not valid base64", attachment.FileName)
	}
	ext := strings.ToLower(filepath.Ext(attachment.FileName))
	if ext == ".docx" || strings.Contains(strings.ToLower(attachment.MimeType), "wordprocessingml.document") {
		return docxText(data)
	}
	return "", fmt.Errorf("attachment %s is not a supported text file yet", attachment.FileName)
}

func docxText(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var parts []string
	for _, file := range reader.File {
		if file.Name != "word/document.xml" && file.Name != "word/footnotes.xml" && file.Name != "word/endnotes.xml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", err
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, 12<<20))
		closeErr := rc.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		parts = append(parts, xmlText(content))
	}
	if len(parts) == 0 {
		return "", errors.New("docx has no readable document text")
	}
	return strings.Join(parts, "\n"), nil
}

func xmlText(content []byte) string {
	decoder := json.NewDecoder(strings.NewReader("null"))
	_ = decoder
	text := string(content)
	replacements := []struct {
		old string
		new string
	}{
		{"</w:p>", "\n"},
		{"</w:tr>", "\n"},
		{"</w:tab>", "\t"},
		{"<w:tab/>", "\t"},
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&amp;", "&"},
		{"&quot;", "\""},
		{"&apos;", "'"},
	}
	for _, replacement := range replacements {
		text = strings.ReplaceAll(text, replacement.old, replacement.new)
	}
	var builder strings.Builder
	inTag := false
	lastSpace := false
	for _, char := range text {
		switch char {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if inTag {
				continue
			}
			if char == '\r' || char == '\t' || char == ' ' {
				if !lastSpace {
					builder.WriteRune(' ')
					lastSpace = true
				}
				continue
			}
			builder.WriteRune(char)
			lastSpace = char == '\n'
		}
	}
	return strings.TrimSpace(builder.String())
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		status = http.StatusRequestEntityTooLarge
	}
	if isWorkspaceBusyError(err) {
		writeAPIError(w, http.StatusServiceUnavailable, "WORKSPACE_BUSY", "作品状态正在同步，请刷新查看最新状态后再决定是否重试。", true, false, map[string]any{"outcome": "refresh_required"})
		return
	}
	code := "REQUEST_FAILED"
	switch status {
	case http.StatusBadRequest:
		code = "INVALID_REQUEST"
	case http.StatusNotFound:
		code = "NOT_FOUND"
	case http.StatusConflict:
		code = "CONFLICT"
	case http.StatusRequestEntityTooLarge:
		code = "REQUEST_TOO_LARGE"
	case http.StatusTooManyRequests:
		code = "RATE_LIMITED"
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		code = "SERVICE_FAILURE"
	}
	message := err.Error()
	if status >= 500 {
		message = safeModelError(err)
	}
	writeAPIError(w, status, code, message, status >= 500 || status == http.StatusConflict || status == http.StatusTooManyRequests, status >= 500 || status == http.StatusTooManyRequests, nil)
}

func isWorkspaceBusyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database table is locked")
}

func writeAPIError(w http.ResponseWriter, status int, code string, message string, recoverable bool, retryable bool, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "recoverable": recoverable, "retryable": retryable, "details": details,
	}})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && !isAllowedOrigin(origin) {
			writeAPIError(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "请求来源不被允许", false, false, nil)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAllowedLocalOrigin(origin string) bool {
	if origin == "http://127.0.0.1:8832" || origin == "http://localhost:8832" {
		return true
	}
	return origin == "http://"+strings.TrimSpace(serverAddress())
}

func isAllowedOrigin(origin string) bool {
	if isAllowedLocalOrigin(origin) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv("N2S_ALLOWED_ORIGINS"), ",") {
		if strings.TrimRight(strings.TrimSpace(allowed), "/") == origin {
			return true
		}
	}
	return false
}

func withRequestLimit(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}
