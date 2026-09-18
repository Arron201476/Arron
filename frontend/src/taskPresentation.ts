import type { TaskItem } from "./types";

export const modelRecoveryCode = "SDK_MODEL_RECOVERY_REQUIRED";
export const modelRecoveryMessage = "模型连接中断，原执行状态已保存。";

export function executionRecovery(code?: string | null): { label: string; message: string } | null {
  if (code === modelRecoveryCode) return { label: "等待恢复连接", message: modelRecoveryMessage };
  if (code === "SDK_TOOL_OUTCOME_UNRESOLVED") return { label: "等待核对外部操作", message: "外部操作结果未确认，原执行已暂停。" };
  return null;
}

const episodeTaskPattern = /^episode:(\d+)(?:-(\d+))?$/;

const taskLabels: Record<string, string> = {
  "preparation:source_analysis": "分析小说原文",
  "aggregate:story_bible_aggregate": "汇总故事圣经",
  "preparation:global_plan": "准备分集规划",
  "review:global": "全局审核",
};

export function taskProgressLabel(task: TaskItem): string {
  if (taskLabels[task.item_key]) return taskLabels[task.item_key];
  const episode = episodeTaskPattern.exec(task.item_key);
  if (episode) {
    return episode[2] ? `第 ${episode[1]}-${episode[2]} 集` : `第 ${episode[1]} 集`;
  }
  if (task.item_key.startsWith("preparation:")) return "准备数据";
  if (task.item_key.startsWith("aggregate:")) return "汇总结果";
  if (task.item_key === "review:global") return "全局审核";
  if (task.item_key.startsWith("asset:")) return `视频任务 ${task.item_order}`;
  return `任务 ${task.item_order}`;
}

export function taskProgressTitle(tasks: TaskItem[]): string {
  return tasks.length > 0 && tasks.every((task) => episodeTaskPattern.test(task.item_key))
    ? "逐集进度"
    : "任务进度";
}

export function currentStepTasks(tasks: TaskItem[], currentStepRunID?: string | null): TaskItem[] {
  if (!currentStepRunID) return [];
  return tasks
    .filter((task) => task.step_run_id === currentStepRunID)
    .sort((left, right) => left.item_order - right.item_order || left.task_item_id.localeCompare(right.task_item_id));
}

export function taskIsCompleted(task: TaskItem): boolean {
  return task.status === "completed" || task.status === "succeeded";
}

export function taskIsRetrying(task: TaskItem): boolean {
  return task.status === "running" && !task.output_repair && task.attempt_count > 1 && Boolean(task.failure);
}

export function taskRuntimeStatusLabel(task: TaskItem): string {
  const recovery = task.status === "paused" ? executionRecovery(task.failure) : null;
  if (recovery) return recovery.label;
  if (task.output_repair?.status === "claimed") {
    if (task.status === "running") return "正在修复结果";
    if (task.status === "paused") return "修复已暂停";
  }
  if (task.output_repair?.status === "closed" && task.status === "running") return "正在校验结果";
  if (task.status === "running" && task.program_status) {
    const programLabel = ({ running: "程序执行中", completed: "整理程序结果", incomplete: "程序未完成" } as Record<string, string>)[task.program_status];
    if (programLabel) return programLabel;
  }
  if (taskIsRetrying(task)) return `自动重试中（第 ${task.attempt_count} 次）`;
  return ({
    pending: "等待中",
    queued: "排队中",
    repair_pending: "等待修复",
    running: "处理中",
    waiting_approval: "等待工具授权",
	completed: "已完成",
	succeeded: "已完成",
	paused: "已暂停",
	failed: "失败",
    cancelled: "已结束",
  } as Record<string, string>)[task.status] ?? "处理中";
}
