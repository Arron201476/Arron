// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { createHash, webcrypto } from "node:crypto";
import { App } from "./App";
import { api, ApiError } from "./api";
import { agentTurnSubmissionID } from "./agentTurns";
import { messageSubmission, starterContext } from "./messageSubmission";
import { composerEntry, legacyComposerEntries } from "./testComposerFixtures";
import { skillDraft, skillInstallResult } from "./components/files/skillDraftTestFixtures";
import type { AgentTurn, ArtifactVersion, ProjectWorkspaceProjection, ProposedAction, QualityReview, RunSnapshot, VersionResult } from "./types";
import type { AgentTask, AgentToolCall, FinalSelectionResult, PendingScriptEdit, RevisionRequest, ScriptCandidate, TargetResolution } from "./types";

class RecoveryEventSource extends EventTarget {
  static instances: RecoveryEventSource[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  close = vi.fn();
  constructor(public url: string) { super(); RecoveryEventSource.instances.push(this); }
}

const stamp = "2026-09-09T00:00:00Z";
const dialogMethods = ["showModal", "close"] as const;
const dialogDescriptors = dialogMethods.map((name) => Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, name));
beforeAll(() => { for (const name of dialogMethods) Object.defineProperty(HTMLDialogElement.prototype, name, { configurable: true, writable: true, value() {} }); });
afterAll(() => { dialogMethods.forEach((name, index) => { const descriptor = dialogDescriptors[index]; if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, name, descriptor); else Reflect.deleteProperty(HTMLDialogElement.prototype, name); }); });
function turn(projectID = "recovery", status: AgentTurn["status"] = "running"): AgentTurn {
  return { agent_turn_id: `turn-${projectID}`, workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}`, status, request: { content: "Current request" }, created_at: stamp, updated_at: stamp };
}
function submissionReceipt(value: AgentTurn, key: string | undefined): AgentTurn {
  if (!key) throw new Error("Message fixture requires the original request key");
  return { ...value, submission_id: createHash("sha256").update(["agent-submission-v1", value.user_id, value.conversation_id, key].join("\0")).digest("hex") };
}
const messageReply = (value: AgentTurn): typeof api.sendMessage => async (...args) => submissionReceipt(value, args[6]);
function projection(projectID = "recovery"): ProjectWorkspaceProjection {
  return {
    contract_version: "1", registry_revision: "fixture",
    registries: { artifacts: [], navigation: [], composer: [], interactions: [], approvals: [], tasks: [] },
    snapshot: {
      project: { project_id: projectID, workspace_id: "w", owner_user_id: "u", title: `Project ${projectID}`, version: 1, status: "active", primary_conversation_id: `conversation-${projectID}`, active_write_run_id: null, current_capability_id: null, latest_capability_id: null, latest_run_status: null, current_focus_artifact_version_id: null, updated_at: stamp },
      messages: [], artifacts: [], approvals: [], capabilities: [], artifact_presentations: [], script_candidates: [], final_selection: null, active_run: null, pending_proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [], agent_turns: [], agent_tasks: [], agent_tool_calls: [],
    },
  };
}

it("shows the durable project goal and clears it after an authoritative completion refresh", async () => {
  const state = projection();
  state.snapshot.goal = { goal_id: "goal-1", project_id: "recovery", conversation_id: "conversation-recovery", title: "Deliver the agreed story", success_criteria: ["Approved outline", "Complete manuscript"], status: "active", version: 1 };
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  await act(async () => { render(<App />); });
  expect(screen.getByText("Deliver the agreed story")).toBeTruthy();
  fireEvent.click(screen.getByText("当前目标"));
  expect(screen.getByText("Approved outline")).toBeTruthy();
  expect(screen.getByText("Complete manuscript")).toBeTruthy();
  read.mockResolvedValue({ ...state, snapshot: { ...state.snapshot, goal: null } });
  await act(async () => {
    RecoveryEventSource.instances.at(-1)!.dispatchEvent(new MessageEvent("agent.turn.committed", { data: JSON.stringify({ schema_version: "1.0.0", event_id: "goal-finished", event_type: "agent.turn.committed", project_id: "recovery", conversation_id: "conversation-recovery", turn_id: "goal-turn", terminal: true, payload: {}, occurred_at: stamp }) }));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(screen.queryByText("Deliver the agreed story")).toBeNull();
});

it.each(["project_id", "conversation_id", "status", "success_criteria"])("does not display a mismatched or invalid project goal: %s", async (field) => {
  const state = projection();
  state.snapshot.goal = { goal_id: "goal-1", project_id: "recovery", conversation_id: "conversation-recovery", title: "Private goal", success_criteria: ["Private criterion"], status: "active", version: 1, [field]: "foreign" };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  await act(async () => { render(<App />); });
  expect(screen.queryByText("Private goal")).toBeNull();
  expect(screen.queryByText("Private criterion")).toBeNull();
});

it("restores a homepage request through the actual App and API path without adopting a newer Skill or client context", async () => {
  vi.stubGlobal("crypto", webcrypto);
  const owner = { workspace_id: "w", user_id: "u", project_id: "recovery", conversation_id: "conversation-recovery" };
  const entry = legacyComposerEntries[0];
  const original = messageSubmission.prepare(owner, { content: "Original homepage request", capability: entry, attachments: [{ asset_id: "source", asset_snapshot_id: "source-v1", display_name: "original.txt", kind: "text" }], video_batch: null, context: starterContext(owner.project_id, entry, null), clientInstanceID: "original-client" }, "starter", "f0000000-0000-4000-8000-000000000001");
  const state = projection();
  state.registries.composer = [entry];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const accepted = { ...turn("recovery", "accepted"), submission_id: await agentTurnSubmissionID(owner.user_id, owner.conversation_id, original.key) };
  const send = vi.spyOn(api, "sendMessage").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Homepage response lost", 503)).mockResolvedValue(accepted);
  let first!: ReturnType<typeof render>;
  await act(async () => { first = render(<App />); });
  await act(async () => { await vi.waitFor(() => expect(send).toHaveBeenCalledTimes(1)); });
  expect(screen.getByText("Homepage response lost")).toBeTruthy();
  expect(messageSubmission.read(owner)?.phase).toBe("pending");
  first.unmount();
  state.registries.composer = [{ ...entry, version: "99.0.0" }];
  await act(async () => { render(<App />); });
  expect(screen.getByText("上次发送结果未确认，已恢复原消息。")).toBeTruthy();
  expect(send).toHaveBeenCalledTimes(1);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); await vi.waitFor(() => expect(send).toHaveBeenCalledTimes(2)); });
  const firstArgs = send.mock.calls[0], retryArgs = send.mock.calls[1];
  expect(retryArgs.slice(0, 5)).toEqual(firstArgs.slice(0, 5));
  expect(retryArgs[4]).toEqual(original.draft.context);
  expect(retryArgs.slice(6)).toEqual([original.key, original.draft.clientInstanceID]);
  expect(messageSubmission.read(owner)).toBeNull();
});
function executing(mode: string, status: string) {
  const result = projection();
  if (mode === "conversation") result.snapshot.agent_turns = [turn("recovery", status as AgentTurn["status"])];
  if (mode === "background") result.snapshot.agent_tasks = [{
    agent_task_id: "background", workspace_id: "w", user_id: "u", project_id: "recovery", conversation_id: "conversation-recovery", skill_invocation_id: "skill", capability_id: "review", capability_version: "1", status,
    progress_current: 0, progress_total: 1, progress_message: "Working", input: {}, config: {}, attempt_count: 1, max_attempts: 1, cancel_requested: false, created_at: stamp, queued_at: stamp, updated_at: stamp,
  }];
  if (mode === "workflow") result.snapshot.active_run = { run: { run_id: "run", capability_id: "review", status, current_step_run_id: null }, available_actions: [], current_approval: null };
  return result;
}

it.each([false, true].flatMap((hasCancel) => [false, true].map((recovering) => ({ hasCancel, recovering }))))("sends a message without cancelling a failed run, cancel action=$hasCancel recovery=$recovering", async ({ hasCancel, recovering }) => {
  const projectID = `failed-run-${recovering}-${hasCancel}`;
  window.history.replaceState({}, "", `/projects/${projectID}`);
  const owner = { workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}` };
  const entry = legacyComposerEntries[0];
  const original = messageSubmission.prepare(owner, { content: "Earlier message", capability: entry, attachments: [{ asset_id: "source", asset_snapshot_id: "source-v1", display_name: "source.txt", kind: "text" }], video_batch: null, context: starterContext(owner.project_id, entry, null), clientInstanceID: "original-client" }, "composer");
  if (recovering) messageSubmission.claim(original);
  const state = projection(projectID);
  state.registries.composer = [entry];
  state.snapshot.project.active_write_run_id = "later-failed-run";
  state.snapshot.active_run = { run: { run_id: "later-failed-run", capability_id: entry.capability_id, status: "failed", current_step_run_id: null }, current_approval: null, available_actions: hasCancel ? [{ action_id: "cancel_run", target_id: "later-failed-run", target_type: "run", enabled: true, disabled_reason: null }] : [] };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const cancel = vi.spyOn(api, "executeRunAction").mockRejectedValue(new Error("Recovery must not cancel another execution"));
  const send = vi.spyOn(api, "sendMessage").mockImplementation(messageReply({ ...turn(projectID, "accepted"), request: { content: original.draft.content } }));
  await act(async () => { render(<App />); });
  expect(send).not.toHaveBeenCalled();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send).toHaveBeenCalledOnce();
  expect(cancel).not.toHaveBeenCalled();
  expect(send.mock.calls[0][6]).toBe(original.key);
  expect(messageSubmission.read(owner)).toBeNull();
});

it.each(["inline", "background_task", "stateful_workflow"])("does not change a failed run when a new %s message is rejected", async (mode) => {
  const projectID = `rejected-with-failed-run-${mode}`;
  window.history.replaceState({}, "", `/projects/${projectID}`);
  const owner = { workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}` };
  const entry = composerEntry({ capability_id: `selected-${mode}`, label: "Selected Skill", execution_mode: mode, creates_run: mode === "stateful_workflow" });
  const original = messageSubmission.prepare(owner, { content: "New request", capability: entry, attachments: [], video_batch: null, context: starterContext(projectID, entry, null), clientInstanceID: "original-client" }, "composer");
  const state = projection(projectID);
  state.registries.composer = [entry];
  state.snapshot.project.active_write_run_id = "previous-failed-run";
  state.snapshot.active_run = { run: { run_id: "previous-failed-run", capability_id: "previous-skill", status: "failed", current_step_run_id: null }, current_approval: null, available_actions: [{ action_id: "cancel_run", target_id: "previous-failed-run", target_type: "run", enabled: true, disabled_reason: null }] };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const cancel = vi.spyOn(api, "executeRunAction").mockRejectedValue(new Error("Sending must not cancel the existing run"));
  const start = vi.spyOn(api, "startRun");
  const send = vi.spyOn(api, "sendMessage").mockRejectedValue(new ApiError("INVALID_MATERIAL", "Selected source is unavailable", 422));
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { await vi.waitFor(() => expect(screen.getByText("Selected source is unavailable")).toBeTruthy()); });
  expect(send).toHaveBeenCalledOnce();
  expect(send.mock.calls[0][6]).toBe(original.key);
  expect(cancel).not.toHaveBeenCalled();
  expect(start).not.toHaveBeenCalled();
  expect(state.snapshot.active_run?.run.status).toBe("failed");
});

it.each(["workspace", "user", "project", "conversation", "submission", "empty-id", "status", "timestamp", "request", "null", "empty-exchange"])("retains the original submission after a %s receipt mismatch and retries the same request", async (mismatch) => {
  vi.stubGlobal("crypto", webcrypto);
  const projectID = `receipt-${mismatch}`;
  window.history.replaceState({}, "", `/projects/${projectID}`);
  const owner = { workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}` };
  const original = messageSubmission.prepare(owner, { content: "Original receipt target", capability: null, attachments: [], video_batch: null, context: starterContext(owner.project_id, null, null), clientInstanceID: "original-client" }, "composer");
  messageSubmission.claim(original);
  const accepted: AgentTurn = { ...turn(projectID, "accepted"), submission_id: await agentTurnSubmissionID(owner.user_id, owner.conversation_id, original.key), request: { content: "Original receipt target" } };
  const mutations: Record<string, unknown> = {
    workspace: { ...accepted, workspace_id: "foreign" }, user: { ...accepted, user_id: "foreign" }, project: { ...accepted, project_id: "foreign" }, conversation: { ...accepted, conversation_id: "foreign" },
    submission: { ...accepted, submission_id: "foreign" }, "empty-id": { ...accepted, agent_turn_id: "" }, status: { ...accepted, status: "unknown" }, timestamp: { ...accepted, updated_at: "invalid" }, request: { ...accepted, request: null }, null: null, "empty-exchange": {},
  };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection(projectID));
  const send = vi.spyOn(api, "sendMessage").mockResolvedValueOnce(mutations[mismatch] as AgentTurn).mockResolvedValue(accepted);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { await vi.waitFor(() => expect(screen.getByText(/消息回执.*结果仍待确认/)).toBeTruthy()); });
  expect(messageSubmission.read(owner)?.key).toBe(original.key);
  expect(document.querySelectorAll(".timeline-message.unconfirmed")).toHaveLength(1);
  expect(document.querySelectorAll(".timeline-message.queued")).toHaveLength(0);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { await vi.waitFor(() => expect(messageSubmission.read(owner)).toBeNull()); });
  expect(send.mock.calls[1][6]).toBe(original.key);
  expect(send.mock.calls[1].slice(0, 5)).toEqual(send.mock.calls[0].slice(0, 5));
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
});

it.each([["ROLE_FORBIDDEN", 403], ["IDEMPOTENCY_KEY_REUSED", 400], ["COMMAND_IN_PROGRESS", 409]] as const)("does not call the original message unexecuted when recovery returns %s", async (code, status) => {
  const projectID = `denied-${code}`;
  window.history.replaceState({}, "", `/projects/${projectID}`);
  const owner = { workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}` };
  const original = messageSubmission.prepare(owner, { content: "Unknown earlier submission", capability: null, attachments: [], video_batch: null, context: starterContext(owner.project_id, null, null), clientInstanceID: "original-client" }, "composer");
  messageSubmission.claim(original);
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection(projectID));
  vi.spyOn(api, "sendMessage").mockRejectedValue(new ApiError(code, "Recovery denied", status));
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(screen.getByText("Recovery denied")).toBeTruthy();
  expect(document.querySelectorAll(".timeline-message.unconfirmed")).toHaveLength(1);
  expect(document.querySelectorAll(".timeline-message.failed")).toHaveLength(0);
  expect(messageSubmission.read(owner)?.key).toBe(original.key);
});

beforeEach(() => {
  vi.useFakeTimers();
  sessionStorage.clear();
  window.history.replaceState({}, "", "/projects/recovery");
  RecoveryEventSource.instances = [];
  vi.stubGlobal("EventSource", RecoveryEventSource);
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("Unexpected fixture network access")));
  vi.spyOn(api, "getCurrentPrincipal").mockResolvedValue({ kind: "user", user_id: "u", display_name: "Fixture", workspace_id: "w", workspace_name: "Fixture", role: "editor", auth_method: "fixture" });
  vi.spyOn(api, "listAssets").mockResolvedValue([]);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); sessionStorage.clear(); window.history.replaceState({}, "", "/"); });

it.each(["focus", "visible", "route", "timer"])("refreshes session identity on %s without dropping a same-owner draft", async (trigger) => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const send = vi.spyOn(api, "sendMessage");
  const editor = await api.getCurrentPrincipal();
  vi.mocked(api.getCurrentPrincipal).mockClear();
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByRole("textbox", { name: "给 Agent 的消息" }), { target: { value: "Keep this draft" } });
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ ...editor, role: "viewer" });
  await act(async () => {
    if (trigger === "focus") window.dispatchEvent(new Event("focus"));
    if (trigger === "visible") document.dispatchEvent(new Event("visibilitychange"));
    if (trigger === "route") window.dispatchEvent(new PopStateEvent("popstate"));
    if (trigger === "timer") await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(api.getCurrentPrincipal).toHaveBeenCalledTimes(2);
  const input = screen.getByRole("textbox", { name: "给 Agent 的消息" }) as HTMLTextAreaElement;
  expect(input.value).toBe("Keep this draft");
  expect(input.disabled).toBe(true);
  expect((screen.getByRole("button", { name: "发送" }) as HTMLButtonElement).disabled).toBe(true);
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue(editor);
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect((screen.getByRole("textbox", { name: "给 Agent 的消息" }) as HTMLTextAreaElement).value).toBe("Keep this draft");
  expect((screen.getByRole("button", { name: "发送" }) as HTMLButtonElement).disabled).toBe(false);
  expect(send).not.toHaveBeenCalled();
});

it.each([401, 403])("invalidates session identity after authoritative %s without deleting an unknown submission", async (status) => {
  const owner = { workspace_id: "w", user_id: "u", project_id: "recovery", conversation_id: "conversation-recovery" };
  const original = messageSubmission.prepare(owner, { content: "Unknown request", capability: null, attachments: [], video_batch: null, context: starterContext(owner.project_id, null, null), clientInstanceID: "session-fixture" }, "composer");
  messageSubmission.claim(original);
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const send = vi.spyOn(api, "sendMessage");
  await act(async () => { render(<App />); });
  const source = RecoveryEventSource.instances[0];
  vi.mocked(api.getCurrentPrincipal).mockRejectedValue(new ApiError("SESSION_REVOKED", "Session expired", status));
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(screen.getByRole("heading", { name: "登录工作区" })).toBeTruthy();
  expect(source.close).toHaveBeenCalledOnce();
  expect(messageSubmission.read(owner)?.key).toBe(original.key);
  const calls = vi.mocked(api.getCurrentPrincipal).mock.calls.length;
  await act(async () => { window.dispatchEvent(new Event("focus")); await vi.advanceTimersByTimeAsync(60_000); });
  expect(api.getCurrentPrincipal).toHaveBeenCalledTimes(calls);
  expect(send).not.toHaveBeenCalled();
});

it.each([new Error("Offline"), new ApiError("UNAVAILABLE", "Try later", 503)])("preserves session identity and draft on a transient refresh failure: %s", async (error) => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const editor = await api.getCurrentPrincipal();
  vi.mocked(api.getCurrentPrincipal).mockClear();
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByRole("textbox", { name: "给 Agent 的消息" }), { target: { value: "Offline draft" } });
  vi.mocked(api.getCurrentPrincipal).mockRejectedValueOnce(error).mockResolvedValue({ ...editor, role: "viewer" });
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect((screen.getByRole("textbox", { name: "给 Agent 的消息" }) as HTMLTextAreaElement).value).toBe("Offline draft");
  expect(screen.queryByRole("heading", { name: "登录工作区" })).toBeNull();
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect((screen.getByRole("textbox", { name: "给 Agent 的消息" }) as HTMLTextAreaElement).disabled).toBe(true);
});

it("does not replace cookie session identity with implicit local authentication after expiry", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const editor = await api.getCurrentPrincipal();
  vi.mocked(api.getCurrentPrincipal).mockReset().mockResolvedValue({ ...editor, auth_method: "session_cookie" });
  await act(async () => { render(<App />); });
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ ...editor, role: "owner", auth_method: "local_loopback" });
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(screen.getByRole("heading", { name: "登录工作区" })).toBeTruthy();
  expect(screen.queryByRole("textbox", { name: "给 Agent 的消息" })).toBeNull();
});

it("rechecks session identity on stream revocation and ignores an older read after explicit login", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const editor = await api.getCurrentPrincipal();
  vi.mocked(api.getCurrentPrincipal).mockClear();
  await act(async () => { render(<App />); });
  let resolveOld!: (value: typeof editor) => void;
  vi.mocked(api.getCurrentPrincipal).mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }))
    .mockRejectedValueOnce(new ApiError("AUTHENTICATION_REQUIRED", "Expired", 401));
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { RecoveryEventSource.instances[0].dispatchEvent(new MessageEvent("stream.access_revoked", { data: JSON.stringify({ stream_scope: "project" }) })); });
  expect(screen.getByRole("heading", { name: "登录工作区" })).toBeTruthy();
  vi.spyOn(api, "createAuthSession").mockResolvedValue({ ...editor, role: "viewer", auth_method: "bearer_token" });
  fireEvent.change(screen.getByLabelText("访问令牌"), { target: { value: "fixture-token" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "登录" })); });
  await act(async () => { resolveOld(editor); });
  expect((screen.getByRole("textbox", { name: "给 Agent 的消息" }) as HTMLTextAreaElement).disabled).toBe(true);
  expect(api.createAuthSession).toHaveBeenCalledOnce();
});

it.each(["local_loopback", "session_cookie"])("tracks a freshly created cookie session identity before the first recheck: %s", async (authMethod) => {
  const editor = await api.getCurrentPrincipal();
  vi.mocked(api.getCurrentPrincipal).mockRejectedValueOnce(new ApiError("AUTHENTICATION_REQUIRED", "Login required", 401));
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  vi.spyOn(api, "createAuthSession").mockResolvedValue({ ...editor, auth_method: "bearer_token" });
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("访问令牌"), { target: { value: "fixture-token" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "登录" })); });
  expect(screen.getByRole("textbox", { name: "给 Agent 的消息" })).toBeTruthy();
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ ...editor, auth_method: authMethod });
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  if (authMethod === "local_loopback") {
    expect(screen.getByRole("heading", { name: "登录工作区" })).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "给 Agent 的消息" })).toBeNull();
  } else {
    expect(screen.queryByRole("heading", { name: "登录工作区" })).toBeNull();
    expect(screen.getByRole("textbox", { name: "给 Agent 的消息" })).toBeTruthy();
  }
});

it("deduplicates session identity refreshes and removes timers and listeners on unmount", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const editor = await api.getCurrentPrincipal();
  vi.mocked(api.getCurrentPrincipal).mockClear();
  let view!: ReturnType<typeof render>;
  await act(async () => { view = render(<App />); });
  let resolveRead!: (value: typeof editor) => void;
  vi.mocked(api.getCurrentPrincipal).mockImplementationOnce(() => new Promise((resolve) => { resolveRead = resolve; }));
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new PopStateEvent("popstate"));
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(api.getCurrentPrincipal).toHaveBeenCalledTimes(2);
  view.unmount();
  await act(async () => {
    resolveRead(editor);
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new PopStateEvent("popstate"));
    await vi.advanceTimersByTimeAsync(60_000);
  });
  expect(api.getCurrentPrincipal).toHaveBeenCalledTimes(2);
  expect(RecoveryEventSource.instances).toHaveLength(1);
});

it("keeps session identity checks idle while hidden and refreshes on visibility return", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  await act(async () => { render(<App />); });
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  await act(async () => {
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(60_000);
  });
  expect(api.getCurrentPrincipal).toHaveBeenCalledOnce();
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  expect(api.getCurrentPrincipal).toHaveBeenCalledTimes(2);
});

it("applies a viewer session identity to homepage write controls without losing the prompt", async () => {
  window.history.replaceState({}, "", "/");
  vi.spyOn(api, "listProjects").mockResolvedValue([projection().snapshot.project]);
  vi.spyOn(api, "getWorkspaceProjection").mockResolvedValue(projection());
  const editor = await api.getCurrentPrincipal();
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("新作品要求"), { target: { value: "Keep the homepage prompt" } });
  fireEvent.click(screen.getByRole("button", { name: "打开 Project recovery 的操作菜单" }));
  expect(screen.getByRole("button", { name: "删除作品" })).toBeTruthy();
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ ...editor, role: "viewer" });
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect((screen.getByRole("button", { name: "创建作品并发送" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByLabelText("新作品要求") as HTMLTextAreaElement).value).toBe("Keep the homepage prompt");
  expect(screen.queryByRole("button", { name: "删除作品" })).toBeNull();
  expect(screen.queryByRole("button", { name: "重命名" })).toBeNull();
  expect(screen.queryByRole("button", { name: "新建作品" })).toBeNull();
});

it.each(["starter", "empty-project"])("ignores a pending %s creation after session identity changes", async (mode) => {
  window.history.replaceState({}, "", "/");
  vi.spyOn(api, "listProjects").mockResolvedValue([]);
  vi.spyOn(api, "getWorkspaceProjection").mockResolvedValue(projection());
  const original = await api.getCurrentPrincipal();
  let resolveCreate!: (value: ReturnType<typeof projection>["snapshot"]["project"]) => void;
  const create = vi.spyOn(api, "createProject").mockImplementation(() => new Promise((resolve) => { resolveCreate = resolve; }));
  const upload = vi.spyOn(api, "uploadFiles");
  await act(async () => { render(<App />); });
  if (mode === "starter") {
    fireEvent.change(screen.getByLabelText("新作品要求"), { target: { value: "Original account prompt" } });
    fireEvent.change(document.querySelector('.starter-composer input[type="file"]')!, { target: { files: [new File(["Original account source"], "source.txt", { type: "text/plain" })] } });
    fireEvent.click(screen.getByRole("button", { name: "创建作品并发送" }));
  } else {
    fireEvent.click(screen.getByRole("button", { name: "新建作品" }));
    fireEvent.change(screen.getByLabelText("作品名称"), { target: { value: "Original account project" } });
    fireEvent.click(screen.getByRole("button", { name: "创建并打开" }));
  }
  expect(create).toHaveBeenCalledOnce();
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ ...original, user_id: "another-user", display_name: "Another user", workspace_id: "another-workspace" });
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { resolveCreate(projection().snapshot.project); });
  expect(location.pathname).toBe("/");
  expect((screen.getByLabelText("新作品要求") as HTMLTextAreaElement).value).toBe("");
  expect(upload).not.toHaveBeenCalled();
  expect(messageSubmission.read({ workspace_id: "w", user_id: "u", project_id: "recovery", conversation_id: "conversation-recovery" })).toBeNull();
});

function proposalSnapshot(mode = "workflow", configured = false) {
  const entry = legacyComposerEntries.find((item) => item.capability_id === "novel_to_script")!;
  const snapshot = projection();
  snapshot.registries.composer = [entry];
  const action: ProposedAction = { proposed_action_id: "proposal", version: 1, snapshot_hash: "original", status: "pending", action_type: mode === "background" ? "start_background_task" : configured ? "start_run" : "collect_run_configuration", capability_ref: { capability_id: entry.capability_id, version: entry.version }, input: { assets: [{ asset_id: "asset", asset_snapshot_id: "asset-version", role: "primary_source", order: 1 }] }, config: { payload: { target_episode_count: 7, episode_duration_minutes: 3 } }, confirmation_message_id: "confirmation", created_at: stamp, updated_at: stamp };
  snapshot.snapshot.proposed_actions = [action];
  vi.spyOn(api, "getProjectCapability").mockResolvedValue({ capability_id: entry.capability_id, version: entry.version, label: entry.label, description: entry.description, status: entry.status, execution_mode: entry.execution_mode, creates_run: entry.creates_run, accepted_asset_kinds: entry.accepted_asset_kinds, input_binding: entry.input_binding, config_options: entry.config.options, default_config_ref: entry.config.default_ref, ui_entry: { icon_key: entry.icon_key, menu_order: entry.menu_order, entry_view_key: entry.view_key, config_view_key: entry.config.view_key } });
  return snapshot;
}

function approvalSnapshot(runID: string | undefined = "approval-run") {
  const state = projection();
  state.snapshot.project.active_write_run_id = "different-run";
  state.registries.approvals = [{ registry_key: "confirm", view_key: "action_list", match: { option: "approve" } }];
  state.snapshot.approvals = [{ approval_request_id: "business-approval", run_id: runID, status: "pending", scope: "transition", version: 1, subject_kind: "artifact_version", subject_ref_id: "av", subject_version: 1, subject_snapshot_hash: "subject", title: "Confirm source", reason: "Continue source", options: ["approve"], requested_at: stamp }];
  return state;
}

function resumable(runID: string): RunSnapshot {
  return { run: { run_id: runID, capability_id: "test", status: "paused", current_step_run_id: null }, current_approval: null, available_actions: [{ action_id: "resume_run", disabled_reason: null, enabled: true, target_id: runID, target_type: "run" }] };
}

function qualityEditSnapshot() {
  const state = approvalSnapshot();
  state.snapshot.project.current_focus_artifact_version_id = "old-script-v1";
  state.registries.approvals = [{ registry_key: "quality", view_key: "quality_review", match: { scope: "quality_review" } }];
  state.snapshot.approvals[0] = { ...state.snapshot.approvals[0], scope: "quality_review", subject_ref_id: "review", options: ["manual_edit"] };
  state.registries.artifacts = [{ artifact_type: "script_unit", view_key: "script", label: "Script", description: "Script", editable: true, preferred_fields: [], available_actions: [] }];
  state.snapshot.artifacts = ["old-script", "current-script"].map((id, index) => ({ artifact_id: id, run_id: index ? "approval-run" : "old-run", scope_key: "episode:1", artifact_type: "script_unit", current_version_id: `${id}-v1`, updated_at: stamp }));
  const review: QualityReview = { quality_review_id: "review", project_id: "recovery", run_id: "approval-run", step_run_id: "review-step", review_version: 1, input_snapshot_hash: "subject", status: "action_required", scope: "set", issue_counts: { blocker: 1, high: 0, medium: 0, low: 0 }, affected_episode_nos: [1], recommended_route: "script_generation", result: {} };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const read = vi.spyOn(api, "getArtifactVersion").mockImplementation(async (id): Promise<ArtifactVersion> => ({ artifact_id: id.replace(/-v1$/, ""), artifact_version_id: id, version: 1, status: "confirmed", payload: { script_text: `Body of ${id}` }, creation_reason: "generated", created_at: stamp }));
  return { state, review, read };
}

function mainTurnCommandFixture(action: "edit" | "pause" | "resume" | "cancel") {
  const state = executing("conversation", action === "edit" ? "accepted" : action === "resume" ? "paused" : "running");
  const current = state.snapshot.agent_turns![0];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const click = async () => {
    if (action === "edit") {
      if (!screen.queryByRole("textbox", { name: "编辑排队消息内容" })) {
        await act(async () => fireEvent.click(screen.getByRole("button", { name: "编辑排队消息", exact: true })));
        fireEvent.change(screen.getByRole("textbox", { name: "编辑排队消息内容" }), { target: { value: "Edited request" } });
      }
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "保存排队消息" })));
    } else await act(async () => fireEvent.click(screen.getByRole("button", { name: action === "cancel" ? "停止本轮" : action === "pause" ? "暂停本轮" : "继续本轮" })));
  };
  return { state, current, click };
}

it.each([false, true])("refreshes an authored Skill into the real workbench menu and sends its version, refresh failure=%s", async (failRefresh) => {
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (this: HTMLDialogElement) { this.removeAttribute("open"); });
  const state = projection(), draft = skillDraft("recovery", "draft"), saved = skillInstallResult(draft);
  saved.installation.workspace_id = "w";
  const entry = composerEntry({ capability_id: draft.capability_id!, version: draft.version, label: "Authored Skill", execution_mode: "inline", creates_run: false, default_prompt: "Use this authored rubric", skill: draft.manifest });
  let failNextRead = false;
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => {
    if (failNextRead) { failNextRead = false; throw new ApiError("UNAVAILABLE", "Catalog unavailable", 503); }
    return structuredClone(state);
  });
  const file = { project_id: "recovery", path: "draft/SKILL.md", version: 1, content_hash: "file-hash", size_bytes: 4, deleted: false, agent_tool_call_id: "write", created_at: stamp };
  vi.spyOn(api, "listProjectFiles").mockResolvedValue([file]);
  vi.spyOn(api, "readProjectFile").mockResolvedValue({ file, content: "Text", offset: 0, next_offset: 4, truncated: false });
  vi.spyOn(api, "previewProjectSkillDraft").mockResolvedValue(draft);
  vi.spyOn(api, "getSkillManagementOptions").mockResolvedValue({ install_scopes: ["project"], system_read_only: true });
  const install = vi.spyOn(api, "installProjectSkillDraft").mockImplementation(async () => { state.registries.composer = [entry]; failNextRead = failRefresh; return saved; });
  const send = vi.spyOn(api, "sendMessage").mockImplementation(messageReply(turn("recovery", "accepted")));
  const click = (name: string | RegExp) => act(async () => fireEvent.click(screen.getByRole("button", { name })));
  await act(async () => render(<App />));
  await click("工作文件"); await click(/draft\/SKILL.md/); await click("校验 Skill"); await click("确认安装");
  expect(screen.getByText("已安装，后续对话可调用。")).toBeTruthy();
  if (failRefresh) {
    expect(screen.getByText("Skill 已安装，能力目录刷新失败，请重试刷新。")).toBeTruthy();
    await click("刷新能力目录");
  }
  expect(install).toHaveBeenCalledOnce();
  await click("关闭工作文件"); await click("Skill"); await click(/Authored Skill/);
  await click("发送");
  expect(send).toHaveBeenCalledOnce();
  expect(send.mock.calls[0][2]).toEqual(entry);
  expect(send.mock.calls[0][1]).toBe("Use this authored rubric");
});

it.each(["edit", "pause", "resume", "cancel"] as const)("retains the same main-turn %s command after an unknown response", async (action) => {
  const { current, click } = mainTurnCommandFixture(action);
  const updated = { ...current, status: action === "edit" ? "accepted" : action === "pause" ? "pausing" : action === "resume" ? "accepted" : "cancel_requested", request: { ...current.request, content: action === "edit" ? "Edited request" : current.request.content } } as AgentTurn;
  const command = action === "edit" ? vi.spyOn(api, "updateQueuedAgentTurn").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue(updated) : action === "cancel" ? vi.spyOn(api, "cancelAgentTurn").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue({ turn: updated, accepted: true }) : vi.spyOn(api, "controlAgentTurnPause").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue(updated);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("暂时无法连接服务，请检查后端是否已启动。")).toBeTruthy();
  await click();
  expect(command).toHaveBeenCalledTimes(2);
  expect(command.mock.calls[1]).toEqual(command.mock.calls[0]);
  expect(command.mock.calls[0].at(-1)).toMatch(/^[a-f0-9-]{36}$/i);
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(3);
});

it.each(["edit", "pause", "resume", "cancel"] as const)("rejects a main-turn %s receipt for another execution", async (action) => {
  const { current, click } = mainTurnCommandFixture(action);
  const foreign = { ...current, agent_turn_id: "foreign" };
  const command = action === "edit" ? vi.spyOn(api, "updateQueuedAgentTurn").mockResolvedValue(foreign) : action === "cancel" ? vi.spyOn(api, "cancelAgentTurn").mockResolvedValue({ turn: foreign, accepted: true }) : vi.spyOn(api, "controlAgentTurnPause").mockResolvedValue(foreign);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("消息操作回执不属于当前执行，请刷新确认。")).toBeTruthy();
  await click();
  expect(command.mock.calls[1]).toEqual(command.mock.calls[0]);
});

it("keeps an uncertain queued edit and its frozen draft across Agent view switches", async () => {
  const { current, click } = mainTurnCommandFixture("edit");
  const command = vi.spyOn(api, "updateQueuedAgentTurn").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue({ ...current, request: { content: "Edited request" } });
  await act(async () => render(<App />));
  await click();
  await act(async () => fireEvent.click(screen.getByRole("tab", { name: "进度" })));
  expect(screen.queryByRole("textbox", { name: "编辑排队消息内容" })).toBeNull();
  await act(async () => fireEvent.click(screen.getByRole("tab", { name: "对话" })));
  expect(screen.getByRole("textbox", { name: "编辑排队消息内容" })).toHaveProperty("value", "Edited request");
  expect(screen.getByRole("textbox", { name: "编辑排队消息内容" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "取消排队消息" })).toHaveProperty("disabled", true);
  await click();
  expect(command.mock.calls[1]).toEqual(command.mock.calls[0]);
});

it("reloads a committed cancellation from the projection even when the command response was lost", async () => {
  const { state, current, click } = mainTurnCommandFixture("cancel");
  const cancel = vi.spyOn(api, "cancelAgentTurn").mockImplementation(async () => {
    state.snapshot.agent_turns = [{ ...current, status: "cancelled" }];
    throw new Error("Response lost after commit");
  });
  await act(async () => render(<App />));
  await click();
  expect(cancel).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: "停止本轮" })).toBeNull();
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(2);
});

it.each(["viewer", "collaborator"] as const)("honors %s permissions for main-turn controls", async (mode) => {
  mainTurnCommandFixture("edit");
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ kind: "user", user_id: mode === "viewer" ? "u" : "another-user", workspace_id: "w", display_name: "Reader", workspace_name: "Fixture", role: mode === "viewer" ? "viewer" : "editor", auth_method: "fixture" });
  await act(async () => render(<App />));
  expect(screen.queryByRole("button", { name: "编辑排队消息", exact: true })).toBeNull();
  expect(screen.queryByRole("button", { name: "暂停本轮" })).toBeNull();
  if (mode === "viewer") expect(screen.queryByRole("button", { name: "取消排队消息" })).toBeNull();
  else expect(screen.getByRole("button", { name: "取消排队消息" })).toBeTruthy();
});

function toolApprovalFixture() {
  const state = projection();
  const call: AgentToolCall = {
    agent_tool_call_id: "tool-call", workspace_id: "w", project_id: "recovery", conversation_id: "conversation-recovery",
    sdk_tool_call_id: "sdk-call", tool_id: "project.write", tool_kind: "local", tool_name: "Write file", access_mode: "write",
    approval_policy: "always", approval_status: "pending", status: "pending_approval", arguments_hash: "arguments", arguments_summary: {}, requested_at: stamp, updated_at: stamp,
    approval: { agent_tool_approval_id: "tool-approval", agent_tool_call_id: "tool-call", workspace_id: "w", project_id: "recovery", conversation_id: "conversation-recovery", status: "pending", version: 1, title: "Confirm tool", reason: "Write file", options: ["approve", "reject"], subject_snapshot_hash: "subject", requested_at: stamp },
  };
  state.registries.interactions = [{ registry_key: "agent_tool", view_key: "agent_tool_approval", commands: ["approve", "deny"] }];
  state.snapshot.agent_tool_calls = [call];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  return { state, call };
}

it.each(["approve", "reject"] as const)("retains the exact tool %s key and resolved receipt despite a stale projection", async (action) => {
  const { call } = toolApprovalFixture();
  const result = { ...call.approval!, status: action === "approve" ? "approved" : "rejected", version: 2 };
  const resolve = vi.spyOn(api, "resolveAgentToolApproval").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue(result);
  await act(async () => render(<App />));
  const name = action === "approve" ? "允许调用" : "拒绝调用";
  await act(async () => fireEvent.click(screen.getByRole("button", { name })));
  await act(async () => fireEvent.click(screen.getByRole("button", { name })));
  expect(resolve).toHaveBeenCalledTimes(2);
  expect(resolve.mock.calls[0]).toEqual([call.approval, action, expect.stringMatching(/^[a-f0-9-]{36}$/i)]);
  expect(resolve.mock.calls[1]).toEqual(resolve.mock.calls[0]);
  expect(screen.queryByRole("button", { name: "允许调用" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝调用" })).toBeNull();
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(3);
});

it.each(["project_id", "conversation_id", "agent_tool_call_id", "agent_tool_approval_id", "workspace_id", "subject_snapshot_hash", "status", "version"] as const)("rejects a mismatched tool approval receipt %s", async (field) => {
  const { call } = toolApprovalFixture();
  const result = { ...call.approval!, status: "approved", version: 2 };
  const resolve = vi.spyOn(api, "resolveAgentToolApproval").mockResolvedValueOnce({ ...result, [field]: field === "version" ? 3 : "foreign" }).mockResolvedValue(result);
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "允许调用" })));
  expect(screen.getByText("工具审批回执不一致，请刷新确认。")).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "允许调用" })));
  expect(resolve.mock.calls[1]).toEqual(resolve.mock.calls[0]);
});

it("does not allow a competing approval after switching between conversation and progress", async () => {
  const { call } = toolApprovalFixture();
  const resolve = vi.spyOn(api, "resolveAgentToolApproval").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue({ ...call.approval!, status: "approved", version: 2 });
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "允许调用" })));
  await act(async () => fireEvent.click(screen.getByRole("tab", { name: "进度" })));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "拒绝调用" })));
  expect(resolve).toHaveBeenCalledOnce();
  expect(screen.getByText("前一次审批结果尚未确认，请先重试原操作或刷新确认。")).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "允许调用" })));
  expect(resolve).toHaveBeenCalledTimes(2);
  expect(resolve.mock.calls[1]).toEqual(resolve.mock.calls[0]);
});

it("hides tool write controls from a viewer even when the registry permits both actions", async () => {
  toolApprovalFixture();
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ kind: "user", user_id: "u", workspace_id: "w", display_name: "Viewer", workspace_name: "Fixture", role: "viewer", auth_method: "fixture" });
  const resolve = vi.spyOn(api, "resolveAgentToolApproval");
  await act(async () => render(<App />));
  expect(screen.getByText("Confirm tool")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "允许调用" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝调用" })).toBeNull();
  expect(resolve).not.toHaveBeenCalled();
});

it.each(["approve", "reject"] as const)("routes private rule %s retries through the same guarded workbench approval", async (action) => {
  const { call } = toolApprovalFixture();
  call.tool_id = "runtime:update_saved_instructions";
  vi.spyOn(api, "getAgentInstructionProposal").mockResolvedValue({
    agent_tool_call_id: call.agent_tool_call_id, project_id: call.project_id, user_id: "u", arguments_hash: call.arguments_hash, can_approve: true, can_reject: true,
    arguments: { scope: "user", expected_version: 1, content: "PRIVATE_RULE", enabled: true },
    current: { scope: "user", scope_ref: "u", version: 1, content: "OLD_RULE", enabled: true, content_hash: "old", can_edit: true },
  });
  const resolve = vi.spyOn(api, "resolveAgentToolApproval").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue({ ...call.approval!, status: action === "approve" ? "approved" : "rejected", version: 2 });
  await act(async () => render(<App />));
  const name = action === "approve" ? "确认保存规则" : "拒绝变更";
  await act(async () => fireEvent.click(screen.getByRole("button", { name })));
  expect(screen.getByText("PRIVATE_RULE")).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name })));
  expect(resolve.mock.calls[1]).toEqual(resolve.mock.calls[0]);
  expect(screen.queryByText("PRIVATE_RULE")).toBeNull();
  expect(screen.queryByRole("button", { name: "确认保存规则" })).toBeNull();
});

function runControlFixture(actionID = "resume_run") {
  const state = projection();
  const step = ["retry_failed_step", "continue_with_partial_results"].includes(actionID);
  const status = step ? "failed" : actionID === "resume_run" ? "paused" : "running";
  const snapshot = resumable("control-run");
  snapshot.run.status = status;
  snapshot.run.current_step_run_id = "control-step";
  snapshot.steps = [{ run_id: "control-run", step_run_id: "control-step", step_id: "test", status, attempt_count: 1 }];
  snapshot.available_actions = [{ action_id: actionID, enabled: true, target_type: step ? "step_run" : "run", target_id: step ? "control-step" : "control-run", disabled_reason: null }];
  state.snapshot.active_run = snapshot;
  state.snapshot.project.active_write_run_id = "control-run";
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const click = async () => {
    if (actionID === "cancel_run") {
      if (!screen.queryByRole("alertdialog")) {
        await act(async () => fireEvent.click(screen.getByRole("button", { name: "更多运行操作" })));
        await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: "结束本次运行" })));
      }
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认结束运行" })));
    } else if (actionID === "continue_with_partial_results") {
      if (!screen.queryByRole("alertdialog")) await act(async () => fireEvent.click(screen.getByRole("button", { name: "按现有结果继续" })));
      await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认按现有结果继续" })));
    } else await act(async () => fireEvent.click(screen.getByRole("button", { name: actionID === "pause_run" ? "暂停任务" : actionID === "resume_run" ? "继续任务" : "重试失败项" })));
  };
  return { state, snapshot, click };
}

it.each(["pause_run", "resume_run", "cancel_run", "retry_failed_step", "continue_with_partial_results"])("retries the exact %s target with the same identity after an unknown response", async (actionID) => {
  const { snapshot, click } = runControlFixture(actionID);
  const control = vi.spyOn(api, "executeRunAction").mockRejectedValueOnce(new Error("Response lost")).mockResolvedValue(snapshot);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("暂时无法连接服务，请检查后端是否已启动。")).toBeTruthy();
  await click();
  expect(control).toHaveBeenCalledTimes(2);
  expect(control.mock.calls[0]).toEqual(["control-run", snapshot.available_actions[0], expect.stringMatching(/^[a-f0-9-]{36}$/i)]);
  expect(control.mock.calls[1]).toEqual(control.mock.calls[0]);
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(3);
});

it("never redirects a visible run control to a different project active run", async () => {
  const { state, click } = runControlFixture();
  state.snapshot.project.active_write_run_id = "another-run";
  const control = vi.spyOn(api, "executeRunAction");
  await act(async () => render(<App />));
  await click();
  expect(control).not.toHaveBeenCalled();
  expect(screen.getByText("运行或操作权限已经变化，请刷新后重试。")).toBeTruthy();
});

it("rejects a foreign run receipt and retains its identity for verification retry", async () => {
  const { snapshot, click } = runControlFixture();
  const control = vi.spyOn(api, "executeRunAction").mockResolvedValueOnce(resumable("foreign-run")).mockResolvedValue(snapshot);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("运行操作回执不一致，请刷新确认。")).toBeTruthy();
  await click();
  expect(control.mock.calls[1]).toEqual(control.mock.calls[0]);
});

it("uses a fresh request identity for a later pause-resume cycle on the same run", async () => {
  const { state, snapshot, click } = runControlFixture("pause_run");
  const control = vi.spyOn(api, "executeRunAction").mockImplementation(async (_runID, action) => {
    const paused = action.action_id === "pause_run";
    state.snapshot.active_run = { ...snapshot, run: { ...snapshot.run, status: paused ? "paused" : "running" }, available_actions: [{ ...action, action_id: paused ? "resume_run" : "pause_run" }] };
    return state.snapshot.active_run;
  });
  await act(async () => render(<App />));
  await click();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "继续任务" })));
  await click();
  expect(control).toHaveBeenCalledTimes(3);
  expect(control.mock.calls[2][2]).not.toBe(control.mock.calls[0][2]);
  expect(control.mock.calls[2][1]).toEqual(control.mock.calls[0][1]);
});

function artifactSaveFixture(script = false) {
  const state = projection();
  const kind = script ? "script_unit" : "generic_document";
  state.snapshot.project.current_focus_artifact_version_id = "save-v1";
  state.snapshot.artifacts = [{ artifact_id: "save-artifact", artifact_type: kind, run_id: script ? "save-run" : undefined, current_version_id: "save-v1", updated_at: stamp }];
  state.registries.artifacts = [{ artifact_type: kind, view_key: script ? "script" : "document", label: "Saved document", description: "Document", editable: true, preferred_fields: [], available_actions: [] }];
  const payload = script ? { script_text: "Original body" } : { title: "Saved document", content_markdown: "Original body" };
  const version: ArtifactVersion = { artifact_id: "save-artifact", artifact_version_id: "save-v1", version: 1, status: "confirmed", payload, creation_reason: "generated", created_at: stamp };
  const saved = { ...version, artifact_version_id: "save-v2", version: 2, payload: script ? { script_text: "Updated body" } : { title: "Saved document", content_markdown: "Updated body" } };
  const result: VersionResult = { artifact_version: saved, approval: approvalSnapshot().snapshot.approvals[0], handoff_refresh_required: script };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  vi.spyOn(api, "getArtifactVersion").mockImplementation(async (id) => id === "save-v2" ? saved : version);
  const bodyLabel = script ? "剧本正文编辑器" : "文档正文";
  const edit = () => { const body = screen.getByRole("textbox", { name: bodyLabel }); body.textContent = "Updated body"; fireEvent.input(body); };
  return { state, version, result, bodyLabel, edit };
}

function pendingScriptEditState(state = projection(), versions = ["save-v2", "other-v3"]) {
  const pending: PendingScriptEdit = { project_id: "recovery", run_id: "save-run", step_run_id: "save-step", expected_script_version_ids: versions, pending_scopes: versions.map((_id, index) => `episode:${index + 1}`), can_complete: true };
  state.snapshot.project.active_write_run_id = pending.run_id;
  state.snapshot.active_run = { run: { run_id: pending.run_id, capability_id: "test", current_step_run_id: pending.step_run_id, status: "waiting_approval" }, available_actions: [], current_approval: null, pending_script_edit: pending };
  return { state, pending };
}

function candidateWorkbenchFixture() {
  const fixture = artifactSaveFixture();
  const { state, version: document } = fixture;
  const candidate: ScriptCandidate = { candidate_id: "candidate", project_id: "recovery", source_run_id: "candidate-run", source_capability_id: "novel_to_script", scripts_artifact_version_id: "collection-old", status: "candidate", label: "Historical draft", updated_at: stamp };
  state.snapshot.script_candidates = [candidate];
  state.snapshot.artifacts.push({ artifact_id: "collection", project_id: "recovery", run_id: "candidate-run", artifact_type: "scripts", current_version_id: "collection-new", updated_at: stamp }, { artifact_id: "candidate-unit", project_id: "recovery", run_id: "candidate-run", artifact_type: "script_unit", scope_key: "episode:1", current_version_id: "unit-new", updated_at: stamp });
  state.registries.artifacts.push({ artifact_type: "scripts", view_key: "script_collection", collection_member_type: "script_unit", label: "Saved scripts", description: "Scripts", editable: false, preferred_fields: [], available_actions: [] }, { artifact_type: "script_unit", view_key: "script", label: "Episode script", description: "Script", editable: true, preferred_fields: [], available_actions: [] });
  const aggregate: ArtifactVersion = { artifact_id: "collection", artifact_version_id: "collection-old", version: 1, status: "superseded", payload: { episode_count: 1, completeness: "complete", unit_refs: [{ episode_no: 1, artifact_version_id: "unit-old" }] }, creation_reason: "aggregate", created_at: stamp };
  vi.mocked(api.getArtifactVersion).mockImplementation(async (id) => {
    if (id === "save-v1") return document;
    if (id === "collection-old") return aggregate;
    if (id === "collection-new") return { ...aggregate, artifact_version_id: id, version: 2, status: "confirmed", payload: { episode_count: 1, completeness: "complete", unit_refs: [{ episode_no: 1, artifact_version_id: "unit-new" }] } };
    if (id === "unit-old" || id === "unit-new") return { ...aggregate, artifact_id: "candidate-unit", artifact_version_id: id, version: id === "unit-old" ? 1 : 2, status: id === "unit-old" ? "superseded" : "confirmed", payload: { episode_no: 1, script_text: id === "unit-old" ? "Historical candidate body" : "Current candidate body" } };
    throw new Error(`Unexpected candidate version ${id}`);
  });
  return { ...fixture, candidate };
}

it("keeps historical candidate body and composer context on its saved versions after a projection refresh", async () => {
  candidateWorkbenchFixture();
  const send = vi.spyOn(api, "sendMessage").mockImplementation(messageReply(turn()));
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "候选稿" })));
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /Historical draft/ })));
  expect(screen.getByText("Historical candidate body")).toBeTruthy();
  expect(screen.queryByText("Current candidate body")).toBeNull();
  await act(async () => window.dispatchEvent(new Event("focus")));
  expect(screen.getByText("Historical candidate body")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Inspect this saved candidate" } });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
  });
  expect(send.mock.calls[0][4]?.view.artifact_version_id).toBe("collection-old");
  expect(api.getArtifactVersion).not.toHaveBeenCalledWith("unit-new");
});

it("retains the final preview when the same run gains a new candidate and switches between their exact unit versions", async () => {
  const { state, candidate } = candidateWorkbenchFixture();
  state.snapshot.script_candidates = [{ ...candidate, status: "final" }];
  state.snapshot.final_selection = { final_selection_id: "old-final", candidate_id: candidate.candidate_id, selection_no: 1, status: "active" };
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "最终稿" })));
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /Historical draft/ })));
  expect(screen.getByText("Historical candidate body")).toBeTruthy();
  state.snapshot.script_candidates = [{ ...candidate, candidate_id: "revised-candidate", scripts_artifact_version_id: "collection-new", label: "Revised draft" }, ...state.snapshot.script_candidates];
  await act(async () => window.dispatchEvent(new Event("focus")));
  expect(screen.getByText("Historical candidate body")).toBeTruthy();
  expect(screen.queryByText("Current candidate body")).toBeNull();
  expect(screen.queryByText("候选稿已有新版本，请重新选择后再导出或定稿。")).toBeNull();
  expect(screen.getByRole("button", { name: "导出剧本" }).hasAttribute("disabled")).toBe(false);
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /Revised draft/ })));
  expect(screen.getByText("Current candidate body")).toBeTruthy();
  expect(screen.queryByText("Historical candidate body")).toBeNull();
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /Historical draft/ })));
  expect(screen.getByText("Historical candidate body")).toBeTruthy();
  expect(screen.queryByText("Current candidate body")).toBeNull();
});

it("keeps an unsaved document when the user declines switching to a candidate", async () => {
  const { edit, bodyLabel } = candidateWorkbenchFixture();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  await act(async () => render(<App />)); edit();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "候选稿" })));
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /Historical draft/ })));
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("textbox", { name: bodyLabel }).textContent).toBe("Updated body");
  expect(screen.queryByText("Historical candidate body")).toBeNull();
});

it("validates the complete final selection receipt and retries the same confirmation after a foreign response", async () => {
  const { state, candidate } = candidateWorkbenchFixture();
  const approval = { ...approvalSnapshot().snapshot.approvals[0], project_id: "recovery", run_id: candidate.source_run_id, scope: "final_selection", subject_kind: "script_candidate", subject_ref_id: candidate.candidate_id, options: ["select_final"] };
  state.snapshot.approvals = [approval];
  state.registries.approvals = [{ registry_key: "final", view_key: "action_list", match: { scope: "final_selection" } }];
  const result: FinalSelectionResult = { final_selection: { final_selection_id: "selection", project_id: "recovery", candidate_id: candidate.candidate_id, approval_request_id: approval.approval_request_id, selection_no: 1, replaced_selection_id: null, status: "active" }, candidate: { ...candidate, status: "final" }, approval: { ...approval, status: "resolved" } };
  const confirm = vi.spyOn(api, "confirmFinalSelection").mockResolvedValueOnce({ ...result, final_selection: { ...result.final_selection, candidate_id: "foreign" } }).mockResolvedValue(result);
  await act(async () => render(<App />));
  const button = () => screen.getByRole("button", { name: "确认设为最终稿" });
  await act(async () => fireEvent.click(button()));
  expect(screen.getByText("最终稿确认回执不一致，请刷新确认。")).toBeTruthy();
  await act(async () => fireEvent.click(button()));
  expect(confirm).toHaveBeenCalledTimes(2);
  expect(confirm.mock.calls[1]).toEqual(confirm.mock.calls[0]);
});

function revisionFixture(status = "proposed") {
  const { state, version, result } = artifactSaveFixture();
  const revision: RevisionRequest = { revision_request_id: "revision", project_id: "recovery", conversation_id: "conversation-recovery", request_message_id: "request", target_resolution_id: "target", artifact_id: version.artifact_id, base_artifact_version_id: version.artifact_version_id, instruction: "Update the saved document", operation: "revise", status, execution_policy: "safe_checkpoint", version: 3, proposal_payload: { content_markdown: "Revised body" }, proposal_summary: "Revised draft", created_at: stamp, updated_at: stamp };
  state.snapshot.revision_requests = [revision];
  return { state, revision, accepted: { revision_request: { ...revision, status: "accepted", version: 4 }, version_result: result } };
}

function revisionStartFixture(action: "target" | "execute") {
  const { state, revision } = revisionFixture(action === "target" ? "waiting_target_confirmation" : "failed");
  const target: TargetResolution = { target_resolution_id: revision.target_resolution_id, project_id: revision.project_id, conversation_id: revision.conversation_id, request_message_id: revision.request_message_id, status: "ambiguous", source: "semantic", display: {}, candidates: ["one", "two"].map((id) => ({ candidate_id: id, artifact_id: `artifact-${id}`, artifact_version_id: `base-${id}`, artifact_type: "generic_document", scope_key: "global", entity: {}, display: { artifact_label: `Draft ${id}` }, score: 0.85 })), created_at: stamp };
  if (action === "target") { revision.artifact_id = null; revision.base_artifact_version_id = null; state.snapshot.target_resolutions = [target]; }
  const queued = { ...revision, status: "queued", version: revision.version + 1, artifact_id: action === "target" ? "artifact-one" : revision.artifact_id, base_artifact_version_id: action === "target" ? "base-one" : revision.base_artifact_version_id };
  const click = () => act(async () => fireEvent.click(screen.getByRole("button", { name: action === "target" ? /Draft one/ : "重新生成修改稿" })));
  return { state, revision, target, queued, click };
}

it.each(["target", "execute"] as const)("retries revision %s with the original key/version and accepts a durable queue receipt", async (action) => {
  const { revision, target, queued, click } = revisionStartFixture(action);
  const start = action === "target" ? vi.spyOn(api, "resolveRevisionTarget") : vi.spyOn(api, "executeRevision");
  start.mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Revision start unavailable", 503)).mockResolvedValue(queued);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("Revision start unavailable")).toBeTruthy();
  await click();
  expect(start).toHaveBeenCalledTimes(2);
  const expected = action === "target" ? [target.target_resolution_id, "one", expect.stringMatching(/^[a-f0-9-]{36}$/i), revision.version] : [revision.revision_request_id, expect.stringMatching(/^[a-f0-9-]{36}$/i), revision.version];
  expect(start.mock.calls[0]).toEqual(expected);
  expect(start.mock.calls[1]).toEqual(start.mock.calls[0]);
  expect(screen.queryByRole("heading", { name: "修改稿已生成" })).toBeNull();
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(3);
});

it.each(["target", "execute"] as const)("rejects a foreign %s receipt and preserves the original retry", async (action) => {
  const { queued, click } = revisionStartFixture(action);
  const start = action === "target" ? vi.spyOn(api, "resolveRevisionTarget") : vi.spyOn(api, "executeRevision");
  start.mockResolvedValueOnce({ ...queued, base_artifact_version_id: "foreign-base" }).mockResolvedValue(queued);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("返修操作回执不属于当前请求或目标，请刷新确认。")).toBeTruthy();
  await click();
  expect(start.mock.calls[1]).toEqual(start.mock.calls[0]);
});

it("preserves an unknown target choice across views and blocks a competing choice or cancellation", async () => {
  const { queued, click } = revisionStartFixture("target");
  const start = vi.spyOn(api, "resolveRevisionTarget").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Target response lost", 503)).mockResolvedValue(queued);
  const cancel = vi.spyOn(api, "cancelRevision");
  await act(async () => render(<App />));
  await click();
  await act(async () => fireEvent.click(screen.getByRole("tab", { name: "进度" })));
  await act(async () => fireEvent.click(screen.getByRole("tab", { name: "对话" })));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: /Draft two/ })));
  expect(start).toHaveBeenCalledTimes(1);
  expect(screen.getByText("前一次返修操作结果尚未确认，请先重试原操作或刷新确认。")).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "取消这次修改" })));
  expect(cancel).not.toHaveBeenCalled();
  await click();
  expect(start.mock.calls[1]).toEqual(start.mock.calls[0]);
});

it("refreshes an unknown execution response into the server's running revision without a second generation", async () => {
  const { state, revision, queued, click } = revisionStartFixture("execute");
  const start = vi.spyOn(api, "executeRevision").mockImplementation(async () => {
    state.snapshot.revision_requests = [{ ...queued, status: "running", version: revision.version + 2 }];
    throw new ApiError("UNAVAILABLE", "Lost execution response", 503);
  });
  await act(async () => render(<App />)); await click();
  expect(start).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("heading", { name: "正在生成修改稿" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "重新生成修改稿" })).toBeNull();
  expect(screen.queryByText("Lost execution response")).toBeNull();
});

function backgroundControlFixture(operation = "pause") {
  const status = operation === "resume" ? "paused" : operation === "retry" ? "failed" : "running";
  const state = executing("background", status);
  state.registries.tasks = ["running", "queued", "paused", "pausing"].map((status) => ({ status, view_key: "task_progress" }));
  state.registries.tasks.push({ status: "failed", view_key: "task_failure" }, { status: "cancelled", view_key: "task_result" });
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const label = ({ pause: "暂停后台任务", resume: "继续后台任务", cancel: "取消任务", retry: "重新执行" } as Record<string, string>)[operation];
  return { state, task: state.snapshot.agent_tasks![0], click: () => act(async () => fireEvent.click(screen.getByRole("button", { name: label }))) };
}

it.each(["pause", "resume", "cancel", "retry"] as const)("retries background %s on the original task with its original request key", async (operation) => {
  const { task, click } = backgroundControlFixture(operation);
  const method = ({ pause: "pauseAgentTask", resume: "resumeAgentTask", cancel: "cancelAgentTask", retry: "retryAgentTask" } as const)[operation];
  const control = vi.spyOn(api, method).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Task response unavailable", 503)).mockResolvedValue(task);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("Task response unavailable")).toBeTruthy();
  await click();
  expect(control.mock.calls[0]).toEqual([task.agent_task_id, expect.stringMatching(/^[a-f0-9-]{36}$/i)]);
  expect(control.mock.calls[1]).toEqual(control.mock.calls[0]);
  expect(api.getProjectWorkspaceProjection).toHaveBeenCalledTimes(3);
});

it("retains the background request key when the server reports the original command in progress", async () => {
  const { task, click } = backgroundControlFixture();
  const control = vi.spyOn(api, "pauseAgentTask").mockRejectedValueOnce(new ApiError("COMMAND_IN_PROGRESS", "Still processing", 409)).mockResolvedValue(task);
  await act(async () => render(<App />));
  await click(); await click();
  expect(control.mock.calls[1]).toEqual(control.mock.calls[0]);
});

it("rejects a foreign background control receipt and does not insert that task", async () => {
  const { task, click } = backgroundControlFixture();
  const control = vi.spyOn(api, "pauseAgentTask").mockResolvedValueOnce({ ...task, agent_task_id: "foreign", progress_message: "Foreign task" }).mockResolvedValue(task);
  await act(async () => render(<App />));
  await click();
  expect(screen.getByText("后台任务操作回执不一致，请刷新确认。")).toBeTruthy();
  expect(screen.queryByText("Foreign task")).toBeNull();
  await click();
  expect(control.mock.calls[1]).toEqual(control.mock.calls[0]);
});

it.each(["workflow", "background"])("refreshes after an uncertain %s start and recovers a consumed proposal without starting again", async (mode) => {
  const state = proposalSnapshot(mode, true);
  const action = state.snapshot.proposed_actions![0];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const start = mode === "workflow" ? vi.spyOn(api, "startRun") : vi.spyOn(api, "startAgentTask");
  start.mockImplementation(async () => {
    state.snapshot.proposed_actions = [{ ...action, status: "consumed", consumed_run_id: mode === "workflow" ? "started-run" : null, consumed_task_id: mode === "background" ? "started-task" : null }];
    throw new ApiError("UNAVAILABLE", "Start response lost", 503);
  });
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认并开始" })));
  expect(start).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "确认并开始" })).toBeNull();
  expect(screen.queryByText("Start response lost")).toBeNull();
});

it.each(["workflow", "background"])("rejects a foreign %s start receipt and retries the same confirmation identity", async (mode) => {
  const state = proposalSnapshot(mode, true);
  const action = state.snapshot.proposed_actions![0];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const run: RunSnapshot = { ...resumable("new-run"), run: { ...resumable("new-run").run, project_id: "recovery", conversation_id: "conversation-recovery", capability_id: action.capability_ref.capability_id, capability_version: action.capability_ref.version } };
  const task: AgentTask = { ...executing("background", "queued").snapshot.agent_tasks![0], proposed_action_id: action.proposed_action_id, capability_id: action.capability_ref.capability_id, capability_version: action.capability_ref.version };
  const start = mode === "workflow" ? vi.spyOn(api, "startRun").mockResolvedValueOnce({ ...run, run: { ...run.run, project_id: "foreign" } }).mockResolvedValue(run) : vi.spyOn(api, "startAgentTask").mockResolvedValueOnce({ ...task, proposed_action_id: "foreign" }).mockResolvedValue(task);
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认并开始" })));
  expect(screen.getByText("启动回执与当前确认不一致，请刷新确认。")).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认并开始" })));
  expect(start.mock.calls[1]).toEqual(start.mock.calls[0]);
  expect(screen.queryByRole("button", { name: "确认并开始" })).toBeNull();
});

it.each(["workflow", "background", "task"])("hides %s write controls for the current viewer in the full workbench", async (mode) => {
  const state = mode === "task" ? backgroundControlFixture().state : proposalSnapshot(mode, true);
  if (mode !== "task") vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ kind: "user", user_id: "u", display_name: "Viewer", workspace_id: "w", workspace_name: "Fixture", role: "viewer", auth_method: "fixture" });
  await act(async () => render(<App />));
  for (const label of ["确认并开始", "暂停后台任务", "取消任务"]) expect(screen.queryByRole("button", { name: label })).toBeNull();
});

it("recovers a persisted multi-script handoff from a fresh page and retries the same completion only", async () => {
  const { state, pending } = pendingScriptEditState();
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const save = vi.spyOn(api, "createArtifactVersion");
  const finish = vi.spyOn(api, "completeScriptEdit").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Completion response unavailable", 503)).mockImplementation(async () => {
    state.snapshot.active_run = { ...resumable("save-run"), run: { ...resumable("save-run").run, status: "running" } };
    return { run_snapshot: state.snapshot.active_run, refresh_task_ids: ["refresh-1", "refresh-2"], pending_scopes: pending.pending_scopes };
  });
  await act(async () => render(<App />));
  expect(screen.getByText("2 项剧本已保存，交接待更新。")).toBeTruthy();
  for (let attempt = 0; attempt < 2; attempt++) await act(async () => fireEvent.click(screen.getByRole("button", { name: "更新交接" })));
  expect(finish).toHaveBeenCalledTimes(2);
  expect(finish.mock.calls[0]).toEqual(["save-run", ["save-v2", "other-v3"], expect.stringMatching(/^[a-f0-9-]{36}$/i)]);
  expect(finish.mock.calls[1]).toEqual(finish.mock.calls[0]);
  expect(save).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "更新交接" })).toBeNull();
});

it("recovers after reloading a page whose saved script never started its handoff", async () => {
  const { state, result, edit } = artifactSaveFixture(true);
  const save = vi.spyOn(api, "createArtifactVersion").mockResolvedValue(result);
  const finish = vi.spyOn(api, "completeScriptEdit").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Handoff unavailable", 503));
  let view!: ReturnType<typeof render>;
  await act(async () => { view = render(<App />); });
  edit();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "保存新版本" })));
  expect(save).toHaveBeenCalledTimes(1);
  view.unmount(); sessionStorage.clear();
  pendingScriptEditState(state, ["save-v2"]);
  state.snapshot.artifacts[0].current_version_id = "save-v2";
  state.snapshot.project.current_focus_artifact_version_id = "save-v2";
  finish.mockResolvedValue({ run_snapshot: resumable("save-run"), refresh_task_ids: ["refresh"], pending_scopes: ["episode:1"] });
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更新交接" })));
  expect(finish.mock.calls[1]?.slice(0, 2)).toEqual(["save-run", ["save-v2"]]);
  expect(save).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("textbox", { name: "剧本正文编辑器" }).textContent).toBe("Updated body");
});

it("keeps accepting a revision bound to the same command and validates actual backend field names", async () => {
  const { accepted } = revisionFixture();
  const accept = vi.spyOn(api, "acceptRevision").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Acceptance response unavailable", 503)).mockResolvedValue(accepted);
  await act(async () => render(<App />));
  for (let attempt = 0; attempt < 2; attempt++) await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认采用" })));
  expect(accept).toHaveBeenCalledTimes(2);
  expect(accept.mock.calls[0][1]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
  expect(accept.mock.calls[1]).toEqual(accept.mock.calls[0]);
  expect(screen.getByRole("heading", { name: "修改稿已采用" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "确认采用" })).toBeNull();
});

it.each([
  ["rejectRevision", "proposed", "放弃修改稿", "rejected", "修改稿已放弃"],
  ["cancelRevision", "queued", "取消请求", "cancelled", "修改请求已取消"],
] as const)("retries %s with the same expected revision and command identity", async (method, status, button, terminal, heading) => {
  const { revision } = revisionFixture(status);
  const finish = vi.spyOn(api, method).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Result unavailable", 503)).mockResolvedValue({ ...revision, version: 4, status: terminal });
  await act(async () => render(<App />));
  for (let attempt = 0; attempt < 2; attempt++) await act(async () => fireEvent.click(screen.getByRole("button", { name: button })));
  expect(finish).toHaveBeenCalledTimes(2);
  expect(finish.mock.calls[1]).toEqual(finish.mock.calls[0]);
  expect(finish.mock.calls[0][1]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
  expect(screen.getByRole("heading", { name: heading })).toBeTruthy();
});

it("offers handoff recovery after an accepted revision's HTTP follow-up fails", async () => {
  const { state, accepted } = revisionFixture();
  const accept = vi.spyOn(api, "acceptRevision").mockImplementation(async () => {
    state.snapshot.revision_requests = [accepted.revision_request];
    pendingScriptEditState(state);
    throw new ApiError("REVISION_HANDOFF_REFRESH_FAILED", "Saved but handoff not started", 409);
  });
  const finish = vi.spyOn(api, "completeScriptEdit").mockResolvedValue({ run_snapshot: resumable("save-run"), refresh_task_ids: ["refresh-1", "refresh-2"], pending_scopes: ["episode:1", "episode:2"] });
  const save = vi.spyOn(api, "createArtifactVersion");
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认采用" })));
  expect(screen.getByRole("heading", { name: "修改稿已采用" })).toBeTruthy();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更新交接" })));
  expect(accept).toHaveBeenCalledTimes(1);
  expect(finish.mock.calls[0]?.slice(0, 2)).toEqual(["save-run", ["save-v2", "other-v3"]]);
  expect(save).not.toHaveBeenCalled();
});

it("rejects an acceptance receipt for another revision instead of declaring success", async () => {
  const { accepted } = revisionFixture();
  vi.spyOn(api, "acceptRevision").mockResolvedValue({ ...accepted, revision_request: { ...accepted.revision_request, revision_request_id: "foreign" } });
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认采用" })));
  expect(screen.getByText("操作回执与当前修改请求不一致，请刷新确认。")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "修改稿已采用" })).toBeNull();
});

it("uses newly projected versions and a new recovery key when a pending handoff changes", async () => {
  const { state } = pendingScriptEditState();
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const finish = vi.spyOn(api, "completeScriptEdit").mockRejectedValue(new ApiError("UNAVAILABLE", "Completion unavailable", 503));
  await act(async () => render(<App />));
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更新交接" })));
  pendingScriptEditState(state, ["save-v3", "other-v3"]);
  await act(async () => {
    RecoveryEventSource.instances[0].dispatchEvent(new Event("script_edit.awaiting_handoff_refresh"));
    await vi.advanceTimersByTimeAsync(130);
  });
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更新交接" })));
  expect(finish.mock.calls[1]?.slice(0, 2)).toEqual(["save-run", ["save-v3", "other-v3"]]);
  expect(finish.mock.calls[1]?.[2]).not.toBe(finish.mock.calls[0]?.[2]);
});

it.each(["viewer", "foreign-run", "blocked", "duplicate-versions"])("does not submit handoff recovery for %s state", async (scenario) => {
  const { state, pending } = pendingScriptEditState();
  if (scenario === "viewer") vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ kind: "user", user_id: "u", display_name: "Viewer", workspace_id: "w", workspace_name: "Fixture", role: "viewer", auth_method: "fixture" });
  if (scenario === "foreign-run") pending.run_id = "other-run";
  if (scenario === "blocked") { pending.can_complete = false; pending.disabled_reason = "Persisted state conflict"; }
  if (scenario === "duplicate-versions") pending.expected_script_version_ids = ["save-v2", "save-v2"];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const finish = vi.spyOn(api, "completeScriptEdit");
  await act(async () => render(<App />));
  const button = screen.queryByRole("button", { name: "更新交接" });
  if (button) { expect((button as HTMLButtonElement).disabled).toBe(true); fireEvent.click(button); }
  expect(finish).not.toHaveBeenCalled();
});

it("retries an uncertain document save with the same frozen base, body, and key", async () => {
  const { result, edit, bodyLabel } = artifactSaveFixture();
  const save = vi.spyOn(api, "createArtifactVersion").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Save response unavailable", 503)).mockResolvedValue(result);
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(screen.getByRole("textbox", { name: bodyLabel }).getAttribute("contenteditable")).toBe("false");
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(save).toHaveBeenCalledTimes(2);
  expect((save.mock.calls[0] as unknown[])[3]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
  expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  expect(screen.getByRole("textbox", { name: bodyLabel }).textContent).toBe("Updated body");
  expect(screen.getByText("版本 2")).toBeTruthy();
});

it("retries only the script completion after its artifact version was saved", async () => {
  const { result, edit } = artifactSaveFixture(true);
  const save = vi.spyOn(api, "createArtifactVersion").mockResolvedValue(result);
  const finish = vi.spyOn(api, "completeScriptEdit").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Completion unavailable", 503)).mockResolvedValue({ run_snapshot: resumable("save-run"), refresh_task_ids: ["refresh"], pending_scopes: ["episode:1"] });
  await act(async () => { render(<App />); });
  edit();
  for (let attempt = 0; attempt < 2; attempt++) {
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  }
  expect(save).toHaveBeenCalledTimes(1);
  expect(finish).toHaveBeenCalledTimes(2);
  expect((finish.mock.calls[0] as unknown[])[2]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
  expect(finish.mock.calls[1]).toEqual(finish.mock.calls[0]);
  expect(finish.mock.calls[0].slice(0, 2)).toEqual(["save-run", ["save-v2"]]);
});

it("keeps an uncertain save bound to its submitted base when a newer projection arrives", async () => {
  const { state, result, edit } = artifactSaveFixture();
  const save = vi.spyOn(api, "createArtifactVersion").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Save response unavailable", 503)).mockResolvedValue(result);
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  state.snapshot.artifacts[0] = { ...state.snapshot.artifacts[0], current_version_id: "save-v2" };
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(save).toHaveBeenCalledTimes(2);
  expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  expect(save.mock.calls[1][1].artifact_version_id).toBe("save-v1");
});

it("locks repeated save commands before the first request settles", async () => {
  const { result, edit, bodyLabel } = artifactSaveFixture();
  let finish!: (value: VersionResult) => void;
  const save = vi.spyOn(api, "createArtifactVersion").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { const button = screen.getByRole("button", { name: "保存新版本" }); fireEvent.click(button); fireEvent.click(button); });
  expect(save).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("textbox", { name: bodyLabel }).getAttribute("contenteditable")).toBe("false");
  await act(async () => { finish(result); });
});

it("does not expose direct document editing to a viewer", async () => {
  artifactSaveFixture();
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ kind: "user", user_id: "u", display_name: "Viewer", workspace_id: "w", workspace_name: "Fixture", role: "viewer", auth_method: "fixture" });
  const save = vi.spyOn(api, "createArtifactVersion");
  await act(async () => { render(<App />); });
  expect(screen.queryByRole("textbox", { name: "文档正文" })).toBeNull();
  expect(screen.queryByRole("button", { name: "编辑内容" })).toBeNull();
  expect(save).not.toHaveBeenCalled();
});

it("does not refresh a script from a save receipt for another artifact", async () => {
  const { result, edit } = artifactSaveFixture(true);
  vi.spyOn(api, "createArtifactVersion").mockResolvedValue({ ...result, artifact_version: { ...result.artifact_version, artifact_id: "another-artifact" } });
  const finish = vi.spyOn(api, "completeScriptEdit").mockResolvedValue({ run_snapshot: resumable("save-run"), refresh_task_ids: ["refresh"], pending_scopes: ["episode:1"] });
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(finish).not.toHaveBeenCalled();
  expect(screen.getByText("保存回执与当前产物不符。")).toBeTruthy();
});

it("retains the original editing base and local draft when a remote version arrives", async () => {
  const { state, edit, bodyLabel } = artifactSaveFixture();
  const save = vi.spyOn(api, "createArtifactVersion").mockRejectedValue(new ApiError("ARTIFACT_VERSION_CONFLICT", "Remote version changed", 409));
  const send = vi.spyOn(api, "sendMessage").mockImplementation(messageReply(turn()));
  await act(async () => { render(<App />); });
  edit();
  state.snapshot.artifacts[0] = { ...state.snapshot.artifacts[0], current_version_id: "save-v2" };
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(screen.getByRole("textbox", { name: bodyLabel }).textContent).toBe("Updated body");
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Inspect the saved context" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send.mock.calls[0][4]?.view.artifact_version_id).toBeNull();
  expect(send.mock.calls[0][4]?.selection).toBeNull();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(save.mock.calls[0][1].artifact_version_id).toBe("save-v1");
  expect(screen.getByText("Remote version changed")).toBeTruthy();
});

it("keeps a saved version pending when its script completion receipt names another run", async () => {
  const { result, edit } = artifactSaveFixture(true);
  const save = vi.spyOn(api, "createArtifactVersion").mockResolvedValue(result);
  const finish = vi.spyOn(api, "completeScriptEdit").mockResolvedValueOnce({ run_snapshot: resumable("another-run"), refresh_task_ids: ["refresh"], pending_scopes: ["episode:1"] }).mockResolvedValue({ run_snapshot: resumable("save-run"), refresh_task_ids: ["refresh"], pending_scopes: ["episode:1"] });
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(screen.getByText("剧本刷新回执与当前生成任务不符。")).toBeTruthy();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  expect(save).toHaveBeenCalledTimes(1);
  expect(finish.mock.calls[1]).toEqual(finish.mock.calls[0]);
});

it("requires confirmation before discarding an uncertain save and loading the current version", async () => {
  const { state, edit, bodyLabel } = artifactSaveFixture();
  const save = vi.spyOn(api, "createArtifactVersion").mockRejectedValue(new ApiError("UNAVAILABLE", "Unknown response", 503));
  const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "刷新当前版本" })); });
  expect(screen.getByText("保存结果待确认")).toBeTruthy();
  state.snapshot.artifacts[0] = { ...state.snapshot.artifacts[0], current_version_id: "save-v2" };
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "刷新当前版本" })); });
  expect(confirm).toHaveBeenCalledTimes(2);
  expect(save).toHaveBeenCalledTimes(1);
  expect(screen.getByText("版本 2")).toBeTruthy();
  expect(screen.getByRole("textbox", { name: bodyLabel }).getAttribute("contenteditable")).toBe("true");
});

it.each([true, false])("uses the authoritative complete refresh version set, present=%s", async (hasVersions) => {
  const { result, edit } = artifactSaveFixture(true);
  result.pending_refresh_scopes = ["episode:1", "episode:2"];
  if (hasVersions) result.pending_refresh_version_ids = ["save-v2", "other-v3"];
  vi.spyOn(api, "createArtifactVersion").mockResolvedValue(result);
  const finish = vi.spyOn(api, "completeScriptEdit").mockResolvedValue({ run_snapshot: resumable("save-run"), refresh_task_ids: ["refresh-1", "refresh-2"], pending_scopes: result.pending_refresh_scopes });
  await act(async () => { render(<App />); });
  edit();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存新版本" })); });
  if (hasVersions) expect(finish.mock.calls[0].slice(0, 2)).toEqual(["save-run", ["save-v2", "other-v3"]]);
  else {
    expect(finish).not.toHaveBeenCalled();
    expect(screen.getByText("保存回执缺少完整的待刷新版本集合。")).toBeTruthy();
  }
});

it("opens the quality review's run instead of an older script with the same episode number", async () => {
  const { review, read } = qualityEditSnapshot();
  vi.spyOn(api, "getQualityReview").mockResolvedValue(review);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "手动修改" })); });
  expect(read).toHaveBeenLastCalledWith("current-script-v1");
  expect(screen.getByText("Body of current-script-v1")).toBeTruthy();
  expect(screen.queryByText("Body of old-script-v1")).toBeNull();
});

it("does not fall back to another run when the reviewed script is absent", async () => {
  const { state, review, read } = qualityEditSnapshot();
  state.snapshot.artifacts = state.snapshot.artifacts.slice(0, 1);
  vi.spyOn(api, "getQualityReview").mockResolvedValue(review);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "手动修改" })); });
  expect(screen.getByText("没有找到质量审核对应的可编辑单集剧本。")).toBeTruthy();
  expect(read).toHaveBeenCalledTimes(1);
});

it.each(["quality_review_id", "project_id", "run_id", "review_version", "input_snapshot_hash"])("rejects a changed quality %s on the edit-time read", async (field) => {
  const { review, read } = qualityEditSnapshot();
  vi.spyOn(api, "getQualityReview").mockResolvedValueOnce(review).mockResolvedValue({ ...review, [field]: field === "review_version" ? 2 : "different" });
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "手动修改" })); });
  expect(screen.getByText("审核内容与当前确认版本不符。")).toBeTruthy();
  expect(read).toHaveBeenCalledTimes(1);
});

it("continues the approval's run rather than a different current project run", async () => {
  const current = approvalSnapshot();
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  vi.spyOn(api, "resolveApproval").mockResolvedValue({});
  const read = vi.spyOn(api, "getRunSnapshot").mockImplementation(async (id) => resumable(id));
  const resume = vi.spyOn(api, "executeRunAction").mockResolvedValue(resumable("approval-run"));
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  expect(read).toHaveBeenCalledExactlyOnceWith("approval-run");
  expect(resume.mock.calls[0][0]).toBe("approval-run");
});

function approvalRevisionSnapshot(group: boolean) {
  const state = approvalSnapshot();
  const approval = state.snapshot.approvals[0];
  Object.assign(approval, { project_id: "recovery", scope: "artifact", options: ["approve", "request_ai_revision"] });
  if (group) Object.assign(approval, { subject_kind: "artifact_version_set", subject_ref_id: "step" });
  const target = { artifact_id: "notes", artifact_version_id: group ? "notes-v1" : "av", artifact_type: "review_notes", scope_key: "singleton", version: 1, label: "Review notes - v1" };
  vi.spyOn(api, "getApprovalRevisionTargets").mockResolvedValue({ approval, targets: [target] });
  const result: RevisionRequest = { revision_request_id: "new-revision", project_id: "recovery", conversation_id: "conversation-recovery", request_message_id: "new-message", target_resolution_id: "new-target", artifact_id: target.artifact_id, base_artifact_version_id: target.artifact_version_id, source_approval_request_id: approval.approval_request_id, instruction: "Improve the ending", operation: "revise", status: "queued", execution_policy: "safe_checkpoint", version: 1, created_at: stamp, updated_at: stamp };
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(state);
  const run = vi.spyOn(api, "getRunSnapshot").mockResolvedValue(resumable("approval-run"));
  const resume = vi.spyOn(api, "executeRunAction").mockResolvedValue(resumable("approval-run"));
  return { state, approval, target, result, read, run, resume };
}

it.each([false, true])("admits an approval revision without automatically resuming its workflow, group=%s", async (group) => {
  const { state, approval, target, result, read, run, resume } = approvalRevisionSnapshot(group);
  const submit = vi.spyOn(api, "requestApprovalRegeneration").mockResolvedValue(result);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" })); });
  fireEvent.change(screen.getByRole("textbox", { name: "修改要求" }), { target: { value: result.instruction } });
  state.snapshot.revision_requests = [result];
  const readsBefore = read.mock.calls.length;
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交修改要求" })); });
  expect(submit).toHaveBeenCalledExactlyOnceWith(approval, "request_ai_revision", result.instruction, expect.stringMatching(/^[a-f0-9-]{36}$/i), group ? target.artifact_version_id : undefined);
  expect(run).not.toHaveBeenCalled(); expect(resume).not.toHaveBeenCalled();
  expect(read.mock.calls.length).toBeGreaterThan(readsBefore);
  expect(screen.getByRole("button", { name: "提交修改要求" })).toHaveProperty("disabled", true);
});

it.each(["lost", "project_id", "conversation_id", "base_artifact_version_id", "source_approval_request_id", "instruction", "version", "status"])("keeps exact approval revision retries after %s receipt failure", async (fault) => {
  const { result, run, resume } = approvalRevisionSnapshot(true);
  const submit = vi.spyOn(api, "requestApprovalRegeneration");
  if (fault === "lost") submit.mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Response lost", 503));
  else submit.mockResolvedValueOnce({ ...result, [fault]: fault === "version" ? 0 : "wrong" });
  submit.mockResolvedValue(result);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改" })); });
  fireEvent.change(screen.getByRole("textbox", { name: "修改要求" }), { target: { value: result.instruction } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交修改要求" })); });
  expect(screen.getByRole("combobox", { name: "修改对象" })).toHaveProperty("disabled", true);
  expect(screen.getByText(fault === "lost" ? "Response lost" : "返修回执与所选产物版本不一致，请核对后重试。")).toBeTruthy();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "提交修改要求" })); });
  expect(submit).toHaveBeenCalledTimes(2);
  expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
  expect(run).not.toHaveBeenCalled(); expect(resume).not.toHaveBeenCalled();
});

it("reconciles an uncertain approval response and reuses the exact confirmation key", async () => {
  const current = approvalSnapshot();
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  const resolve = vi.spyOn(api, "resolveApproval").mockRejectedValue(new ApiError("UNAVAILABLE", "Confirmation unavailable", 503));
  await act(async () => { render(<App />); });
  const before = read.mock.calls.length;
  for (let index = 0; index < 2; index++) {
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  }
  expect(read.mock.calls.length).toBeGreaterThan(before);
  const first = resolve.mock.calls[0] as unknown[];
  expect(first[3]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
  expect((resolve.mock.calls[1] as unknown[])[3]).toBe(first[3]);
  current.snapshot.approvals[0] = { ...current.snapshot.approvals[0], title: "Updated display title" };
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  expect((resolve.mock.calls[2] as unknown[])[3]).toBe(first[3]);
  current.snapshot.approvals[0] = { ...current.snapshot.approvals[0], version: 2, subject_snapshot_hash: "changed" };
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  expect((resolve.mock.calls[3] as unknown[])[3]).not.toBe(first[3]);
});

it("does not guess a run for an approval that has no run identity", async () => {
  const current = approvalSnapshot();
  delete current.snapshot.approvals[0].run_id;
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  vi.spyOn(api, "resolveApproval").mockResolvedValue({});
  const read = vi.spyOn(api, "getRunSnapshot").mockImplementation(async (id) => resumable(id));
  const resume = vi.spyOn(api, "executeRunAction").mockResolvedValue(resumable("different-run"));
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  expect(read).not.toHaveBeenCalled();
  expect(resume).not.toHaveBeenCalled();
});

it("does not resume a snapshot belonging to another run", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(approvalSnapshot());
  vi.spyOn(api, "resolveApproval").mockResolvedValue({});
  vi.spyOn(api, "getRunSnapshot").mockResolvedValue(resumable("wrong-run"));
  const resume = vi.spyOn(api, "executeRunAction").mockResolvedValue(resumable("wrong-run"));
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  expect(resume).not.toHaveBeenCalled();
});

it("does not follow a resume action targeting a different run", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(approvalSnapshot());
  vi.spyOn(api, "resolveApproval").mockResolvedValue({});
  const state = resumable("approval-run");
  state.available_actions[0].target_id = "wrong-run";
  vi.spyOn(api, "getRunSnapshot").mockResolvedValue(state);
  const resume = vi.spyOn(api, "executeRunAction").mockResolvedValue(state);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并继续" })); });
  expect(resume).not.toHaveBeenCalled();
});

it("does not expose a business approval command to a viewer in the workbench", async () => {
  vi.mocked(api.getCurrentPrincipal).mockResolvedValue({ kind: "user", user_id: "u", display_name: "Viewer", workspace_id: "w", workspace_name: "Fixture", role: "viewer", auth_method: "fixture" });
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(approvalSnapshot());
  const resolve = vi.spyOn(api, "resolveApproval").mockResolvedValue({});
  await act(async () => { render(<App />); });
  expect(screen.getByText("当前账号无编辑权限。")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "确认并继续" })).toBeNull();
  expect(resolve).not.toHaveBeenCalled();
});

it.each([false, true])("confirms the episode and remaining generation mode in one approval command, lost receipt=%s", async (lostReceipt) => {
  const current = approvalSnapshot();
  const approval = current.snapshot.approvals[0];
  approval.scope = "episode_checkpoint";
  approval.subject_kind = "artifact_version_set";
  approval.options = ["approve"];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  const mode = vi.spyOn(api, "setEpisodeExecutionMode").mockResolvedValue(resumable("approval-run"));
  const resolve = vi.spyOn(api, "resolveApproval").mockResolvedValue(resumable("approval-run"));
  if (lostReceipt) resolve.mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Confirmation receipt lost", 503));
  vi.spyOn(api, "getRunSnapshot").mockResolvedValue(resumable("approval-run"));
  const resume = vi.spyOn(api, "executeRunAction").mockResolvedValue(resumable("approval-run"));
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并自动生成剩余集" })); });
  if (lostReceipt) {
    expect(screen.getByText("Confirmation receipt lost")).toBeTruthy();
    expect(resume).not.toHaveBeenCalled();
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并自动生成剩余集" })); });
    expect(resolve.mock.calls[1]).toEqual(resolve.mock.calls[0]);
  }
  expect(resolve).toHaveBeenCalledTimes(lostReceipt ? 2 : 1);
  expect(resolve.mock.calls[0].slice(0, 3)).toEqual([approval, "approve", { episode_execution_mode: "continuous" }]);
  expect(mode).not.toHaveBeenCalled();
  expect(resume).toHaveBeenCalledOnce();
  expect(resume.mock.calls[0][0]).toBe("approval-run");
});

it("ignores a projection begun before a configuration receipt committed", async () => {
  const original = proposalSnapshot();
  const action = original.snapshot.proposed_actions![0];
  const saved = { ...action, version: 2, snapshot_hash: "configured", action_type: "start_run" };
  let current = original;
  let finishOld!: (value: ProjectWorkspaceProjection) => void;
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => current);
  vi.spyOn(api, "configureProposedAction").mockImplementation(async () => {
    current = { ...original, snapshot: { ...original.snapshot, proposed_actions: [saved] } };
    return saved;
  });
  await act(async () => { render(<App />); });
  read.mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve; }));
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "保存配置" })); });
  expect(screen.getByRole("button", { name: "确认并开始" })).toBeTruthy();
  await act(async () => { finishOld(original); });
  expect(screen.queryByRole("button", { name: "保存配置" })).toBeNull();
  expect(screen.getByText(/共 7 集，每集约 3 分钟/)).toBeTruthy();
});

it.each(["workflow", "background"])("reuses the exact %s start receipt key after an unknown response", async (mode) => {
  let current = proposalSnapshot(mode, true);
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => current);
  const start = mode === "workflow" ? vi.spyOn(api, "startRun") : vi.spyOn(api, "startAgentTask");
  start.mockRejectedValue(new ApiError("UNAVAILABLE", "Start receipt unavailable", 503));
  await act(async () => { render(<App />); });
  for (let attempt = 0; attempt < 2; attempt++) {
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并开始" })); });
    expect(screen.getByText("Start receipt unavailable")).toBeTruthy();
  }
  expect(start).toHaveBeenCalledTimes(2);
  const first = start.mock.calls[0] as unknown[];
  expect(first[2]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
  expect((start.mock.calls[1] as unknown[])[2]).toBe(first[2]);
  expect(start.mock.calls[1][1]).toEqual(start.mock.calls[0][1]);
  current = { ...current, snapshot: { ...current.snapshot, proposed_actions: [{ ...current.snapshot.proposed_actions![0], version: 2, snapshot_hash: "changed-confirmation" }] } };
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并开始" })); });
  expect(start).toHaveBeenCalledTimes(3);
  expect((start.mock.calls[2] as unknown[])[2]).not.toBe(first[2]);
});

it("rejects a legacy synchronous exchange without binding inputs or clearing the original submission", async () => {
  const projectID = "legacy-receipt";
  window.history.replaceState({}, "", `/projects/${projectID}`);
  const owner = { workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}` };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection(projectID));
  const content = "Configure the existing source";
  const exchange = { user_message: { message_id: "legacy-user", role: "user", content, created_at: stamp }, agent_message: { message_id: "legacy-agent", role: "assistant", content: "Legacy confirmation", created_at: stamp }, message_context: { attachment_refs: [{ asset_id: "asset", asset_snapshot_id: "asset-version", display_name: "source.txt", kind: "text" }] }, proposed_action: proposalSnapshot().snapshot.proposed_actions![0] };
  const send = vi.spyOn(api, "sendMessage").mockResolvedValueOnce(exchange as unknown as AgentTurn).mockImplementation(messageReply({ ...turn(projectID, "accepted"), request: { content } }));
  const bind = vi.spyOn(api, "bindProposedActionInput");
  const configure = vi.spyOn(api, "configureProposedAction");
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: content } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { await vi.waitFor(() => expect(screen.getByText(/消息回执.*结果仍待确认/)).toBeTruthy()); });
  expect(screen.queryByText("Legacy confirmation")).toBeNull();
  expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", content);
  expect(messageSubmission.read(owner)?.key).toBe(send.mock.calls[0][6]);
  expect(document.querySelectorAll(".timeline-message.unconfirmed")).toHaveLength(1);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { await vi.waitFor(() => expect(messageSubmission.read(owner)).toBeNull()); });
  expect(send.mock.calls[1][6]).toBe(send.mock.calls[0][6]);
  expect(bind).not.toHaveBeenCalled();
  expect(configure).not.toHaveBeenCalled();
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
});

it.each(legacyComposerEntries.flatMap((entry) => ["commit-event", "fallback-poll", "completed-receipt"].map((refresh) => ({ entry, refresh }))))("uses only the server projection for $entry.capability_id after $refresh, then waits for explicit configuration and start", async ({ entry, refresh }) => {
  const projectID = `async-${entry.capability_id}-${refresh}`;
  window.history.replaceState({}, "", `/projects/${projectID}`);
  const owner = { workspace_id: "w", user_id: "u", project_id: projectID, conversation_id: `conversation-${projectID}` };
  const video = entry.view_key === "video_asset_set";
  const continuation = entry.config.view_key === "script_continuation";
  const content = `Run ${entry.capability_id} with the selected source`;
  const attachments = [{ asset_id: "original-source", asset_snapshot_id: "original-source-v1", display_name: video ? "source.mp4" : "source.txt", kind: video ? "video" : "text" }];
  const context = starterContext(projectID, entry, null);
  if (video) context.view.asset_set_version_id = "sealed-batch-v1";
  const original = messageSubmission.prepare(owner, { content, capability: entry, attachments, video_batch: null, context, clientInstanceID: "original-client" }, "starter");
  let current = projection(projectID);
  current.registries.composer = [entry];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => current);
  vi.spyOn(api, "getProjectCapability").mockResolvedValue({ capability_id: entry.capability_id, version: entry.version, label: entry.label, description: entry.description, status: entry.status, execution_mode: entry.execution_mode, creates_run: entry.creates_run, accepted_asset_kinds: entry.accepted_asset_kinds, input_binding: entry.input_binding, config_options: entry.config.options, default_config_ref: entry.config.default_ref, ui_entry: { icon_key: entry.icon_key, menu_order: entry.menu_order, entry_view_key: entry.view_key, config_view_key: entry.config.view_key } });
  const accepted = submissionReceipt({ ...turn(projectID, "accepted"), request: { content, capability_ref: { capability_id: entry.capability_id, version: entry.version, selection_mode: "explicit" }, attachment_refs: attachments } }, original.key);
  const completed: AgentTurn = { ...accepted, status: "committed", user_message_id: "durable-user", agent_message_id: "durable-agent" };
  const send = vi.spyOn(api, "sendMessage").mockImplementation(async () => {
    if (refresh === "completed-receipt") { publishResult(); return completed; }
    return accepted;
  });
  const bind = vi.spyOn(api, "bindProposedActionInput");
  let action: ProposedAction = {
    proposed_action_id: `proposal-${projectID}`, project_id: projectID, conversation_id: owner.conversation_id,
    version: 1, snapshot_hash: "server-proposal-hash", status: "pending", action_type: video ? "start_run" : "collect_run_configuration",
    capability_ref: { capability_id: entry.capability_id, version: entry.version },
    input: { project_id: projectID, source_type: entry.input_binding?.source_type, user_request_message_id: "durable-user", user_notes: ["Server-selected notes"], assets: attachments.map((asset) => ({ ...asset, role: "primary_source", order: 1 })), ...(video ? { asset_set_id: "sealed-batch", asset_set_version_id: "sealed-batch-v1", collection_state: "sealed" } : {}) },
    config: { config_ref: entry.config.default_ref, payload: video ? { fidelity_level: "high", timecode_precision: "second", uncertain_content_policy: "mark" } : continuation ? { target_length_chars: 19000 } : { target_episode_count: 9, episode_duration_minutes: 3.5, episode_execution_mode: "review_each" } },
    confirmation_message_id: "durable-agent", created_at: stamp, updated_at: stamp,
  };
  const configure = vi.spyOn(api, "configureProposedAction").mockImplementation(async (submitted, input, config) => {
    action = { ...submitted, input, config, action_type: "start_run", version: submitted.version + 1, snapshot_hash: "configured-hash" };
    current = { ...current, snapshot: { ...current.snapshot, proposed_actions: [action], pending_proposed_actions: [action] } };
    return action;
  });
  const started: RunSnapshot = { run: { run_id: `run-${projectID}`, project_id: projectID, capability_id: entry.capability_id, capability_version: entry.version, status: "running", current_step_run_id: null }, current_approval: null, available_actions: [] };
  const start = vi.spyOn(api, "startRun").mockImplementation(async () => {
    current = { ...current, snapshot: { ...current.snapshot, active_run: started, proposed_actions: [{ ...action, status: "consumed", consumed_run_id: started.run.run_id }], pending_proposed_actions: [] } };
    return started;
  });
  const publishResult = () => {
    current = { ...current, snapshot: { ...current.snapshot,
      agent_turns: [completed],
      messages: [{ message_id: "durable-user", role: "user", content, created_at: stamp }, { message_id: "durable-agent", role: "assistant", content: "Server confirmation is ready", created_at: stamp }],
      proposed_actions: [action], pending_proposed_actions: [action],
    } };
  };
  await act(async () => { render(<App />); });
  await act(async () => { await vi.waitFor(() => expect(messageSubmission.read(owner)).toBeNull()); });
  expect(send).toHaveBeenCalledOnce();
  expect(send.mock.calls[0].slice(0, 5)).toEqual([owner.conversation_id, content, entry, attachments, context]);
  if (refresh !== "completed-receipt") {
    expect(document.querySelectorAll(".config-card")).toHaveLength(0);
    publishResult();
    await act(async () => {
      if (refresh === "commit-event") {
        RecoveryEventSource.instances.at(-1)!.dispatchEvent(new MessageEvent("agent.turn.committed", { data: JSON.stringify({ schema_version: "1.0.0", event_id: "commit", event_type: "agent.turn.committed", project_id: projectID, conversation_id: owner.conversation_id, turn_id: accepted.agent_turn_id, terminal: true, payload: {}, occurred_at: stamp }) }));
        await vi.advanceTimersByTimeAsync(120);
      } else await vi.advanceTimersByTimeAsync(3000);
    });
  }
  expect(screen.getByText("Server confirmation is ready")).toBeTruthy();
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
  expect(document.querySelectorAll(".config-card")).toHaveLength(1);
  expect(bind).not.toHaveBeenCalled();
  expect(configure).not.toHaveBeenCalled();
  expect(start).not.toHaveBeenCalled();
  const originalAction = structuredClone(action);
  if (!video) {
    if (continuation) expect(screen.getByRole("spinbutton", { name: "目标字数" })).toHaveProperty("value", "19000");
    else {
      expect(screen.getByRole("spinbutton", { name: "目标集数" })).toHaveProperty("value", "9");
      expect(screen.getByRole("spinbutton", { name: /^每集时长/ })).toHaveProperty("value", "3.5");
    }
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: continuation ? "保存并生成方向" : "保存配置" })); });
    expect(configure).toHaveBeenCalledOnce();
    expect(configure.mock.calls[0][0]).toEqual(originalAction);
    expect(configure.mock.calls[0][1]).toEqual(originalAction.input);
    expect(configure.mock.calls[0][2]).toMatchObject(originalAction.config);
    expect(start).not.toHaveBeenCalled();
  }
  const configuredAction = structuredClone(action);
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "确认并开始" })); });
  expect(start).toHaveBeenCalledOnce();
  expect(start.mock.calls[0][1]).toEqual(configuredAction);
  expect(bind).not.toHaveBeenCalled();
  expect(configure).toHaveBeenCalledTimes(video ? 0 : 1);
  expect(send).toHaveBeenCalledOnce();
});

it.each(["proposed_action.configured", "proposed_action.input_bound", "proposed_action.consumed"])("refreshes the authoritative projection on %s without another execution event", async (eventType) => {
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  await act(async () => { render(<App />); });
  const before = read.mock.calls.length;
  await act(async () => {
    RecoveryEventSource.instances.at(-1)!.dispatchEvent(new Event(eventType));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(read.mock.calls.length).toBeGreaterThan(before);
});

it("keeps an aborted submission unconfirmed instead of claiming the server execution stopped", async () => {
  window.history.replaceState({}, "", "/projects/interrupted");
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection("interrupted"));
  let reject!: (error: unknown) => void;
  const send = vi.spyOn(api, "sendMessage").mockImplementation(() => new Promise((_, failure) => { reject = failure; }));
  const cancel = vi.spyOn(api, "cancelAgentTurn");
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Unconfirmed request" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { await vi.waitFor(() => expect(send).toHaveBeenCalledOnce()); });
  fireEvent.click(screen.getByRole("button", { name: /停止.*(回复|等待)/ }));
  await act(async () => { reject(new DOMException("Aborted", "AbortError")); });
  expect(screen.getByText("发送结果未确认")).toBeTruthy();
  expect(screen.queryByText("本轮已停止，内容已保留")).toBeNull();
  expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "Unconfirmed request");
  expect(cancel).not.toHaveBeenCalled();
});

it("does not let an older aborted transport overwrite a retry of the same submission", async () => {
  window.history.replaceState({}, "", "/projects/retry-race");
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection("retry-race"));
  let rejectFirst!: (error: unknown) => void;
  let finishRetry!: (turn: AgentTurn) => void;
  const send = vi.spyOn(api, "sendMessage")
    .mockImplementationOnce(() => new Promise((_, reject) => { rejectFirst = reject; }))
    .mockImplementationOnce(() => new Promise((resolve) => { finishRetry = resolve; }));
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Same request retry" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  fireEvent.click(screen.getByRole("button", { name: "停止等待" }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send.mock.calls[1][6]).toBe(send.mock.calls[0][6]);
  await act(async () => { rejectFirst(new DOMException("Aborted", "AbortError")); });
  expect(document.querySelectorAll(".timeline-message.sending")).toHaveLength(1);
  expect(screen.queryByText("发送结果未确认")).toBeNull();
  expect(screen.getByRole("button", { name: "停止等待" })).toBeTruthy();
  await act(async () => { finishRetry(submissionReceipt({ ...turn("retry-race"), request: { content: "Same request retry" } }, send.mock.calls[1][6])); });
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
});

it("ignores an older successful transport after stop and a retry has started", async () => {
  window.history.replaceState({}, "", "/projects/late-success");
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection("late-success"));
  let finishFirst!: (value: AgentTurn) => void, finishRetry!: (value: AgentTurn) => void;
  const send = vi.spyOn(api, "sendMessage")
    .mockImplementationOnce(() => new Promise((resolve) => { finishFirst = resolve; }))
    .mockImplementationOnce(() => new Promise((resolve) => { finishRetry = resolve; }));
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Keep waiting for the current transport" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  fireEvent.click(screen.getByRole("button", { name: "停止等待" }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  await act(async () => { finishFirst(submissionReceipt({ ...turn("late-success", "accepted"), request: { content: "Old receipt must not settle the retry" } }, send.mock.calls[0][6])); });
  expect(screen.queryByText("Old receipt must not settle the retry")).toBeNull();
  expect(document.querySelectorAll(".timeline-message.sending")).toHaveLength(1);
  expect(screen.getByRole("button", { name: "停止等待" })).toBeTruthy();
  expect(send.mock.calls[1][6]).toBe(send.mock.calls[0][6]);
  await act(async () => { finishRetry(submissionReceipt({ ...turn("late-success", "running"), request: { content: "Current confirmed receipt" } }, send.mock.calls[1][6])); });
  expect(screen.getByText("Current confirmed receipt")).toBeTruthy();
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
});

it("reconciles a lost HTTP receipt through the exact public submission identity", async () => {
  vi.stubGlobal("crypto", webcrypto);
  window.history.replaceState({}, "", "/projects/lost-receipt");
  let current = projection("lost-receipt");
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => current);
  const send = vi.spyOn(api, "sendMessage").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Lost HTTP receipt", 503));
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Same exact content" } });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await vi.waitFor(() => expect(send).toHaveBeenCalledOnce());
  });
  expect(document.querySelectorAll(".timeline-message.unconfirmed")).toHaveLength(1);
  const submissionID = await agentTurnSubmissionID("u", "conversation-lost-receipt", send.mock.calls[0][6]!);
  const accepted = { ...turn("lost-receipt"), submission_id: submissionID, request: { content: "Same exact content" } };
  current = projection("lost-receipt");
  current.snapshot.agent_turns = [accepted];
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
  expect(document.querySelector(".timeline-message.unconfirmed")).toBeNull();
  current = projection("lost-receipt");
  current.snapshot.agent_turns = [{ ...accepted, status: "committed", user_message_id: "saved" }];
  current.snapshot.messages = [{ message_id: "saved", role: "user", content: accepted.request.content, created_at: stamp }];
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
  expect(document.querySelector(".timeline-message.unconfirmed")).toBeNull();
  expect(send).toHaveBeenCalledOnce();
});

it.each(["conversation", "background", "workflow"].flatMap((mode) => ["running", "waiting_approval"].map((status) => ({ mode, status }))))("refreshes the entire $mode projection while $status without SSE and leaves the fast interval on completion", async ({ mode, status }) => {
  let current = executing(mode, status);
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => structuredClone(current));
  vi.spyOn(api, "getRunSnapshot").mockResolvedValue({ run: { run_id: "run", capability_id: "review", status: "running", current_step_run_id: null }, available_actions: [], current_approval: null });
  await act(async () => { render(<App />); });
  expect(read).toHaveBeenCalledTimes(1);
  current = projection();
  current.snapshot.project.title = "Refreshed terminal project";
  await act(async () => { RecoveryEventSource.instances[0].onerror?.(); await vi.advanceTimersByTimeAsync(3000); });
  expect(read).toHaveBeenCalledTimes(2);
  expect(screen.getByText("Refreshed terminal project")).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
  expect(read).toHaveBeenCalledTimes(2);
  expect(api.getRunSnapshot).not.toHaveBeenCalled();
});

it.each(["queued", "waiting_safe_checkpoint", "running"])("recovers a %s revision without another active execution or SSE", async (status) => {
  let current = projection();
  current.snapshot.revision_requests = [{ revision_request_id: "revision", project_id: "recovery", conversation_id: "conversation-recovery", request_message_id: "message", target_resolution_id: "target", artifact_id: "artifact", base_artifact_version_id: "base", instruction: "Revise the draft", operation: "revise", status, execution_policy: "safe_checkpoint", version: 1, created_at: stamp, updated_at: stamp }];
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => structuredClone(current));
  const execute = vi.spyOn(api, "executeRevision");
  await act(async () => { render(<App />); });
  current = projection();
  current.snapshot.project.title = "Revision finished";
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(read).toHaveBeenCalledTimes(2);
  expect(screen.getByText("Revision finished")).toBeTruthy();
  expect(execute).not.toHaveBeenCalled();
});

it("checks an idle disconnected project at a slower interval and stops after reconnect", async () => {
  let current = projection();
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => structuredClone(current));
  const send = vi.spyOn(api, "sendMessage");
  await act(async () => { render(<App />); });
  await act(async () => { await vi.advanceTimersByTimeAsync(14999); });
  expect(read).toHaveBeenCalledTimes(1);
  current = executing("conversation", "running");
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(read).toHaveBeenCalledTimes(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(read).toHaveBeenCalledTimes(3);
  await act(async () => { RecoveryEventSource.instances[0].onopen?.(); });
  await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
  expect(read).toHaveBeenCalledTimes(4);
  expect(send).not.toHaveBeenCalled();
});

it("removes project controls immediately on stream revocation and ignores a late snapshot", async () => {
  let finish!: (value: ProjectWorkspaceProjection) => void;
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValueOnce(projection()).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  let view!: ReturnType<typeof render>;
  await act(async () => { view = render(<App />); });
  const source = RecoveryEventSource.instances[0];
  await act(async () => { source.onopen?.(); });
  expect(read).toHaveBeenCalledTimes(2);
  await act(async () => {
    source.dispatchEvent(new MessageEvent("stream.access_revoked", { data: JSON.stringify({ stream_scope: "project" }) }));
    finish(projection());
    source.onopen?.();
    source.dispatchEvent(new Event("message.created"));
    await vi.advanceTimersByTimeAsync(30000);
  });
  expect(screen.getByText("当前登录或作品访问权限已失效，请重新登录或刷新。")).toBeTruthy();
  expect(screen.queryByLabelText("给 Agent 的消息")).toBeNull();
  expect(screen.queryByRole("button", { name: "工作文件" })).toBeNull();
  expect(read).toHaveBeenCalledTimes(2);
  view.unmount();
  expect(source.close).toHaveBeenCalledOnce();
});

it("leaves the loading state when access is revoked before the first snapshot", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(() => new Promise(() => {}));
  await act(async () => { render(<App />); });
  await act(async () => { RecoveryEventSource.instances[0].dispatchEvent(new MessageEvent("stream.access_revoked", { data: JSON.stringify({ stream_scope: "project" }) })); });
  expect(screen.getByText("当前登录或作品访问权限已失效，请重新登录或刷新。")).toBeTruthy();
});

it.each([false, true])("accepts an authoritative lower revision version only after cursor reset and preserves the draft, retry=%s", async (retry) => {
  const current = projection();
  const revision: RevisionRequest = { revision_request_id: "revision", project_id: "recovery", conversation_id: "conversation-recovery", request_message_id: "message", target_resolution_id: "target", artifact_id: "artifact", base_artifact_version_id: "base", instruction: "Revise the draft", operation: "revise", status: "proposed", execution_policy: "safe_checkpoint", version: 4, created_at: stamp, updated_at: stamp };
  current.snapshot.revision_requests = [revision];
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async () => structuredClone(current));
  const execute = vi.spyOn(api, "executeRevision");
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Keep this unsent draft" } });
  current.snapshot.revision_requests = [{ ...revision, status: "queued", version: 1 }];
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(screen.getByRole("button", { name: "确认采用" })).toBeTruthy();
  if (retry) read.mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Reset snapshot unavailable", 503));
  await act(async () => {
    RecoveryEventSource.instances[0].dispatchEvent(new MessageEvent("stream.reset_required", { data: JSON.stringify({ stream_scope: "project", requires_snapshot: true, reason: "cursor_ahead", current_seq: 1 }) }));
    await vi.advanceTimersByTimeAsync(120);
  });
  if (retry) {
    expect(screen.getByText("Reset snapshot unavailable")).toBeTruthy();
    await act(async () => { RecoveryEventSource.instances[0].onopen?.(); });
  }
  expect(read).toHaveBeenCalledTimes(retry ? 4 : 3);
  expect(screen.queryByRole("button", { name: "确认采用" })).toBeNull();
  expect(screen.getByRole("button", { name: "生成修改稿" })).toBeTruthy();
  expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "Keep this unsent draft");
  expect(execute).not.toHaveBeenCalled();
});

it("keeps an in-flight message unconfirmed across repeated snapshots after a cursor reset", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const send = vi.spyOn(api, "sendMessage").mockImplementation(() => new Promise(() => {}));
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Uncertain message" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send).toHaveBeenCalledOnce();
  await act(async () => {
    RecoveryEventSource.instances[0].dispatchEvent(new MessageEvent("stream.reset_required", { data: JSON.stringify({ stream_scope: "project", requires_snapshot: true }) }));
    await vi.advanceTimersByTimeAsync(120);
  });
  expect(document.querySelectorAll(".timeline-message.unconfirmed")).toHaveLength(1);
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(document.querySelectorAll(".timeline-message.unconfirmed")).toHaveLength(1);
  expect(document.querySelectorAll(".timeline-message.sending")).toHaveLength(0);
  expect(send).toHaveBeenCalledOnce();
});

it("does not overlap slow fallback reads and stops polling when SSE reconnects", async () => {
  const current = executing("conversation", "running");
  let finish!: (value: ProjectWorkspaceProjection) => void;
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValueOnce(current).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; })).mockResolvedValue(current);
  await act(async () => { render(<App />); });
  await act(async () => { await vi.advanceTimersByTimeAsync(12000); });
  expect(read).toHaveBeenCalledTimes(2);
  await act(async () => { RecoveryEventSource.instances[0].onopen?.(); });
  expect(read).toHaveBeenCalledTimes(3);
  const stale = projection(); stale.snapshot.project.title = "Stale poll result";
  await act(async () => { finish(stale); await vi.advanceTimersByTimeAsync(9000); });
  expect(read).toHaveBeenCalledTimes(3);
  expect(screen.queryByText("Stale poll result")).toBeNull();
  expect(screen.getByText("Project recovery")).toBeTruthy();
});

it("preserves the active composer draft through a transient refresh failure", async () => {
  const current = executing("conversation", "running");
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValueOnce(current).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Refresh temporarily unavailable", 503)).mockResolvedValue(current);
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Do not discard this draft" } });
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "Do not discard this draft");
  expect(screen.getByText("Refresh temporarily unavailable")).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(screen.getByLabelText("给 Agent 的消息")).toHaveProperty("value", "Do not discard this draft");
  expect(screen.queryByText("Refresh temporarily unavailable")).toBeNull();
});

it("does not merge a previous project's failed messages into a new route", async () => {
  const first = projection();
  first.snapshot.messages = [{ message_id: "failed-local", role: "user", content: "Only belongs to previous project", delivery_status: "failed", created_at: stamp }];
  vi.spyOn(api, "getProjectWorkspaceProjection").mockImplementation(async (id) => id === "recovery" ? first : projection(id));
  await act(async () => { render(<App />); });
  expect(screen.getByText("Only belongs to previous project")).toBeTruthy();
  await act(async () => { window.history.pushState({}, "", "/projects/second"); window.dispatchEvent(new PopStateEvent("popstate")); });
  expect(screen.getByText("Project second")).toBeTruthy();
  expect(screen.queryByText("Only belongs to previous project")).toBeNull();
  expect(RecoveryEventSource.instances[0].close).toHaveBeenCalledOnce();
});

it.each([401, 403, 404])("removes project controls on an authoritative access failure (%s)", async (status) => {
  const read = vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValueOnce(executing("conversation", "running")).mockRejectedValue(new ApiError("ACCESS_REVOKED", "Project access revoked", status));
  await act(async () => { render(<App />); });
  expect(screen.getByLabelText("给 Agent 的消息")).toBeTruthy();
  await act(async () => { window.dispatchEvent(new Event("focus")); });
  expect(screen.queryByLabelText("给 Agent 的消息")).toBeNull();
  expect(screen.queryByRole("button", { name: "工作文件" })).toBeNull();
  expect(screen.getByText("Project access revoked")).toBeTruthy();
  await act(async () => { await vi.advanceTimersByTimeAsync(9000); });
  expect(read).toHaveBeenCalledTimes(2);
});

it("clears the old artifact body and version binding while a different artifact loads", async () => {
  const current = projection();
  current.snapshot.project.current_focus_artifact_version_id = "version-alpha";
  current.snapshot.artifacts = ["alpha", "beta"].map((id) => ({ artifact_id: id, artifact_type: id, current_version_id: `version-${id}`, updated_at: stamp }));
  current.registries.artifacts = ["alpha", "beta"].map((id) => ({ artifact_type: id, view_key: "document", label: id, description: id, editable: true, preferred_fields: [], available_actions: [] }));
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  const version = (id: string): ArtifactVersion => ({ artifact_id: id, artifact_version_id: `version-${id}`, version: 1, status: "draft", payload: `${id} document body`, creation_reason: "generated", created_at: stamp });
  let finish!: (value: ArtifactVersion) => void;
  vi.spyOn(api, "getArtifactVersion").mockResolvedValueOnce(version("alpha")).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const send = vi.spyOn(api, "sendMessage").mockImplementation(messageReply(turn()));
  await act(async () => { render(<App />); });
  expect(screen.getByText("alpha document body")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: /beta/ }));
  expect(screen.queryByText("alpha document body")).toBeNull();
  expect(screen.queryByRole("button", { name: "编辑内容" })).toBeNull();
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Inspect the selected artifact" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send.mock.calls[0][4]?.view).toMatchObject({ artifact_id: "beta", artifact_version_id: null });
  await act(async () => { finish(version("beta")); });
  expect(screen.getByText("beta document body")).toBeTruthy();
});

it.each(["continuation_script", "plugin_script_result", "plugin_structured_result"])("delivers the selected %s version without requiring a script candidate", async (kind) => {
  const current = projection();
  current.snapshot.project.current_focus_artifact_version_id = "script-v2";
  current.snapshot.artifacts = [{ artifact_id: "script-artifact", artifact_type: kind, current_version_id: "script-v2", updated_at: stamp }];
  current.registries.artifacts = [{ artifact_type: kind, view_key: kind === "plugin_structured_result" ? "structured_document" : "script", label: "Delivered result", description: "Result", editable: false, preferred_fields: [], available_actions: [] }];
  const text = "Scene 1\nCharacter: Saved continuation.";
  const original: ArtifactVersion = { artifact_id: "script-artifact", artifact_version_id: "script-v2", version: 2, status: "confirmed", payload: kind === "plugin_structured_result" ? { title: "Result", findings: ["Finding"] } : { title: "Result", selected_option_id: "option_3", script_text: text }, creation_reason: "generated", created_at: stamp };
  const older = { ...original, artifact_version_id: "script-v1", version: 1, status: "superseded" };
  const format = kind === "plugin_structured_result" ? "json" : "txt";
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  vi.spyOn(api, "getArtifactVersion").mockResolvedValue(original);
  vi.spyOn(api, "listArtifactVersions").mockResolvedValue([older, original]);
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (this: HTMLDialogElement) { this.removeAttribute("open"); });
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  vi.stubGlobal("URL", class extends URL { static createObjectURL = vi.fn(() => "blob:delivery"); static revokeObjectURL = vi.fn(); });
  const delivery = vi.spyOn(api, "getArtifactDelivery").mockImplementation(async (id) => ({ project_id: "recovery", artifact_id: "script-artifact", artifact_version_id: id, version: id === "script-v1" ? 1 : 2, status: id === "script-v1" ? "superseded" : "confirmed", title: "Saved file", warnings: [], downloads: [{ format, filename: `${id}.${format}`, content_type: "text/plain", download_url: "https://untrusted.invalid/not-used" }] }));
  const download = vi.spyOn(api, "downloadArtifactVersion").mockResolvedValue(new Blob([text]));
  const send = vi.spyOn(api, "sendMessage").mockImplementation(messageReply(turn()));
  await act(async () => { render(<App />); });
  expect(current.snapshot.script_candidates).toHaveLength(0);
  expect(delivery).not.toHaveBeenCalled();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "下载产物" })); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "下载", exact: true })); });
  expect(download).toHaveBeenLastCalledWith("script-v2", format, expect.any(AbortSignal));
  expect((click.mock.instances[0] as HTMLAnchorElement).download).toBe(`script-v2.${format}`);
  fireEvent.click(screen.getByRole("button", { name: "关闭下载" }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "版本历史" })); });
  fireEvent.click(screen.getByRole("button", { name: /版本 1/ }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "下载产物" })); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "下载", exact: true })); });
  expect(download).toHaveBeenLastCalledWith("script-v1", format, expect.any(AbortSignal));
  expect((click.mock.instances[1] as HTMLAnchorElement).download).toBe(`script-v1.${format}`);
  fireEvent.click(screen.getByRole("button", { name: "关闭下载" }));
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: "Inspect this historical version" } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send.mock.calls[0][4]?.view.artifact_version_id).toBe("script-v1");
});

it("ignores a history response from an artifact that is no longer selected", async () => {
  const current = projection();
  current.snapshot.project.current_focus_artifact_version_id = "version-alpha";
  current.snapshot.artifacts = ["alpha", "beta"].map((id) => ({ artifact_id: id, artifact_type: id, current_version_id: `version-${id}`, updated_at: stamp }));
  current.registries.artifacts = ["alpha", "beta"].map((id) => ({ artifact_type: id, view_key: "document", label: id, description: id, editable: true, preferred_fields: [], available_actions: [] }));
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  const version = (id: string): ArtifactVersion => ({ artifact_id: id, artifact_version_id: `version-${id}`, version: 1, status: "draft", payload: `${id} document body`, creation_reason: "generated", created_at: stamp });
  vi.spyOn(api, "getArtifactVersion").mockImplementation(async (id) => version(id.replace("version-", "")));
  let finish!: (value: ArtifactVersion[]) => void;
  vi.spyOn(api, "listArtifactVersions").mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  await act(async () => { render(<App />); });
  fireEvent.click(screen.getByRole("button", { name: "版本历史" }));
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: /beta/ })); });
  await act(async () => { finish([version("alpha")]); });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByText("beta document body")).toBeTruthy();
});

it("reports a failed history read and allows retry without losing the current body", async () => {
  const current = projection();
  current.snapshot.artifacts = [{ artifact_id: "alpha", artifact_type: "alpha", current_version_id: "version-alpha", updated_at: stamp }];
  current.registries.artifacts = [{ artifact_type: "alpha", view_key: "document", label: "alpha", description: "alpha", editable: false, preferred_fields: [], available_actions: [] }];
  const version: ArtifactVersion = { artifact_id: "alpha", artifact_version_id: "version-alpha", version: 1, status: "draft", payload: "Current body", creation_reason: "generated", created_at: stamp };
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(current);
  vi.spyOn(api, "getArtifactVersion").mockResolvedValue(version);
  const history = vi.spyOn(api, "listArtifactVersions").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "History unavailable", 503)).mockResolvedValue([version]);
  await act(async () => { render(<App />); });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "版本历史" })); });
  expect(screen.getByText("History unavailable")).toBeTruthy();
  expect(screen.getByText("Current body")).toBeTruthy();
  expect(screen.queryByRole("dialog")).toBeNull();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "版本历史" })); });
  expect(history).toHaveBeenCalledTimes(2);
  expect(screen.getByRole("dialog", { name: "版本历史" })).toBeTruthy();
  expect(screen.queryByText("History unavailable")).toBeNull();
});

it("settles the same optimistic message after an explicit retry instead of leaving a failed duplicate", async () => {
  vi.spyOn(api, "getProjectWorkspaceProjection").mockResolvedValue(projection());
  const accepted = { ...turn(), request: { content: "Retry this request once" } };
  const send = vi.spyOn(api, "sendMessage").mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Send unavailable", 503)).mockImplementation(messageReply(accepted));
  await act(async () => { render(<App />); });
  fireEvent.change(screen.getByLabelText("给 Agent 的消息"), { target: { value: accepted.request.content } });
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(screen.getByText("Send unavailable")).toBeTruthy();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "发送" })); });
  expect(send).toHaveBeenCalledTimes(2);
  expect(send.mock.calls[1][6]).toBe(send.mock.calls[0][6]);
  expect(document.querySelectorAll(".timeline-message.user")).toHaveLength(1);
  expect(document.querySelector(".timeline-message.failed")).toBeNull();
});
