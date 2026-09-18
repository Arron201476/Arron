import type { AgentTurn, Message } from "./types";
import { sha256Hex } from "./contextTargeting";

export async function agentTurnSubmissionID(userID: string, conversationID: string, key: string): Promise<string | undefined> {
  // Older non-secure clients retain an unconfirmed row until the direct receipt.
  if (!key || typeof crypto === "undefined" || !crypto.subtle) return undefined;
  return sha256Hex(["agent-submission-v1", userID, conversationID, key].join("\0"));
}

const deliveryByStatus: Record<AgentTurn["status"], Message["delivery_status"]> = {
  accepted: "queued",
  running: "running",
  waiting_approval: "waiting_approval",
  pausing: "pausing",
  paused: "paused",
  cancel_requested: "cancelling",
  committing: "committing",
  committed: undefined,
  failed: "failed",
  cancelled: "cancelled",
};

export function agentTurnReceiptMatches(turn: AgentTurn, expected: Pick<AgentTurn, "workspace_id" | "user_id" | "project_id" | "conversation_id" | "submission_id">): boolean {
  return turn != null && typeof turn === "object"
    && typeof turn.agent_turn_id === "string" && turn.agent_turn_id.length > 0
    && turn.workspace_id === expected.workspace_id && turn.user_id === expected.user_id
    && turn.project_id === expected.project_id && turn.conversation_id === expected.conversation_id
    && (!expected.submission_id || turn.submission_id === expected.submission_id)
    && Object.hasOwn(deliveryByStatus, turn.status)
    && Number.isFinite(Date.parse(turn.created_at)) && Number.isFinite(Date.parse(turn.updated_at))
    && typeof turn.request?.content === "string"
    && (turn.request.attachment_refs == null || Array.isArray(turn.request.attachment_refs) && turn.request.attachment_refs.every((ref) => ref && typeof ref.asset_id === "string" && typeof ref.asset_snapshot_id === "string"));
}

export function agentTurnMessage(turn: AgentTurn): Message {
  return {
    message_id: `agent-turn:${turn.agent_turn_id}`,
    agent_turn_id: turn.agent_turn_id,
    submission_id: turn.submission_id,
    role: "user",
    content: turn.request.content,
    created_at: turn.created_at,
    delivery_status: deliveryByStatus[turn.status],
    delivery_error: turn.status === "failed" ? turn.error_message : undefined,
    message_context: {
      capability_ref: turn.request.capability_ref ?? null,
      attachment_refs: turn.request.attachment_refs ?? [],
      selection_snapshot: turn.request.selection_snapshot ?? null,
      client_context: turn.request.client_context ?? {},
      routing_context: { scope: "project" },
    },
  };
}

export function mergeMessagesWithAgentTurns(
  messages: Message[], turns: AgentTurn[],
): Message[] {
  const turnsBySyntheticMessageID = new Map(
    turns.map((turn) => [`agent-turn:${turn.agent_turn_id}`, turn]),
  );
  const projected = turns
    .filter((turn) => turn.status !== "committed")
    .filter((turn) => !turn.user_message_id || !messages.some((message) => message.message_id === turn.user_message_id))
    .map(agentTurnMessage);
  const retainedMessages = messages.filter((message) => {
    const durableTurn = turnsBySyntheticMessageID.get(message.message_id);
    if (durableTurn?.status === "committed") return false;
    if (!message.message_id.startsWith("local_") || !message.delivery_status || !message.submission_id) return true;
    return !turns.some((turn) => {
      if (turn.submission_id !== message.submission_id) return false;
      return turn.status !== "committed" || Boolean(turn.user_message_id && messages.some((item) => item.message_id === turn.user_message_id));
    });
  });
  const unique = new Map<string, Message>();
  for (const message of [...retainedMessages, ...projected]) {
    unique.set(message.message_id, message);
  }
  return [...unique.values()].sort((left, right) => {
    const byTime = new Date(left.created_at).getTime() - new Date(right.created_at).getTime();
    // Persisted user/assistant messages share a commit timestamp. Preserve
    // the server's conversational order instead of sorting their random IDs.
    return Number.isFinite(byTime) ? byTime : 0;
  });
}
