import { describe, expect, it } from "vitest";
import type { TaskItem } from "./types";
import { currentStepTasks, taskIsCompleted, taskIsRetrying, taskProgressLabel, taskProgressTitle, taskRuntimeStatusLabel } from "./taskPresentation";

function task(itemKey: string, itemOrder = 1): TaskItem {
  return {
    task_item_id: `task-${itemOrder}`,
    step_run_id: "step-1",
    item_key: itemKey,
    item_order: itemOrder,
    status: "pending",
    attempt_count: 0,
    failure: null,
  };
}

describe("task progress presentation", () => {
  it.each([
    ["running", "running", "程序执行中"], ["running", "completed", "整理程序结果"],
    ["running", "incomplete", "程序未完成"], ["running", "unknown", "处理中"],
    ["paused", "running", "已暂停"], ["failed", "running", "失败"],
    ["succeeded", "incomplete", "已完成"],
  ])("shows program status %s/%s without overriding task lifecycle", (status, program_status, label) => {
    expect(taskRuntimeStatusLabel({ ...task("episode:1"), status, program_status })).toBe(label);
  });
  it.each([
    ["repair_pending", "queued", "等待修复"],
    ["running", "claimed", "正在修复结果"],
    ["paused", "claimed", "修复已暂停"],
    ["running", "closed", "正在校验结果"],
    ["failed", "terminal", "失败"],
    ["succeeded", "closed", "已完成"],
    ["cancelled", "claimed", "已结束"],
  ] as const)("shows result repair %s/%s without pretending to retry generation", (status, repair, label) => {
    const repairing = { ...task("episode:1"), status, attempt_count: 2, failure: "previous attempt",
      output_repair: { status: repair, rejection_no: 1, error_code: "BATCH_COVERAGE_INVALID" } };
    expect(taskRuntimeStatusLabel(repairing)).toBe(label);
    expect(taskIsRetrying(repairing)).toBe(false);
  });

  it("shows only tasks from the current step after regeneration", () => {
    const historical = { ...task("preparation:source_analysis"), step_run_id: "story-old" };
    const regenerated = { ...task("aggregate:story_bible_aggregate", 2), step_run_id: "story-new" };
    const currentSecond = { ...task("episode:1-3", 2), step_run_id: "episode-plan" };
    const currentFirst = { ...task("preparation:global_plan", 1), step_run_id: "episode-plan" };

    expect(currentStepTasks([historical, regenerated, currentSecond, currentFirst], "episode-plan").map((item) => item.item_key)).toEqual([
      "preparation:global_plan",
      "episode:1-3",
    ]);
  });

  it("shows episode ranges from item keys instead of task order", () => {
    expect(taskProgressLabel(task("episode:1-5", 1))).toBe("第 1-5 集");
    expect(taskProgressLabel(task("episode:6-10", 2))).toBe("第 6-10 集");
    expect(taskProgressLabel(task("episode:11-12", 3))).toBe("第 11-12 集");
  });

  it("uses semantic labels for non-episode tasks", () => {
    expect(taskProgressLabel(task("preparation:global_plan"))).toBe("准备分集规划");
    expect(taskProgressLabel(task("preparation:source_analysis"))).toBe("分析小说原文");
    expect(taskProgressLabel(task("aggregate:story_bible_aggregate"))).toBe("汇总故事圣经");
    expect(taskProgressLabel(task("review:global"))).toBe("全局审核");
    expect(taskProgressLabel(task("asset:video-1", 2))).toBe("视频任务 2");
  });

  it("only calls a fully episode-scoped list episode progress", () => {
    expect(taskProgressTitle([task("episode:1-5"), task("episode:6-10", 2)])).toBe("逐集进度");
    expect(taskProgressTitle([task("episode:1"), task("review:global", 2)])).toBe("任务进度");
  });

  it("counts Runtime succeeded tasks as completed", () => {
    expect(taskIsCompleted({ ...task("episode:1"), status: "succeeded" })).toBe(true);
    expect(taskIsCompleted({ ...task("episode:2"), status: "completed" })).toBe(true);
    expect(taskIsCompleted({ ...task("episode:3"), status: "running" })).toBe(false);
  });

  it("shows automatic retry instead of a generic running state", () => {
    const retrying = { ...task("aggregate:story_bible_aggregate"), status: "running", attempt_count: 2, failure: "OUTPUT_REPAIR_FAILED" };
    expect(taskIsRetrying(retrying)).toBe(true);
    expect(taskRuntimeStatusLabel(retrying)).toBe("自动重试中（第 2 次）");
  });

  it("does not present paused tasks as processing", () => {
    expect(taskRuntimeStatusLabel({ ...task("episode:1"), status: "paused" })).toBe("已暂停");
  });

  it("distinguishes SDK approval waits from processing and automatic retries", () => {
    const waiting = { ...task("episode:1"), status: "waiting_approval", attempt_count: 2, failure: "previous attempt" };
    expect(taskRuntimeStatusLabel(waiting)).toBe("等待工具授权");
    expect(taskIsRetrying(waiting)).toBe(false);
    expect(taskIsCompleted(waiting)).toBe(false);
  });
});
