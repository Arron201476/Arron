import type { AgentComposerContext, AgentTask, AgentToolApproval, AgentTurn, AgentTurnCancelResult, Approval, Artifact, ArtifactVersion, Asset, AssetResult, AssetSetSnapshot, AttachmentRef, AvailableAction, Capability, CapabilityDefinition, ComposerRegistryEntry, FinalSelection, Message, Principal, Project, ProjectDeletePreview, ProjectSnapshot, ProjectWorkspaceProjection, ProposedAction, QualityReview, RevisionAcceptResult, RevisionRequest, RunSnapshot, ScriptCandidate, ScriptExport, ScriptSandboxPolicy, SkillInstallAttempt, SkillInstallation, SkillInstallTarget, TaskItem, UploadSession, VersionResult, WorkspaceProjection } from "./types";
import { messageContextPayload } from "./contextTargeting";
import type { MemoryGenerationPage, MemoryGenerationSummary } from "./components/agent/MemoryGenerations";
import type { MemorySource, MemorySourcePage, MemoryQueueReceipt } from "./components/agent/MemorySourcePicker";
import type { MemoryPreferencesValue, MemoryPreferencesUpdate } from "./components/agent/MemoryPreferences";
import { createUUID } from "./uuid";
import type { AgentMemoryDocument, AgentMemoryUpdate, AgentMemoryProposal, AgentMemoryToolProposal } from "./types";
import { runActionTargetMatches } from "./runActions";
import type { AgentToolConfiguration, AgentToolDescriptor, SkillDirectoryUpdate } from "./types";
import type { ProjectFile, ProjectFileContent, ProjectSkillDraft, ProjectSkillInstallArguments, ProjectSkillInstallResult } from "./types";
import type { ArtifactDelivery } from "./types";
import type { FinalSelectionPreviewResult, FinalSelectionResult } from "./types";
import type { AgentTaskInput, AgentTurnInput } from "./types";
import type { ExecutionInput, ExecutionInputsView, ExecutionInputAttemptPage, InputChangeScope, InputChangeReceipt } from "./types";
import type { MCPConnectionInventory, MCPConnectionState, MCPConnectionUpdate } from "./types";
import type { AgentInstructionDocument, AgentInstructionProposal, AgentInstructionUpdate, AgentInstructionView } from "./types";
import type { AgentSubtaskResult, AgentToolOutcomeReview, AgentToolOutcomeCommand } from "./types";
import type { SkillLifecycleResult, SkillPackageResult } from "./types";
import type { ApprovalRevisionTargets } from "./types";

type Envelope<T> = { data: T };

function skillLifecycleExpected(installation: SkillInstallation) {
  return { active_version_id: installation.active_version_id ?? "", enabled: installation.enabled, status: installation.status, event_count: installation.events.length };
}

export class ApiError extends Error {
  constructor(public code: string, message: string, public status: number) {
    super(message);
  }
}

function clientInstanceID() {
  let value = createUUID();
  try {
    const storage = globalThis.sessionStorage;
    value = storage?.getItem("content-agent-client-id") ?? value;
    storage?.setItem("content-agent-client-id", value);
  } catch { /* Read-only storage can still provide the original client ID. */ }
  return value;
}
// Transport identity; individual writes also carry their original command key.
const clientID = clientInstanceID();
export const messageClientInstanceID = () => clientID;

export function fileKind(file: Pick<File, "name" | "type">): "video" | "image" | "text" | "document" | "archive" {
  const extension = file.name.toLowerCase().match(/\.[^.]+$/)?.[0] ?? "";
  if (extension === ".zip" || file.type === "application/zip" || file.type === "application/x-zip-compressed") return "archive";
  if (file.type.startsWith("video/") || [".mp4", ".mov"].includes(extension)) return "video";
  if (file.type.startsWith("image/") || [".jpg", ".jpeg", ".png", ".webp"].includes(extension)) return "image";
  if ([".docx", ".pdf"].includes(extension)) return "document";
  return "text";
}

export function uploadMIME(file: Pick<File, "name" | "type">): string {
  const extension = file.name.toLowerCase().match(/\.[^.]+$/)?.[0] ?? "";
  const types: Record<string, string> = { ".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv", ".json": "application/json", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".pdf": "application/pdf", ".zip": "application/zip", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".mp4": "video/mp4", ".mov": "video/quicktime" };
  return types[extension] ?? (file.type || "application/octet-stream");
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (typeof init?.body === "string" && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (init?.method && init.method !== "GET") {
    if (!headers.has("Idempotency-Key")) headers.set("Idempotency-Key", createUUID());
    if (!headers.has("X-Client-Instance-ID")) headers.set("X-Client-Instance-ID", clientID);
  }
  const requestInit = { ...init, headers };
  let response: Response;
  try {
    response = await fetch(path, requestInit);
  } catch (error) {
    if (init?.signal?.aborted || (error instanceof DOMException && error.name === "AbortError")) throw error;
    response = await fetch(path, requestInit).catch(() => { throw error; });
  }
  if (!init?.signal?.aborted && [502, 503, 504].includes(response.status)) response = await fetch(path, requestInit);
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    throw new ApiError(payload?.error?.code ?? "REQUEST_FAILED", payload?.error?.message ?? "请求失败，请稍后重试。", response.status);
  }
  return (payload as Envelope<T>).data;
}

export const api = {
  assetDownloadURL: (assetID: string) => `/api/v1/assets/${encodeURIComponent(assetID)}/download`,
  getArtifactDelivery: (versionID: string, signal?: AbortSignal) => request<ArtifactDelivery>(`/api/v1/artifact-versions/${encodeURIComponent(versionID)}/delivery`, { signal }),
  downloadArtifactVersion: async (versionID: string, format: string, signal?: AbortSignal) => {
    const response = await fetch(`/api/v1/artifact-versions/${encodeURIComponent(versionID)}/download?${new URLSearchParams({ format })}`, { signal });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      throw new ApiError(payload?.error?.code ?? "DOWNLOAD_FAILED", payload?.error?.message ?? "下载失败，请重试。", response.status);
    }
    return response.blob();
  },
  getCurrentPrincipal: () => request<Principal>("/api/v1/auth/me"),
  createAuthSession: (token: string) => request<Principal>("/api/v1/auth/session", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  }),
  deleteAuthSession: () => request<null>("/api/v1/auth/session", { method: "DELETE" }),
  listProjects: async () => (await request<{ items: Project[] }>("/api/v1/projects")).items,
  createProject: (title: string) => request<Project>("/api/v1/projects", { method: "POST", body: JSON.stringify({ title }) }),
  getProject: (id: string) => request<Project>(`/api/v1/projects/${id}`),
  getProjectSnapshot: (id: string) => request<ProjectSnapshot>(`/api/v1/projects/${id}/snapshot`),
  listProjectFiles: (id: string, signal?: AbortSignal) => request<ProjectFile[]>(`/api/v1/projects/${encodeURIComponent(id)}/files`, { signal }),
  readProjectFile: (id: string, path: string, version: number, offset = 0, signal?: AbortSignal) => request<ProjectFileContent>(
    `/api/v1/projects/${encodeURIComponent(id)}/files/read?${new URLSearchParams({ path, version: String(version), offset: String(offset), limit: "16000" })}`, { signal },
  ),
  projectFileDownloadURL: (id: string, path: string, version: number) => `/api/v1/projects/${encodeURIComponent(id)}/files/content?${new URLSearchParams({ path, version: String(version) })}`,
  previewProjectSkillDraft: (id: string, root: string, signal?: AbortSignal) => request<ProjectSkillDraft>(`/api/v1/projects/${encodeURIComponent(id)}/skill-drafts/preview?${new URLSearchParams({ root_path: root })}`, { signal }),
  projectSkillDraftDownloadURL: (id: string, root: string, hash: string) => `/api/v1/projects/${encodeURIComponent(id)}/skill-drafts/archive?${new URLSearchParams({ root_path: root, snapshot_hash: hash })}`,
  installProjectSkillDraft: (id: string, args: ProjectSkillInstallArguments, idempotencyKey: string) => request<ProjectSkillInstallResult>(`/api/v1/projects/${encodeURIComponent(id)}/skill-drafts/installations`, {
    method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify({ ...args, confirmed: true }),
  }),
  getWorkspaceProjection: () => request<WorkspaceProjection>("/api/v1/workspace-projection"),
  getProjectWorkspaceProjection: (id: string) => request<ProjectWorkspaceProjection>(`/api/v1/projects/${id}/workspace-projection`),
  getProjectCapability: (projectID: string, capabilityID: string, version: string, proposedActionID?: string) => request<CapabilityDefinition>(
    `/api/v1/projects/${projectID}/capabilities/${encodeURIComponent(capabilityID)}?${new URLSearchParams({ version, ...(proposedActionID ? { proposed_action_id: proposedActionID } : {}) })}`,
  ),
  renameProject: (project: Project, title: string) => request<Project>(`/api/v1/projects/${project.project_id}`, {
    method: "PATCH",
    body: JSON.stringify({ title, expected_version: project.version }),
  }),
  previewProjectDelete: (id: string) => request<ProjectDeletePreview>(`/api/v1/projects/${id}/delete-previews`, { method: "POST" }),
  confirmProjectDelete: (id: string, previewHash: string) => request(`/api/v1/projects/${id}/delete-confirmations`, {
    method: "POST",
    body: JSON.stringify({ preview_hash: previewHash, confirmed: true }),
  }),
  listMessages: async (conversationID: string) => (await request<{ items: Message[] }>(`/api/v1/conversations/${conversationID}/messages`)).items,
  listArtifacts: async (projectID: string) => (await request<{ items: Artifact[] }>(`/api/v1/projects/${projectID}/artifacts`)).items,
  listAssets: async (projectID: string) => (await request<{ items: Asset[] }>(`/api/v1/projects/${projectID}/assets`)).items,
  getArtifactVersion: (versionID: string) => request<ArtifactVersion>(`/api/v1/artifact-versions/${versionID}`),
  listArtifactVersions: async (artifactID: string) => (await request<{ items: ArtifactVersion[] }>(`/api/v1/artifacts/${artifactID}/versions`)).items,
  createArtifactVersion: (artifact: Artifact, base: ArtifactVersion, newPayload: unknown, idempotencyKey?: string) => request<VersionResult>(`/api/v1/artifacts/${artifact.artifact_id}/versions`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ base_version_id: base.artifact_version_id, base_version: base.version, change_mode: "full_payload", new_payload: newPayload }),
  }),
  completeScriptEdit: (runID: string, expectedScriptVersionIDs: string[], idempotencyKey?: string) => request<{ run_snapshot: RunSnapshot; refresh_task_ids: string[]; pending_scopes: string[] }>(`/api/v1/runs/${runID}/script-edit-completions`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ expected_script_version_ids: expectedScriptVersionIDs }),
  }),
  listApprovals: async (projectID: string) => (await request<{ items: Approval[] }>(`/api/v1/projects/${projectID}/approvals?status=pending`)).items,
  resolveApproval: (approval: Approval, action: string, resolutionPayload?: unknown, idempotencyKey?: string) => request(`/api/v1/approvals/${approval.approval_request_id}/resolutions`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ action, expected_approval_version: approval.version, subject_snapshot_hash: approval.subject_snapshot_hash, resolution_payload: resolutionPayload }),
  }),
  resolveAgentToolApproval: (approval: AgentToolApproval, action: "approve" | "reject", idempotencyKey?: string) => request<AgentToolApproval>(`/api/v1/agent-tool-approvals/${encodeURIComponent(approval.agent_tool_approval_id)}/resolutions`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({
      expected_version: approval.version,
      subject_snapshot_hash: approval.subject_snapshot_hash,
      action,
    }),
  }),
  getApprovalRevisionTargets: (approvalID: string) => request<ApprovalRevisionTargets>(`/api/v1/approvals/${encodeURIComponent(approvalID)}/revision-targets`),
  requestApprovalRegeneration: <T = unknown>(approval: Approval, action: "request_ai_revision" | "regenerate_artifact", instruction = "", idempotencyKey?: string, targetArtifactVersionID?: string) => request<T>(`/api/v1/approvals/${approval.approval_request_id}/regeneration-requests`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({
      action,
      instruction,
      target_artifact_version_id: targetArtifactVersionID,
      expected_approval_version: approval.version,
      subject_snapshot_hash: approval.subject_snapshot_hash,
    }),
  }),
  getQualityReview: (reviewID: string) => request<QualityReview>(`/api/v1/quality-reviews/${reviewID}`),
  acceptQualityReviewRisk: (approval: Approval, idempotencyKey?: string) => request(`/api/v1/quality-reviews/${approval.subject_ref_id}/actions`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({
      expected_review_status: "action_required",
      input_snapshot_hash: approval.subject_snapshot_hash,
      action: "accept_with_risk",
      ignored_issue_ids: [],
    }),
  }),
  resolveQualityReviewAction: (approval: Approval, action: "ai_revise" | "confirm_change", instruction: string, idempotencyKey?: string) => request(`/api/v1/quality-reviews/${approval.subject_ref_id}/actions`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({
      expected_review_status: "action_required",
      input_snapshot_hash: approval.subject_snapshot_hash,
      action,
      instruction,
      ignored_issue_ids: [],
    }),
  }),
  listScriptCandidates: async (projectID: string) => (await request<{ items: ScriptCandidate[] }>(`/api/v1/projects/${projectID}/script-candidates`)).items,
  getCurrentFinalSelection: async (projectID: string) => {
    try { return await request<FinalSelection>(`/api/v1/projects/${projectID}/final-script-selection`); }
    catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error; }
  },
  previewFinalSelection: (projectID: string, candidateID: string, currentSelectionID: string | null, idempotencyKey?: string, expectedArtifactVersionID?: string) => request<FinalSelectionPreviewResult>(`/api/v1/projects/${encodeURIComponent(projectID)}/final-script-selection-previews`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ candidate_id: candidateID, expected_current_selection_id: currentSelectionID, expected_artifact_version_id: expectedArtifactVersionID }),
  }),
  confirmFinalSelection: (projectID: string, approval: Approval, currentSelectionID: string | null, idempotencyKey?: string) => request<FinalSelectionResult>(`/api/v1/projects/${encodeURIComponent(projectID)}/final-script-selections`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ candidate_id: approval.subject_ref_id, preview_hash: approval.subject_snapshot_hash, expected_current_selection_id: currentSelectionID, confirmed: true }),
  }),
  createScriptExport: (candidate: ScriptCandidate, format: "txt" | "docx", idempotencyKey?: string) => request<ScriptExport>(`/api/v1/script-candidates/${encodeURIComponent(candidate.candidate_id)}/exports`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ format, artifact_version_id: candidate.scripts_artifact_version_id }),
  }),
  downloadScriptExport: async (exportID: string, signal?: AbortSignal) => {
    const response = await fetch(`/api/v1/exports/${encodeURIComponent(exportID)}/content`, { signal });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      throw new ApiError(payload?.error?.code ?? "DOWNLOAD_FAILED", payload?.error?.message ?? "下载失败，请重试。", response.status);
    }
    return response.blob();
  },
  getRunSnapshot: (runID: string) => request<RunSnapshot>(`/api/v1/runs/${runID}/snapshot`),
	setEpisodeExecutionMode: (runID: string, mode: "continuous" | "review_each", idempotencyKey?: string) => request<RunSnapshot>(`/api/v1/runs/${runID}/episode-execution-mode`, {
		method: "PUT",
		headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
		body: JSON.stringify({ mode }),
	}),
  listStepTasks: async (stepRunID: string) => (await request<{ items: TaskItem[] }>(`/api/v1/steps/${stepRunID}/tasks`)).items,
  executeRunAction: async (runID: string, action: AvailableAction, idempotencyKey?: string) => {
    if (!runActionTargetMatches(runID, action)) throw new ApiError("RUN_STATE_CONFLICT", "操作目标与当前运行不一致，请刷新后重试。", 409);
    const headers = idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined;
    if (action.action_id === "pause_run") return request<RunSnapshot>(`/api/v1/runs/${runID}/pause`, { method: "POST", headers });
    if (action.action_id === "resume_run") return request<RunSnapshot>(`/api/v1/runs/${runID}/resume`, { method: "POST", headers });
    if (action.action_id === "cancel_run") return request<RunSnapshot>(`/api/v1/runs/${runID}/cancel`, { method: "POST", headers, body: JSON.stringify({ confirmation: { confirmed: true } }) });
    if (action.action_id === "retry_failed_step") return request<RunSnapshot>(`/api/v1/steps/${action.target_id}/retries`, { method: "POST", headers });
		if (action.action_id === "continue_with_partial_results") return request<RunSnapshot>(`/api/v1/steps/${action.target_id}/partial-continuations`, { method: "POST", headers, body: JSON.stringify({ confirmation: { confirmed: true } }) });
    throw new ApiError("ACTION_UNSUPPORTED", "当前操作暂不受支持。", 400);
  },
  configureProposedAction: (action: ProposedAction, input: Record<string, unknown>, config: Record<string, unknown>, idempotencyKey?: string) => request<ProposedAction>(`/api/v1/proposed-actions/${action.proposed_action_id}/configuration`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ expected_version: action.version, input, config }),
  }),
  bindProposedActionInput: (action: ProposedAction, input: Record<string, unknown>, idempotencyKey?: string) => request<ProposedAction>(`/api/v1/proposed-actions/${action.proposed_action_id}/input-binding`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ expected_version: action.version, input }),
  }),
  startRun: (project: Project, action: ProposedAction, idempotencyKey?: string) => request<RunSnapshot>(`/api/v1/projects/${project.project_id}/runs`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({
      capability_id: action.capability_ref.capability_id,
      capability_version: action.capability_ref.version,
      run_kind: "generation",
      conversation_id: project.primary_conversation_id,
      input: action.input,
      config: action.config,
      confirmation: { confirmed: true, proposed_action_id: action.proposed_action_id, action_version: action.version, confirmation_message_id: action.confirmation_message_id, snapshot_hash: action.snapshot_hash },
    }),
  }),
  listAgentTasks: async (projectID: string) => (await request<{ items: AgentTask[] }>(`/api/v1/projects/${projectID}/agent-tasks`)).items,
  startAgentTask: (project: Project, action: ProposedAction, idempotencyKey?: string) => request<AgentTask>(`/api/v1/proposed-actions/${action.proposed_action_id}/agent-task`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({
      capability_id: action.capability_ref.capability_id,
      capability_version: action.capability_ref.version,
      conversation_id: project.primary_conversation_id,
      input: action.input,
      config: action.config,
      confirmation: {
        confirmed: true,
        action_version: action.version,
        confirmation_message_id: action.confirmation_message_id,
        snapshot_hash: action.snapshot_hash,
      },
    }),
  }),
  cancelAgentTask: (taskID: string, idempotencyKey?: string) => request<AgentTask>(`/api/v1/agent-tasks/${encodeURIComponent(taskID)}/cancel`, { method: "POST", headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined }),
  pauseAgentTask: (taskID: string, idempotencyKey?: string) => request<AgentTask>(`/api/v1/agent-tasks/${encodeURIComponent(taskID)}/pause`, { method: "POST", headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined }),
  appendAgentTaskInput: (taskID: string, content: string, idempotencyKey?: string, attachments?: AttachmentRef[]) => request<AgentTaskInput>(`/api/v1/agent-tasks/${encodeURIComponent(taskID)}/inputs`, { method: "POST", headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined, body: JSON.stringify({ content, ...(attachments?.length ? { attachment_refs: attachments.map(({ asset_id, asset_snapshot_id }) => ({ asset_id, asset_snapshot_id })) } : {}) }) }),
  getExecutionInputs: (projectID: string, attemptID: string, signal?: AbortSignal) => request<ExecutionInputsView>(`/api/v1/projects/${encodeURIComponent(projectID)}/execution-attempts/${encodeURIComponent(attemptID)}/inputs`, { signal }),
  listExecutionInputAttempts: (projectID: string, runID: string, after = "", signal?: AbortSignal) => request<ExecutionInputAttemptPage>(`/api/v1/projects/${encodeURIComponent(projectID)}/runs/${encodeURIComponent(runID)}/input-attempts?after=${encodeURIComponent(after)}`, { signal }),
  changeExecutionInput: (scope: InputChangeScope, executionID: string, inputID: string, action: "withdraw" | "revise", content: string, key: string) => request<InputChangeReceipt>(`/api/v1/projects/${encodeURIComponent(scope.projectID)}/execution-input-changes`, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ mode: scope.mode, execution_id: executionID, input_id: inputID, action, content }) }),
  appendExecutionInput: (projectID: string, attemptID: string, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => request<ExecutionInput>(`/api/v1/projects/${encodeURIComponent(projectID)}/execution-attempts/${encodeURIComponent(attemptID)}/inputs`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify({ content, ...(attachments?.length ? { attachment_refs: attachments.map(({ asset_id, asset_snapshot_id }) => ({ asset_id, asset_snapshot_id })) } : {}) }) }),
  appendAgentTurnInput: (turnID: string, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => request<AgentTurnInput>(`/api/v1/agent-turns/${encodeURIComponent(turnID)}/inputs`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify({ content, ...(attachments?.length ? { attachment_refs: attachments.map(({ asset_id, asset_snapshot_id }) => ({ asset_id, asset_snapshot_id })) } : {}) }) }),
  resumeAgentTask: (taskID: string, idempotencyKey?: string) => request<AgentTask>(`/api/v1/agent-tasks/${encodeURIComponent(taskID)}/resume`, { method: "POST", headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined }),
  retryAgentTask: (taskID: string, idempotencyKey?: string) => request<AgentTask>(`/api/v1/agent-tasks/${encodeURIComponent(taskID)}/retry`, { method: "POST", headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined }),
  listCapabilities: async () => (await request<{ items: Capability[] }>("/api/v1/capabilities")).items,
  uploadFiles: async (projectID: string, files: File[]): Promise<AttachmentRef[]> => {
    const session = await request<UploadSession>(`/api/v1/projects/${projectID}/upload-sessions`, {
      method: "POST",
      body: JSON.stringify({ items: files.map((file, index) => ({ client_item_key: `${Date.now()}_${index}`, kind: fileKind(file), original_filename: file.name, declared_mime_type: uploadMIME(file), declared_size_bytes: file.size })) }),
    });
    const results: AttachmentRef[] = [];
    for (let index = 0; index < session.items.length; index += 1) {
      const item = session.items[index];
      const file = files[index];
      await request(`/api/v1/upload-items/${item.upload_item_id}/content`, { method: "PUT", headers: { "Content-Type": uploadMIME(file) }, body: file });
      const completed = await request<AssetResult>(`/api/v1/upload-items/${item.upload_item_id}/complete`, { method: "POST" });
      results.push({ asset_id: completed.asset.asset_id, asset_snapshot_id: completed.asset_snapshot.asset_snapshot_id, display_name: completed.asset.display_name || file.name, kind: completed.asset.kind, ignored_entries: completed.ignored_entries });
      for (const extracted of completed.extracted_assets ?? []) results.push({
        asset_id: extracted.asset.asset_id,
        asset_snapshot_id: extracted.asset_snapshot.asset_snapshot_id,
        display_name: extracted.asset.display_name,
        kind: extracted.asset.kind,
        hidden: true,
        container_asset_id: completed.asset.asset_id,
      });
    }
    return results;
  },
  appendVideoBatch: async (projectID: string, current: AssetSetSnapshot | null, attachments: AttachmentRef[], purpose = "video_reference_source", key = createUUID()) => {
    let snapshot = current;
    if (!snapshot) snapshot = await request<AssetSetSnapshot>(`/api/v1/projects/${encodeURIComponent(projectID)}/asset-sets`, { method: "POST", headers: { "Idempotency-Key": `${key}:create` }, body: JSON.stringify({ purpose, display_name: "参考视频", created_by_message_id: null }) });
    if (snapshot.asset_set.project_id !== projectID || snapshot.asset_set.status !== "collecting" || snapshot.asset_set.current_version_id !== snapshot.version.asset_set_version_id || snapshot.asset_set.current_version !== snapshot.version.version) throw new ApiError("ASSET_SET_VERSION_CONFLICT", "只能向当前作品正在收集的素材批次追加。", 409);
    const missing = attachments.filter((item, index) => !snapshot.members.some((member) => member.asset_id === item.asset_id) && attachments.findIndex((other) => other.asset_id === item.asset_id) === index);
    if (!missing.length) return snapshot;
    return request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/versions`, { method: "POST", headers: { "Idempotency-Key": `${key}:append` }, body: JSON.stringify({ expected_asset_set_version: snapshot.asset_set.current_version, changes: missing.map((item) => ({ operation: "add_asset", asset_id: item.asset_id })) }) });
  },
  getAssetSet: (id: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(id)}`),
  removeVideoBatchAsset: (snapshot: AssetSetSnapshot, assetID: string, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/versions`, {
    method: "POST", headers: key ? { "Idempotency-Key": key } : undefined,
    body: JSON.stringify({ expected_asset_set_version: snapshot.asset_set.current_version, changes: [{ operation: "remove_asset", asset_id: assetID }] }),
  }),
  setVideoBatchAssetIncluded: (snapshot: AssetSetSnapshot, assetID: string, included: boolean, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/versions`, {
    method: "POST", headers: key ? { "Idempotency-Key": key } : undefined,
    body: JSON.stringify({ expected_asset_set_version: snapshot.asset_set.current_version, changes: [{ operation: included ? "include_asset" : "exclude_asset", asset_id: assetID, ...(!included ? { exclusion_reason: "用户在批次中取消纳入" } : {}) }] }),
  }),
  reopenVideoBatch: (snapshot: AssetSetSnapshot, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/reopen`, {
    method: "POST", headers: key ? { "Idempotency-Key": key } : undefined,
    body: JSON.stringify({ expected_asset_set_version: snapshot.asset_set.current_version }),
  }),
  swapVideoEpisodes: (snapshot: AssetSetSnapshot, firstAssetID: string, firstOrder: number, secondAssetID: string, secondOrder: number, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/versions`, {
    method: "POST", headers: key ? { "Idempotency-Key": key } : undefined,
    body: JSON.stringify({
      expected_asset_set_version: snapshot.asset_set.current_version,
      changes: [
        { operation: "set_episode_order", asset_id: firstAssetID, episode_order: firstOrder },
        { operation: "set_episode_order", asset_id: secondAssetID, episode_order: secondOrder },
      ],
    }),
  }),
  setVideoEpisodeNo: (snapshot: AssetSetSnapshot, assetID: string, episodeNo: number, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/versions`, {
    method: "POST", headers: key ? { "Idempotency-Key": key } : undefined,
    body: JSON.stringify({
      expected_asset_set_version: snapshot.asset_set.current_version,
      changes: [{ operation: "set_episode_no", asset_id: assetID, episode_no: episodeNo }],
    }),
  }),
  setVideoEpisodeNumbers: (snapshot: AssetSetSnapshot, assignments: Array<{ assetID: string; episodeNo: number }>, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/versions`, {
    method: "POST", headers: key ? { "Idempotency-Key": key } : undefined,
    body: JSON.stringify({
      expected_asset_set_version: snapshot.asset_set.current_version,
      changes: assignments.map((item) => ({ operation: "set_episode_no", asset_id: item.assetID, episode_no: item.episodeNo })),
    }),
  }),
  sealVideoBatch: (snapshot: AssetSetSnapshot, allowIncomplete: boolean, key?: string) => request<AssetSetSnapshot>(`/api/v1/asset-sets/${encodeURIComponent(snapshot.asset_set.asset_set_id)}/seal`, { method: "POST", headers: key ? { "Idempotency-Key": key } : undefined, body: JSON.stringify({ expected_asset_set_version: snapshot.asset_set.current_version, continuation_policy: allowIncomplete ? { mode: "continue_incomplete", confirmed_by: "user", missing_episode_numbers: snapshot.version.completeness.missing_episode_numbers } : null, user_confirmed_upload_complete: true }) }),
  sendMessage: async (conversationID: string, content: string, capability: Capability | ComposerRegistryEntry | null, attachments: AttachmentRef[] = [], context: AgentComposerContext | null = null, signal?: AbortSignal, idempotencyKey?: string, originalClientID?: string) => {
    const contextPayload = await messageContextPayload(context);
    return request<AgentTurn>(`/api/v1/conversations/${conversationID}/messages`, {
      method: "POST",
      signal,
      headers: { ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}), ...(originalClientID ? { "X-Client-Instance-ID": originalClientID } : {}) },
      body: JSON.stringify({
      content,
      display_content: content,
      capability_ref: capability ? { capability_id: capability.capability_id, version: capability.version, selection_mode: "explicit" } : null,
      attachment_refs: attachments.map(({ asset_id, asset_snapshot_id, display_name, hidden, container_asset_id }) => ({ asset_id, asset_snapshot_id, display_name, hidden, container_asset_id })),
      ...contextPayload,
      }),
    });
  },
  getAgentTurn: (agentTurnID: string) => request<AgentTurn>(`/api/v1/agent-turns/${agentTurnID}`),
  updateQueuedAgentTurn: (turn: AgentTurn, content: string, idempotencyKey?: string) => request<AgentTurn>(`/api/v1/agent-turns/${encodeURIComponent(turn.agent_turn_id)}/queued-message`, {
    method: "PATCH",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ content, expected_content: turn.request.content }),
  }),
  cancelAgentTurn: (agentTurnID: string, idempotencyKey?: string) => request<AgentTurnCancelResult>(`/api/v1/agent-turns/${encodeURIComponent(agentTurnID)}/cancel`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({}),
  }),
  controlAgentTurnPause: (agentTurnID: string, action: "pause" | "resume", idempotencyKey: string) => request<AgentTurn>(`/api/v1/agent-turns/${encodeURIComponent(agentTurnID)}/${action}`, {
    method: "POST", body: JSON.stringify({}), headers: { "Idempotency-Key": idempotencyKey },
  }),
  executeRevision: (revisionID: string, idempotencyKey?: string, expectedVersion?: number) => request<RevisionRequest>(`/api/v1/revision-requests/${encodeURIComponent(revisionID)}/execute`, {
    method: "POST", headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: expectedVersion === undefined ? undefined : JSON.stringify({ expected_revision_version: expectedVersion }),
  }),
  resolveRevisionTarget: (resolutionID: string, candidateID: string, idempotencyKey?: string, expectedVersion?: number) => request<RevisionRequest>(`/api/v1/target-resolutions/${encodeURIComponent(resolutionID)}/resolve`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ candidate_id: candidateID, expected_revision_version: expectedVersion }),
  }),
  acceptRevision: (revision: RevisionRequest, idempotencyKey?: string) => request<RevisionAcceptResult>(`/api/v1/revision-requests/${revision.revision_request_id}/accept`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ expected_revision_version: revision.version }),
  }),
  rejectRevision: (revision: RevisionRequest, idempotencyKey?: string) => request<RevisionRequest>(`/api/v1/revision-requests/${revision.revision_request_id}/reject`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ expected_revision_version: revision.version }),
  }),
  cancelRevision: (revision: RevisionRequest, idempotencyKey?: string) => request<RevisionRequest>(`/api/v1/revision-requests/${revision.revision_request_id}/cancel`, {
    method: "POST",
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
    body: JSON.stringify({ expected_revision_version: revision.version }),
  }),
  listSkills: async (includeUninstalled = false) => (await request<{ items: SkillInstallation[] }>(`/api/v1/skills?include_uninstalled=${includeUninstalled}`)).items,
  getSkillManagementOptions: () => request<{ install_scopes: SkillInstallTarget["scope"][]; system_read_only: boolean; directory_updates?: boolean; lifecycle_commands?: boolean; package_commands?: boolean }>("/api/v1/skill-management-options"),
  previewSkillDirectoryUpdate: (installationID: string) => request<SkillDirectoryUpdate>(`/api/v1/skills/${encodeURIComponent(installationID)}/directory-update`),
  updateSkillFromDirectory: (preview: SkillDirectoryUpdate, installation: SkillInstallation, key: string) => request<SkillPackageResult>(`/api/v1/skills/${encodeURIComponent(preview.skill_installation_id)}/directory-update`, {
    method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ expected: skillLifecycleExpected(installation), expected_active_version_id: preview.current_version_id, version: preview.version, content_hash: preview.content_hash }),
  }),
  refreshSkills: (projectID?: string) => request<{ diagnostics: Array<{ code: string; message: string; path: string }> }>(`/api/v1/skills/refresh${projectID ? `?project_id=${encodeURIComponent(projectID)}` : ""}`, { method: "POST" }),
  installDiscoveredSkill: (capabilityID: string, version: string, contentHash: string, target: SkillInstallTarget, key: string) => request<SkillPackageResult>(`/api/v1/skills/discovered/${encodeURIComponent(capabilityID)}/install`, {
    method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ version, content_hash: contentHash, ...target }),
  }),
  listAgentTools: async () => (await request<{ tools: AgentToolDescriptor[] }>("/api/v1/agent-tools")).tools,
  getAgentMemory: (projectID: string, signal?: AbortSignal) => request<AgentMemoryDocument>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory`, { cache: "no-store", signal }),
  getMemoryPreferences: (projectID: string, signal?: AbortSignal) => request<MemoryPreferencesValue>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory/preferences`, { cache: "no-store", signal }),
  updateMemoryPreferences: (projectID: string, command: MemoryPreferencesUpdate) => request<MemoryPreferencesValue>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory/preferences`, { method: "PUT", cache: "no-store", body: JSON.stringify(command) }),
  listMemoryGenerations: (projectID: string, cursor: string, signal?: AbortSignal) => request<MemoryGenerationPage>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory/generations${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`, { cache: "no-store", signal }),
  listMemorySources: (projectID: string, cursor: string, signal?: AbortSignal) => request<MemorySourcePage>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory/sources${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`, { cache: "no-store", signal }),
  queueMemoryGeneration: (projectID: string, source: Omit<MemorySource, "generation_id">) => request<MemoryQueueReceipt>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory/generations`, { method: "POST", cache: "no-store", body: JSON.stringify(source) }),
  controlMemoryGeneration: (projectID: string, generationID: string, action: "pause" | "resume" | "cancel", expectedRevision: number) => request<Omit<MemoryGenerationSummary, "created_at">>(`/api/v1/projects/${encodeURIComponent(projectID)}/memory/generations/${encodeURIComponent(generationID)}/control`, { method: "POST", cache: "no-store", body: JSON.stringify({ action, expected_revision: expectedRevision }) }),
  updateAgentMemory: (command: AgentMemoryUpdate) => request<AgentMemoryDocument>(`/api/v1/projects/${encodeURIComponent(command.project_id)}/memory`, { method: "PUT", cache: "no-store", body: JSON.stringify(command) }),
  getAgentToolConfiguration: () => request<AgentToolConfiguration>("/api/v1/agent-tools/configuration"),
  listMCPConnections: () => request<MCPConnectionInventory>("/api/v1/mcp-connections", { cache: "no-store" }),
  getAgentInstructions: (projectID = "") => request<AgentInstructionView>(`/api/v1/agent-instructions${projectID ? `?project_id=${encodeURIComponent(projectID)}` : ""}`, { cache: "no-store" }),
  getAgentInstructionProposal: (callID: string) => request<AgentInstructionProposal>(`/api/v1/agent-tool-calls/${encodeURIComponent(callID)}/instruction-proposal`, { cache: "no-store" }),
  getAgentMemoryProposal: (callID: string) => request<AgentMemoryProposal>(`/api/v1/agent-tool-calls/${encodeURIComponent(callID)}/memory-proposal`, { cache: "no-store" }),
  getAgentMemoryToolProposal: (callID: string) => request<AgentMemoryToolProposal>(`/api/v1/agent-tool-calls/${encodeURIComponent(callID)}/memory-tool-proposal`, { cache: "no-store" }),
  getAgentSubtaskResult: (callID: string, signal?: AbortSignal) => request<AgentSubtaskResult>(`/api/v1/agent-tool-calls/${encodeURIComponent(callID)}/subtask-result`, { cache: "no-store", signal }),
  getAgentToolOutcomeReview: (callID: string, signal?: AbortSignal) => request<AgentToolOutcomeReview>(`/api/v1/agent-tool-calls/${encodeURIComponent(callID)}/outcome-review`, { cache: "no-store", signal }),
  resolveAgentToolOutcome: (callID: string, command: AgentToolOutcomeCommand, signal?: AbortSignal) => request<AgentToolOutcomeReview>(`/api/v1/agent-tool-calls/${encodeURIComponent(callID)}/outcome-review`, { method: "POST", cache: "no-store", signal, body: JSON.stringify(command) }),
  updateAgentInstructions: (command: AgentInstructionUpdate) => request<AgentInstructionDocument>("/api/v1/agent-instructions", {
    method: "PUT", cache: "no-store", body: JSON.stringify(command),
  }),
  updateMCPConnection: (command: MCPConnectionUpdate) => request<MCPConnectionState>("/api/v1/mcp-connections", {
    method: "PUT", cache: "no-store", body: JSON.stringify(command),
  }),
  updateAgentToolConfiguration: (expectedVersion: number, enabled: Record<string, boolean>) => request<AgentToolConfiguration>("/api/v1/agent-tools/configuration", { method: "PATCH", body: JSON.stringify({ expected_version: expectedVersion, enabled }) }),
  getScriptSandboxPolicy: () => request<ScriptSandboxPolicy>("/api/v1/script-sandbox-policy"),
  updateScriptSandboxPolicy: (policy: ScriptSandboxPolicy, enabled: boolean) => request<ScriptSandboxPolicy>("/api/v1/script-sandbox-policy", {
    method: "PUT",
    body: JSON.stringify({ expected_version: policy.version, enabled }),
  }),
  getSkill: (installationID: string) => request<SkillInstallation>(`/api/v1/skills/${encodeURIComponent(installationID)}`),
  installSkill: (file: File, target: SkillInstallTarget, key: string) => request<SkillPackageResult>(`/api/v1/skills?${new URLSearchParams({ scope: target.scope, ...(target.project_id ? { project_id: target.project_id } : {}) })}`, {
    method: "POST",
    headers: { "Content-Type": "application/zip", "Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(file.name)}`, "Idempotency-Key": key },
    body: file,
  }),
  upgradeSkill: (installation: SkillInstallation, file: File, key: string) => request<SkillPackageResult>(`/api/v1/skills/${encodeURIComponent(installation.skill_installation_id)}/versions`, {
    method: "POST",
    headers: { "Content-Type": "application/zip", "Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(file.name)}`, "Idempotency-Key": key, "X-Skill-Expected": JSON.stringify(skillLifecycleExpected(installation)) },
    body: file,
  }),
  setSkillEnabled: (installation: SkillInstallation, enabled: boolean, key: string) => request<SkillLifecycleResult>(`/api/v1/skills/${encodeURIComponent(installation.skill_installation_id)}/${enabled ? "enable" : "disable"}`, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ expected: skillLifecycleExpected(installation) }) }),
  activateSkillVersion: (installation: SkillInstallation, version: string, key: string) => request<SkillLifecycleResult>(`/api/v1/skills/${encodeURIComponent(installation.skill_installation_id)}/versions/${encodeURIComponent(version)}/activate`, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ expected: skillLifecycleExpected(installation) }) }),
  uninstallSkill: (installation: SkillInstallation, key: string) => request<SkillLifecycleResult>(`/api/v1/skills/${encodeURIComponent(installation.skill_installation_id)}`, { method: "DELETE", headers: { "Idempotency-Key": key }, body: JSON.stringify({ expected: skillLifecycleExpected(installation) }) }),
  listSkillInstallAttempts: async () => (await request<{ items: SkillInstallAttempt[] }>("/api/v1/skills/install-attempts?limit=100")).items,
};
