import type { AgentTask, AgentToolCall, Approval, Message, ProjectActivity, ProposedAction, RevisionRequest, RunSnapshot } from "./types";

export type ConversationTimelineItem =
  | { kind: "message"; id: string; at: string; value: Message }
  | { kind: "revision"; id: string; at: string; value: RevisionRequest }
  | { kind: "proposed_action"; id: string; at: string; value: ProposedAction }
  | { kind: "agent_task"; id: string; at: string; value: AgentTask }
  | { kind: "agent_tool_call"; id: string; at: string; value: AgentToolCall }
  | { kind: "run"; id: string; at: string; value: RunSnapshot }
  | { kind: "approval"; id: string; at: string; value: Approval };

type TimelineInput = {
  messages: Message[];
  revisions?: RevisionRequest[];
  proposedActions?: ProposedAction[];
  agentTasks?: AgentTask[];
  agentToolCalls?: AgentToolCall[];
  approvals?: Approval[];
  runSnapshot?: RunSnapshot | null;
  activities?: ProjectActivity[];
};

const kindOrder: Record<ConversationTimelineItem["kind"], number> = {
  message: 10,
  revision: 20,
  proposed_action: 30,
  agent_task: 35,
  agent_tool_call: 38,
  run: 36,
  approval: 40,
};

export function buildConversationTimeline(input: TimelineInput): ConversationTimelineItem[] {
  const items: ConversationTimelineItem[] = input.messages.map((message) => ({
    kind: "message",
    id: message.message_id,
    at: message.created_at,
    value: message,
  }));
  for (const revision of input.revisions ?? []) {
    items.push({ kind: "revision", id: revision.revision_request_id, at: revision.created_at, value: revision });
  }
  for (const action of input.proposedActions ?? []) {
    items.push({ kind: "proposed_action", id: action.proposed_action_id, at: action.created_at, value: action });
  }
  for (const task of input.agentTasks ?? []) {
    items.push({ kind: "agent_task", id: task.agent_task_id, at: task.created_at, value: task });
  }
  for (const call of input.agentToolCalls ?? []) {
    if (call.approval) {
      items.push({ kind: "agent_tool_call", id: call.agent_tool_call_id, at: call.requested_at, value: call });
    }
  }
  if (input.runSnapshot) {
    const runID = input.runSnapshot.run.run_id;
    const currentStep = input.runSnapshot.steps?.find((step) => step.step_run_id === input.runSnapshot?.run.current_step_run_id);
    const latestMatchingStepActivity = [...(input.activities ?? [])]
      .filter((activity) => activity.run_id === runID && activity.event_type === "step.started" && activity.step_id === currentStep?.step_id)
      .sort((left, right) => Date.parse(right.occurred_at) - Date.parse(left.occurred_at))[0];
    const startedAt = currentStep?.started_at
      ?? latestMatchingStepActivity?.occurred_at
      ?? input.activities?.find((activity) => activity.run_id === runID && activity.event_type === "run.started")?.occurred_at
      ?? input.runSnapshot.run.started_at
      ?? input.activities?.find((activity) => activity.run_id === runID)?.occurred_at
      ?? "9999-12-31T23:59:59Z";
    items.push({ kind: "run", id: runID, at: startedAt, value: input.runSnapshot });
  }
  for (const approval of input.approvals ?? []) {
    items.push({ kind: "approval", id: approval.approval_request_id, at: approval.requested_at, value: approval });
  }
  return items
    .map((item, index) => ({ item, index, time: Date.parse(item.at) }))
    .sort((left, right) => {
      const leftTime = Number.isNaN(left.time) ? Number.MAX_SAFE_INTEGER : left.time;
      const rightTime = Number.isNaN(right.time) ? Number.MAX_SAFE_INTEGER : right.time;
      return leftTime - rightTime || kindOrder[left.item.kind] - kindOrder[right.item.kind] || left.index - right.index;
    })
    .map(({ item }) => item);
}

export function revisionBlocksApproval(revisions: RevisionRequest[], approval: Approval): RevisionRequest | null {
  const blockingStatuses = new Set(["waiting_target_confirmation", "waiting_safe_checkpoint", "queued", "running", "proposed"]);
  return [...revisions]
    .reverse()
    .find((revision) =>
      blockingStatuses.has(revision.status) &&
      (!approval.project_id || revision.project_id === approval.project_id) &&
      (revision.base_artifact_version_id === approval.subject_ref_id ||
        revision.source_approval_request_id === approval.approval_request_id),
    ) ?? null;
}
