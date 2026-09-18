import { describe, expect, it } from "vitest";
import type { Artifact, Run, RunEvent } from "../api/types";
import { activeLabelFromRun, agentRequestFailureMessage, buildExecutionSteps, isPrimaryRunEvent, labelEventMessage, mainSubtitle, runFailureMessage } from "./workbenchLabels";

describe("workbench labels", () => {
  it("shows product-level batched split progress without internal task names", () => {
    const run: Run = {
      run_id: "run_1", project_id: "project_1", intent: "generate_novel", source_mode: "novel", status: "running",
      metadata: { episode_split_progress: { completed_count: 10, target_count: 30, status_message: "已完成 10/30 集原文边界" } },
    };
    const artifact = { artifact_type: "story_bible" } as Artifact;
    const subtitle = mainSubtitle("artifacts", "episode_split", [artifact], run);
    expect(subtitle).toContain("已完成 10/30 集原文边界");
    expect(subtitle).not.toContain("task_split_batch");
  });

  it("keeps internal split progress out of the visible process record", () => {
    const event = { event_id: "event_progress", run_id: "run_1", step_id: "step_split_episodes", type: "progress_updated", message: "已完成 5/30 集" } as RunEvent;
    expect(buildExecutionSteps([event])).toEqual([]);
    expect(isPrimaryRunEvent(event)).toBe(false);
  });

  it("shows product-level batched episode-card progress", () => {
	const run: Run = {
	  run_id: "run_cards", project_id: "project_1", intent: "generate_novel", source_mode: "novel", status: "running",
	  current_step_id: "step_plan_episode_cards",
	  metadata: { episode_cards_progress: { completed_count: 10, target_count: 14, status_message: "已完成 10/14 集分集卡" } },
	};
	expect(activeLabelFromRun(run)).toBe("已完成 10/14 集分集卡");
	expect(mainSubtitle("artifacts", "episode_cards", [{} as Artifact], run)).toContain("10/14");
  });

  it("shows the backend model-timeout explanation in chat and process records", () => {
    const event = {
      event_id: "event_timeout", run_id: "run_1", step_id: "step_build_story_bible", type: "step_failed", message: "Planning failed",
      payload: { error_code: "MODEL_TIMEOUT", user_message: "模型服务响应超时，本次未生成有效内容。已保留当前进度，你可以稍后从失败位置重试。" },
    } as RunEvent;
    expect(labelEventMessage(event)).toContain("模型服务响应超时");
    expect(runFailureMessage([event])).toBe(labelEventMessage(event));
  });

  it("recognizes timeout errors from an older backend event", () => {
    const event = {
      event_id: "event_legacy_timeout", run_id: "run_1", step_id: "step_build_story_bible", type: "step_failed", message: "Planning failed",
      payload: { error: "[NodeRunError] Post model endpoint: context deadline exceeded" },
    } as RunEvent;
    expect(runFailureMessage([event])).toContain("模型服务响应超时");
  });

  it("keeps a generic explanation for non-timeout failures", () => {
    const event = { event_id: "event_failed", run_id: "run_1", step_id: "step_build_story_bible", type: "step_failed", message: "Planning failed" } as RunEvent;
    expect(labelEventMessage(event)).not.toContain("模型服务响应超时");
  });

  it("does not claim a failed response means the command never executed", () => {
	const message = agentRequestFailureMessage("网络连接中断");
	expect(message).toContain("是否执行尚未确认");
	expect(message).not.toContain("本次没有继续执行");
  });
});
