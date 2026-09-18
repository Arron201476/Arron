package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

const maxJSONBodyBytes = 2 << 20

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func (s *Server) runtimeRoutes() {
	s.mux.HandleFunc("GET /api/v1/workspace/quota", s.getWorkspaceQuota)
	s.mux.HandleFunc("DELETE /api/v1/workspace", s.deleteWorkspace)
	s.mux.HandleFunc("GET /api/v1/workspace-mcp-credentials", s.listWorkspaceMCPCredentials)
	s.mux.HandleFunc("PUT /api/v1/workspace-mcp-credentials", s.putWorkspaceMCPCredential)
	s.mux.HandleFunc("DELETE /api/v1/workspace-mcp-credentials/{credential_id}", s.deleteWorkspaceMCPCredential)
	s.mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	s.mux.HandleFunc("GET /api/v1/skill-management-options", s.skillManagementOptions)
	s.mux.HandleFunc("POST /api/v1/skills", s.installSkill)
	s.mux.HandleFunc("POST /api/v1/skills/refresh", s.refreshSkills)
	s.mux.HandleFunc("POST /api/v1/skills/discovered/{capability_id}/install", s.installDiscoveredSkill)
	s.mux.HandleFunc("GET /api/v1/skills/install-attempts", s.listSkillInstallAttempts)
	s.mux.HandleFunc("GET /api/v1/skills/{skill_installation_id}", s.getSkill)
	s.mux.HandleFunc("GET /api/v1/skills/{skill_installation_id}/directory-update", s.previewSkillDirectoryUpdate)
	s.mux.HandleFunc("POST /api/v1/skills/{skill_installation_id}/directory-update", s.updateSkillFromDirectory)
	s.mux.HandleFunc("POST /api/v1/skills/{skill_installation_id}/versions", s.upgradeSkill)
	s.mux.HandleFunc("POST /api/v1/skills/{skill_installation_id}/versions/{version}/activate", s.activateSkillVersion)
	s.mux.HandleFunc("POST /api/v1/skills/{skill_installation_id}/enable", s.enableSkill)
	s.mux.HandleFunc("POST /api/v1/skills/{skill_installation_id}/disable", s.disableSkill)
	s.mux.HandleFunc("DELETE /api/v1/skills/{skill_installation_id}", s.uninstallSkill)
	s.mux.HandleFunc("GET /api/v1/script-sandbox-policy", s.getScriptSandboxPolicy)
	s.mux.HandleFunc("PUT /api/v1/script-sandbox-policy", s.updateScriptSandboxPolicy)
	s.mux.HandleFunc("GET /api/v1/projects", s.listProjects)
	s.mux.HandleFunc("POST /api/v1/projects", s.createProject)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}", s.getProject)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/goal", s.getProjectGoal)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/snapshot", s.getProjectSnapshot)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/workspace-projection", s.getProjectWorkspaceProjection)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/capabilities", s.listProjectCapabilities)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/capabilities/{capability_id}", s.getProjectCapability)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/capabilities/{capability_id}/skill-resources", s.getProjectSkillResources)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/proposed-actions", s.listPendingProposedActions)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/agent-tasks", s.listAgentTasks)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/agent-turns", s.listAgentTurns)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/agent-tool-calls", s.listAgentToolCalls)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/agent-tool-approvals", s.listAgentToolApprovals)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/skill-script-executions", s.listSkillScriptExecutions)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/revision-requests", s.listRevisionRequests)
	s.mux.HandleFunc("PATCH /api/v1/projects/{project_id}", s.renameProject)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/delete-previews", s.createProjectDeletePreview)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/delete-confirmations", s.confirmProjectDelete)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/conversations", s.listConversations)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/upload-sessions", s.createUploadSession)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/assets", s.listAssets)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/files", s.listProjectFiles)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/files/read", s.readProjectFile)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/files/content", s.downloadProjectFile)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/skill-drafts/preview", s.previewProjectSkillDraft)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/skill-drafts/archive", s.exportProjectSkillDraft)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/skill-drafts/installations", s.installProjectSkillDraft)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/asset-sets", s.createAssetSet)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/artifacts", s.listProjectArtifacts)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/approvals", s.listProjectApprovals)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/script-candidates", s.listScriptCandidates)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/final-script-selection", s.getCurrentFinalSelection)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/final-script-selection-previews", s.createFinalSelectionPreview)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/final-script-selections", s.confirmFinalSelection)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/events/stream", s.streamProjectEvents)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/runs", s.startRun)
	s.mux.HandleFunc("POST /internal/v1/agent/turn-commits", s.commitInternalAgentTurn)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls", s.beginInternalAgentToolCall)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/start", s.startInternalAgentToolCall)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/complete", s.completeInternalAgentToolCall)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/failures", s.failInternalAgentToolCall)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/cancel", s.cancelInternalAgentToolCall)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/skill-script-executions", s.executeInternalSkillScript)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/outputs", s.storeInternalAgentToolOutput)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/project-file-patches", s.applyInternalProjectFilePatch)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/skill-installations", s.installInternalProjectSkillDraft)
	s.mux.HandleFunc("GET /api/v1/conversations/{conversation_id}/messages", s.listMessages)
	s.mux.HandleFunc("POST /api/v1/conversations/{conversation_id}/messages", s.createMessage)
	s.mux.HandleFunc("GET /api/v1/agent-turns/{agent_turn_id}", s.getAgentTurn)
	s.mux.HandleFunc("PATCH /api/v1/agent-turns/{agent_turn_id}/queued-message", s.updateQueuedAgentTurn)
	s.mux.HandleFunc("POST /api/v1/agent-turns/{agent_turn_id}/cancel", s.cancelAgentTurn)
	s.mux.HandleFunc("POST /api/v1/agent-turns/{agent_turn_id}/pause", s.pauseAgentTurn)
	s.mux.HandleFunc("POST /api/v1/agent-turns/{agent_turn_id}/resume", s.resumeAgentTurn)
	s.mux.HandleFunc("POST /api/v1/agent-turns/{agent_turn_id}/inputs", s.appendAgentTurnInput)
	s.mux.HandleFunc("POST /api/v1/agent-tasks/{agent_task_id}/inputs", s.appendAgentTaskInput)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/execution-attempts/{attempt_id}/inputs", s.getExecutionInputs)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/runs/{run_id}/input-attempts", s.listExecutionInputAttempts)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/execution-input-changes", s.changeExecutionInput)
	s.mux.HandleFunc("POST /api/v1/projects/{project_id}/execution-attempts/{attempt_id}/inputs", s.appendExecutionInput)
	s.mux.HandleFunc("GET /api/v1/revision-requests/{revision_request_id}", s.getRevisionRequest)
	s.mux.HandleFunc("POST /api/v1/revision-requests/{revision_request_id}/execute", s.executeRevisionRequest)
	s.mux.HandleFunc("POST /api/v1/revision-requests/{revision_request_id}/accept", s.acceptRevisionRequest)
	s.mux.HandleFunc("POST /api/v1/revision-requests/{revision_request_id}/reject", s.rejectRevisionRequest)
	s.mux.HandleFunc("POST /api/v1/revision-requests/{revision_request_id}/cancel", s.cancelRevisionRequest)
	s.mux.HandleFunc("POST /api/v1/target-resolutions/{target_resolution_id}/resolve", s.resolveTargetCandidate)
	s.mux.HandleFunc("POST /api/v1/proposed-actions/{proposed_action_id}/configuration", s.configureProposedAction)
	s.mux.HandleFunc("POST /api/v1/proposed-actions/{proposed_action_id}/input-binding", s.bindProposedActionInput)
	s.mux.HandleFunc("POST /api/v1/proposed-actions/{proposed_action_id}/agent-task", s.startAgentTask)
	s.mux.HandleFunc("GET /api/v1/upload-sessions/{upload_session_id}", s.getUploadSession)
	s.mux.HandleFunc("PUT /api/v1/upload-items/{upload_item_id}/content", s.writeUploadContent)
	s.mux.HandleFunc("POST /api/v1/upload-items/{upload_item_id}/complete", s.completeUploadItem)
	s.mux.HandleFunc("GET /api/v1/assets/{asset_id}", s.getAsset)
	s.mux.HandleFunc("GET /api/v1/assets/{asset_id}/content", s.getAssetContent)
	s.mux.HandleFunc("GET /api/v1/assets/{asset_id}/download", s.downloadAsset)
	s.mux.HandleFunc("GET /api/v1/assets/{asset_id}/parsed-text", s.getParsedAssetText)
	s.mux.HandleFunc("POST /api/v1/assets/{asset_id}/parse-retries", s.retryAssetParse)
	s.mux.HandleFunc("POST /api/v1/assets/{asset_id}/delete-previews", s.createAssetDeletePreview)
	s.mux.HandleFunc("POST /api/v1/assets/{asset_id}/delete-confirmations", s.confirmAssetDelete)
	s.mux.HandleFunc("GET /api/v1/asset-sets/{asset_set_id}", s.getAssetSet)
	s.mux.HandleFunc("GET /api/v1/asset-set-versions/{asset_set_version_id}", s.getAssetSetVersion)
	s.mux.HandleFunc("POST /api/v1/asset-sets/{asset_set_id}/versions", s.createAssetSetVersion)
	s.mux.HandleFunc("POST /api/v1/asset-sets/{asset_set_id}/checks", s.checkAssetSet)
	s.mux.HandleFunc("POST /api/v1/asset-sets/{asset_set_id}/seal", s.sealAssetSet)
	s.mux.HandleFunc("POST /api/v1/asset-sets/{asset_set_id}/reopen", s.reopenAssetSet)
	s.mux.HandleFunc("GET /api/v1/runs/{run_id}", s.getRun)
	s.mux.HandleFunc("GET /api/v1/runs/{run_id}/snapshot", s.getRunSnapshot)
	s.mux.HandleFunc("GET /api/v1/runs/{run_id}/quality-reviews/current", s.getCurrentQualityReview)
	s.mux.HandleFunc("GET /api/v1/runs/{run_id}/events/stream", s.streamRunEvents)
	s.mux.HandleFunc("POST /api/v1/runs/{run_id}/pause", s.pauseRun)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/execution-targets", s.listExecutionControlTargets)
	s.mux.HandleFunc("GET /api/v1/projects/{project_id}/execution-controls", s.inspectExecutionControl)
	s.mux.HandleFunc("POST /internal/v1/agent-tool-calls/{agent_tool_call_id}/execution-control", s.controlInternalExecution)
	s.mux.HandleFunc("POST /api/v1/runs/{run_id}/resume", s.resumeRun)
	s.mux.HandleFunc("PUT /api/v1/runs/{run_id}/episode-execution-mode", s.setEpisodeExecutionMode)
	s.mux.HandleFunc("POST /api/v1/runs/{run_id}/cancel", s.cancelRun)
	s.mux.HandleFunc("POST /api/v1/runs/{run_id}/script-edit-completions", s.completeScriptEdit)
	s.mux.HandleFunc("GET /api/v1/runs/{run_id}/steps", s.listRunSteps)
	s.mux.HandleFunc("GET /api/v1/steps/{step_run_id}/tasks", s.listStepTasks)
	s.mux.HandleFunc("POST /api/v1/steps/{step_run_id}/retries", s.retryFailedStep)
	s.mux.HandleFunc("POST /api/v1/steps/{step_run_id}/partial-continuations", s.continueWithPartialResults)
	s.mux.HandleFunc("POST /internal/v1/executor/task-claims", s.claimExecutionTask)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/heartbeat", s.heartbeatExecutionAttempt)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/approval-checkpoint", s.pauseExecutionForApproval)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/pause-checkpoint", s.pauseExecutionAtNativeBoundary)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/inputs/included", s.recordExecutionInputsIncluded)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/results", s.submitExecutionResult)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/failures", s.failExecutionAttempt)
	s.mux.HandleFunc("POST /internal/v1/executor/attempts/{attempt_id}/commit", s.commitExecutionResult)
	s.mux.HandleFunc("POST /internal/v1/agent-tasks/claims", s.claimAgentTask)
	s.mux.HandleFunc("POST /internal/v1/agent-task-attempts/{attempt_id}/progress", s.updateAgentTaskProgress)
	s.mux.HandleFunc("POST /internal/v1/agent-task-attempts/{attempt_id}/pause-for-approval", s.pauseAgentTaskForApproval)
	s.mux.HandleFunc("POST /internal/v1/agent-task-attempts/{attempt_id}/pause", s.completeAgentTaskPause)
	s.mux.HandleFunc("POST /internal/v1/agent-task-attempts/{attempt_id}/complete", s.completeAgentTask)
	s.mux.HandleFunc("POST /internal/v1/agent-task-attempts/{attempt_id}/failures", s.failAgentTask)
	s.mux.HandleFunc("POST /internal/v1/runs/{run_id}/pause-completions", s.completeRunPause)
	s.mux.HandleFunc("GET /api/v1/artifacts/{artifact_id}", s.getArtifact)
	s.mux.HandleFunc("GET /api/v1/agent-tasks/{agent_task_id}", s.getAgentTask)
	s.mux.HandleFunc("GET /api/v1/agent-tool-calls/{agent_tool_call_id}", s.getAgentToolCall)
	s.mux.HandleFunc("GET /api/v1/agent-tool-calls/{agent_tool_call_id}/subtask-result", s.getAgentSubtaskResult)
	s.mux.HandleFunc("GET /api/v1/agent-tool-calls/{agent_tool_call_id}/outcome-review", s.getAgentToolOutcomeReview)
	s.mux.HandleFunc("POST /api/v1/agent-tool-calls/{agent_tool_call_id}/outcome-review", s.resolveAgentToolOutcome)
	s.mux.HandleFunc("GET /api/v1/agent-tool-approvals/{agent_tool_approval_id}", s.getAgentToolApproval)
	s.mux.HandleFunc("POST /api/v1/agent-tool-approvals/{agent_tool_approval_id}/resolutions", s.resolveAgentToolApproval)
	s.mux.HandleFunc("GET /api/v1/skill-script-executions/{skill_script_execution_id}", s.getSkillScriptExecution)
	s.mux.HandleFunc("GET /api/v1/skill-script-executions/{skill_script_execution_id}/artifact", s.getSkillScriptArtifact)
	s.mux.HandleFunc("GET /api/v1/agent-tasks/{agent_task_id}/attempts", s.listAgentTaskAttempts)
	s.mux.HandleFunc("POST /api/v1/agent-tasks/{agent_task_id}/cancel", s.cancelAgentTask)
	s.mux.HandleFunc("POST /api/v1/agent-tasks/{agent_task_id}/pause", s.requestAgentTaskPause)
	s.mux.HandleFunc("POST /api/v1/agent-tasks/{agent_task_id}/resume", s.resumeAgentTask)
	s.mux.HandleFunc("POST /api/v1/agent-tasks/{agent_task_id}/retry", s.retryAgentTask)
	s.mux.HandleFunc("GET /api/v1/artifacts/{artifact_id}/versions", s.listArtifactVersions)
	s.mux.HandleFunc("POST /api/v1/artifacts/{artifact_id}/versions", s.createArtifactVersion)
	s.mux.HandleFunc("GET /api/v1/artifact-versions/{artifact_version_id}", s.getArtifactVersion)
	s.mux.HandleFunc("GET /api/v1/artifact-versions/{artifact_version_id}/lineage", s.getArtifactVersionLineage)
	s.mux.HandleFunc("GET /api/v1/artifact-versions/{artifact_version_id}/delivery", s.getArtifactDelivery)
	s.mux.HandleFunc("GET /api/v1/artifact-versions/{artifact_version_id}/download", s.downloadArtifactVersion)
	s.mux.HandleFunc("GET /api/v1/script-candidates/{candidate_id}", s.getScriptCandidate)
	s.mux.HandleFunc("POST /api/v1/script-candidates/{candidate_id}/exports", s.createScriptExport)
	s.mux.HandleFunc("GET /api/v1/exports/{export_id}", s.getScriptExport)
	s.mux.HandleFunc("GET /api/v1/exports/{export_id}/content", s.getScriptExportContent)
	s.mux.HandleFunc("GET /api/v1/impact-reviews/{impact_review_id}", s.getImpactReview)
	s.mux.HandleFunc("POST /api/v1/impact-reviews/{impact_review_id}/resolve", s.resolveImpactReview)
	s.mux.HandleFunc("GET /api/v1/regeneration-plans/{regeneration_plan_id}", s.getRegenerationPlan)
	s.mux.HandleFunc("GET /api/v1/approvals/{approval_request_id}", s.getApproval)
	s.mux.HandleFunc("GET /api/v1/approvals/{approval_request_id}/revision-targets", s.getApprovalRevisionTargets)
	s.mux.HandleFunc("POST /api/v1/approvals/{approval_request_id}/resolutions", s.resolveApproval)
	s.mux.HandleFunc("POST /api/v1/approvals/{approval_request_id}/regeneration-requests", s.requestApprovalRegeneration)
	s.mux.HandleFunc("GET /api/v1/quality-reviews/{quality_review_id}", s.getQualityReview)
	s.mux.HandleFunc("POST /api/v1/quality-reviews/{quality_review_id}/actions", s.resolveQualityReviewAction)
}

type projectRunSnapshot struct {
	businessruntime.RunSnapshot
	TaskItems []businessruntime.TaskItem `json:"task_items"`
}

func (s *Server) runSnapshotWithTasks(
	ctx context.Context,
	snapshot businessruntime.RunSnapshot,
) (projectRunSnapshot, error) {
	tasks := make([]businessruntime.TaskItem, 0)
	for _, step := range snapshot.Steps {
		stepTasks, err := s.runtime.ListTaskItems(ctx, step.StepRunID)
		if err != nil {
			return projectRunSnapshot{}, err
		}
		tasks = append(tasks, stepTasks...)
	}
	return projectRunSnapshot{RunSnapshot: snapshot, TaskItems: tasks}, nil
}

func (s *Server) writeRunSnapshot(
	writer http.ResponseWriter,
	ctx context.Context,
	status int,
	snapshot businessruntime.RunSnapshot,
) {
	hydrated, err := s.runSnapshotWithTasks(ctx, snapshot)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, status, map[string]any{"data": hydrated})
}

type projectSnapshot struct {
	Project                businessruntime.Project            `json:"project"`
	Goal                   *businessruntime.ProjectGoal       `json:"goal"`
	Messages               []businessruntime.Message          `json:"messages"`
	Artifacts              []businessruntime.Artifact         `json:"artifacts"`
	Approvals              []businessruntime.Approval         `json:"approvals"`
	Capabilities           any                                `json:"capabilities"`
	ArtifactPresentations  []capability.ArtifactPresentation  `json:"artifact_presentations"`
	ScriptCandidates       []businessruntime.ScriptCandidate  `json:"script_candidates"`
	FinalSelection         *businessruntime.FinalSelection    `json:"final_selection"`
	ActiveRun              *projectRunSnapshot                `json:"active_run"`
	LatestRun              *projectRunSnapshot                `json:"latest_run"`
	PendingProposedActions []businessruntime.ProposedAction   `json:"pending_proposed_actions"`
	ProposedActions        []businessruntime.ProposedAction   `json:"proposed_actions"`
	Activities             []businessruntime.ProjectActivity  `json:"activities"`
	RevisionRequests       []businessruntime.RevisionRequest  `json:"revision_requests"`
	TargetResolutions      []businessruntime.TargetResolution `json:"target_resolutions"`
	AgentTurns             []businessruntime.AgentTurn        `json:"agent_turns"`
	AgentTasks             []businessruntime.AgentTask        `json:"agent_tasks"`
	AgentToolCalls         []businessruntime.AgentToolCall    `json:"agent_tool_calls"`
}

func (s *Server) listPendingProposedActions(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListPendingProposedActions(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getProjectSnapshot(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.loadProjectSnapshot(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": snapshot})
}

func (s *Server) loadProjectSnapshot(ctx context.Context, projectID string) (projectSnapshot, error) {
	project, err := s.runtime.GetProject(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	messages, err := s.runtime.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil {
		return projectSnapshot{}, err
	}
	artifacts, err := s.runtime.ListArtifactsByProject(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	approvals, err := s.runtime.ListApprovals(ctx, projectID, "pending")
	if err != nil {
		return projectSnapshot{}, err
	}
	candidates, err := s.runtime.ListScriptCandidates(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	selection, err := s.runtime.GetCurrentFinalSelection(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	proposedActions, err := s.runtime.ListPendingProposedActions(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	allProposedActions, err := s.runtime.ListProjectProposedActions(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	activities, err := s.runtime.ListProjectActivities(ctx, projectID, 0)
	if err != nil {
		return projectSnapshot{}, err
	}
	revisionRequests, err := s.runtime.ListRevisionRequests(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	targetResolutions, err := s.runtime.ListTargetResolutions(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	agentTurns, err := s.runtime.ListProjectAgentTurns(ctx, projectID, 50)
	if err != nil {
		return projectSnapshot{}, err
	}
	agentTasks, err := s.runtime.ListAgentTasks(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	agentToolCalls, err := s.runtime.ListAgentToolCalls(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	goal, err := s.runtime.GetActiveProjectGoal(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	var activeRun *projectRunSnapshot
	if project.ActiveWriteRunID != nil {
		run, runErr := s.runtime.GetRunSnapshot(ctx, *project.ActiveWriteRunID)
		if runErr != nil {
			return projectSnapshot{}, runErr
		}
		hydrated, hydrateErr := s.runSnapshotWithTasks(ctx, run)
		if hydrateErr != nil {
			return projectSnapshot{}, hydrateErr
		}
		activeRun = &hydrated
	}
	var latestRun *projectRunSnapshot
	latestSnapshot, latestErr := s.runtime.GetLatestRunSnapshotByProject(ctx, projectID)
	if latestErr != nil {
		return projectSnapshot{}, latestErr
	}
	if latestSnapshot != nil {
		hydrated, hydrateErr := s.runSnapshotWithTasks(ctx, *latestSnapshot)
		if hydrateErr != nil {
			return projectSnapshot{}, hydrateErr
		}
		latestRun = &hydrated
	}
	registry, err := s.runtime.CapabilityRegistryForProject(ctx, projectID)
	if err != nil {
		return projectSnapshot{}, err
	}
	return projectSnapshot{
		Project:                project,
		Goal:                   goal,
		Messages:               messages,
		Artifacts:              artifacts,
		Approvals:              approvals,
		Capabilities:           registry.PublicList(),
		ArtifactPresentations:  registry.PublicArtifactPresentations(),
		ScriptCandidates:       candidates,
		FinalSelection:         selection,
		ActiveRun:              activeRun,
		LatestRun:              latestRun,
		PendingProposedActions: proposedActions,
		ProposedActions:        allProposedActions,
		Activities:             activities,
		RevisionRequests:       revisionRequests,
		TargetResolutions:      targetResolutions,
		AgentTurns:             agentTurns,
		AgentTasks:             agentTasks,
		AgentToolCalls:         agentToolCalls,
	}, nil
}

func (s *Server) getCurrentQualityReview(writer http.ResponseWriter, request *http.Request) {
	review, err := s.runtime.GetCurrentQualityReview(
		request.Context(),
		request.PathValue("run_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": review})
}

func (s *Server) getQualityReview(writer http.ResponseWriter, request *http.Request) {
	review, err := s.runtime.GetQualityReview(
		request.Context(),
		request.PathValue("quality_review_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": review})
}

func (s *Server) resolveQualityReviewAction(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedReviewStatus string   `json:"expected_review_status"`
		InputSnapshotHash    string   `json:"input_snapshot_hash"`
		Action               string   `json:"action"`
		Instruction          string   `json:"instruction"`
		IgnoredIssueIDs      []string `json:"ignored_issue_ids"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	review, err := s.runtime.GetQualityReview(
		request.Context(),
		request.PathValue("quality_review_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, review.ProjectID, "resolve_quality_review_action", body)
	if !ok {
		return
	}
	result, err := s.runtime.ResolveQualityReviewAction(
		request.Context(),
		businessruntime.ResolveQualityReviewActionCommand{
			CommandMeta:          meta,
			QualityReviewID:      review.QualityReviewID,
			ExpectedReviewStatus: body.ExpectedReviewStatus,
			InputSnapshotHash:    body.InputSnapshotHash,
			Action:               body.Action,
			Instruction:          body.Instruction,
			ActorRef:             identity.ActorRefFromContext(request.Context()),
			IgnoredIssueIDs:      body.IgnoredIssueIDs,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) listScriptCandidates(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListScriptCandidates(
		request.Context(),
		request.PathValue("project_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getScriptCandidate(writer http.ResponseWriter, request *http.Request) {
	candidate, err := s.runtime.GetScriptCandidate(
		request.Context(),
		request.PathValue("candidate_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": candidate})
}

func (s *Server) createScriptExport(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Format            string `json:"format"`
		ArtifactVersionID string `json:"artifact_version_id"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	candidateID := request.PathValue("candidate_id")
	candidate, err := s.runtime.GetScriptCandidate(request.Context(), candidateID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, candidate.ProjectID, "create_script_export", body)
	if !ok {
		return
	}
	result, err := s.runtime.CreateScriptExport(request.Context(), businessruntime.CreateScriptExportCommand{
		CommandMeta: meta, CandidateID: candidateID, ArtifactVersionID: body.ArtifactVersionID,
		Format: body.Format,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) getScriptExport(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.GetScriptExport(request.Context(), request.PathValue("export_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getScriptExportContent(writer http.ResponseWriter, request *http.Request) {
	content, err := s.runtime.OpenScriptExportContent(request.Context(), request.PathValue("export_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	defer content.File.Close()
	writer.Header().Set("Content-Type", content.Export.ContentType)
	writer.Header().Set("Content-Length", strconv.FormatInt(content.Export.SizeBytes, 10))
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(content.Export.Filename)))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	if _, err := io.Copy(writer, content.File); err != nil {
		s.logger.Error("stream script export", "export_id", content.Export.ExportID, "error", err)
	}
}

func (s *Server) getCurrentFinalSelection(writer http.ResponseWriter, request *http.Request) {
	selection, err := s.runtime.GetCurrentFinalSelection(
		request.Context(),
		request.PathValue("project_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": selection})
}

func (s *Server) createFinalSelectionPreview(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CandidateID                string  `json:"candidate_id"`
		ExpectedArtifactVersionID  string  `json:"expected_artifact_version_id,omitempty"`
		ExpectedCurrentSelectionID *string `json:"expected_current_selection_id"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	projectID := request.PathValue("project_id")
	meta, ok := commandMeta(
		writer,
		request,
		projectID,
		"create_final_selection_preview",
		body,
	)
	if !ok {
		return
	}
	result, err := s.runtime.CreateFinalSelectionPreview(
		request.Context(),
		businessruntime.CreateFinalSelectionPreviewCommand{
			CommandMeta:                meta,
			ProjectID:                  projectID,
			CandidateID:                body.CandidateID,
			ExpectedArtifactVersionID:  body.ExpectedArtifactVersionID,
			ExpectedCurrentSelectionID: body.ExpectedCurrentSelectionID,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) confirmFinalSelection(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CandidateID                string  `json:"candidate_id"`
		PreviewHash                string  `json:"preview_hash"`
		ExpectedCurrentSelectionID *string `json:"expected_current_selection_id"`
		Confirmed                  bool    `json:"confirmed"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	projectID := request.PathValue("project_id")
	meta, ok := commandMeta(
		writer,
		request,
		projectID,
		"confirm_final_selection",
		body,
	)
	if !ok {
		return
	}
	result, err := s.runtime.ConfirmFinalSelection(
		request.Context(),
		businessruntime.ConfirmFinalSelectionCommand{
			CommandMeta:                meta,
			ProjectID:                  projectID,
			CandidateID:                body.CandidateID,
			PreviewHash:                body.PreviewHash,
			ExpectedCurrentSelectionID: body.ExpectedCurrentSelectionID,
			Confirmed:                  body.Confirmed,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) createUploadSession(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Items []businessruntime.UploadItemSpec `json:"items"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("project_id"), "create_upload_session", body)
	if !ok {
		return
	}
	session, err := s.runtime.CreateUploadSessionCommand(
		request.Context(),
		request.PathValue("project_id"),
		body.Items,
		meta,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": session})
}

func (s *Server) getUploadSession(writer http.ResponseWriter, request *http.Request) {
	session, err := s.runtime.GetUploadSession(request.Context(), request.PathValue("upload_session_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": session})
}

func (s *Server) writeUploadContent(writer http.ResponseWriter, request *http.Request) {
	itemID := request.PathValue("upload_item_id")
	projectID, err := s.runtime.UploadItemProjectID(request.Context(), itemID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := streamCommandMeta(writer, request, projectID, "write_upload_content")
	if !ok {
		return
	}
	// Runtime still enforces the tighter per-kind limits. This outer bound also
	// permits a ZIP transport containing multiple individually valid assets.
	request.Body = http.MaxBytesReader(writer, request.Body, (1<<30)+1)
	item, err := s.runtime.WriteUploadContentCommand(request.Context(), itemID, request.Body, meta)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) completeUploadItem(writer http.ResponseWriter, request *http.Request) {
	itemID := request.PathValue("upload_item_id")
	projectID, err := s.runtime.UploadItemProjectID(request.Context(), itemID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, projectID, "complete_upload_item", struct{}{})
	if !ok {
		return
	}
	result, err := s.runtime.CompleteUploadItemCommand(request.Context(), itemID, meta)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) listAssets(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListAssets(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getAsset(writer http.ResponseWriter, request *http.Request) {
	asset, err := s.runtime.GetAsset(request.Context(), request.PathValue("asset_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": asset})
}

func (s *Server) getAssetContent(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "private, no-store")
	content, err := s.runtime.OpenAssetContent(
		request.Context(),
		request.PathValue("asset_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	defer content.File.Close()
	if snapshotID := request.URL.Query().Get("asset_snapshot_id"); snapshotID != "" &&
		(content.Asset.CurrentSnapshotID != snapshotID || content.Asset.Status != "available") {
		writeError(writer, http.StatusConflict, "ASSET_SNAPSHOT_CONFLICT", "材料快照已变化或不可用，请重新选择材料。")
		return
	}
	if request.URL.Query().Get("browser_compatible") == "1" {
		if content.Asset.Kind != "video" {
			writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "仅视频材料支持浏览器兼容预览。")
			return
		}
		preview, err := openBrowserVideoPreview(content)
		if err != nil {
			s.logger.Error("generate browser-compatible video preview", "asset_id", content.Asset.AssetID, "error", err)
			writeError(writer, http.StatusInternalServerError, "VIDEO_PREVIEW_FAILED", "视频兼容预览生成失败。")
			return
		}
		defer preview.Close()
		stat, err := preview.Stat()
		if err != nil {
			s.logger.Error("stat browser-compatible video preview", "asset_id", content.Asset.AssetID, "error", err)
			writeError(writer, http.StatusInternalServerError, "VIDEO_PREVIEW_FAILED", "视频兼容预览读取失败。")
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(writer, request, content.Asset.OriginalFilename, stat.ModTime(), preview)
		return
	}
	contentType := content.Asset.DetectedMIMEType
	if content.Asset.Kind == "video" &&
		(content.Asset.DeclaredMIMEType == "video/mp4" || content.Asset.DeclaredMIMEType == "video/quicktime") {
		// Short MP4/MOV headers may be detected as octet-stream. The declared type was
		// already checked against the file extension and sniffed content at upload.
		contentType = content.Asset.DeclaredMIMEType
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, content.Asset.OriginalFilename, content.Asset.UpdatedAt, content.File)
}

func (s *Server) getParsedAssetText(writer http.ResponseWriter, request *http.Request) {
	snapshotID := strings.TrimSpace(request.URL.Query().Get("asset_snapshot_id"))
	if snapshotID == "" {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "读取解析文本需要材料快照。")
		return
	}
	content, err := s.runtime.GetParsedAssetText(
		request.Context(),
		request.PathValue("asset_id"),
		snapshotID,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{
		"asset_id":          request.PathValue("asset_id"),
		"asset_snapshot_id": snapshotID,
		"content":           content,
	}})
}

func (s *Server) createAssetSet(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Purpose            string  `json:"purpose"`
		DisplayName        string  `json:"display_name"`
		CreatedByMessageID *string `json:"created_by_message_id"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	projectID := request.PathValue("project_id")
	meta, ok := commandMeta(writer, request, projectID, "create_asset_set", body)
	if !ok {
		return
	}
	result, err := s.runtime.CreateAssetSet(request.Context(), businessruntime.CreateAssetSetCommand{
		CommandMeta: meta, ProjectID: projectID, Purpose: body.Purpose,
		DisplayName: body.DisplayName, CreatedByMessageID: body.CreatedByMessageID,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) getAssetSet(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.GetAssetSet(request.Context(), request.PathValue("asset_set_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAssetSetVersion(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.GetAssetSetVersion(request.Context(), request.PathValue("asset_set_version_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) createAssetSetVersion(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion int                              `json:"expected_asset_set_version"`
		Changes         []businessruntime.AssetSetChange `json:"changes"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	assetSetID := request.PathValue("asset_set_id")
	assetSet, err := s.runtime.GetAssetSet(request.Context(), assetSetID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, assetSet.AssetSet.ProjectID, "create_asset_set_version", body)
	if !ok {
		return
	}
	result, err := s.runtime.CreateAssetSetVersion(request.Context(), businessruntime.CreateAssetSetVersionCommand{
		CommandMeta: meta, AssetSetID: assetSetID, ExpectedCurrentVersion: body.ExpectedVersion,
		Changes: body.Changes,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) checkAssetSet(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.CheckAssetSet(request.Context(), request.PathValue("asset_set_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) sealAssetSet(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion             int             `json:"expected_asset_set_version"`
		ContinuationPolicy          json.RawMessage `json:"continuation_policy"`
		UserConfirmedUploadComplete bool            `json:"user_confirmed_upload_complete"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	assetSetID := request.PathValue("asset_set_id")
	assetSet, err := s.runtime.GetAssetSet(request.Context(), assetSetID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, assetSet.AssetSet.ProjectID, "seal_asset_set", body)
	if !ok {
		return
	}
	result, err := s.runtime.SealAssetSet(request.Context(), businessruntime.SealAssetSetCommand{
		CommandMeta: meta, AssetSetID: assetSetID, ExpectedCurrentVersion: body.ExpectedVersion,
		ContinuationPolicy:          body.ContinuationPolicy,
		UserConfirmedUploadComplete: body.UserConfirmedUploadComplete,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) reopenAssetSet(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion int `json:"expected_asset_set_version"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	assetSetID := request.PathValue("asset_set_id")
	assetSet, err := s.runtime.GetAssetSet(request.Context(), assetSetID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, assetSet.AssetSet.ProjectID, "reopen_asset_set", body)
	if !ok {
		return
	}
	result, err := s.runtime.ReopenAssetSetCommand(request.Context(), businessruntime.ReopenAssetSetCommand{
		CommandMeta: meta, AssetSetID: assetSetID, ExpectedCurrentVersion: body.ExpectedVersion,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) createAssetDeletePreview(writer http.ResponseWriter, request *http.Request) {
	assetID := request.PathValue("asset_id")
	asset, err := s.runtime.GetAsset(request.Context(), assetID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(
		writer,
		request,
		asset.ProjectID,
		"create_asset_delete_preview",
		struct{}{},
	)
	if !ok {
		return
	}
	preview, err := s.runtime.CreateAssetDeletePreview(
		request.Context(),
		businessruntime.CreateAssetDeletePreviewCommand{
			CommandMeta: meta,
			AssetID:     assetID,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": preview})
}

func (s *Server) retryAssetParse(writer http.ResponseWriter, request *http.Request) {
	assetID := request.PathValue("asset_id")
	asset, err := s.runtime.GetAsset(request.Context(), assetID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, asset.ProjectID, "retry_asset_parse", struct{}{})
	if !ok {
		return
	}
	result, err := s.runtime.RetryAssetParse(
		request.Context(),
		businessruntime.RetryAssetParseCommand{CommandMeta: meta, AssetID: assetID},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) confirmAssetDelete(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		PreviewHash string `json:"preview_hash"`
		Confirmed   bool   `json:"confirmed"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	assetID := request.PathValue("asset_id")
	asset, err := s.runtime.GetAsset(request.Context(), assetID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(
		writer,
		request,
		asset.ProjectID,
		"confirm_asset_delete",
		body,
	)
	if !ok {
		return
	}
	result, err := s.runtime.ConfirmAssetDelete(
		request.Context(),
		businessruntime.ConfirmAssetDeleteCommand{
			CommandMeta: meta,
			AssetID:     assetID,
			PreviewHash: body.PreviewHash,
			Confirmed:   body.Confirmed,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": result})
}

func (s *Server) listProjects(writer http.ResponseWriter, request *http.Request) {
	projects, err := s.runtime.ListProjects(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": projects}})
}

func (s *Server) createProjectDeletePreview(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project_id")
	meta, ok := commandMeta(writer, request, projectID, "create_project_delete_preview", struct{}{})
	if !ok {
		return
	}
	preview, err := s.runtime.CreateProjectDeletePreview(
		request.Context(),
		businessruntime.CreateProjectDeletePreviewCommand{CommandMeta: meta, ProjectID: projectID},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": preview})
}

func (s *Server) confirmProjectDelete(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		PreviewHash string `json:"preview_hash"`
		Confirmed   bool   `json:"confirmed"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	projectID := request.PathValue("project_id")
	meta, ok := commandMeta(writer, request, projectID, "confirm_project_delete", body)
	if !ok {
		return
	}
	result, err := s.runtime.ConfirmProjectDelete(
		request.Context(),
		businessruntime.ConfirmProjectDeleteCommand{
			CommandMeta: meta,
			ProjectID:   projectID,
			PreviewHash: body.PreviewHash,
			Confirmed:   body.Confirmed,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": result})
}

func (s *Server) createProject(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, identity.WorkspaceIDFromContext(request.Context()), "create_project", body)
	if !ok {
		return
	}
	project, err := s.runtime.CreateProjectCommand(request.Context(), body.Title, meta)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": project})
}

func (s *Server) getProject(writer http.ResponseWriter, request *http.Request) {
	project, err := s.runtime.GetProject(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": project})
}

func (s *Server) getProjectGoal(writer http.ResponseWriter, request *http.Request) {
	goal, err := s.runtime.GetActiveProjectGoal(
		request.Context(), request.PathValue("project_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": goal})
}

func (s *Server) renameProject(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Title           string `json:"title"`
		ExpectedVersion int    `json:"expected_version"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("project_id"), "rename_project", body)
	if !ok {
		return
	}
	project, err := s.runtime.RenameProjectCommand(
		request.Context(),
		request.PathValue("project_id"),
		body.Title,
		body.ExpectedVersion,
		meta,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": project})
}

func (s *Server) listConversations(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListConversations(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) listAgentTurns(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListProjectAgentTurns(
		request.Context(), request.PathValue("project_id"), 50,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getAgentTurn(writer http.ResponseWriter, request *http.Request) {
	turn, err := s.runtime.GetAgentTurn(request.Context(), request.PathValue("agent_turn_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": turn})
}

func (s *Server) updateQueuedAgentTurn(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Content         *string `json:"content"`
		ExpectedContent *string `json:"expected_content"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	if body.Content == nil || body.ExpectedContent == nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "缺少消息内容或原内容。")
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("agent_turn_id"), "update_queued_agent_turn", body)
	if !ok {
		return
	}
	turn, err := s.runtime.UpdateQueuedAgentTurn(request.Context(), businessruntime.UpdateQueuedAgentTurnCommand{
		CommandMeta: meta, AgentTurnID: request.PathValue("agent_turn_id"), Content: *body.Content, ExpectedContent: *body.ExpectedContent,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": turn})
}

func (s *Server) cancelAgentTurn(writer http.ResponseWriter, request *http.Request) {
	meta, ok := commandMeta(
		writer, request, request.PathValue("agent_turn_id"), "cancel_agent_turn", map[string]any{},
	)
	if !ok {
		return
	}
	_ = meta // The turn state machine itself is already idempotent by resource state.
	result, err := s.runtime.CancelAgentTurn(request.Context(), request.PathValue("agent_turn_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if result.Accepted && result.Turn.Status == "cancel_requested" && s.agentTurns != nil {
		s.agentTurns.cancel(result.Turn.AgentTurnID)
	}
	if s.agentTurns != nil {
		s.agentTurns.notify()
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) pauseAgentTurn(writer http.ResponseWriter, request *http.Request) {
	s.controlAgentTurnPause(writer, request, false)
}

func (s *Server) appendAgentTurnInput(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Content        string                        `json:"content"`
		AttachmentRefs []agentcontract.AttachmentRef `json:"attachment_refs,omitempty"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("agent_turn_id"), "append_agent_turn_input", body)
	if !ok {
		return
	}
	input, err := s.runtime.AppendAgentTurnInput(request.Context(), businessruntime.AppendAgentTurnInputCommand{CommandMeta: meta, AgentTurnID: request.PathValue("agent_turn_id"), Content: body.Content, AttachmentRefs: body.AttachmentRefs})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if s.agentTurns != nil {
		s.agentTurns.notify()
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": input})
}

func (s *Server) resumeAgentTurn(writer http.ResponseWriter, request *http.Request) {
	s.controlAgentTurnPause(writer, request, true)
}

func (s *Server) controlAgentTurnPause(writer http.ResponseWriter, request *http.Request, resume bool) {
	var body struct{}
	if !decodeBody(writer, request, &body) {
		return
	}
	action := "pause_agent_turn"
	if resume {
		action = "resume_agent_turn"
	}
	meta, ok := commandMeta(writer, request, request.PathValue("agent_turn_id"), action, body)
	if !ok {
		return
	}
	command := businessruntime.ControlAgentTurnCommand{CommandMeta: meta, AgentTurnID: request.PathValue("agent_turn_id")}
	var turn businessruntime.AgentTurn
	var err error
	if resume {
		turn, err = s.runtime.ResumeAgentTurn(request.Context(), command)
	} else {
		turn, err = s.runtime.RequestAgentTurnPause(request.Context(), command)
	}
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if s.agentTurns != nil {
		s.agentTurns.notify()
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": turn})
}

func (s *Server) createMessage(writer http.ResponseWriter, request *http.Request) {
	var body agentcontract.MessageRequest
	if !decodeBody(writer, request, &body) {
		return
	}
	// The client may identify the historical record visible in the workbench,
	// but it never controls the server-owned active Run.
	normalizeViewedContext(&body.ClientContext)
	key := request.Header.Get("Idempotency-Key")
	if !uuidPattern.MatchString(key) {
		writeError(writer, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "写请求必须携带 UUID 格式的 Idempotency-Key。")
		return
	}
	projectID, prior, err := s.runtime.LookupAgentTurnSubmission(request.Context(), request.PathValue("conversation_id"), key)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, projectID, "create_message", body)
	if !ok {
		return
	}
	if prior != nil {
		matches, err := messageSubmissionMatches(request, projectID, body, meta.RequestHash, *prior)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
		if !matches {
			writeError(writer, http.StatusBadRequest, "IDEMPOTENCY_KEY_REUSED", "该 Idempotency-Key 已用于不同请求。")
			return
		}
		writeJSON(writer, http.StatusAccepted, map[string]any{"data": prior.Turn})
		return
	}
	// Freeze the client's request before resolving any current project material.
	meta.RequestHash = messageRequestHashPrefix + meta.RequestHash
	if _, err := s.runtime.PreflightMessage(request.Context(), request.PathValue("conversation_id"), body); err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if !s.agentRollout.Allows(identity.WorkspaceIDFromContext(request.Context())) {
		writeError(
			writer, http.StatusServiceUnavailable, "SDK_AGENT_ROLLOUT_DISABLED",
			"当前工作区暂未开放 Agent 执行，请稍后重试。",
		)
		return
	}
	if body.CapabilityRef != nil && len(body.AttachmentRefs) == 0 {
		body.AttachmentRefs, err = s.resolveSingleDurableCapabilityMaterial(
			request.Context(), projectID, body.CapabilityRef.CapabilityID,
		)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
	}
	if s.agentTurns == nil {
		writeError(
			writer, http.StatusServiceUnavailable, "SDK_AGENT_RUNTIME_UNAVAILABLE",
			"Agent 执行服务尚未启动，请稍后重试。",
		)
		return
	}
	turn, err := s.runtime.AcceptAgentTurn(
		request.Context(), request.PathValue("conversation_id"), body, meta,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.agentTurns.notify()
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": turn})
}

func (s *Server) commitInternalAgentTurn(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		ProjectID      string                       `json:"project_id"`
		ConversationID string                       `json:"conversation_id"`
		AgentTurnID    string                       `json:"agent_turn_id"`
		Request        agentcontract.MessageRequest `json:"request"`
		Decision       agentcontract.AgentDecision  `json:"decision"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	if !validateAgentActivityTarget(writer, request, body.ProjectID, body.AgentTurnID, "", "", "") {
		return
	}
	normalizeViewedContext(&body.Request.ClientContext)
	projectID, err := s.runtime.PreflightMessage(request.Context(), body.ConversationID, body.Request)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if projectID != strings.TrimSpace(body.ProjectID) {
		writeError(writer, http.StatusBadRequest, "PROJECT_CONTEXT_MISMATCH", "对话不属于指定作品。")
		return
	}
	if body.Request.CapabilityRef != nil && len(body.Request.AttachmentRefs) == 0 {
		body.Request.AttachmentRefs, err = s.resolveSingleDurableCapabilityMaterial(
			request.Context(), projectID, body.Request.CapabilityRef.CapabilityID,
		)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
	}
	runtimeContext, err := s.runtime.GetAgentRuntimeContextForRequest(request.Context(), projectID, body.Request)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, projectID, "commit_sdk_agent_turn", body)
	if !ok {
		return
	}
	if body.AgentTurnID != "" {
		turn, turnErr := s.runtime.GetAgentTurn(request.Context(), body.AgentTurnID)
		if turnErr != nil {
			s.handleRuntimeError(writer, turnErr)
			return
		}
		storedRequest, _ := json.Marshal(turn.Request)
		submittedRequest, _ := json.Marshal(body.Request)
		if turn.ProjectID != projectID || turn.ConversationID != body.ConversationID ||
			turn.IdempotencyKey != meta.IdempotencyKey || string(storedRequest) != string(submittedRequest) {
			writeError(writer, http.StatusBadRequest, "AGENT_TURN_CONTEXT_MISMATCH", "Agent Turn 与提交上下文不一致。")
			return
		}
		if _, beginErr := s.runtime.BeginAgentTurnCommit(request.Context(), body.AgentTurnID); beginErr != nil {
			s.handleRuntimeError(writer, beginErr)
			return
		}
	}
	exchange, err := s.commitAgentDecision(
		request.Context(), meta, projectID, body.ConversationID,
		body.Request, body.Decision, runtimeContext,
	)
	if err != nil {
		if body.AgentTurnID != "" {
			_, _ = s.runtime.FailAgentTurn(
				request.Context(), body.AgentTurnID, "AGENT_COMMIT_FAILED", "保存 Agent 结果失败，请重试。",
			)
		}
		s.handleRuntimeError(writer, err)
		return
	}
	if body.AgentTurnID != "" {
		if _, err := s.runtime.CompleteAgentTurnCommit(request.Context(), body.AgentTurnID, exchange); err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": exchange})
}

func authorizeInternalAgent(writer http.ResponseWriter, request *http.Request) bool {
	token := strings.TrimSpace(os.Getenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN"))
	expected := "Bearer " + token
	if token == "" {
		writeError(writer, http.StatusServiceUnavailable, "INTERNAL_AGENT_DISABLED", "内部 Agent 写入通道未启用。")
		return false
	}
	if !hmac.Equal([]byte(request.Header.Get("Authorization")), []byte(expected)) {
		writeError(writer, http.StatusUnauthorized, "INTERNAL_AGENT_UNAUTHORIZED", "内部 Agent 凭据无效。")
		return false
	}
	return true
}

func (s *Server) commitAgentDecision(
	ctx context.Context,
	meta businessruntime.CommandMeta,
	projectID string,
	conversationID string,
	body agentcontract.MessageRequest,
	decision agentcontract.AgentDecision,
	runtimeContext agentcontract.RuntimeContext,
) (businessruntime.MessageExchange, error) {
	exchange, err := s.runtime.CreateMessageExchange(
		ctx,
		businessruntime.CreateMessageExchangeCommand{
			CommandMeta: meta, ConversationID: conversationID, Request: body, Decision: decision,
		},
	)
	if err != nil {
		return businessruntime.MessageExchange{}, err
	}
	if exchange.Decision.Decision.Intent == "create_artifact" {
		drafts := slices.Clone(exchange.Decision.Decision.ArtifactDrafts)
		if exchange.Decision.Decision.ArtifactDraft != nil {
			drafts = append([]agentcontract.ArtifactDraft{*exchange.Decision.Decision.ArtifactDraft}, drafts...)
		}
		artifacts := make([]businessruntime.Artifact, 0, len(drafts))
		for index, draft := range drafts {
			artifactMeta := meta
			artifactMeta.CommandType = "create_generic_artifact"
			artifactMeta.IdempotencyKey += fmt.Sprintf(":generic_artifact:%d", index)
			originType := "agent_turn"
			originID := exchange.Decision.AgentDecisionID
			capabilityID := "agent_shell"
			if exchange.Invocation != nil {
				originType = "invocation"
				originID = exchange.Invocation.SkillInvocationID
				capabilityID = exchange.Invocation.CapabilityID
			}
			artifact, createErr := s.runtime.CreateGenericArtifact(
				ctx,
				businessruntime.CreateGenericArtifactCommand{
					CommandMeta: artifactMeta, ProjectID: projectID,
					ConversationID: conversationID,
					Draft:          draft,
					SourceArtifactVersionIDs: slices.Clone(
						exchange.Decision.Decision.SourceArtifactVersionIDs,
					),
					OriginType: originType, OriginID: originID, CapabilityID: capabilityID,
				},
			)
			if createErr != nil {
				s.logger.Error("create generic artifact", "project_id", projectID, "error", createErr)
				reply := "内容已生成，但保存产物失败，请重试本次请求。"
				var domain *businessruntime.DomainError
				if errors.As(createErr, &domain) {
					reply = domain.Message
				}
				if replyErr := s.runtime.UpdateAgentReply(
					ctx, exchange.AgentMessage.MessageID, exchange.Decision.AgentDecisionID, reply,
				); replyErr != nil {
					return businessruntime.MessageExchange{}, errors.Join(createErr, replyErr)
				}
				exchange.AgentMessage.Content = reply
				exchange.Decision.Decision.Reply = reply
				return exchange, nil
			}
			artifacts = append(artifacts, artifact)
		}
		exchange.GenericArtifacts = artifacts
		if len(artifacts) > 0 {
			exchange.GenericArtifact = &artifacts[len(artifacts)-1]
		}
	}
	if exchange.Decision.Decision.Intent == "regenerate" && exchange.Resolution != nil &&
		exchange.Resolution.Status == "resolved" && exchange.Resolution.ArtifactID != nil {
		regenerationMeta := meta
		regenerationMeta.CommandType = "request_targeted_regeneration"
		regenerationMeta.IdempotencyKey += ":targeted_regeneration"
		result, regenerationErr := s.runtime.RequestTargetedRegeneration(
			ctx,
			businessruntime.RequestTargetedRegenerationCommand{
				CommandMeta:       regenerationMeta,
				ProjectID:         projectID,
				ArtifactID:        *exchange.Resolution.ArtifactID,
				RegenerationScope: regenerationScope(body.Content),
				Instruction:       body.Content,
				ActorRef:          identity.ActorRefFromContext(ctx),
			},
		)
		reply := "已从原始材料开始重新处理指定范围，其他范围保持不变。"
		if regenerationScope(body.Content) == "step" {
			reply = "已从原始材料开始重新处理当前步骤的全部内容。"
		}
		if regenerationErr != nil {
			s.logger.Error("request targeted regeneration", "artifact_id", *exchange.Resolution.ArtifactID, "error", regenerationErr)
			reply = "未能开始重新处理。"
			var domain *businessruntime.DomainError
			if errors.As(regenerationErr, &domain) {
				reply = domain.Message
			}
		} else {
			resumeMeta := meta
			resumeMeta.CommandType = "resume_run"
			resumeMeta.IdempotencyKey += ":resume_targeted_regeneration"
			resumed, resumeErr := s.runtime.ResumeRun(ctx, businessruntime.ResumeRunCommand{
				CommandMeta: resumeMeta,
				RunID:       result.RunSnapshot.Run.RunID,
			})
			if resumeErr != nil {
				s.logger.Error("resume targeted regeneration", "run_id", result.RunSnapshot.Run.RunID, "error", resumeErr)
				reply = "已创建重新处理任务，但未能自动启动，请点击继续运行。"
			} else {
				result.RunSnapshot = resumed
			}
			exchange.Regeneration = &result
		}
		if replyErr := s.runtime.UpdateAgentReply(
			ctx, exchange.AgentMessage.MessageID, exchange.Decision.AgentDecisionID, reply,
		); replyErr != nil {
			s.logger.Error("finalize targeted regeneration reply", "error", replyErr)
		} else {
			exchange.AgentMessage.Content = reply
			exchange.Decision.Decision.Reply = reply
		}
	}
	if exchange.Revision != nil && exchange.Revision.Status == "queued" && s.revisions != nil {
		revised, executeErr := s.revisions.Execute(ctx, exchange.Revision.RevisionRequestID)
		if executeErr != nil {
			s.logger.Error("execute revision request", "revision_request_id", exchange.Revision.RevisionRequestID, "error", executeErr)
			if current, loadErr := s.runtime.GetRevisionRequest(ctx, exchange.Revision.RevisionRequestID); loadErr == nil {
				exchange.Revision = &current
			}
		} else {
			exchange.Revision = &revised
			if runtimeContext.ActiveRun == nil && body.SelectionSnapshot != nil && revised.Status == "proposed" {
				autoAcceptMeta := meta
				autoAcceptMeta.CommandType = "accept_revision"
				autoAcceptMeta.IdempotencyKey += ":auto_accept_revision"
				accepted, acceptErr := s.runtime.AcceptRevision(ctx, businessruntime.AcceptRevisionCommand{
					CommandMeta:       autoAcceptMeta,
					RevisionRequestID: revised.RevisionRequestID,
					ExpectedVersion:   revised.Version,
					ActorRef:          identity.ActorRefFromContext(ctx),
				})
				if acceptErr != nil {
					s.logger.Error("auto accept selected revision", "revision_request_id", revised.RevisionRequestID, "error", acceptErr)
				} else {
					exchange.Revision = &accepted.Revision
					if refreshErr := s.completeAcceptedRevisionHandoff(ctx, autoAcceptMeta, &accepted); refreshErr != nil {
						s.logger.Error("complete auto accepted revision handoff refresh", "revision_request_id", revised.RevisionRequestID, "error", refreshErr)
					} else if replyErr := s.runtime.MarkAutomaticRevisionApplied(
						ctx,
						revised.RevisionRequestID,
						exchange.AgentMessage.MessageID,
						exchange.Decision.AgentDecisionID,
					); replyErr != nil {
						s.logger.Error("finalize automatic revision reply", "revision_request_id", revised.RevisionRequestID, "error", replyErr)
					} else {
						const appliedReply = "已按你的要求完成修改，并保存为新的产物版本。"
						exchange.AgentMessage.Content = appliedReply
						exchange.Decision.Decision.Reply = appliedReply
					}
				}
			}
		}
		if exchange.Revision != nil {
			if reply := terminalRevisionReply(*exchange.Revision); reply != "" {
				if replyErr := s.runtime.UpdateAgentReply(
					ctx, exchange.AgentMessage.MessageID, exchange.Decision.AgentDecisionID, reply,
				); replyErr != nil {
					s.logger.Error("finalize revision reply", "revision_request_id", exchange.Revision.RevisionRequestID, "error", replyErr)
				} else {
					exchange.AgentMessage.Content = reply
					exchange.Decision.Decision.Reply = reply
				}
			}
		}
	}
	return exchange, nil
}

func terminalRevisionReply(revision businessruntime.RevisionRequest) string {
	switch revision.Status {
	case "accepted":
		return ""
	case "proposed":
		return "修改稿已生成，等待你确认后保存。"
	case "cancelled":
		return "本次未生成有效修改，原版本未受影响。请补充更具体的修改要求后重试。"
	case "failed":
		return "修改稿生成失败，原版本未受影响。可重试本次修改。"
	default:
		return ""
	}
}

// A material uploaded to a project remains available independently of the
// conversation turn that created it. When there is exactly one compatible
// source, bind it to a later explicit Skill invocation. Multiple candidates
// remain unbound so the workbench can ask the user which source to use.
func (s *Server) resolveSingleDurableCapabilityMaterial(
	ctx context.Context,
	projectID string,
	capabilityID string,
) ([]agentcontract.AttachmentRef, error) {
	registry, err := s.runtime.CapabilityRegistryForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	entry, ok := registry.Get(capabilityID)
	if !ok || entry.Status != capability.Available || entry.Definition == nil {
		return []agentcontract.AttachmentRef{}, nil
	}
	definition := entry.Definition
	if strings.TrimSpace(definition.InputBinding.AssetSetPurpose) != "" {
		return []agentcontract.AttachmentRef{}, nil
	}
	assets, err := s.runtime.ListAssets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	candidates := make([]agentcontract.AttachmentRef, 0, len(assets))
	for _, asset := range assets {
		if asset.Status != "available" || asset.ParseStatus != "completed" {
			continue
		}
		if slices.Contains(definition.AcceptedAssetKinds, asset.Kind) {
			candidates = append(candidates, agentcontract.AttachmentRef{
				AssetID: asset.AssetID, AssetSnapshotID: asset.CurrentSnapshotID,
			})
		}
	}
	if len(candidates) == 1 {
		return candidates, nil
	}
	return []agentcontract.AttachmentRef{}, nil
}

func normalizeViewedContext(client *agentcontract.ClientContext) {
	if client.ViewedRunID == nil {
		client.ViewedRunID = client.CurrentRunID
	}
	if client.ViewedCapabilityID == nil {
		client.ViewedCapabilityID = client.CurrentCapabilityID
	}
	client.CurrentRunID = nil
	client.CurrentCapabilityID = nil
}

func (s *Server) listMessages(writer http.ResponseWriter, request *http.Request) {
	limit := 0
	if rawLimit := strings.TrimSpace(request.URL.Query().Get("limit")); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			writeError(writer, http.StatusBadRequest, "INVALID_MESSAGE_LIMIT", "limit 必须是 1 到 100 的整数。")
			return
		}
		limit = parsed
	}
	items, err := s.runtime.SearchMessages(
		request.Context(),
		request.PathValue("conversation_id"),
		request.URL.Query().Get("q"),
		limit,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) configureProposedAction(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion int             `json:"expected_version"`
		Input           json.RawMessage `json:"input"`
		Config          json.RawMessage `json:"config"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	action, err := s.runtime.GetProposedAction(
		request.Context(),
		request.PathValue("proposed_action_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, action.ProjectID, "configure_proposed_action", body)
	if !ok {
		return
	}
	configured, err := s.runtime.ConfigureProposedAction(
		request.Context(),
		businessruntime.ConfigureProposedActionCommand{
			CommandMeta:      meta,
			ProposedActionID: action.ProposedActionID,
			ExpectedVersion:  body.ExpectedVersion,
			Input:            body.Input,
			Config:           body.Config,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": configured})
}

func (s *Server) bindProposedActionInput(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion int             `json:"expected_version"`
		Input           json.RawMessage `json:"input"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	action, err := s.runtime.GetProposedAction(request.Context(), request.PathValue("proposed_action_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, action.ProjectID, "bind_proposed_action_input", body)
	if !ok {
		return
	}
	bound, err := s.runtime.BindProposedActionInput(request.Context(), businessruntime.BindProposedActionInputCommand{
		CommandMeta: meta, ProposedActionID: action.ProposedActionID,
		ExpectedVersion: body.ExpectedVersion, Input: body.Input,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": bound})
}

func (s *Server) startRun(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CapabilityID      string          `json:"capability_id"`
		CapabilityVersion string          `json:"capability_version"`
		RunKind           string          `json:"run_kind"`
		ConversationID    string          `json:"conversation_id"`
		Input             json.RawMessage `json:"input"`
		Config            json.RawMessage `json:"config"`
		Confirmation      struct {
			Confirmed        bool   `json:"confirmed"`
			ProposedActionID string `json:"proposed_action_id"`
			ActionVersion    int    `json:"action_version"`
			MessageID        string `json:"confirmation_message_id"`
			SnapshotHash     string `json:"snapshot_hash"`
		} `json:"confirmation"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("project_id"), "start_run", body)
	if !ok {
		return
	}
	snapshot, err := s.runtime.StartRun(request.Context(), businessruntime.StartRunCommand{
		CommandMeta:              meta,
		ProjectID:                request.PathValue("project_id"),
		ConversationID:           body.ConversationID,
		CapabilityID:             body.CapabilityID,
		CapabilityVersion:        body.CapabilityVersion,
		RunKind:                  body.RunKind,
		Input:                    body.Input,
		Config:                   body.Config,
		Confirmed:                body.Confirmation.Confirmed,
		ProposedActionID:         body.Confirmation.ProposedActionID,
		ProposedActionVersion:    body.Confirmation.ActionVersion,
		ConfirmationMessageID:    body.Confirmation.MessageID,
		ConfirmationSnapshotHash: body.Confirmation.SnapshotHash,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusCreated, snapshot)
}

func (s *Server) getRun(writer http.ResponseWriter, request *http.Request) {
	run, err := s.runtime.GetRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": run})
}

func (s *Server) getRunSnapshot(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.runtime.GetRunSnapshot(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusOK, snapshot)
}

func (s *Server) pauseRun(writer http.ResponseWriter, request *http.Request) {
	run, err := s.runtime.GetRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "pause_run", struct{}{})
	if !ok {
		return
	}
	snapshot, err := s.runtime.RequestRunPause(request.Context(), businessruntime.PauseRunCommand{
		CommandMeta: meta,
		RunID:       run.RunID,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusAccepted, snapshot)
}

func (s *Server) resumeRun(writer http.ResponseWriter, request *http.Request) {
	run, err := s.runtime.GetRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "resume_run", struct{}{})
	if !ok {
		return
	}
	snapshot, err := s.runtime.ResumeRun(request.Context(), businessruntime.ResumeRunCommand{
		CommandMeta: meta,
		RunID:       run.RunID,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusOK, snapshot)
}

func (s *Server) setEpisodeExecutionMode(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	run, err := s.runtime.GetRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "set_episode_execution_mode", body)
	if !ok {
		return
	}
	snapshot, err := s.runtime.SetEpisodeExecutionMode(request.Context(), businessruntime.SetEpisodeExecutionModeCommand{
		CommandMeta: meta,
		RunID:       run.RunID,
		Mode:        body.Mode,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusOK, snapshot)
}

func (s *Server) cancelRun(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Confirmation struct {
			Confirmed bool `json:"confirmed"`
		} `json:"confirmation"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	run, err := s.runtime.GetRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "cancel_run", body)
	if !ok {
		return
	}
	snapshot, err := s.runtime.CancelRun(request.Context(), businessruntime.CancelRunCommand{
		CommandMeta: meta,
		RunID:       run.RunID,
		Confirmed:   body.Confirmation.Confirmed,
		ActorRef:    identity.ActorRefFromContext(request.Context()),
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusOK, snapshot)
}

func (s *Server) retryFailedStep(writer http.ResponseWriter, request *http.Request) {
	stepRunID := request.PathValue("step_run_id")
	step, err := s.runtime.GetStepRun(request.Context(), stepRunID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	run, err := s.runtime.GetRun(request.Context(), step.RunID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "retry_failed_step", struct{}{})
	if !ok {
		return
	}
	snapshot, err := s.runtime.RetryFailedStep(
		request.Context(),
		businessruntime.RetryFailedStepCommand{
			CommandMeta: meta,
			StepRunID:   stepRunID,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusAccepted, snapshot)
}

func (s *Server) continueWithPartialResults(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Confirmation struct {
			Confirmed bool `json:"confirmed"`
		} `json:"confirmation"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	stepRunID := request.PathValue("step_run_id")
	step, err := s.runtime.GetStepRun(request.Context(), stepRunID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	run, err := s.runtime.GetRun(request.Context(), step.RunID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "continue_with_partial_results", body)
	if !ok {
		return
	}
	snapshot, err := s.runtime.ContinueWithPartialResults(
		request.Context(),
		businessruntime.ContinueWithPartialResultsCommand{
			CommandMeta: meta,
			StepRunID:   stepRunID,
			Confirmed:   body.Confirmation.Confirmed,
			ActorRef:    identity.ActorRefFromContext(request.Context()),
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	s.writeRunSnapshot(writer, request.Context(), http.StatusOK, snapshot)
}

func (s *Server) listRunSteps(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListStepRuns(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) listStepTasks(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListTaskItems(request.Context(), request.PathValue("step_run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) claimExecutionTask(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		WorkerID     string   `json:"worker_id"`
		ExecutorIDs  []string `json:"executor_ids"`
		ProviderID   string   `json:"provider_id"`
		ModelID      string   `json:"model_id"`
		LeaseSeconds int      `json:"lease_seconds"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	claim, err := s.runtime.ClaimExecutionTask(request.Context(), businessruntime.ClaimExecutionTaskCommand{
		WorkerID:     body.WorkerID,
		ExecutorIDs:  body.ExecutorIDs,
		ProviderID:   body.ProviderID,
		ModelID:      body.ModelID,
		LeaseSeconds: body.LeaseSeconds,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if claim == nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": claim})
}

func (s *Server) pauseExecutionForApproval(writer http.ResponseWriter, request *http.Request) {
	s.saveExecutionCheckpoint(writer, request, false)
}

func (s *Server) pauseExecutionAtNativeBoundary(writer http.ResponseWriter, request *http.Request) {
	s.saveExecutionCheckpoint(writer, request, true)
}

func (s *Server) saveExecutionCheckpoint(writer http.ResponseWriter, request *http.Request, userPause bool) {
	var command businessruntime.PauseExecutionForApprovalCommand
	if !decodeBodyWithLimit(writer, request, &command, agentcontract.MaxSDKRunStateEnvelopeBytes+businessruntime.MaxExecutionWorkerStateBytes) {
		return
	}
	command.AttemptID = request.PathValue("attempt_id")
	command.AttemptToken = request.Header.Get("X-Attempt-Token")
	command.UserPause = userPause
	attempt, err := s.runtime.PauseExecutionForApproval(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": attempt})
}

func (s *Server) heartbeatExecutionAttempt(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ProgramStatus     string `json:"program_status"`
		InputSnapshotHash string `json:"input_snapshot_hash"`
		LeaseSeconds      int    `json:"lease_seconds"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	attempt, err := s.runtime.HeartbeatExecutionAttempt(request.Context(), businessruntime.HeartbeatExecutionAttemptCommand{
		ProgramStatus: body.ProgramStatus,
		AttemptID:     request.PathValue("attempt_id"), AttemptToken: request.Header.Get("X-Attempt-Token"),
		InputSnapshotHash: body.InputSnapshotHash, LeaseSeconds: body.LeaseSeconds,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": attempt})
}

func (s *Server) submitExecutionResult(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		InputSnapshotHash string          `json:"input_snapshot_hash"`
		ResponsePayload   json.RawMessage `json:"response_payload"`
		Usage             json.RawMessage `json:"usage"`
		TraceRef          string          `json:"trace_ref"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	attempt, err := s.runtime.SubmitExecutionResult(request.Context(), businessruntime.SubmitExecutionResultCommand{
		AttemptID:         request.PathValue("attempt_id"),
		AttemptToken:      request.Header.Get("X-Attempt-Token"),
		InputSnapshotHash: body.InputSnapshotHash,
		ResponsePayload:   body.ResponsePayload,
		Usage:             body.Usage,
		TraceRef:          body.TraceRef,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": attempt})
}

func (s *Server) failExecutionAttempt(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		InputSnapshotHash string                                  `json:"input_snapshot_hash"`
		ErrorCode         string                                  `json:"error_code"`
		FailureDetail     *businessruntime.ExecutionFailureDetail `json:"failure_detail"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	result, err := s.runtime.FailExecutionAttempt(
		request.Context(),
		businessruntime.FailExecutionAttemptCommand{
			AttemptID:         request.PathValue("attempt_id"),
			AttemptToken:      request.Header.Get("X-Attempt-Token"),
			InputSnapshotHash: body.InputSnapshotHash,
			ErrorCode:         body.ErrorCode,
			FailureDetail:     body.FailureDetail,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) commitExecutionResult(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedResponseHash string `json:"expected_response_hash"`
		RepairOutput         bool   `json:"repair_output"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	result, err := s.runtime.CommitExecutionResult(
		request.Context(),
		businessruntime.CommitExecutionResultCommand{
			AttemptID:            request.PathValue("attempt_id"),
			ExpectedResponseHash: body.ExpectedResponseHash,
			AttemptToken:         request.Header.Get("X-Attempt-Token"),
			RepairOutput:         body.RepairOutput,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) completeRunPause(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		TaskCursor json.RawMessage `json:"task_cursor"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	snapshot, err := s.runtime.CompleteRunPauseAtSafeBoundary(
		request.Context(),
		businessruntime.CompleteRunPauseCommand{
			RunID:      request.PathValue("run_id"),
			TaskCursor: body.TaskCursor,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": snapshot})
}

func (s *Server) listProjectArtifacts(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListArtifactsByProject(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getArtifact(writer http.ResponseWriter, request *http.Request) {
	artifact, err := s.runtime.GetArtifact(request.Context(), request.PathValue("artifact_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": artifact})
}

func (s *Server) listArtifactVersions(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListArtifactVersions(request.Context(), request.PathValue("artifact_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getArtifactVersion(writer http.ResponseWriter, request *http.Request) {
	version, err := s.runtime.GetArtifactVersion(request.Context(), request.PathValue("artifact_version_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": version})
}

func (s *Server) getArtifactVersionLineage(writer http.ResponseWriter, request *http.Request) {
	maxDepth := 0
	if rawDepth := request.URL.Query().Get("max_depth"); rawDepth != "" {
		parsed, err := strconv.Atoi(rawDepth)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "max_depth 必须是整数。")
			return
		}
		maxDepth = parsed
	}
	lineage, err := s.runtime.GetArtifactVersionLineage(
		request.Context(),
		request.PathValue("artifact_version_id"),
		maxDepth,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": lineage})
}

func (s *Server) createArtifactVersion(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		BaseVersionID string          `json:"base_version_id"`
		BaseVersion   int             `json:"base_version"`
		ChangeMode    string          `json:"change_mode"`
		NewPayload    json.RawMessage `json:"new_payload"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	if body.ChangeMode != "full_payload" {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "当前增量只支持 full_payload 保存。")
		return
	}
	artifact, err := s.runtime.GetArtifact(request.Context(), request.PathValue("artifact_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, artifact.ProjectID, "create_artifact_version", body)
	if !ok {
		return
	}
	result, err := s.runtime.ApplyArtifactVersion(request.Context(), businessruntime.CreateVersionCommand{
		CommandMeta:   meta,
		ArtifactID:    request.PathValue("artifact_id"),
		BaseVersionID: body.BaseVersionID,
		BaseVersion:   body.BaseVersion,
		ChangeMode:    "whole_artifact",
		NewPayload:    body.NewPayload,
		ActorRef:      identity.ActorRefFromContext(request.Context()),
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) completeScriptEdit(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedScriptVersionIDs []string `json:"expected_script_version_ids"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	run, err := s.runtime.GetRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, run.ProjectID, "complete_script_edit", body)
	if !ok {
		return
	}
	result, err := s.runtime.CompleteScriptEdit(
		request.Context(),
		businessruntime.CompleteScriptEditCommand{
			CommandMeta:              meta,
			RunID:                    request.PathValue("run_id"),
			ExpectedScriptVersionIDs: body.ExpectedScriptVersionIDs,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": result})
}

func (s *Server) getImpactReview(writer http.ResponseWriter, request *http.Request) {
	review, err := s.runtime.GetImpactReview(
		request.Context(),
		request.PathValue("impact_review_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": review})
}

func (s *Server) resolveImpactReview(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Action       string `json:"action"`
		SnapshotHash string `json:"snapshot_hash"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	review, err := s.runtime.GetImpactReview(
		request.Context(),
		request.PathValue("impact_review_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(
		writer,
		request,
		review.ProjectID,
		"resolve_impact_review",
		body,
	)
	if !ok {
		return
	}
	result, err := s.runtime.ResolveImpactReview(
		request.Context(),
		businessruntime.ResolveImpactReviewCommand{
			CommandMeta:    meta,
			ImpactReviewID: request.PathValue("impact_review_id"),
			Action:         body.Action,
			SnapshotHash:   body.SnapshotHash,
			ActorRef:       identity.ActorRefFromContext(request.Context()),
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getRegenerationPlan(writer http.ResponseWriter, request *http.Request) {
	plan, err := s.runtime.GetRegenerationPlan(
		request.Context(),
		request.PathValue("regeneration_plan_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": plan})
}

func (s *Server) listProjectApprovals(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListApprovals(
		request.Context(),
		request.PathValue("project_id"),
		request.URL.Query().Get("status"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getApproval(writer http.ResponseWriter, request *http.Request) {
	approval, err := s.runtime.GetApproval(request.Context(), request.PathValue("approval_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": approval})
}

func (s *Server) resolveApproval(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Action                  string          `json:"action"`
		ExpectedApprovalVersion int             `json:"expected_approval_version"`
		SubjectSnapshotHash     string          `json:"subject_snapshot_hash"`
		ResolutionPayload       json.RawMessage `json:"resolution_payload"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	approval, err := s.runtime.GetApproval(request.Context(), request.PathValue("approval_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, approval.ProjectID, "resolve_approval", body)
	if !ok {
		return
	}
	snapshot, err := s.runtime.ResolveApproval(request.Context(), businessruntime.ResolveApprovalCommand{
		CommandMeta:             meta,
		ApprovalRequestID:       request.PathValue("approval_request_id"),
		Action:                  body.Action,
		ExpectedApprovalVersion: body.ExpectedApprovalVersion,
		SubjectSnapshotHash:     body.SubjectSnapshotHash,
		ResolutionPayload:       body.ResolutionPayload,
		ActorRef:                identity.ActorRefFromContext(request.Context()),
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": snapshot})
}

func (s *Server) getApprovalRevisionTargets(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.GetApprovalRevisionTargets(request.Context(), request.PathValue("approval_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) requestApprovalRegeneration(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Action                  string `json:"action"`
		Instruction             string `json:"instruction"`
		ExpectedApprovalVersion int    `json:"expected_approval_version"`
		SubjectSnapshotHash     string `json:"subject_snapshot_hash"`
		TargetArtifactVersionID string `json:"target_artifact_version_id,omitempty"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	if body.TargetArtifactVersionID != "" && body.Action != "request_ai_revision" {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "只有局部返修可以指定单个产物版本。")
		return
	}
	approvalID := request.PathValue("approval_request_id")
	approval, err := s.runtime.GetApproval(request.Context(), approvalID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, approval.ProjectID, "request_approval_regeneration", body)
	if !ok {
		return
	}
	command := businessruntime.RequestApprovalRegenerationCommand{
		CommandMeta: meta, ApprovalRequestID: approvalID,
		ExpectedApprovalVersion: body.ExpectedApprovalVersion,
		SubjectSnapshotHash:     body.SubjectSnapshotHash, Action: body.Action,
		TargetArtifactVersionID: body.TargetArtifactVersionID,
		Instruction:             body.Instruction, ActorRef: identity.ActorRefFromContext(request.Context()),
	}
	if body.Action == "request_ai_revision" {
		result, revisionErr := s.runtime.RequestApprovalRevision(request.Context(), command)
		if revisionErr != nil {
			s.handleRuntimeError(writer, revisionErr)
			return
		}
		writeJSON(writer, http.StatusAccepted, map[string]any{"data": result})
		return
	}
	result, err := s.runtime.RequestApprovalRegeneration(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": result})
}

func decodeBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	return decodeBodyWithLimit(writer, request, target, maxJSONBodyBytes)
}

func decodeBodyWithLimit(writer http.ResponseWriter, request *http.Request, target any, limit int64) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "请求 JSON 无效。")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "请求只能包含一个 JSON 对象。")
		return false
	}
	return true
}

func (s *Server) handleRuntimeError(writer http.ResponseWriter, err error) {
	var domain *businessruntime.DomainError
	if !errors.As(err, &domain) {
		s.logger.Error("runtime request failed", "error", err)
	}
	writeRuntimeError(writer, err)
}

func regenerationScope(content string) string {
	normalized := strings.ToLower(strings.TrimSpace(content))
	for _, cue := range []string{"全部", "都重新", "所有", "整批", "全批", "这批"} {
		if strings.Contains(normalized, cue) {
			return "step"
		}
	}
	return "artifact"
}

func commandMeta(
	writer http.ResponseWriter,
	request *http.Request,
	scope string,
	commandType string,
	body any,
) (businessruntime.CommandMeta, bool) {
	key := request.Header.Get("Idempotency-Key")
	if !uuidPattern.MatchString(key) {
		writeError(writer, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "写请求必须携带 UUID 格式的 Idempotency-Key。")
		return businessruntime.CommandMeta{}, false
	}
	hash, err := commandRequestHash(request, scope, body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "请求无法规范化。")
		return businessruntime.CommandMeta{}, false
	}
	return businessruntime.CommandMeta{
		Scope: scope, CommandType: commandType, IdempotencyKey: key, RequestHash: hash,
	}, true
}

func commandRequestHash(request *http.Request, scope string, body any) (string, error) {
	canonicalBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	clientInstance := request.Header.Get("X-Client-Instance-ID")
	if clientInstance == "" {
		clientInstance = "shared_internal_client"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%s",
		request.Method,
		request.URL.Path,
		scope,
		string(canonicalBody),
		clientInstance,
	)))
	return fmt.Sprintf("%x", sum[:]), nil
}

func streamCommandMeta(
	writer http.ResponseWriter,
	request *http.Request,
	scope string,
	commandType string,
) (businessruntime.CommandMeta, bool) {
	key := request.Header.Get("Idempotency-Key")
	if !uuidPattern.MatchString(key) {
		writeError(writer, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "写请求必须携带 UUID 格式的 Idempotency-Key。")
		return businessruntime.CommandMeta{}, false
	}
	clientInstance := request.Header.Get("X-Client-Instance-ID")
	if clientInstance == "" {
		clientInstance = "shared_internal_client"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\n%s\n%s\n%s",
		request.Method,
		request.URL.Path,
		scope,
		clientInstance,
	)))
	return businessruntime.CommandMeta{
		Scope:          scope,
		CommandType:    commandType,
		IdempotencyKey: key,
		RequestHash:    fmt.Sprintf("%x", sum[:]),
	}, true
}
