// @vitest-environment jsdom
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";

class ProjectEventSource extends EventTarget {
  static instances: ProjectEventSource[] = [];
  onopen = null;
  onerror = null;
  close = vi.fn();

  constructor(public url: string) {
    super();
    ProjectEventSource.instances.push(this);
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  window.history.replaceState({}, "", "/projects/project-events");
  ProjectEventSource.instances = [];
  vi.stubGlobal("EventSource", ProjectEventSource);
  vi.spyOn(api, "getCurrentPrincipal").mockResolvedValue({ kind: "user", user_id: "u", display_name: "Fixture", workspace_id: "w", workspace_name: "Fixture", role: "editor", auth_method: "fixture" });
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(() => new Promise(() => {}));
  vi.spyOn(api, "listAssets").mockResolvedValue([]);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  window.history.replaceState({}, "", "/");
});

it.each(["execution.input_received", "execution.inputs_included", "agent_task.input_received", "agent.turn.queued_updated", "agent_task.waiting_approval", "agent_task.resumed", "agent_task.pause_requested", "agent_task.paused", "agent_task.resume_requested", "task.waiting_approval", "task.resumed", "task.paused", "task.progressed"])("refreshes the project on %s and removes the listener on unmount", async (eventType) => {
  let view: ReturnType<typeof render>;
  await act(async () => { view = render(<App />); });
  expect(ProjectEventSource.instances).toHaveLength(1);
  const source = ProjectEventSource.instances[0];
  expect(source.url).toBe("/api/v1/projects/project-events/events/stream");
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(1);
  await act(async () => {
    source.dispatchEvent(new MessageEvent(eventType, { data: "{}" }));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
  view!.unmount();
  expect(source.close).toHaveBeenCalledOnce();
  source.dispatchEvent(new MessageEvent(eventType, { data: "{}" }));
  await vi.advanceTimersByTimeAsync(120);
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
});

it.each(["agent.turn.pause_requested", "agent.turn.paused", "agent.turn.resume_requested", "agent.turn.input_received", "agent.turn.inputs_included"])("refreshes native lifecycle %s only for a valid project envelope", async (eventType) => {
  let view: ReturnType<typeof render>;
  await act(async () => { view = render(<App />); });
  const source = ProjectEventSource.instances[0];
  const event = { schema_version: "1.0.0", event_type: eventType, event_id: "lifecycle", project_id: "project-events", conversation_id: "c", turn_id: "t", terminal: false, payload: { status: "paused" }, occurred_at: "2026-09-06T00:00:00Z" };
  const dispatch = async (data: unknown) => act(async () => {
    source.dispatchEvent(new MessageEvent(eventType, { data: JSON.stringify(data) }));
    await vi.advanceTimersByTimeAsync(120);
  });
  await dispatch({});
  await dispatch({ ...event, project_id: "other-project" });
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(1);
  await dispatch(event);
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
  view!.unmount();
  await dispatch(event);
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
});

it.each(["revision_request.queued", "revision_request.waiting_safe_checkpoint", "revision_request.no_change", "revision_request.stale"])("refreshes legacy revision lifecycle %s", async (type) => {
  await act(async () => { render(<App />); });
  await act(async () => {
    ProjectEventSource.instances[0].dispatchEvent(new Event(type));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
});

it("accepts generic snapshot invalidation only for the current project and coalesces a batch", async () => {
  let view!: ReturnType<typeof render>;
  await act(async () => { view = render(<App />); });
  const source = ProjectEventSource.instances[0];
  const event = { stream_scope: "project", project_id: "project-events", project_event_seq: 17 };
  const dispatch = (value: unknown) => source.dispatchEvent(new MessageEvent("stream.snapshot_invalidated", { data: JSON.stringify(value) }));
  await act(async () => {
    for (const invalid of [null, {}, { ...event, project_id: "foreign" }, { ...event, stream_scope: "run" }, { ...event, project_event_seq: -1 }, { ...event, project_event_seq: "17" }, { ...event, project_event_seq: 1.5 }, { ...event, project_event_seq: Number.MAX_SAFE_INTEGER + 1 }]) dispatch(invalid);
    source.dispatchEvent(new MessageEvent("stream.snapshot_invalidated", { data: "not json" }));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(1);
  await act(async () => {
    dispatch(event); dispatch({ ...event, project_event_seq: 18 });
    source.dispatchEvent(new Event("revision_request.no_change"));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
  view.unmount();
  await act(async () => { dispatch(event); await vi.advanceTimersByTimeAsync(120); });
  expect(source.close).toHaveBeenCalledOnce();
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
});
