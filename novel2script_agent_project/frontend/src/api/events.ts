import type { RunEvent } from "./types";
import { API_BASE } from "./client";

const runEventTypes = ["run_started", "step_started", "artifact_created", "artifact_updated", "script_batch_inserted", "approval_requested", "approval_resolved", "progress_updated", "step_completed", "run_completed", "step_failed", "run_paused", "run_resumed"];

export function subscribeRunEvents(runID: string, onEvent: (event: RunEvent) => void, onConnectionError: () => void) {
  const source = new EventSource(`${API_BASE}/api/runs/${encodeURIComponent(runID)}/events/stream`);
  const listener = (message: MessageEvent<string>) => {
    try { onEvent(JSON.parse(message.data) as RunEvent); } catch { /* A malformed event is ignored; snapshot recovery remains authoritative. */ }
  };
  for (const eventType of runEventTypes) source.addEventListener(eventType, listener as EventListener);
  source.onerror = onConnectionError;
  return () => source.close();
}

export function mergeEvents(current: RunEvent[], incoming: RunEvent[]) {
  const byID = new Map<string, RunEvent>();
  for (const event of current) byID.set(event.event_id, event);
  for (const event of incoming) byID.set(event.event_id, event);
  return Array.from(byID.values()).sort((left, right) => {
    const leftTime = left.created_at ? Date.parse(left.created_at) : 0;
    const rightTime = right.created_at ? Date.parse(right.created_at) : 0;
    const timeDelta = leftTime - rightTime;
    return timeDelta || left.event_id.localeCompare(right.event_id);
  });
}
