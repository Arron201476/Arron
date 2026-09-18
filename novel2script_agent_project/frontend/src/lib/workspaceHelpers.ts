import { ApiError } from "../api/errors";
import type {
  ApprovalRequest,
  Artifact,
  ArtifactType,
  ChatMessage,
  FileAttachment,
  GenerationConfig,
  GenerationConfigPrompt,
  Message,
  ProjectMessageResponse,
  SelectionContext,
  SourceMode,
} from "../api/types";
import { runStatusLabel } from "./workbenchLabels";

const generationConfigRequestPattern = /(集数|多少集|几集|单集|每集|时长|分钟|多久|多长|目标集|生成配置)/;

export function localID(prefix: string) {
  return `${prefix}_${Math.random().toString(16).slice(2)}_${Date.now()}`;
}

export function latestUnconsumedSelection(messages: Message[]): SelectionContext | null {
  let pending: SelectionContext | null = null;
  for (const message of messages) {
    if (message.role === "user" && message.selection_context?.artifact_id && message.selection_context.selected_text) {
      pending = message.selection_context;
    }
	if (message.role === "agent" && [
	  "revise_checkpoint", "approve_checkpoint", "generate_from_novel", "generate_from_material", "resume_run", "rerun_step",
	].includes(message.intent || "")) {
      pending = null;
    }
  }
  return pending;
}

export function approvalArtifactType(approval?: ApprovalRequest | null): ArtifactType | null {
  const value = approval?.proposed_action?.approved_artifact;
  const supported: ArtifactType[] = [
    "source_input", "story_bible", "episode_split", "material_bank", "story_seed",
    "series_blueprint", "episode_cards", "script_unit", "scripts",
  ];
  return supported.includes(value as ArtifactType) ? value as ArtifactType : null;
}

function artifactTimestamp(artifact: Artifact) {
  const value = artifact.updated_at || artifact.created_at || "";
  const timestamp = value ? Date.parse(value) : 0;
  return Number.isFinite(timestamp) ? timestamp : 0;
}

export function isNewerArtifact(candidate: Artifact, current: Artifact) {
  if (candidate.version !== current.version) return candidate.version > current.version;
  return artifactTimestamp(candidate) >= artifactTimestamp(current);
}

export function mergeArtifacts(current: Artifact[], incoming: Artifact[]) {
  const byID = new Map<string, Artifact>();
  for (const artifact of current) byID.set(artifact.artifact_id, artifact);
  for (const artifact of incoming) {
    const existing = byID.get(artifact.artifact_id);
    const newer = !existing || isNewerArtifact(artifact, existing);
    if (newer) byID.set(artifact.artifact_id, artifact);
  }
  return Array.from(byID.values()).sort((left, right) => {
    const timeDelta = artifactTimestamp(left) - artifactTimestamp(right);
    return timeDelta || left.artifact_id.localeCompare(right.artifact_id);
  });
}

export function latestActiveArtifactsByType(artifacts: Artifact[]) {
  const latest = new Map<ArtifactType, Artifact>();
  for (const artifact of artifacts) {
    if (artifact.status === "superseded" || artifact.status === "invalidated") continue;
    const current = latest.get(artifact.artifact_type);
    if (!current || isNewerArtifact(artifact, current)) latest.set(artifact.artifact_type, artifact);
  }
  return latest;
}

export function isNotFoundError(error: unknown) {
  return error instanceof ApiError
    ? error.status === 404
    : Boolean(error && typeof error === "object" && "status" in error && (error as { status?: number }).status === 404);
}

export function restoredWorkspaceSummary(title: string, status: string, artifacts: Artifact[], waitingApproval: boolean) {
  const active = artifacts.filter((artifact) => artifact.status !== "superseded" && artifact.status !== "invalidated");
  const latestVersion = active.reduce((maximum, artifact) => Math.max(maximum, artifact.version || 0), 0);
  const pending = waitingApproval
    ? "当前有一项内容等待你确认。"
    : status === "failed" ? "当前停在失败任务，可从失败位置重试。" : "当前没有待确认操作。";
  return `已恢复作品“${title}”：流程${runStatusLabel(status)}，可用产物 ${active.length} 份，最新版本 v${latestVersion || 1}。${pending}`;
}

export async function readAttachment(file: File): Promise<FileAttachment> {
  if (file.size > 5 * 1024 * 1024) throw new Error(`${file.name} 超过 5MB，当前先限制小文件上传。`);
  const base = { file_name: file.name, mime_type: file.type || "", size: file.size };
  if (isTextFile(file)) return { ...base, text_content: await file.text() };
  if (file.name.toLowerCase().endsWith(".docx")) return { ...base, content_base64: await fileToBase64(file) };
  throw new Error(`${file.name} 暂不支持。当前支持 txt/md/json/csv/log/html/xml/yaml 和 docx。`);
}

export function sourceTextFromAttachments(files: FileAttachment[]) {
  return files.map((file) => {
    const text = file.text_content?.trim();
    return text ? `# ${file.file_name}\n\n${text}` : "";
  }).filter(Boolean).join("\n\n");
}

export function buildGenerationConfigPrompt(
  payload: ProjectMessageResponse,
  fallbackSourceMode: SourceMode,
): ChatMessage["generationConfigPrompt"] {
  if (payload.approval_request?.approval_request_id) return undefined;
  if (payload.run?.status === "running" || payload.run?.status === "waiting_approval") return undefined;
  const userMessage = payload.user_message?.content?.trim() || "";
  const asksForConfig = generationConfigRequestPattern.test(payload.agent_message?.content || "");
  const hasGenerationIntent = isGenerationIntent(payload.decision?.intent) || isGenerationIntent(payload.agent_message?.intent);
  if (!hasGenerationIntent || (!payload.decision?.requires_generation_config && !asksForConfig) || !userMessage) return undefined;

  const mode = generationPromptSourceMode(payload, fallbackSourceMode);
  const reason = mode === "novel"
    ? "已识别为小说改编。开始生成前，请确认目标集数和单集时长。"
    : mode === "non_novel"
      ? "已识别为非小说素材生成。开始生成前，请确认目标集数和单集时长。"
      : "开始生成前，请先确认目标集数和单集时长。";
  return {
    source_mode: mode,
    original_message: userMessage,
    file_ids: (payload.user_message?.attachments || []).map((attachment) =>
      typeof attachment === "string" ? attachment : attachment.file_id,
    ).filter(Boolean),
    title: "确认生成配置",
    reason,
    defaults: {
      ...(payload.decision?.generation_config || {}),
      preserve_existing_episode_marks: Boolean(payload.decision?.generation_config?.preserve_existing_episode_marks),
    },
  };
}

export function restoredGenerationConfigPrompt(messages: Message[], fallbackSourceMode: SourceMode) {
  let latestUser: Message | undefined;
  let pending: { message_id: string; prompt: GenerationConfigPrompt } | undefined;
  for (const message of messages) {
    if (message.role === "user") {
      pending = undefined;
      latestUser = message;
      continue;
    }
    if (message.role !== "agent") continue;
    if (message.decision_context?.next_action === "start_run" || message.run_id) pending = undefined;
    if (!latestUser || !message.decision_context?.requires_generation_config) continue;
    const prompt = buildGenerationConfigPrompt({
      user_message: latestUser,
      agent_message: message,
      decision: {
        intent: message.decision_context.intent || message.intent,
        source_mode: message.decision_context.source_mode,
        requires_generation_config: true,
        generation_config: message.decision_context.generation_config,
      },
    }, fallbackSourceMode);
    if (prompt) pending = { message_id: message.message_id, prompt };
  }
  return pending;
}

function isGenerationIntent(intent?: string) {
  return ["generate_novel", "generate_non_novel", "generate_from_novel", "generate_from_material"].includes(intent || "");
}

function generationPromptSourceMode(payload: ProjectMessageResponse, fallbackSourceMode: SourceMode): SourceMode {
  const decisionMode = payload.decision?.source_mode;
  if (decisionMode === "novel" || decisionMode === "non_novel") return decisionMode;
  const projectMode = payload.project?.source_mode;
  if (projectMode === "novel" || projectMode === "non_novel") return projectMode;
  const intent = payload.decision?.intent || payload.agent_message?.intent;
  if (intent === "generate_novel" || intent === "generate_from_novel") return "novel";
  if (intent === "generate_non_novel" || intent === "generate_from_material") return "non_novel";
  return fallbackSourceMode === "novel" || fallbackSourceMode === "non_novel" ? fallbackSourceMode : "auto";
}

export function normalizedGenerationConfig(config: GenerationConfig): GenerationConfig | undefined {
  const next: GenerationConfig = {};
  if (config.target_episode_count && config.target_episode_count > 0) next.target_episode_count = Math.floor(config.target_episode_count);
  if (config.episode_duration_minutes && config.episode_duration_minutes > 0) {
    next.episode_duration_minutes = config.episode_duration_minutes;
    next.target_script_chars = config.target_script_chars || Math.round(config.episode_duration_minutes * 333);
  }
  if (config.boundary_detection_window_chars && config.boundary_detection_window_chars > 0) {
    next.boundary_detection_window_chars = Math.floor(config.boundary_detection_window_chars);
  }
  if (config.preserve_existing_episode_marks) next.preserve_existing_episode_marks = true;
  if (config.existing_episode_markers_detected) next.existing_episode_markers_detected = true;
  if (config.detected_episode_count && config.detected_episode_count > 0) {
    next.detected_episode_count = Math.floor(config.detected_episode_count);
  }
  return Object.keys(next).length ? next : undefined;
}

function isTextFile(file: File) {
  const name = file.name.toLowerCase();
  return file.type.startsWith("text/") || [".txt", ".md", ".json", ".csv", ".log", ".html", ".xml", ".yaml", ".yml"].some((ext) => name.endsWith(ext));
}

function fileToBase64(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result || "");
      resolve(result.includes(",") ? result.split(",").pop() || "" : result);
    };
    reader.onerror = () => reject(new Error(`${file.name} 读取失败。`));
    reader.readAsDataURL(file);
  });
}
