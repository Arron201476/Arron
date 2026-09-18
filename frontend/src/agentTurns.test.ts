import { describe, expect, it } from "vitest";
import { agentTurnMessage, agentTurnSubmissionID, mergeMessagesWithAgentTurns } from "./agentTurns";
import type { AgentTurn, Message } from "./types";

function turn(status: AgentTurn["status"]): AgentTurn {
  return {
    agent_turn_id: "turn-1", workspace_id: "workspace", project_id: "project",
    conversation_id: "conversation", status,
    request: { content: "生成大纲", attachment_refs: [] },
    created_at: "2026-09-04T00:00:00Z", updated_at: "2026-09-04T00:00:00Z",
  };
}

describe("Agent Turn projection", () => {
  it("derives the shared submission identifier without exposing the request key", async () => {
    const expected = "3b296e84998e503bc56fd2a11a453f42bd498512e9a47cdcb2e4225e5d3478f3";
    expect(await agentTurnSubmissionID("u", "conversation", "request")).toBe(expected);
    expect(await agentTurnSubmissionID("other", "conversation", "request")).not.toBe(expected);
    expect(await agentTurnSubmissionID("u", "other", "request")).not.toBe(expected);
    expect(await agentTurnSubmissionID("u", "conversation", "other")).not.toBe(expected);
  });

  it("does not merge independent identical submissions or guess from matching text", () => {
    const pending = { message_id: "local_pending", role: "user", content: "生成大纲", created_at: "2026-09-04T00:00:01Z", delivery_status: "sending", submission_id: "second" } as Message;
    for (const submission_id of ["first", undefined]) {
      const durable = { ...turn("running"), submission_id };
      expect(mergeMessagesWithAgentTurns([pending], [durable])).toHaveLength(2);
    }
  });

  it("settles an exact submission despite a delayed receipt or a queued content edit", () => {
    const pending = { message_id: "local_pending", role: "user", content: "Original", created_at: "2026-09-04T00:00:01Z", delivery_status: "sending", submission_id: "exact" } as Message;
    const durable = { ...turn("accepted"), submission_id: "exact", request: { content: "Edited queued content" }, created_at: "2026-09-04T00:20:00Z" };
    expect(mergeMessagesWithAgentTurns([pending], [durable])).toEqual([agentTurnMessage(durable)]);
  });

  it("settles only the committed submission and only after its durable message is available", () => {
    const pending = { message_id: "local_pending", role: "user", content: "Original", created_at: "2026-09-04T00:00:01Z", delivery_status: "sending", submission_id: "exact" } as Message;
    const durable = { ...turn("committed"), submission_id: "exact", user_message_id: "saved" };
    const saved = { message_id: "saved", role: "user", content: "Edited queued content", created_at: "2026-09-04T00:20:00Z" } as Message;
    expect(mergeMessagesWithAgentTurns([pending], [durable])).toEqual([pending]);
    expect(mergeMessagesWithAgentTurns([saved, pending], [durable])).toEqual([saved]);
  });

  it("projects public failure details only for failed turns", () => {
    const failed = { ...turn("failed"), error_message: "原生上下文压缩不可用，原会话已保留。" };
    expect(agentTurnMessage(failed).delivery_error).toBe(failed.error_message);
    expect(agentTurnMessage({ ...failed, status: "running" }).delivery_error).toBeUndefined();
  });

  it("preserves server order when a user message and reply share a commit timestamp", () => {
    const messages: Message[] = [
      { message_id: "msg_z_user", role: "user", content: "question", created_at: "2026-09-05T00:00:01Z" },
      { message_id: "msg_a_reply", role: "assistant", content: "reply", created_at: "2026-09-05T00:00:01Z" },
    ];
    expect(mergeMessagesWithAgentTurns(messages, [])).toEqual(messages);
  });

  it("rebuilds a running user instruction after browser refresh", () => {
    expect(agentTurnMessage(turn("running"))).toMatchObject({
      message_id: "agent-turn:turn-1",
      agent_turn_id: "turn-1",
      content: "生成大纲",
      delivery_status: "running",
    });
  });

  it("keeps failed and cancelled requests but replaces committed turns with messages", () => {
    const committed = { ...turn("committed"), user_message_id: "message-user" };
    const message = {
      message_id: "message-user", role: "user", content: "生成大纲",
      created_at: "2026-09-04T00:00:01Z",
    } as Message;
    expect(mergeMessagesWithAgentTurns([message], [committed])).toEqual([message]);
    expect(mergeMessagesWithAgentTurns([], [turn("failed")])[0].delivery_status).toBe("failed");
    expect(mergeMessagesWithAgentTurns([], [turn("cancelled")])[0].delivery_status).toBe("cancelled");
  });

  it("removes the running synthetic message after a committed snapshot arrives", () => {
    const synthetic = agentTurnMessage(turn("running"));
    const committed = {
      ...turn("committed"), user_message_id: "message-user", agent_message_id: "message-agent",
    };
    const confirmed = [
      { message_id: "message-user", role: "user", content: "生成大纲", created_at: "2026-09-04T00:00:01Z" },
      { message_id: "message-agent", role: "assistant", content: "已完成", created_at: "2026-09-04T00:00:02Z" },
    ] as Message[];
    expect(mergeMessagesWithAgentTurns([...confirmed, synthetic], [committed])).toEqual(confirmed);
  });

  it("replaces an optimistic message when the durable turn reaches the snapshot first", () => {
    const optimistic = {
      message_id: "local_pending", role: "user", content: "生成大纲",
      created_at: "2026-09-04T00:00:01Z", delivery_status: "sending",
      message_context: { attachment_refs: [] }, submission_id: "exact",
    } as Message;
    const merged = mergeMessagesWithAgentTurns([optimistic], [{ ...turn("running"), submission_id: "exact" }]);
    expect(merged).toHaveLength(1);
    expect(merged[0].message_id).toBe("agent-turn:turn-1");
  });
});
