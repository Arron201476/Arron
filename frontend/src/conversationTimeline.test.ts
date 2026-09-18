import { describe, expect, it } from "vitest";
import { buildConversationTimeline, revisionBlocksApproval } from "./conversationTimeline";
import type { AgentTask, AgentToolCall, Approval, Message, ProjectActivity, ProposedAction, RevisionRequest, RunSnapshot } from "./types";

describe("buildConversationTimeline", () => {
  it("orders approval before the later revision conversation", () => {
    const approval = approvalAt("approval", "2026-08-13T10:46:07Z", "version-4");
    const messages = [
      message("request", "user", "2026-08-13T10:49:22Z"),
      message("reply", "assistant", "2026-08-13T10:49:22Z"),
    ];
    const revision = revisionAt("revision", "2026-08-13T10:49:22Z", "version-4", "running");
    const result = buildConversationTimeline({ messages, approvals: [approval], revisions: [revision] });
    expect(result.map((item) => `${item.kind}:${item.id}`)).toEqual([
      "approval:approval",
      "message:request",
      "message:reply",
      "revision:revision",
    ]);
  });

  it("anchors the initial mutable run progress at its current step without duplicating it", () => {
    const run = runAt("run-1", "2026-08-13T09:06:00Z", "running");
    const activities: ProjectActivity[] = [{ event_id: "event-1", event_type: "run.started", run_id: "run-1", capability_id: "video_reference_creation", step_id: "ingest_source", occurred_at: "2026-08-13T09:06:00Z" }];
    const result = buildConversationTimeline({
      messages: [message("before", "user", "2026-08-13T09:05:30Z"), message("after", "user", "2026-08-13T09:07:00Z")],
      runSnapshot: run,
      activities,
    });
    expect(result.map((item) => `${item.kind}:${item.id}`)).toEqual(["message:before", "run:run-1", "message:after"]);

    const updated = buildConversationTimeline({ messages: [], runSnapshot: { ...run, run: { ...run.run, status: "waiting_approval" } }, activities });
    expect(updated).toHaveLength(1);
    expect(updated[0]).toMatchObject({ kind: "run", id: "run-1", value: { run: { status: "waiting_approval" } } });
  });

  it("moves regenerated progress to the regenerated step position in the conversation", () => {
    const run = runAt("run-1", "2026-08-13T09:06:00Z", "running");
    run.run.current_step_run_id = "step-regenerated";
    run.steps = [
      ...run.steps,
      { step_run_id: "step-regenerated", run_id: "run-1", step_id: "generate_story_bible", status: "running", attempt_count: 1, started_at: "2026-08-13T09:12:00Z" },
    ];
    const result = buildConversationTimeline({
      messages: [
        message("initial-request", "user", "2026-08-13T09:05:30Z"),
        message("later-chat", "user", "2026-08-13T09:10:00Z"),
        message("after-regeneration", "user", "2026-08-13T09:13:00Z"),
      ],
      runSnapshot: run,
      activities: [{ event_id: "regenerated", event_type: "step.started", run_id: "run-1", capability_id: "novel_to_script", step_id: "generate_story_bible", occurred_at: "2026-08-13T09:12:00Z" }],
    });

    expect(result.map((item) => `${item.kind}:${item.id}`)).toEqual([
      "message:initial-request",
      "message:later-chat",
      "run:run-1",
      "message:after-regeneration",
    ]);
  });

  it("keeps confirmed configuration immediately before its started run", () => {
    const action: ProposedAction = {
      proposed_action_id: "action-1",
      version: 2,
      snapshot_hash: "hash",
      status: "consumed",
      action_type: "start_run",
      capability_ref: { capability_id: "video_reference_creation", version: "1.0.0" },
      input: { collection_state: "sealed" },
      config: {},
      confirmation_message_id: "confirmation-1",
      consumed_run_id: "run-1",
      created_at: "2026-08-13T09:05:00Z",
      updated_at: "2026-08-13T09:06:00Z",
    };
    const run = runAt("run-1", "2026-08-13T09:06:00Z", "running");

    const result = buildConversationTimeline({
      messages: [message("request", "user", "2026-08-13T09:04:00Z")],
      proposedActions: [action],
      runSnapshot: run,
    });

    expect(result.map((item) => `${item.kind}:${item.id}`)).toEqual([
      "message:request",
      "proposed_action:action-1",
      "run:run-1",
    ]);
  });

  it("places a background task at the time it was created", () => {
    const task = agentTaskAt("background-1", "2026-08-13T09:06:00Z", "running");
    const result = buildConversationTimeline({
      messages: [
        message("request", "user", "2026-08-13T09:05:00Z"),
        message("later", "assistant", "2026-08-13T09:07:00Z"),
      ],
      agentTasks: [task],
    });
    expect(result.map((item) => `${item.kind}:${item.id}`)).toEqual([
      "message:request", "agent_task:background-1", "message:later",
    ]);
  });

  it("places only approval-bearing Agent tool calls in the conversation", () => {
    const approvedTool = agentToolCallAt("tool-1", "2026-08-13T09:06:00Z", true);
    const readTool = agentToolCallAt("tool-2", "2026-08-13T09:06:30Z", false);
    const result = buildConversationTimeline({
      messages: [message("request", "user", "2026-08-13T09:05:00Z")],
      agentToolCalls: [approvedTool, readTool],
    });
    expect(result.map((item) => `${item.kind}:${item.id}`)).toEqual([
      "message:request", "agent_tool_call:tool-1",
    ]);
  });
});

describe("revisionBlocksApproval", () => {
  it("blocks the exact source version-set approval without blocking other approvals or projects", () => {
    const approval = { ...approvalAt("approval", "2026-08-13T10:46:07Z", "step"), project_id: "project", subject_kind: "artifact_version_set" };
    const revision = { ...revisionAt("revision", "2026-08-13T10:49:22Z", "secondary-output-version", "queued"), project_id: "project", source_approval_request_id: "approval" };
    expect(revisionBlocksApproval([revision], approval)).toBe(revision);
    expect(revisionBlocksApproval([{ ...revision, source_approval_request_id: "another" }], approval)).toBeNull();
    expect(revisionBlocksApproval([{ ...revision, project_id: "foreign" }], approval)).toBeNull();
    expect(revisionBlocksApproval([{ ...revision, status: "accepted" }], approval)).toBeNull();
  });
  it("blocks the approval while a draft for its version is running", () => {
    const approval = approvalAt("approval", "2026-08-13T10:46:07Z", "version-4");
    expect(revisionBlocksApproval([revisionAt("revision", "2026-08-13T10:49:22Z", "version-4", "running")], approval)?.revision_request_id).toBe("revision");
    expect(revisionBlocksApproval([revisionAt("revision", "2026-08-13T10:49:22Z", "version-4", "cancelled")], approval)).toBeNull();
  });
});

function message(id: string, role: Message["role"], at: string): Message {
  return { message_id: id, role, content: id, created_at: at };
}

function approvalAt(id: string, at: string, versionID: string): Approval {
  return { approval_request_id: id, scope: "artifact", status: "pending", version: 1, title: "确认故事圣经", reason: "确认后继续", options: ["approve"], subject_kind: "artifact_version", subject_ref_id: versionID, subject_version: 4, subject_snapshot_hash: "hash", requested_at: at };
}

function revisionAt(id: string, at: string, versionID: string, status: string): RevisionRequest {
  return { revision_request_id: id, project_id: "project", conversation_id: "conversation", request_message_id: "message", target_resolution_id: "resolution", artifact_id: "artifact", base_artifact_version_id: versionID, instruction: "细化一下", operation: "revise", status, execution_policy: "safe_checkpoint", version: 1, created_at: at, updated_at: at };
}

function runAt(id: string, at: string, status: string): RunSnapshot {
  return {
    run: { run_id: id, capability_id: "video_reference_creation", status, current_step_run_id: "step-1", started_at: at },
    steps: [{ step_run_id: "step-1", run_id: id, step_id: "extract_video_scripts", status: "running", attempt_count: 1, started_at: at }],
    task_items: [],
    available_actions: [],
    current_approval: null,
  };
}

function agentTaskAt(id: string, at: string, status: string): AgentTask {
  return {
    agent_task_id: id, workspace_id: "workspace", project_id: "project", conversation_id: "conversation",
    skill_invocation_id: "invocation", capability_id: "background_skill", capability_version: "1.0.0",
    status, progress_current: 1, progress_total: 2, progress_message: "处理中", input: {}, config: {},
    attempt_count: 1, max_attempts: 3, cancel_requested: false, created_at: at, queued_at: at, updated_at: at,
  };
}

function agentToolCallAt(id: string, at: string, needsApproval: boolean): AgentToolCall {
  return {
    agent_tool_call_id: id, workspace_id: "workspace", project_id: "project", conversation_id: "conversation",
    agent_turn_id: "turn-1", sdk_tool_call_id: `sdk-${id}`, tool_id: "tool.write", tool_kind: "local",
    tool_name: "Write file", access_mode: "write", approval_policy: needsApproval ? "always" : "never",
    approval_status: needsApproval ? "pending" : "not_required", status: needsApproval ? "pending_approval" : "completed",
    arguments_summary: { path: "draft.txt" }, requested_at: at, updated_at: at,
    approval: needsApproval ? {
      agent_tool_approval_id: `approval-${id}`, agent_tool_call_id: id, workspace_id: "workspace",
      project_id: "project", conversation_id: "conversation", status: "pending", version: 1,
      title: "Allow write", reason: "Writes project state", options: ["approve", "reject"],
      subject_snapshot_hash: "hash", requested_at: at,
    } : null,
  };
}
