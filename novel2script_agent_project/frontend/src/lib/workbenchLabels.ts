import type { Artifact, ArtifactType, ExecutionStep, Run, RunEvent, SourceMode, ViewMode } from "../api/types";

export interface NavItem {
  view: ViewMode;
  artifactType: ArtifactType | null;
  label: string;
  status: string;
}

export const artifactLabels: Record<ArtifactType, string> = {
  source_input: "输入材料",
  story_bible: "故事圣经",
  episode_split: "原文拆集",
  material_bank: "素材库",
  story_seed: "故事种子",
  series_blueprint: "剧集蓝图",
  episode_cards: "分集卡",
  script_context: "剧本上下文",
  script_unit: "剧本单元",
  scripts: "剧本",
};

export const novelFlow: ArtifactType[] = ["source_input", "story_bible", "episode_split", "episode_cards"];
export const materialFlow: ArtifactType[] = ["source_input", "material_bank", "story_seed", "series_blueprint", "episode_cards"];

export function buildNavItems(mode: SourceMode, latestByType: Map<ArtifactType, Artifact>, events: RunEvent[], run: Run | null): NavItem[] {
  const base: NavItem[] = [
    { view: "artifacts", artifactType: "source_input", label: "输入材料", status: statusForType("source_input", latestByType, run) },
  ];
  const flow = mode === "novel" ? novelFlow.slice(1) : mode === "non_novel" ? materialFlow.slice(1) : [];
  const items: NavItem[] = flow.length
    ? [...base, ...flow.map((type) => ({ view: "artifacts" as ViewMode, artifactType: type, label: artifactLabels[type], status: statusForType(type, latestByType, run) }))]
    : base;
  items.push({ view: "script", artifactType: null, label: "剧本", status: latestByType.has("scripts") || latestByType.has("script_unit") ? "已生成" : "待生成" });
  items.push({ view: "events", artifactType: null, label: "运行记录", status: String(events.filter(isPrimaryRunEvent).length) });
  return items;
}

function statusForType(type: ArtifactType, latestByType: Map<ArtifactType, Artifact>, run: Run | null) {
  const artifact = latestByType.get(type);
  if (!artifact) return "待生成";
  if (artifact.status === "pending_approval") return "待确认";
  if (run?.current_step_id && activeStepToArtifactType(run.current_step_id) === type && run.status === "running") return "生成中";
  return artifactStatusLabel(artifact.status);
}

export function buildExecutionSteps(events: RunEvent[]) {
  const seen = new Set<string>();
  const steps: ExecutionStep[] = [];
  for (const event of events) {
    const step = stepFromEvent(event);
    if (!step || seen.has(step.key)) continue;
    seen.add(step.key);
    steps.push(step);
  }
  return steps;
}

function stepFromEvent(event: RunEvent): ExecutionStep | null {
  if (event.type === "progress_updated") return null;
  if (event.type === "approval_requested") return null;
  if (event.type === "approval_resolved") return approvalStep(event, "确认");
  if (event.type === "run_started") return { key: "understand", label: "理解请求" };
  if (event.type === "script_batch_inserted") return { key: "script_insert", label: "写入剧本节点" };
  const artifactType = typeof event.payload?.artifact_type === "string" ? event.payload.artifact_type as ArtifactType : activeStepToArtifactType(event.step_id);
  if (artifactType) return { key: `${event.type}:${artifactType}`, label: event.type === "artifact_updated" ? `更新${artifactLabels[artifactType]}` : `${event.type === "step_started" ? "处理" : "生成"}${artifactLabels[artifactType]}` };
  if (event.step_id === "step_route") return { key: "route", label: "识别意图" };
  return null;
}

function approvalStep(event: RunEvent, prefix: string): ExecutionStep {
  const artifactType = typeof event.payload?.artifact_type === "string" ? (event.payload.artifact_type as ArtifactType) : artifactTypeFromApprovalStep(event.step_id);
  const label = artifactType ? `${prefix}${artifactLabels[artifactType] || "当前产物"}` : prefix;
  return { key: `${event.type}:${artifactType || event.step_id || event.event_id}`, label };
}

function artifactTypeFromApprovalStep(stepID?: string): ArtifactType | null {
  const prefix = "step_approval_";
  if (!stepID?.startsWith(prefix)) return null;
  const artifactType = stepID.slice(prefix.length) as ArtifactType;
  return artifactLabels[artifactType] ? artifactType : null;
}

function activeStepToArtifactType(stepID?: string): ArtifactType | null {
  const mapping: Record<string, ArtifactType> = {
    step_ingest_source: "source_input",
    step_build_story_bible: "story_bible",
    step_split_episodes: "episode_split",
    step_build_material_bank: "material_bank",
    step_build_story_seed: "story_seed",
    step_build_series_blueprint: "series_blueprint",
    step_plan_episode_cards: "episode_cards",
    step_build_script_context: "script_context",
    step_generate_script_unit: "script_unit",
    step_build_scripts: "scripts",
  };
  return mapping[stepID || ""] || artifactTypeFromApprovalStep(stepID);
}

export function activeLabelFromRun(nextRun?: Run) {
  if (!nextRun) return "";
  if (nextRun.status === "waiting_approval") {
    const artifactType = artifactTypeFromApprovalStep(nextRun.current_step_id);
    return artifactType ? `等待你确认${artifactLabels[artifactType] || "当前产物"}` : "等待你确认";
  }
  if (nextRun.status === "running") {
	const progress = runProgress(nextRun);
	if (progress.status) return progress.status;
	return activeStepLabel(nextRun.current_step_id);
  }
  return "";
}

function activeStepLabel(stepID?: string) {
  const labels: Record<string, string> = {
    step_route: "正在识别意图",
    step_ingest_source: "正在保存输入",
    step_build_story_bible: "正在生成故事圣经",
    step_split_episodes: "正在拆分原文集数",
    step_build_material_bank: "正在整理素材库",
    step_build_story_seed: "正在提炼故事种子",
    step_build_series_blueprint: "正在规划剧集蓝图",
    step_plan_episode_cards: "正在规划分集卡",
    step_build_script_context: "正在准备剧本上下文",
    step_generate_script_unit: "正在写单集剧本",
    step_build_scripts: "正在整理最终剧本",
  };
  return labels[stepID || ""] || "正在执行";
}

export function mainTitle(view: ViewMode, selected: ArtifactType | null) {
  if (view === "script") return "剧本";
  if (view === "events") return "运行记录";
  return selected ? artifactLabels[selected] : "过程产物";
}

export function mainSubtitle(view: ViewMode, selected: ArtifactType | null, artifacts: Artifact[], run: Run | null) {
  if (view === "script") {
    return artifacts.some((artifact) => artifact.artifact_type === "scripts" || artifact.artifact_type === "script_unit")
      ? "剧本可在正文编辑器中手动修改，也可以让 Agent 局部调整。"
      : "剧本生成完成后显示在这里。";
  }
  if (view === "events") return run ? "展示 Agent 执行过程、确认点和产物变更。" : "还没有运行记录。";
  if (selected === "source_input" || artifacts.length === 0) return "只读预览已上传或已发送的材料；修改请通过 Agent 发起。";
	if ((selected === "episode_split" || selected === "episode_cards") && run?.status === "running") {
	  const progress = runProgress(run, selected);
	  if (progress.status) return progress.target ? `${progress.status}，已完成 ${progress.completed}/${progress.target} 集。` : progress.status;
	  return selected === "episode_split" ? "正在规划原文拆集。" : "正在规划分集卡。";
	}
  return selected ? `${artifactLabels[selected]} 的结构化阅读区。` : "当前链路的过程产物。";
}

function readRecord(value: unknown): Record<string, unknown> {
	return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function readPositiveNumber(value: unknown) {
	const number = Number(value);
	return Number.isFinite(number) && number > 0 ? number : 0;
}

function runProgress(run: Run, selected?: ArtifactType) {
	const keys = selected === "episode_split"
		? ["episode_split_progress"]
		: selected === "episode_cards"
			? ["episode_cards_progress"]
			: run.current_step_id === "step_plan_episode_cards"
				? ["episode_cards_progress"]
				: run.current_step_id === "step_split_episodes"
					? ["episode_split_progress"]
					: [];
	for (const key of keys) {
		const progress = readRecord(run.metadata?.[key]);
		const status = typeof progress.status_message === "string" ? progress.status_message.trim() : "";
		if (status) {
			return {
				status,
				completed: readPositiveNumber(progress.completed_count),
				target: readPositiveNumber(progress.target_count),
			};
		}
	}
	return { status: "", completed: 0, target: 0 };
}

export function tabLabel(view: ViewMode) {
  return view === "script" ? "剧本" : view === "events" ? "事件" : "过程产物";
}

export function sourceModeLabel(mode?: string) {
  const labels: Record<string, string> = {
    auto: "自动识别",
    novel: "小说链路",
    non_novel: "非小说链路",
    unknown: "未知",
  };
  return labels[mode || "auto"] || mode || "自动识别";
}

export function runStatusLabel(status?: string) {
  const labels: Record<string, string> = {
    pending: "待运行",
    running: "运行中",
    waiting_approval: "等待确认",
    paused: "已暂停",
    completed: "已完成",
    failed: "失败",
    cancelled: "已取消",
  };
  return labels[status || ""] || status || "未开始";
}

export function artifactStatusLabel(status?: string) {
  const labels: Record<string, string> = {
    draft: "草稿",
		pending_approval: "待确认",
		stale: "待更新",
    confirmed: "已生成",
    superseded: "已替换",
    invalidated: "已失效",
    failed: "失败",
  };
  return labels[status || ""] || status || "已生成";
}

export function labelEventType(type: string) {
  const labels: Record<string, string> = {
    run_started: "开始任务",
    step_started: "执行步骤",
    step_completed: "完成步骤",
    artifact_created: "生成产物",
    artifact_updated: "更新产物",
    script_batch_inserted: "写入剧本",
    approval_requested: "等待确认",
    approval_resolved: "确认处理",
    progress_updated: "进度更新",
    run_completed: "任务完成",
    step_failed: "步骤失败",
    run_paused: "任务暂停",
    run_resumed: "继续任务",
  };
  return labels[type] || type;
}

export function isPrimaryRunEvent(event: RunEvent) {
  return event.type !== "step_started" && event.type !== "step_completed" && event.type !== "progress_updated";
}

export function labelEventMessage(event: RunEvent) {
	if (event.type === "step_failed") {
		const userMessage = typeof event.payload?.user_message === "string" ? event.payload.user_message.trim() : "";
		if (userMessage) return userMessage;
		if (isTimeoutFailure(event.payload?.error)) {
			return "模型服务响应超时，本次未生成有效内容。已保留当前进度，你可以稍后从失败位置重试。";
		}
	}
  const artifactType = typeof event.payload?.artifact_type === "string" ? event.payload.artifact_type as ArtifactType : activeStepToArtifactType(event.step_id);
  const artifactName = artifactType ? artifactLabels[artifactType] : "当前内容";
  const labels: Record<string, string> = {
    run_started: "Agent 已开始处理输入。", step_started: `正在处理${artifactName}。`, artifact_created: `已生成${artifactName}。`,
    artifact_updated: `已更新${artifactName}。`, script_batch_inserted: "已写入一集剧本。", approval_requested: `${artifactName}需要确认后才能继续。`,
    approval_resolved: `${artifactName}的确认已处理。`, step_completed: `${artifactName}处理完成。`, run_completed: "任务已完成。",
    step_failed: `${artifactName}处理失败，可按失败位置重试。`, run_paused: "任务已暂停，已完成内容已保留。", run_resumed: "任务已从暂停位置继续。",
  };
  return labels[event.type] || "运行状态已更新。";
}

function isTimeoutFailure(error: unknown) {
	if (typeof error !== "string") return false;
	const message = error.toLowerCase();
	return ["context deadline exceeded", "client.timeout exceeded", "timeout awaiting response headers", "status=504", "status_code=504"].some((marker) => message.includes(marker));
}

export function runFailureMessage(events: RunEvent[]) {
	for (let index = events.length - 1; index >= 0; index -= 1) {
		const event = events[index];
		if (event.type !== "step_failed") continue;
		return labelEventMessage(event);
	}
	return "当前任务处理失败，已保留现有进度。你可以稍后从失败位置重试。";
}

export function agentRequestFailureMessage(message: string) {
	const detail = String(message || "请求失败").trim();
	return `后端请求未成功返回，本次是否执行尚未确认。请先刷新作品状态后再决定是否重试。\n${detail}`;
}

export function formatFileSize(size?: number) {
  if (!Number.isFinite(size)) return "";
  const safeSize = Number(size);
  if (safeSize < 1024) return `${safeSize} B`;
  if (safeSize < 1024 * 1024) return `${(safeSize / 1024).toFixed(1)} KB`;
  return `${(safeSize / 1024 / 1024).toFixed(1)} MB`;
}
