import type { AgentComposerContext, Message } from "./types";
import { createUUID } from "./uuid";

export function createOptimisticUserMessage(content: string, context: AgentComposerContext): Message {
  const selection = context.selection
    ? {
        artifact_version_id: context.view.artifact_version_id ?? "",
        snapshot_hash: "",
        selection: context.selection,
      }
    : null;
  return {
    message_id: `local_${createUUID()}`,
    role: "user",
    content,
    created_at: new Date().toISOString(),
    delivery_status: "sending",
    message_context: {
      attachment_refs: [],
      selection_snapshot: selection,
    },
  };
}

export function markOptimisticMessageFailed(messages: Message[], messageID: string): Message[] {
  return messages.map((message) => message.message_id === messageID
    ? { ...message, delivery_status: "failed" }
    : message);
}

export function markOptimisticMessageUnconfirmed(messages: Message[], messageID: string): Message[] {
  return messages.map((message) => message.message_id === messageID
    ? { ...message, delivery_status: "unconfirmed" }
    : message);
}

export function settleOptimisticMessage(messages: Message[], pendingID: string, confirmed: Message[]): Message[] {
  const confirmedIDs = new Set(confirmed.map((message) => message.message_id));
  return [
    ...messages.filter((message) => message.message_id !== pendingID && !confirmedIDs.has(message.message_id)),
    ...confirmed,
  ];
}

export function mergeSnapshotMessages(snapshot: Message[], current: Message[]): Message[] {
  const localMessages = [...new Map(
    current
      .filter((message) => message.delivery_status)
      .map((message) => [message.message_id, message]),
  ).values()];
  const snapshotIDs = new Set(snapshot.map((message) => message.message_id));
  const unmatchedLocalMessages = localMessages.filter((local) => !snapshotIDs.has(local.message_id));

  return [...snapshot, ...unmatchedLocalMessages].sort((left, right) =>
    Date.parse(left.created_at) - Date.parse(right.created_at),
  );
}
