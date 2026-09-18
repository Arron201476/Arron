import { describe, expect, it } from "vitest";
import { agentEventIsTerminal, parseAgentEvent } from "./agentEventProtocol";

const committed = {
  schema_version: "1.0.0",
  event_id: "agevt_1",
  event_type: "agent.turn.committed",
  project_id: "prj_1",
  conversation_id: "conv_1",
  turn_id: "turn_1",
  terminal: true,
  payload: { message_id: "msg_1" },
  occurred_at: "2026-08-25T10:00:00Z",
};

describe("agent event protocol", () => {
  it("accepts the provider-independent envelope", () => {
    const parsed = parseAgentEvent(committed);
    expect(parsed?.event_type).toBe("agent.turn.committed");
    expect(parsed && agentEventIsTerminal(parsed)).toBe(true);
  });

  it("rejects provider payloads and unknown event names", () => {
    expect(parseAgentEvent({ type: "response.output_text.delta", delta: "x" })).toBeNull();
    expect(parseAgentEvent({ ...committed, event_type: "run.completed" })).toBeNull();
  });
});
