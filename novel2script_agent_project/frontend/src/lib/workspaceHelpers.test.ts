import { describe, expect, it } from "vitest";
import type { Artifact, ProjectMessageResponse } from "../api/types";
import { buildGenerationConfigPrompt, latestActiveArtifactsByType, latestUnconsumedSelection, mergeArtifacts, normalizedGenerationConfig, restoredGenerationConfigPrompt } from "./workspaceHelpers";
import { buildNavItems } from "./workbenchLabels";

function artifact(id: string, version: number, updatedAt: string, value: string): Artifact {
  return {
    artifact_id: id,
    artifact_type: "story_bible",
    project_id: "project_1",
    run_id: "run_1",
    version,
    status: "confirmed",
    source_mode: "novel",
    updated_at: updatedAt,
    payload: { value },
  };
}

describe("workspace helpers", () => {
	it("hides the process entry until a flow or process artifact exists", () => {
		const source = { ...artifact("source_1", 1, "2026-07-13T10:00:00Z", "source"), artifact_type: "source_input" as const };
		const items = buildNavItems("auto", new Map([["source_input", source]]), [], null);
		expect(items.map((item) => item.label)).toEqual(["输入材料", "剧本", "运行记录"]);
	});

	it("restores a selected revision target until a revision action consumes it", () => {
		const selection = { artifact_id: "script_2", artifact_type: "script_unit" as const, version: 2, selected_text: "旧台词" };
		expect(latestUnconsumedSelection([
			{ message_id: "u1", project_id: "p", role: "user", content: "加强", selection_context: selection },
			{ message_id: "a1", project_id: "p", role: "agent", content: "需要确认", intent: "chat_idle" },
		])).toEqual(selection);
		expect(latestUnconsumedSelection([
			{ message_id: "u1", project_id: "p", role: "user", content: "加强", selection_context: selection },
			{ message_id: "a1", project_id: "p", role: "agent", content: "已执行", intent: "revise_checkpoint" },
		])).toBeNull();
		expect(latestUnconsumedSelection([
			{ message_id: "u1", project_id: "p", role: "user", content: "加强", selection_context: selection },
			{ message_id: "a1", project_id: "p", role: "agent", content: "已确认继续", intent: "approve_checkpoint" },
		])).toBeNull();
		expect(latestUnconsumedSelection([
			{ message_id: "u1", project_id: "p", role: "user", content: "加强", selection_context: selection },
			{ message_id: "a1", project_id: "p", role: "agent", content: "已恢复", intent: "resume_run" },
		])).toBeNull();
		expect(latestUnconsumedSelection([
			{ message_id: "u1", project_id: "p", role: "user", content: "加强", selection_context: selection },
			{ message_id: "a1", project_id: "p", role: "agent", content: "正在重试", intent: "rerun_step" },
		])).toBeNull();
	});
  it("keeps the newest payload for the same artifact id", () => {
    const oldVersion = artifact("artifact_1", 1, "2026-07-13T10:00:00Z", "old");
    const newVersion = artifact("artifact_1", 2, "2026-07-13T11:00:00Z", "new");
    expect(mergeArtifacts([oldVersion], [newVersion])).toEqual([newVersion]);

    const sameVersionRefresh = artifact("artifact_1", 2, "2026-07-13T12:00:00Z", "refreshed");
    expect(mergeArtifacts([newVersion], [sameVersionRefresh])[0].payload.value).toBe("refreshed");
  });

  it("selects only the latest active version after local edits and regenerations", () => {
    const oldVersion = { ...artifact("artifact_v1", 1, "2026-07-13T10:00:00Z", "old"), status: "superseded" as const };
    const regenerated = { ...artifact("artifact_v2", 2, "2026-07-13T11:00:00Z", "regenerated"), status: "pending_approval" as const };
    const invalidated = { ...artifact("artifact_v3", 3, "2026-07-13T12:00:00Z", "invalid"), status: "invalidated" as const };

    const latest = latestActiveArtifactsByType([oldVersion, regenerated, invalidated]);
    expect(latest.get("story_bible")).toEqual(regenerated);
  });

  it("keeps stale downstream content visible until its replacement arrives", () => {
    const stale = { ...artifact("artifact_stale", 2, "2026-07-13T13:00:00Z", "old downstream"), status: "stale" as const };
    expect(latestActiveArtifactsByType([stale]).get("story_bible")).toEqual(stale);
  });

  it("uses a same-id refresh before selecting the active artifact", () => {
    const pending = { ...artifact("artifact_1", 2, "2026-07-13T11:00:00Z", "pending"), status: "pending_approval" as const };
    const confirmed = { ...artifact("artifact_1", 2, "2026-07-13T12:00:00Z", "confirmed"), status: "confirmed" as const };
    const merged = mergeArtifacts([pending], [confirmed]);

    expect(latestActiveArtifactsByType(merged).get("story_bible")?.payload.value).toBe("confirmed");
  });

  it("shows generation config only for an explicit generation decision", () => {
    const generation = {
      user_message: { content: "把这个小说改成短剧", attachments: ["file_1", { file_id: "file_2" }] },
      agent_message: { content: "请确认集数和单集时长", intent: "generate_from_novel" },
      decision: { intent: "generate_from_novel", source_mode: "novel", requires_generation_config: true },
    } as unknown as ProjectMessageResponse;
    expect(buildGenerationConfigPrompt(generation, "auto")?.source_mode).toBe("novel");
    expect(buildGenerationConfigPrompt(generation, "auto")?.file_ids).toEqual(["file_1", "file_2"]);

    const casual = {
      user_message: { content: "不要生成，介绍一下功能" },
      agent_message: { content: "我可以协助改编。", intent: "chat_idle" },
      decision: { intent: "chat_idle", requires_generation_config: false },
    } as unknown as ProjectMessageResponse;
    expect(buildGenerationConfigPrompt(casual, "auto")).toBeUndefined();
  });

  it("restores an unconsumed generation config card with its original files", () => {
    const messages = [
      { message_id: "user_1", project_id: "project_1", role: "user" as const, content: "把附件改成短剧", attachments: ["file_1"] },
      {
        message_id: "agent_1", project_id: "project_1", role: "agent" as const, content: "请确认集数和单集时长",
        intent: "generate_from_novel" as const,
        decision_context: { intent: "generate_from_novel", next_action: "reply", source_mode: "novel" as const, requires_generation_config: true },
      },
    ];
    expect(restoredGenerationConfigPrompt(messages, "auto")).toEqual({
      message_id: "agent_1",
      prompt: expect.objectContaining({ original_message: "把附件改成短剧", source_mode: "novel", file_ids: ["file_1"] }),
    });

    expect(restoredGenerationConfigPrompt([...messages,
      { message_id: "user_chat", project_id: "project_1", role: "user" as const, content: "聊点别的" },
      { message_id: "agent_chat", project_id: "project_1", role: "agent" as const, content: "好的", intent: "chat_idle" as const },
    ], "auto")).toBeUndefined();

    expect(restoredGenerationConfigPrompt([...messages,
      { message_id: "user_2", project_id: "project_1", role: "user" as const, content: "确认生成配置：2 集，每集 1.5 分钟" },
      {
        message_id: "agent_2", project_id: "project_1", role: "agent" as const, content: "已经开始生成",
        intent: "generate_from_novel" as const, run_id: "run_1",
        decision_context: { next_action: "start_run", requires_generation_config: false },
      },
    ], "auto")).toBeUndefined();
  });

  it("normalizes the authoritative generation config", () => {
    expect(normalizedGenerationConfig({ target_episode_count: 2.8, episode_duration_minutes: 1.5 })).toEqual({
      target_episode_count: 2,
      episode_duration_minutes: 1.5,
      target_script_chars: 500,
    });
    expect(normalizedGenerationConfig({})).toBeUndefined();
  });

  it("preserves detected source episode metadata in the authoritative config", () => {
    expect(normalizedGenerationConfig({
      target_episode_count: 14,
      episode_duration_minutes: 1.5,
      preserve_existing_episode_marks: true,
      existing_episode_markers_detected: true,
      detected_episode_count: 14,
    })).toEqual({
      target_episode_count: 14,
      episode_duration_minutes: 1.5,
      target_script_chars: 500,
      preserve_existing_episode_marks: true,
      existing_episode_markers_detected: true,
      detected_episode_count: 14,
    });
  });
});
