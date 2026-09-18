export const agentEventTypes = [
  "agent.turn.queued_updated",
  "agent.turn.started",
  "agent.updated",
  "agent.tool.started",
  "agent.tool.completed",
  "agent.output.delta",
  "agent.approval.requested",
  "agent.artifact.created",
  "agent.turn.waiting_approval",
  "agent.turn.pause_requested",
  "agent.turn.paused",
  "agent.turn.resume_requested",
  "agent.turn.input_received",
  "agent.turn.inputs_included",
  "agent.turn.committed",
  "agent.turn.failed",
  "agent.turn.cancelled",
] as const;

export type AgentEventType = typeof agentEventTypes[number];

export type AgentEventEnvelope = {
  schema_version: "1.0.0";
  event_id: string;
  event_type: AgentEventType;
  project_id: string;
  conversation_id: string;
  turn_id: string;
  terminal: boolean;
  payload: Record<string, unknown>;
  occurred_at: string;
};

const eventTypeSet = new Set<string>(agentEventTypes);

export function parseAgentEvent(value: unknown): AgentEventEnvelope | null {
  if (!value || typeof value !== "object") return null;
  const item = value as Record<string, unknown>;
  if (item.schema_version !== "1.0.0" || typeof item.event_type !== "string" || !eventTypeSet.has(item.event_type)) return null;
  for (const key of ["event_id", "project_id", "conversation_id", "turn_id", "occurred_at"]) {
    if (typeof item[key] !== "string" || item[key] === "") return null;
  }
  if (typeof item.terminal !== "boolean" || !item.payload || typeof item.payload !== "object" || Array.isArray(item.payload)) return null;
  return item as AgentEventEnvelope;
}

export function agentEventIsTerminal(event: AgentEventEnvelope): boolean {
  return event.terminal && ["agent.turn.committed", "agent.turn.failed", "agent.turn.cancelled"].includes(event.event_type);
}
