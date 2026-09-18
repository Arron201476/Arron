# Agent Turn Streaming Contract

## 1. Boundary

`AgentTurn` is the durable unit for one user instruction. The public message API
persists the instruction and returns `202 Accepted` with `agent_turn_id`; the
request connection does not own the execution lifetime.

The Go Runtime owns ordering, state transitions, cancellation, replay and the
terminal decision. The Python Sidecar owns the OpenAI Agents SDK run and emits
authenticated internal execution events. The browser only consumes the
project event stream and never connects to the Sidecar directly.

## 2. State machine

```text
accepted -> running -> committing -> committed
    |          |    |
    |          |    +-> waiting_approval -> accepted -> running
    |          |                  |
    |          |                  +-> cancelled
    |          +-> cancel_requested -> cancelled
    +--------------------------------> cancelled
    +--------------------------------> failed
```

- A terminal state is exactly one of `committed`, `failed` or `cancelled`.
- `committing` is the point of no return. Cancellation cannot win after the
  validated commit has begun.
- Repeating the same POST idempotency key returns the original turn.
- Repeating cancel is resource-state idempotent and cannot create a second
  terminal event.
- Only the oldest accepted turn for a Conversation may be claimed. Different
  Conversations may execute concurrently, bounded by the manager worker limit.
- An SDK tool approval pauses the run as `waiting_approval`. The Runtime stores
  the SDK `RunState` in a private checkpoint keyed by the exact SDK tool call
  IDs. After every item is approved or rejected, the same turn returns to
  `accepted` and resumes through the SDK Runner without replaying approved work.
- The privacy-filtered observation accumulator is serialized with that private
  context, so response/request/trace IDs, token usage, and stage latency remain
  cumulative across every approval pause and process restart.

## 3. Event protocol

Project SSE events use the versioned Agent envelope and retain the durable
`project_event_seq` used by `Last-Event-ID` replay:

```json
{
  "schema_version": "1.0.0",
  "event_id": "evt_...",
  "project_event_seq": 42,
  "event_type": "agent.tool.started",
  "project_id": "prj_...",
  "conversation_id": "conv_...",
  "turn_id": "turn_...",
  "terminal": false,
  "payload": {}
}
```

Supported lifecycle events are:

- `agent.turn.started`, `agent.updated`
- `agent.tool.started`, `agent.tool.completed`
- `agent.output.delta`
- `agent.approval.requested`, `agent.artifact.created`
- `agent.turn.waiting_approval` (nonterminal)
- `agent.turn.committed`, `agent.turn.failed`, `agent.turn.cancelled`

The Sidecar uses the SDK `Runner.run_streamed` path. Its raw model deltas are
structured control output and are not safe user-facing prose, so they are not
forwarded. After the backend validates and commits the exchange, the Runtime
emits chunks from the committed assistant message as `agent.output.delta`.
Tool events are reduced to safe lifecycle metadata; hidden reasoning and raw
tool arguments are never projected to the browser.

The Sidecar's internal `agent.turn.waiting_approval` payload carries the SDK
`RunState` only to the authenticated Go Runtime. The public project event stores
checkpoint version, schema and approval counts, never the serialized state.

## 4. Recovery and cancellation

- Browser disconnect or refresh does not cancel a turn. A refreshed workspace
  snapshot includes active turns and SSE replay fills events after the last
  acknowledged sequence.
- User cancellation first persists `cancel_requested`, then cancels the active
  Go context and Sidecar SDK task. The durable terminal transition remains in
  Go, so transport loss cannot invent a second terminal state.
- On Go Runtime restart, `running` and `committing` turns become a sanitized
  `RUNTIME_RESTART_INTERRUPTED` failure; `cancel_requested` becomes cancelled;
  accepted turns remain queued and are reclaimed after the listener is ready.
  `waiting_approval` turns and their private checkpoints remain durable; once
  all exact-call decisions exist, the turn is reclaimed and resumed.
- Sidecar disconnect, malformed SSE, missing terminal, provider error and
  configuration error all become one sanitized `agent.turn.failed` event.

## 5. Local gates

W6/W9 tests cover fast acceptance with a slow executor, same-Conversation
serialization, cross-Conversation claiming, cancel/commit races, idempotent
cancellation, browser refresh projection, Last-Event-ID replay, Sidecar
disconnect/missing terminal, SDK streaming, durable SDK approval pause/resume,
approve/reject behavior, private-state non-disclosure, and restart recovery.

Run the local gates from the repository root:

```powershell
.\scripts\run-agent-platform-local-gate.ps1
```

These are local-only gates. They do not upload artifacts, contact the test
machine or restart any remote service.
