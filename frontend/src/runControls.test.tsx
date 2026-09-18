// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AgentTaskCard, AgentToolApprovalCard, RunConfigurationCard, RunEvent } from "./App";
import { api, ApiError } from "./api";
import { dynamicStoryEntry, legacyComposerEntries } from "./testComposerFixtures";
import type { AgentTask, AgentToolCall, CapabilityDefinition, ComposerRegistryEntry, Project, ProposedAction, RunSnapshot } from "./types";

beforeEach(() => {
  vi.spyOn(api, "getProjectCapability").mockImplementation(async (_projectID, capabilityID, version) => {
    const entry = capabilityFor(capabilityID);
    return capabilityDefinition(entry, version);
  });
});

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

async function clickProposalButton(name: string) {
  await waitFor(() => expect(screen.getByRole("button", { name })).toHaveProperty("disabled", false));
  fireEvent.click(screen.getByRole("button", { name }));
}

describe("run controls", () => {
  it("initializes from the frozen package default and preserves a later explicit choice", async () => {
    const action = { ...configurationAction(), capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" } };
    vi.mocked(api.getProjectCapability).mockResolvedValue({ ...capabilityDefinition(dynamicStoryEntry, "1.0.0"), default_config_ref: "quick_review", config_options: ["strict_review", "quick_review"], config_schemas: { strict_review: { type: "object", properties: {} }, quick_review: { type: "object", properties: {} } } });
    const props = { project: project(), projectAssets: [], action, capability: dynamicStoryEntry, onConfigured: vi.fn(), onStart: vi.fn() };
    const view = render(<RunConfigurationCard {...props} />);
    await waitFor(() => expect(screen.getByLabelText("配置项")).toHaveProperty("value", "quick_review"));
    fireEvent.change(screen.getByLabelText("配置项"), { target: { value: "strict_review" } });
    view.rerender(<RunConfigurationCard {...props} action={{ ...action }} />);
    expect(screen.getByLabelText("配置项")).toHaveProperty("value", "strict_review");
  });

  it.each([false, true])("waits for the frozen schema and preserves drafts through a failed read (configured=%s)", async (configured) => {
    const capability = capabilityFor("novel_to_script");
    const action = configured ? startAction("novel_to_script") : configurationAction();
    const command = configured ? "确认并开始" : "保存配置";
    let reject!: (error: unknown) => void;
    vi.mocked(api.getProjectCapability).mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
    const onStart = vi.fn(async () => {});
    const configure = vi.spyOn(api, "configureProposedAction").mockResolvedValue({ ...action, version: 2, action_type: "start_run" });
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={capability} onConfigured={vi.fn()} onStart={onStart} />);
    expect(screen.getByRole("button", { name: command })).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: command }));
    expect(configure).not.toHaveBeenCalled();
    expect(onStart).not.toHaveBeenCalled();
    if (!configured) fireEvent.change(screen.getByLabelText("目标集数"), { target: { value: "9" } });
    await act(async () => { reject(new ApiError("UNAVAILABLE", "Schema unavailable", 503)); });
    expect(screen.getByText("Schema unavailable")).toBeTruthy();
    expect(screen.getByRole("button", { name: command })).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "重试读取配置" }));
    await clickProposalButton(command);
    expect(api.getProjectCapability).toHaveBeenLastCalledWith("project-1", capability.capability_id, action.capability_ref.version, action.proposed_action_id);
    if (configured) expect(onStart).toHaveBeenCalledExactlyOnceWith(action);
    else expect(configure.mock.calls[0][2]).toMatchObject({ payload: { target_episode_count: 9 } });
  });

  it.each(["version", "capability_id", "status"])("blocks a schema response with incompatible %s", async (field) => {
    const capability = capabilityFor("novel_to_script");
    vi.mocked(api.getProjectCapability).mockResolvedValue({ ...capabilityDefinition(capability, capability.version), [field]: field === "status" ? "unavailable" : "another" });
    const start = vi.fn(async () => {});
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={startAction("novel_to_script")} capability={capability} onConfigured={vi.fn()} onStart={start} />);
    await screen.findByRole("button", { name: "重试读取配置" });
    expect(screen.getByRole("button", { name: "确认并开始" })).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "确认并开始" }));
    expect(start).not.toHaveBeenCalled();
  });

  it.each(["novel_to_script", "non_novel_to_script", "script_continuation"])("uses the authoritative %s configuration after a proposal revision", async (capabilityID) => {
    const capability = capabilityFor(capabilityID);
    const action = { ...configurationAction(), capability_ref: { capability_id: capabilityID, version: capability.version } };
    const onStart = vi.fn(async () => {});
    const props = { project: project(), projectAssets: [], capability, onConfigured: vi.fn(), onStart };
    const view = render(<RunConfigurationCard {...props} action={action} />);
    const revised = { ...action, version: 2, action_type: "start_run", snapshot_hash: "revised", config: { payload: { target_episode_count: 7, episode_duration_minutes: 3, target_length_chars: 21000, episode_execution_mode: "review_each" } } };
    view.rerender(<RunConfigurationCard {...props} action={revised} />);
    expect(screen.getByText(capabilityID === "script_continuation" ? /目标 21,000 字/ : /共 7 集，每集约 3 分钟/)).toBeTruthy();
    await clickProposalButton("确认并开始");
    await waitFor(() => expect(onStart).toHaveBeenCalledExactlyOnceWith(revised));
  });

  it("preserves an unsaved draft on an unchanged projection but discards a late save after revision", async () => {
    const action = configurationAction();
    const onConfigured = vi.fn();
    let resolve!: (value: ProposedAction) => void;
    vi.spyOn(api, "configureProposedAction").mockImplementation(() => new Promise((done) => { resolve = done; }));
    const props = { project: project(), projectAssets: [], capability: capabilityFor("novel_to_script"), onConfigured, onStart: vi.fn() };
    const view = render(<RunConfigurationCard {...props} action={action} />);
    fireEvent.change(screen.getByLabelText("目标集数"), { target: { value: "9" } });
    view.rerender(<RunConfigurationCard {...props} action={{ ...action }} />);
    expect(screen.getByLabelText("目标集数")).toHaveProperty("value", "9");
    await clickProposalButton("保存配置");
    view.rerender(<RunConfigurationCard {...props} action={{ ...action, version: 3, status: "superseded" }} />);
    await act(async () => { resolve({ ...action, version: 2, action_type: "start_run" }); });
    expect(onConfigured).not.toHaveBeenCalled();
  });

  it("reuses a configuration receipt identity for manual retry and changes it for edited input", async () => {
    const configure = vi.spyOn(api, "configureProposedAction").mockRejectedValue(new ApiError("UNAVAILABLE", "Save receipt unavailable", 503));
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={configurationAction()} capability={capabilityFor("novel_to_script")} onConfigured={vi.fn()} onStart={vi.fn()} />);
    for (let attempt = 0; attempt < 2; attempt++) {
      await clickProposalButton("保存配置");
      await screen.findByText("Save receipt unavailable");
    }
    const first = configure.mock.calls[0] as unknown[];
    const second = configure.mock.calls[1] as unknown[];
    expect(first[3]).toEqual(expect.stringMatching(/^[a-f0-9-]{36}$/i));
    expect(second[3]).toBe(first[3]);
    fireEvent.change(screen.getByLabelText("目标集数"), { target: { value: "8" } });
    await clickProposalButton("保存配置");
    await screen.findByText("Save receipt unavailable");
    expect((configure.mock.calls[2] as unknown[])[3]).not.toBe(first[3]);
  });

  it("locks configuration inputs until its save completes", async () => {
    let resolve!: (value: ProposedAction) => void;
    const configure = vi.spyOn(api, "configureProposedAction").mockImplementation(() => new Promise((done) => { resolve = done; }));
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={configurationAction()} capability={capabilityFor("novel_to_script")} onConfigured={vi.fn()} onStart={vi.fn()} />);
    await clickProposalButton("保存配置");
    expect(screen.getByLabelText("目标集数")).toHaveProperty("disabled", true);
    expect(screen.getByLabelText(/每集时长/)).toHaveProperty("disabled", true);
    expect(screen.getByRole("radio", { name: /逐集确认/ })).toHaveProperty("disabled", true);
    expect(configure).toHaveBeenCalledOnce();
    await act(async () => { resolve(configurationAction()); });
  });

  it.each(["superseded", "expired", "cancelled"])("does not offer configuration or execution for a %s proposal", (status) => {
    const props = { project: project(), projectAssets: [], capability: capabilityFor("novel_to_script"), onConfigured: vi.fn(), onStart: vi.fn() };
    const view = render(<RunConfigurationCard {...props} action={{ ...configurationAction(), status }} />);
    expect(screen.queryByRole("button", { name: "保存配置" })).toBeNull();
    expect(screen.queryByLabelText("目标集数")).toBeNull();
    view.rerender(<RunConfigurationCard {...props} action={{ ...startAction("novel_to_script"), status }} />);
    expect(screen.queryByRole("button", { name: "确认并开始" })).toBeNull();
  });

  it("keeps external tool outcome review separate from connection recovery", () => {
    const task = { ...agentTask("paused"), failure_code: "SDK_TOOL_OUTCOME_UNRESOLVED" };
    const view = render(<AgentTaskCard task={task} viewKey="task_progress" capabilityName="调研" onCancel={vi.fn()} onResume={vi.fn()} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    expect(screen.getByText("等待核对外部操作")).toBeTruthy();
    expect(screen.getByText("外部操作结果未确认，原执行已暂停。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重新执行" })).toBeNull();
    view.unmount();
    const snapshot = runSnapshot("paused", [runAction("resume_run")]);
    snapshot.task_items![0] = { ...snapshot.task_items![0], status: "paused", failure: "SDK_TOOL_OUTCOME_UNRESOLVED" };
    render(<RunEvent snapshot={snapshot} onRunAction={vi.fn()} />);
    expect(screen.getByText("等待核对外部操作")).toBeTruthy();
    expect(screen.getByText("外部操作结果未确认，原执行已暂停。")).toBeTruthy();
  });

  it("continues the original background execution after a saved model failure", async () => {
    const task = { ...agentTask("paused"), failure_code: "SDK_MODEL_RECOVERY_REQUIRED" };
    const onResume = vi.fn(async () => {});
    render(<AgentTaskCard task={task} viewKey="task_progress" capabilityName="调研" onCancel={vi.fn()} onResume={onResume} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    expect(screen.getByText("等待恢复连接")).toBeTruthy();
    expect(screen.getByText("模型连接中断，原执行状态已保存。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重新执行" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "继续后台任务" }));
    await waitFor(() => expect(onResume).toHaveBeenCalledExactlyOnceWith(task));
  });

  it("shows a workflow recovery checkpoint and uses the existing resume action", async () => {
    const action = runAction("resume_run");
    const snapshot = runSnapshot("paused", [action]);
    snapshot.task_items![0] = { ...snapshot.task_items![0], status: "paused", failure: "SDK_MODEL_RECOVERY_REQUIRED" };
    const onRunAction = vi.fn(async () => {});
    render(<RunEvent snapshot={snapshot} onRunAction={onRunAction} />);
    expect(screen.getByText("模型连接中断，原执行状态已保存。")).toBeTruthy();
    expect(screen.getByText("等待恢复连接")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "继续任务" }));
    await waitFor(() => expect(onRunAction).toHaveBeenCalledExactlyOnceWith(action));
  });

  it("keeps native pause and resume controls usable throughout result repair", async () => {
    const action = (id: string) => ({ action_id: id, target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null });
    const snapshot = runSnapshot("running", [action("pause_run")]);
    snapshot.task_items![0] = { ...snapshot.task_items![0], status: "repair_pending", output_repair: { status: "queued", rejection_no: 1, error_code: "BATCH_COVERAGE_INVALID" } };
    const onRunAction = vi.fn(async () => {});
    const view = render(<RunEvent snapshot={snapshot} onRunAction={onRunAction} />);
    expect(screen.getByText("等待修复")).toBeTruthy();
    expect(view.container.querySelector(".task-status .task-spinner")).toBeNull();
    snapshot.task_items![0] = { ...snapshot.task_items![0], status: "running", output_repair: { ...snapshot.task_items![0].output_repair!, status: "claimed" } };
    view.rerender(<RunEvent snapshot={snapshot} onRunAction={onRunAction} />);
    expect(screen.getByText("正在修复结果")).toBeTruthy();
    expect(view.container.querySelector(".task-status .task-spinner")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "暂停任务" }));
    await waitFor(() => expect(onRunAction).toHaveBeenCalledExactlyOnceWith(action("pause_run")));
    snapshot.run.status = "paused";
    snapshot.task_items![0].status = "paused";
    snapshot.available_actions = [action("resume_run")];
    view.rerender(<RunEvent snapshot={snapshot} onRunAction={onRunAction} />);
    expect(screen.getByText("修复已暂停")).toBeTruthy();
    expect(view.container.querySelector(".task-status .task-spinner")).toBeNull();
    await waitFor(() => expect((screen.getByRole("button", { name: "继续任务" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "继续任务" }));
    await waitFor(() => expect(onRunAction).toHaveBeenLastCalledWith(action("resume_run")));
  });

  it.each(["queued", "running", "paused", "failed", "cancelled"])("keeps %s background task controls read-only without hiding the result", (status) => {
    const task = { ...agentTask(status), result_artifact_id: "result" };
    render(<AgentTaskCard task={task} readOnly viewKey={status === "failed" ? "task_failure" : status === "cancelled" ? "task_result" : "task_progress"} capabilityName="Task" onCancel={vi.fn()} onPause={vi.fn()} onResume={vi.fn()} onRetry={vi.fn()} onAppend={vi.fn()} onOpenResult={vi.fn()} />);
    expect(screen.getAllByRole("button").map((button) => button.textContent)).toEqual(["查看结果"]);
  });

  it("retains task input drafts through execution and permission changes and discards a late control error", async () => {
    let reject!: (error: Error) => void;
    const onPause = vi.fn(() => new Promise<void>((_resolve, fail) => { reject = fail; }));
    const task = agentTask("running");
    const props = { viewKey: "task_progress" as const, capabilityName: "Task", onCancel: vi.fn(), onPause, onResume: vi.fn(), onRetry: vi.fn(), onAppend: vi.fn(), onOpenResult: vi.fn() };
    const view = render(<AgentTaskCard {...props} task={task} />);
    fireEvent.click(screen.getByRole("button", { name: "追加后台任务要求" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "Unsent requirement" } });
    const pause = screen.getByRole("button", { name: "暂停后台任务" });
    act(() => { fireEvent.click(pause); fireEvent.click(pause); });
    expect(onPause).toHaveBeenCalledTimes(1);
    const paused = { ...task, status: "paused" };
    view.rerender(<AgentTaskCard {...props} task={paused} />);
    await act(async () => reject(new ApiError("UNAVAILABLE", "Old control response", 503)));
    expect(screen.queryByText("Old control response")).toBeNull();
    expect(screen.getByRole("textbox")).toHaveProperty("value", "Unsent requirement");
    view.rerender(<AgentTaskCard {...props} task={paused} readOnly />);
    expect(screen.getByRole("textbox")).toHaveProperty("value", "Unsent requirement");
    expect(screen.getByRole("button", { name: "提交追加要求" })).toHaveProperty("disabled", true);
  });

  it("retains a pending task control identity while rejecting competing controls", async () => {
    const onPause = vi.fn().mockRejectedValueOnce(new ApiError("COMMAND_IN_PROGRESS", "Awaiting the same command", 409)).mockResolvedValue(undefined);
    render(<AgentTaskCard task={agentTask("running")} viewKey="task_progress" capabilityName="Task" onCancel={vi.fn()} onPause={onPause} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "暂停后台任务" }));
    await screen.findByText("Awaiting the same command");
    expect(screen.getByRole("button", { name: "取消任务" })).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "暂停后台任务" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "取消任务" })).toHaveProperty("disabled", false));
    expect(onPause).toHaveBeenCalledTimes(2);
  });

  it.each(["collect_run_configuration", "start_run", "start_background_task"])("does not let a read-only user configure or execute %s", async (actionType) => {
    const action = { ...configurationAction(), action_type: actionType };
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={capabilityFor("novel_to_script")} readOnly onConfigured={vi.fn()} onStart={vi.fn()} onStartAgentTask={vi.fn()} />);
    await waitFor(() => expect(api.getProjectCapability).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "保存配置" })).toBeNull();
    expect(screen.queryByRole("button", { name: "确认并开始" })).toBeNull();
    expect(screen.queryByLabelText("目标集数")).toBeNull();
  });

  it("rejects a foreign configuration receipt without consuming the current proposal", async () => {
    const action = configurationAction();
    const onConfigured = vi.fn();
    vi.spyOn(api, "configureProposedAction").mockResolvedValue({ ...action, proposed_action_id: "foreign", version: action.version + 1, action_type: "start_run" });
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={capabilityFor("novel_to_script")} onConfigured={onConfigured} onStart={vi.fn()} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "保存配置" })).toHaveProperty("disabled", false));
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }));
    await screen.findByText("配置回执与当前确认不一致，请刷新确认。");
    expect(onConfigured).not.toHaveBeenCalled();
  });

  it.each(["queued", "running", "waiting_approval"])("pauses the exact %s background task and prevents duplicate actions", async (status) => {
    let finish!: () => void;
    const onPause = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const task = agentTask(status);
    render(<AgentTaskCard task={task} viewKey="task_progress" capabilityName="调研" onCancel={vi.fn()} onPause={onPause} onResume={vi.fn()} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    const pause = screen.getByRole("button", { name: "暂停后台任务" });
    fireEvent.click(pause);
    expect(onPause).toHaveBeenCalledExactlyOnceWith(task);
    expect((pause as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "取消任务" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(pause);
    expect(onPause).toHaveBeenCalledTimes(1);
    finish();
    await waitFor(() => expect((pause as HTMLButtonElement).disabled).toBe(false));
  });

  it("waits for the paused checkpoint before offering resume and displays resume failures", async () => {
    const onResume = vi.fn(async () => { throw new ApiError("AGENT_RUN_STATE_CORRUPT", "保存的执行状态不可恢复", 400); });
    const onCancel = vi.fn(async () => {});
    const task = agentTask("pausing");
    const props = { capabilityName: "调研", onCancel, onPause: vi.fn(), onResume, onRetry: vi.fn(), onOpenResult: vi.fn() };
    const view = render(<AgentTaskCard {...props} task={task} viewKey="task_progress" />);
    expect(screen.getByText("暂停中")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "暂停后台任务" })).toBeNull();
    expect(screen.queryByRole("button", { name: "继续后台任务" })).toBeNull();
    const paused = { ...task, status: "paused", progress_message: "已暂停" };
    view.rerender(<AgentTaskCard {...props} task={paused} viewKey="task_progress" />);
    fireEvent.click(screen.getByRole("button", { name: "继续后台任务" }));
    await waitFor(() => expect(screen.getByText("保存的执行状态不可恢复")).toBeTruthy());
    expect(onResume).toHaveBeenCalledExactlyOnceWith(paused);
    fireEvent.click(screen.getByRole("button", { name: "取消任务" }));
    await waitFor(() => expect(onCancel).toHaveBeenCalledExactlyOnceWith(paused));
    expect(screen.queryByText("保存的执行状态不可恢复")).toBeNull();
  });

  it.each(["approve", "reject"] as const)("shows workflow origin without changing the %s approval identity", async (action) => {
    const onResolve = vi.fn(async () => {});
    const call: AgentToolCall = { ...agentToolCall(), execution: { mode: "stateful_workflow", run_id: "run-origin", step_id: "write_skill", item_key: "draft:1", attempt_id: "attempt-origin", attempt_no: 1, capability_id: "review" } };
    render(<AgentToolApprovalCard call={call} capabilityNames={{ review: "故事评审" }} interaction={{ viewKey: "agent_tool_approval", commands: ["approve", "deny"] }} onResolve={onResolve} />);
    expect(screen.getByText("工作流 · 故事评审")).toBeTruthy();
    expect(screen.getByText("write_skill")).toBeTruthy();
    expect(screen.getByText("run-origin")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: action === "approve" ? "允许调用" : "拒绝调用" }));
    await waitFor(() => expect(onResolve).toHaveBeenCalledExactlyOnceWith(call.approval, action));
  });

  it("keeps waiting background approvals cancellable and displays replay warnings", async () => {
    const onCancel = vi.fn(async () => {});
    const task = { ...agentTask("waiting_approval"), progress_message: "等待工具授权" };
    const view = render(<AgentTaskCard task={task} viewKey="task_progress" capabilityName="调研" onCancel={onCancel} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    expect(screen.getAllByText("等待工具授权").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "取消任务" }));
    await waitFor(() => expect(onCancel).toHaveBeenCalledWith(task));
    view.rerender(<AgentTaskCard task={{ ...task, status: "failed", failure_message: "请核对外部状态后再重新执行。" }} viewKey="task_failure" capabilityName="调研" onCancel={onCancel} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    expect(screen.getByText("请核对外部状态后再重新执行。")).toBeTruthy();
  });

  it.each(["json_schema", "none"])("allows a %s workflow whose input schema does not require uploaded material", async (viewKey) => {
    const definition = capabilityDefinition(dynamicStoryEntry, "1.0.0");
    vi.mocked(api.getProjectCapability).mockResolvedValue({
      ...definition,
      ui_entry: { ...definition.ui_entry, config_view_key: viewKey },
      input_schema: { type: "object", properties: { source_type: { type: "string" } }, required: ["source_type"] },
      config_schemas: { strict_review: { type: "object", additionalProperties: false, properties: {} } },
    });
    const action = { ...configurationAction(), capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" }, input: { source_type: "story", user_notes: ["keep this instruction"] } };
    const configure = vi.spyOn(api, "configureProposedAction").mockResolvedValue({ ...action, version: action.version + 1, action_type: "start_run" });
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={dynamicStoryEntry} onConfigured={vi.fn()} onStart={vi.fn()} />);
    await waitFor(() => expect((screen.getByRole("button", { name: "保存配置" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByRole("combobox", { name: /本次使用的材料/ })).toBeNull();
    await clickProposalButton("保存配置");
    await waitFor(() => expect(configure).toHaveBeenCalledWith(action, expect.objectContaining({ user_notes: ["keep this instruction"] }), expect.anything(), expect.any(String)));
  });

  it.each(["json_schema", "none"])("keeps required material mandatory in the %s view", async (viewKey) => {
    const definition = capabilityDefinition(dynamicStoryEntry, "1.0.0");
    vi.mocked(api.getProjectCapability).mockResolvedValue({
      ...definition,
      ui_entry: { ...definition.ui_entry, config_view_key: viewKey },
      input_schema: { type: "object", properties: { assets: { type: "array", minItems: 1 } }, required: ["assets"] },
      config_schemas: { strict_review: { type: "object", properties: {} } },
    });
    const action = { ...configurationAction(), capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" }, input: { source_type: "story" } };
    const configure = vi.spyOn(api, "configureProposedAction");
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={dynamicStoryEntry} onConfigured={vi.fn()} onStart={vi.fn()} />);
    await waitFor(() => expect(screen.queryByText("正在读取 Skill 配置…")).toBeNull());
    expect(screen.getByRole("combobox", { name: /本次使用的材料/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "保存配置" })).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }));
    expect(configure).not.toHaveBeenCalled();
  });

  it("submits the user's continuation length and rejects empty or out-of-range values", async () => {
    const capability = capabilityFor("script_continuation");
    const action = { ...configurationAction(), capability_ref: { capability_id: capability.capability_id, version: capability.version } };
    const configure = vi.spyOn(api, "configureProposedAction").mockResolvedValue({ ...action, version: action.version + 1, action_type: "start_run" });
    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={capability} onConfigured={vi.fn()} onStart={vi.fn()} />);
    const input = screen.getByLabelText("目标字数");
    const save = screen.getByRole("button", { name: "保存并生成方向" }) as HTMLButtonElement;
    await waitFor(() => expect(save.disabled).toBe(false));
    fireEvent.change(input, { target: { value: "" } });
    expect(save.disabled).toBe(true);
    fireEvent.change(input, { target: { value: "50001" } });
    expect(save.disabled).toBe(true);
    fireEvent.change(input, { target: { value: "20000" } });
    fireEvent.click(save);
    await waitFor(() => expect(configure).toHaveBeenCalledWith(action, expect.anything(), { config_ref: "continuation", payload: { target_length_chars: 20000 } }, expect.any(String)));
  });

  it("lets users clear numeric configuration fields before entering a new value", async () => {
    render(<RunConfigurationCard
      project={project()}
      projectAssets={[]}
      action={configurationAction()}
      capability={capabilityFor("novel_to_script")}
      onConfigured={vi.fn()}
      onStart={vi.fn()}
    />);

    const episodes = screen.getByLabelText("目标集数") as HTMLInputElement;
    const save = screen.getByRole("button", { name: "保存配置" }) as HTMLButtonElement;
    await waitFor(() => expect(save.disabled).toBe(false));
    fireEvent.change(episodes, { target: { value: "" } });
    expect(episodes.value).toBe("");
    expect(save.disabled).toBe(true);

    fireEvent.change(episodes, { target: { value: "3" } });
    expect(episodes.value).toBe("3");
    expect(save.disabled).toBe(false);
  });

	it("persists review-each as part of the creation configuration", async () => {
		const onConfigured = vi.fn();
		const configured = { ...configurationAction(), action_type: "start_run", version: 2 };
		vi.spyOn(api, "configureProposedAction").mockResolvedValue(configured);
		render(<RunConfigurationCard
			project={project()}
			projectAssets={[]}
			action={configurationAction()}
			capability={capabilityFor("novel_to_script")}
			onConfigured={onConfigured}
			onStart={vi.fn()}
		/>);

		fireEvent.click(screen.getByRole("radio", { name: /逐集确认/ }));
		await clickProposalButton("保存配置");
		await waitFor(() => expect(onConfigured).toHaveBeenCalledWith(configured));
		expect(api.configureProposedAction).toHaveBeenCalledWith(
			expect.anything(),
			expect.anything(),
			expect.objectContaining({ payload: expect.objectContaining({ episode_execution_mode: "review_each" }) }),
			expect.any(String),
		);
	});

  it("renders an installed Skill config schema and submits structured values", async () => {
    const onConfigured = vi.fn();
    const configured = { ...configurationAction(), action_type: "start_run", version: 2, capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" } };
    vi.mocked(api.getProjectCapability).mockResolvedValue({
      ...capabilityDefinition(dynamicStoryEntry, "1.0.0"),
      default_config_ref: "strict_review",
      config_options: ["strict_review", "quick_review"],
      config_schemas: {
        strict_review: {
          type: "object",
          additionalProperties: false,
          required: ["review_depth"],
          properties: {
            review_depth: { title: "评审深度", type: "string", enum: ["standard", "deep"], default: "standard" },
            include_sources: { title: "包含来源", type: "boolean", default: true },
          },
        },
      },
    });
    vi.spyOn(api, "configureProposedAction").mockResolvedValue(configured);
    const action = {
      ...configurationAction(),
      capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" },
    };

    render(<RunConfigurationCard
      project={project()}
      projectAssets={[]}
      action={action}
      capability={dynamicStoryEntry}
      onConfigured={onConfigured}
      onStart={vi.fn()}
    />);

    const depth = await screen.findByLabelText(/评审深度/) as HTMLSelectElement;
    await waitFor(() => expect(depth.value).toBe(JSON.stringify("standard")));
    await waitFor(() => expect((screen.getByLabelText(/包含来源/) as HTMLInputElement).checked).toBe(true));
    fireEvent.change(depth, { target: { value: JSON.stringify("deep") } });
    await clickProposalButton("保存配置");

    await waitFor(() => expect(onConfigured).toHaveBeenCalledWith(configured));
    expect(api.getProjectCapability).toHaveBeenCalledWith("project-1", "dynamic_story_skill", "1.0.0", action.proposed_action_id);
    expect(api.configureProposedAction).toHaveBeenCalledWith(
      action,
      expect.objectContaining({ source_type: "story" }),
      { config_ref: "strict_review", payload: { review_depth: "deep", include_sources: true } },
      expect.any(String),
    );
  });

  it("preserves a generic artifact source without adding an empty assets field", async () => {
    const onConfigured = vi.fn();
    vi.mocked(api.getProjectCapability).mockResolvedValue({
      ...capabilityDefinition(dynamicStoryEntry, "1.0.0"),
      default_config_ref: "strict_review",
      config_options: ["strict_review"],
      config_schemas: {
        strict_review: {
          type: "object",
          additionalProperties: false,
          required: ["review_depth"],
          properties: {
            review_depth: { type: "string", enum: ["standard", "deep"], default: "standard" },
          },
        },
      },
    });
    vi.spyOn(api, "configureProposedAction").mockResolvedValue({
      ...configurationAction(), action_type: "start_run", version: 2,
      capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" },
    });
    const action = {
      ...configurationAction(),
      capability_ref: { capability_id: dynamicStoryEntry.capability_id, version: "1.0.0" },
      input: {
        project_id: "project-1",
        source_type: "story",
        user_request_message_id: "message-1",
        artifact_versions: [{ artifact_version_id: "version-source-1", role: "primary_source", order: 1 }],
      },
    };

    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={dynamicStoryEntry} onConfigured={onConfigured} onStart={vi.fn()} />);
    await screen.findByDisplayValue("standard");
    await clickProposalButton("保存配置");

    await waitFor(() => expect(api.configureProposedAction).toHaveBeenCalled());
    const submittedInput = vi.mocked(api.configureProposedAction).mock.calls.at(-1)?.[1];
    expect(submittedInput).toMatchObject({
      artifact_versions: [{ artifact_version_id: "version-source-1", role: "primary_source", order: 1 }],
    });
    expect(submittedInput).not.toHaveProperty("assets");
  });

  it("shows motion while a task is processing", () => {
    const { container } = render(<RunEvent snapshot={runSnapshot("running", [])} onRunAction={vi.fn()} />);
    expect(container.querySelector(".task-spinner")).toBeTruthy();
    expect(screen.getByText("处理中")).toBeTruthy();
  });

  it.each([
    ["novel_to_script", "小说转剧本"],
    ["non_novel_to_script", "非小说文本转剧本"],
    ["video_reference_creation", "视频参考创作"],
  ])("requires explicit confirmation before starting %s", async (capabilityID, _label) => {
    const onStart = vi.fn().mockResolvedValue(undefined);
    render(<RunConfigurationCard
      project={project()}
      projectAssets={[]}
      action={startAction(capabilityID)}
      capability={capabilityFor(capabilityID)}
      onConfigured={vi.fn()}
      onStart={onStart}
    />);

    expect(onStart).not.toHaveBeenCalled();
    await clickProposalButton("确认并开始");
    await waitFor(() => expect(onStart).toHaveBeenCalledTimes(1));
  });

  it("confirms a background Skill into an Agent Task without starting a Run", async () => {
    const onStart = vi.fn().mockResolvedValue(undefined);
    const onStartAgentTask = vi.fn().mockResolvedValue(undefined);
    const capability: ComposerRegistryEntry = {
      ...dynamicStoryEntry,
      capability_id: "background_research_skill",
      label: "创作调研",
      execution_mode: "background_task",
      creates_run: false,
      config: { view_key: "none", options: [] },
    };
    vi.mocked(api.getProjectCapability).mockResolvedValue(capabilityDefinition(capability, "1.0.0"));
    const action: ProposedAction = {
      ...configurationAction(),
      action_type: "start_background_task",
      capability_ref: { capability_id: capability.capability_id, version: "1.0.0" },
      config: { depth: "focused" },
    };

    render(<RunConfigurationCard project={project()} projectAssets={[]} action={action} capability={capability} onConfigured={vi.fn()} onStart={onStart} onStartAgentTask={onStartAgentTask} />);

    expect(screen.getByRole("heading", { name: "确认启动创作调研" })).toBeTruthy();
    await clickProposalButton("确认并开始");
    await waitFor(() => expect(onStartAgentTask).toHaveBeenCalledWith(action));
    expect(onStart).not.toHaveBeenCalled();
  });

  it("exposes generic background-task progress, cancellation, retry, and result controls", async () => {
    const onCancel = vi.fn().mockResolvedValue(undefined);
    const onRetry = vi.fn().mockResolvedValue(undefined);
    const onOpenResult = vi.fn();
    const running = agentTask("running");
    const { rerender } = render(<AgentTaskCard task={running} viewKey="task_progress" capabilityName="创作调研" onCancel={onCancel} onRetry={onRetry} onOpenResult={onOpenResult} />);

    expect(screen.getByText("1/3")).toBeTruthy();
    expect((screen.getByRole("progressbar") as HTMLProgressElement).value).toBe(1);
    fireEvent.click(screen.getByRole("button", { name: "取消任务" }));
    await waitFor(() => expect(onCancel).toHaveBeenCalledWith(running));

    const failed = { ...agentTask("failed"), failure_code: "PROVIDER_FAILED" };
    rerender(<AgentTaskCard task={failed} viewKey="task_failure" capabilityName="创作调研" onCancel={onCancel} onRetry={onRetry} onOpenResult={onOpenResult} />);
    fireEvent.click(screen.getByRole("button", { name: "重新执行" }));
    await waitFor(() => expect(onRetry).toHaveBeenCalledWith(failed));

    const completed = { ...agentTask("completed"), result: { summary: "调研摘要已完成。" }, result_artifact_id: "artifact-result" };
    rerender(<AgentTaskCard task={completed} viewKey="task_result" capabilityName="创作调研" onCancel={onCancel} onRetry={onRetry} onOpenResult={onOpenResult} />);
    expect(screen.getByText("调研摘要已完成。")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "查看结果" }));
    expect(onOpenResult).toHaveBeenCalledWith("artifact-result");
  });

  it.each(["failed", "cancelled"] as const)("does not offer unsafe background retry for %s tasks", (status) => {
    const task = { ...agentTask(status), retry_blocked_reason: "已有写入，请先核对结果。" };
    render(<AgentTaskCard task={task} viewKey={status === "failed" ? "task_failure" : "task_result"} capabilityName="调研" onCancel={vi.fn()} onRetry={vi.fn()} onOpenResult={vi.fn()} />);
    expect(screen.getByText("已有写入，请先核对结果。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重新执行" })).toBeNull();
  });

  it("submits only server-registered Agent tool approval commands", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined);
    const call = agentToolCall();
    const { rerender } = render(<AgentToolApprovalCard
      call={call}
      interaction={{ viewKey: "agent_tool_approval", commands: ["approve", "deny"] }}
      onResolve={onResolve}
    />);

    expect(screen.getByText("Write project file")).toBeTruthy();
    expect(screen.getByText(/draft\.txt/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "允许调用" }));
    await waitFor(() => expect(onResolve).toHaveBeenCalledWith(call.approval, "approve"));

    rerender(<AgentToolApprovalCard
      call={call}
      interaction={{ viewKey: "inspector", commands: [] }}
      onResolve={onResolve}
    />);
    expect(screen.queryByRole("button", { name: "允许调用" })).toBeNull();
    expect(screen.queryByRole("button", { name: "拒绝调用" })).toBeNull();
    expect(screen.getByText(/已阻止提交操作/)).toBeTruthy();
  });

  it("keeps a consumed configuration as read-only run history", () => {
    const onStart = vi.fn();
    render(<RunConfigurationCard
      project={project()}
      projectAssets={[]}
      action={{ ...startAction("video_reference_creation"), status: "consumed", consumed_run_id: "run-1" }}
      capability={capabilityFor("video_reference_creation")}
      onConfigured={vi.fn()}
      onStart={onStart}
    />);

    expect(screen.getByText("已确认 · 已启动")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "视频解析配置" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "确认并开始" })).toBeNull();
    expect(onStart).not.toHaveBeenCalled();
  });

  it("only promises retry when Runtime exposes a retry action", () => {
    const { rerender } = render(<RunEvent snapshot={runSnapshot("failed", [])} onRunAction={vi.fn()} />);
    expect(screen.queryByText("处理失败，可重试")).toBeNull();
    expect(screen.queryByRole("button", { name: "重试失败项" })).toBeNull();

    const retry = { action_id: "retry_failed_step", target_type: "step_run", target_id: "step-1", enabled: true, disabled_reason: null };
    rerender(<RunEvent snapshot={runSnapshot("failed", [retry])} onRunAction={vi.fn()} />);
    expect(screen.getByText("处理失败，可重试")).toBeTruthy();
    expect(screen.getByRole("button", { name: "重试失败项" })).toBeTruthy();
  });

  it("shows persisted failure diagnostics without exposing them as the primary status", () => {
    const snapshot = runSnapshot("failed", []);
    snapshot.task_items[0].failure_detail = {
      error_code: "OUTPUT_REPAIR_FAILED",
      stage: "output_validation",
      summary: "模型输出在自动修复后仍未通过结果校验。",
      technical_detail: "confirmed subtitle evidence missing from dialogue blocks",
      provider_request_id: "provider-request-1",
      retryable: false,
    };
    render(<RunEvent snapshot={snapshot} onRunAction={vi.fn()} />);

    expect(screen.getByText("模型输出在自动修复后仍未通过结果校验。")).toBeTruthy();
    fireEvent.click(screen.getByText("查看技术详情"));
    expect(screen.getByText("confirmed subtitle evidence missing from dialogue blocks")).toBeTruthy();
    expect(screen.getByText("请求 ID：provider-request-1")).toBeTruthy();
  });

  it("shows content policy blocks as non-retryable provider failures", () => {
    const snapshot = runSnapshot("failed", []);
    snapshot.task_items[0].failure_detail = {
      error_code: "PROVIDER_CONTENT_POLICY_BLOCKED",
      stage: "provider_call",
      summary: "模型因内容安全策略未处理该素材，请调整相关内容后重新发起。",
      retryable: false,
    };
    render(<RunEvent snapshot={snapshot} onRunAction={vi.fn()} />);

    expect(screen.getByText("模型因内容安全策略未处理该素材，请调整相关内容后重新发起。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重试失败项" })).toBeNull();
  });

  it("keeps pause and resume as recoverable run controls", async () => {
    const onRunAction = vi.fn().mockResolvedValue(undefined);
    const pause = runAction("pause_run");
    const cancel = runAction("cancel_run");
    const { rerender } = render(<RunEvent snapshot={runSnapshot("running", [pause, cancel])} onRunAction={onRunAction} />);

    expect(screen.getByRole("button", { name: "暂停任务" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "停止任务" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "暂停任务" }));
    await waitFor(() => expect(onRunAction).toHaveBeenCalledWith(pause));

    const resume = runAction("resume_run");
    rerender(<RunEvent snapshot={runSnapshot("pausing", [resume, cancel])} onRunAction={onRunAction} />);
    expect(screen.getByRole("button", { name: "撤销暂停" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "撤销暂停" }));
    await waitFor(() => expect(onRunAction).toHaveBeenCalledWith(resume));

    rerender(<RunEvent snapshot={runSnapshot("paused", [resume, cancel])} onRunAction={onRunAction} />);
    fireEvent.click(screen.getByRole("button", { name: "继续任务" }));
    await waitFor(() => expect(onRunAction).toHaveBeenCalledWith(resume));
  });

  it("does not expose pause while a run is waiting for approval", () => {
    render(<RunEvent snapshot={runSnapshot("waiting_approval", [runAction("pause_run"), runAction("cancel_run")])} onRunAction={vi.fn()} />);

    expect(screen.queryByRole("button", { name: "暂停任务" })).toBeNull();
    expect(screen.getByRole("button", { name: "更多运行操作" })).toBeTruthy();
  });

  it.each(["cancel_run", "continue_with_partial_results"])("keeps a failed %s confirmation open and synchronously locks its controls", async (actionID) => {
    const partial = actionID === "continue_with_partial_results";
    const action = partial ? { ...runAction(actionID), target_type: "step_run", target_id: "step-1" } : runAction(actionID);
    let reject!: (error: Error) => void;
    const onRunAction = vi.fn().mockImplementationOnce(() => new Promise<void>((_resolve, fail) => { reject = fail; })).mockResolvedValue(undefined);
    render(<RunEvent snapshot={runSnapshot("failed", [action])} onRunAction={onRunAction} />);
    if (partial) fireEvent.click(screen.getByRole("button", { name: "按现有结果继续" }));
    else {
      fireEvent.click(screen.getByRole("button", { name: "更多运行操作" }));
      fireEvent.click(screen.getByRole("menuitem", { name: "结束本次运行" }));
    }
    const confirmName = partial ? "确认按现有结果继续" : "确认结束运行";
    const confirm = screen.getByRole("button", { name: confirmName });
    act(() => { fireEvent.click(confirm); fireEvent.click(confirm); });
    expect(onRunAction).toHaveBeenCalledExactlyOnceWith(action);
    expect((screen.getByRole("button", { name: "关闭" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: partial ? "返回重试" : "保留运行" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => reject(new Error("Response unavailable")));
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(screen.getByText("暂时无法连接服务，请检查后端是否已启动。")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: confirmName }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(onRunAction).toHaveBeenCalledTimes(2);
  });

  it("does not carry a confirmation or a late failure into a different run", async () => {
    let reject!: (error: Error) => void;
    const action = runAction("cancel_run");
    const onRunAction = vi.fn(() => new Promise<void>((_resolve, fail) => { reject = fail; }));
    const view = render(<RunEvent snapshot={runSnapshot("running", [action])} onRunAction={onRunAction} />);
    fireEvent.click(screen.getByRole("button", { name: "更多运行操作" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "结束本次运行" }));
    fireEvent.click(screen.getByRole("button", { name: "确认结束运行" }));
    const next = runSnapshot("running", [{ ...action, target_id: "run-2" }]);
    next.run.run_id = "run-2";
    view.rerender(<RunEvent snapshot={next} onRunAction={onRunAction} />);
    expect(screen.queryByRole("alertdialog")).toBeNull();
    await act(async () => reject(new Error("Old run failure")));
    expect(screen.queryByText("Old run failure")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "更多运行操作" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "结束本次运行" }));
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(onRunAction).toHaveBeenCalledTimes(1);
  });

  it("blocks competing actions after an unknown pause result but permits the same action retry", async () => {
    const onRunAction = vi.fn().mockRejectedValueOnce(new Error("Unknown result")).mockResolvedValue(undefined);
    render(<RunEvent snapshot={runSnapshot("running", [runAction("pause_run"), runAction("cancel_run")])} onRunAction={onRunAction} />);
    fireEvent.click(screen.getByRole("button", { name: "暂停任务" }));
    await screen.findByText("暂时无法连接服务，请检查后端是否已启动。");
    expect((screen.getByRole("button", { name: "更多运行操作" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "暂停任务" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "更多运行操作" }) as HTMLButtonElement).disabled).toBe(false));
    expect(onRunAction).toHaveBeenCalledTimes(2);
  });

  it.each(["foreign-run", "foreign-step", "step-owner", "disabled", "wrong-type"])("does not expose a %s control", (mode) => {
    const action = mode === "foreign-run" ? { ...runAction("cancel_run"), target_id: "foreign" } : { ...runAction("retry_failed_step"), target_type: "step_run", target_id: "step-1" };
    const snapshot = runSnapshot("failed", [action]);
    if (mode === "foreign-step") action.target_id = "step-2";
    if (mode === "step-owner") snapshot.steps![0].run_id = "foreign";
    if (mode === "disabled") action.enabled = false;
    if (mode === "wrong-type") action.target_type = "run";
    render(<RunEvent snapshot={snapshot} onRunAction={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "重试失败项" })).toBeNull();
    expect(screen.queryByRole("button", { name: "更多运行操作" })).toBeNull();
  });

  it("puts irreversible run cancellation in the overflow menu", async () => {
    const onRunAction = vi.fn().mockResolvedValue(undefined);
    const cancel = runAction("cancel_run");
    render(<RunEvent snapshot={runSnapshot("running", [runAction("pause_run"), cancel])} onRunAction={onRunAction} />);

    expect(screen.queryByText("结束本次运行")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "更多运行操作" }));
    expect(screen.getByRole("menuitem", { name: "结束本次运行" })).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menuitem", { name: "结束本次运行" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "更多运行操作" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "结束本次运行" }));
    expect(screen.getByRole("heading", { name: "结束本次运行" })).toBeTruthy();
    expect(screen.getByText(/结束后不能从当前执行位置恢复/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "确认结束运行" }));
    await waitFor(() => expect(onRunAction).toHaveBeenCalledWith(cancel));
  });
});

function runAction(actionID: string): RunSnapshot["available_actions"][number] {
  return { action_id: actionID, target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null };
}

function capabilityFor(capabilityID: string): ComposerRegistryEntry {
  const capability = [...legacyComposerEntries, dynamicStoryEntry].find((entry) => entry.capability_id === capabilityID);
  if (!capability) throw new Error(`Missing composer fixture: ${capabilityID}`);
  return capability;
}

function capabilityDefinition(entry: ComposerRegistryEntry, version: string): CapabilityDefinition {
  return {
    capability_id: entry.capability_id,
    version,
    label: entry.label,
    description: entry.description,
    status: entry.status,
    execution_mode: entry.execution_mode,
    creates_run: entry.creates_run,
    accepted_asset_kinds: entry.accepted_asset_kinds,
    input_binding: entry.input_binding,
    default_config_ref: entry.config.default_ref,
    config_options: entry.config.options,
    ui_entry: {
      icon_key: entry.icon_key,
      menu_order: entry.menu_order,
      entry_view_key: entry.view_key,
      config_view_key: entry.config.view_key,
    },
  };
}

function project(): Project {
  return {
    project_id: "project-1", title: "测试", version: 1, status: "ready",
    primary_conversation_id: "conversation-1", active_write_run_id: null,
    current_capability_id: null, latest_capability_id: null, latest_run_status: null,
    current_focus_artifact_version_id: null, updated_at: "2026-08-17T08:00:00Z",
  };
}

function configurationAction(): ProposedAction {
  return {
    proposed_action_id: "action-1", version: 1, snapshot_hash: "hash", status: "pending",
    action_type: "collect_run_configuration",
    capability_ref: { capability_id: "novel_to_script", version: "1.4.0" },
    input: { assets: [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1", role: "primary_source", order: 1 }] },
    config: {}, confirmation_message_id: "message-1",
    created_at: "2026-08-17T08:00:00Z", updated_at: "2026-08-17T08:00:00Z",
  };
}

function startAction(capabilityID: string): ProposedAction {
  const video = capabilityID === "video_reference_creation";
  return {
    proposed_action_id: `action-${capabilityID}`,
    version: 2,
    snapshot_hash: "hash",
    status: "pending",
    action_type: "start_run",
    capability_ref: { capability_id: capabilityID, version: "1.4.0" },
    input: video ? { collection_state: "sealed" } : { assets: [] },
    config: { payload: { target_episode_count: 3, episode_duration_minutes: 1.5 } },
    confirmation_message_id: "message-1",
    created_at: "2026-08-17T08:00:00Z",
    updated_at: "2026-08-17T08:00:00Z",
  };
}

function runSnapshot(status: string, availableActions: RunSnapshot["available_actions"]): RunSnapshot {
  const taskStatus = status === "failed" ? "failed" : "running";
  return {
    run: { run_id: "run-1", capability_id: "novel_to_script", status, current_step_run_id: "step-1" },
    steps: [{ step_run_id: "step-1", run_id: "run-1", step_id: "build_story_bible", status, attempt_count: 1 }],
    task_items: [{ task_item_id: "task-1", step_run_id: "step-1", item_key: "aggregate:story_bible_aggregate", item_order: 1, status: taskStatus, attempt_count: 1, failure: status === "failed" ? "PROVIDER_RESPONSE_INVALID" : null }],
    available_actions: availableActions,
    current_approval: null,
  };
}

function agentTask(status: AgentTask["status"]): AgentTask {
  return {
    agent_task_id: "agent-task-1", workspace_id: "workspace-1", project_id: "project-1",
    conversation_id: "conversation-1", skill_invocation_id: "invocation-1",
    capability_id: "background_research_skill", capability_version: "1.0.0", status,
    progress_current: 1, progress_total: 3, progress_message: "正在整理事实",
    input: {}, config: {}, attempt_count: 1, max_attempts: 3, cancel_requested: false,
    created_at: "2026-08-17T08:00:00Z", queued_at: "2026-08-17T08:00:00Z", updated_at: "2026-08-17T08:01:00Z",
  };
}

it("does not route a private memory tool to generic approval without its owner", async () => {
  const call = { ...agentToolCall(), arguments_summary: { private_memory_tool: true } };
  const resolve = vi.fn();
  render(<AgentToolApprovalCard call={call} interaction={{ viewKey: "agent_tool_approval", commands: ["approve", "deny"] }} onResolve={resolve} />);
  expect(screen.getByRole("region", { name: "私有记忆工具确认" })).toBeTruthy();
  await screen.findByRole("alert");
  expect(screen.queryByRole("button", { name: "允许调用" })).toBeNull();
  expect(screen.queryByRole("button", { name: "允许执行" })).toBeNull();
  expect(resolve).not.toHaveBeenCalled();
});

it.each(["approve", "reject"] as const)("locks duplicate and competing tool %s commands until its outcome is known", async (action) => {
  let reject!: (error: Error) => void;
  const onResolve = vi.fn().mockImplementationOnce(() => new Promise<void>((_resolve, no) => { reject = no; })).mockResolvedValue(undefined);
  const call = agentToolCall();
  render(<AgentToolApprovalCard call={call} interaction={{ viewKey: "agent_tool_approval", commands: ["approve", "deny"] }} onResolve={onResolve} />);
  const label = action === "approve" ? "允许调用" : "拒绝调用";
  const other = action === "approve" ? "拒绝调用" : "允许调用";
  const button = screen.getByRole("button", { name: label });
  act(() => { button.click(); button.click(); });
  expect(onResolve).toHaveBeenCalledOnce();
  await act(async () => reject(new ApiError("COMMAND_IN_PROGRESS", "Outcome unknown", 409)));
  expect(screen.getByRole("button", { name: other })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: label })).toHaveProperty("disabled", false);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: label })));
  expect(onResolve).toHaveBeenCalledTimes(2);
});

it("isolates old tool approval errors from a new version and hides writes for readers", async () => {
  let reject!: (error: Error) => void;
  const onResolve = vi.fn(() => new Promise<void>((_resolve, no) => { reject = no; }));
  const call = agentToolCall();
  const interaction = { viewKey: "agent_tool_approval" as const, commands: ["approve", "deny"] };
  const { rerender } = render(<AgentToolApprovalCard call={call} interaction={interaction} onResolve={onResolve} />);
  fireEvent.click(screen.getByRole("button", { name: "允许调用" }));
  const next = { ...call, approval: { ...call.approval!, version: 2, subject_snapshot_hash: "new-subject" } };
  rerender(<AgentToolApprovalCard call={next} interaction={interaction} onResolve={onResolve} />);
  await act(async () => reject(new ApiError("LATE", "Old failure", 502)));
  expect(screen.queryByText("Old failure")).toBeNull();
  expect(screen.getByRole("button", { name: "拒绝调用" })).toHaveProperty("disabled", false);
  rerender(<AgentToolApprovalCard call={next} interaction={interaction} readOnly onResolve={onResolve} />);
  expect(screen.queryByRole("button", { name: "允许调用" })).toBeNull();
  expect(screen.queryByRole("button", { name: "拒绝调用" })).toBeNull();
});

function agentToolCall(): AgentToolCall {
  const at = "2026-08-17T08:01:00Z";
  return {
    agent_tool_call_id: "tool-call-1", workspace_id: "workspace-1", project_id: "project-1",
    conversation_id: "conversation-1", agent_turn_id: "turn-1", sdk_tool_call_id: "sdk-tool-1",
    tool_id: "project.write", tool_kind: "local", tool_name: "Write project file", access_mode: "write",
    approval_policy: "always", approval_status: "pending", status: "pending_approval",
    arguments_summary: { path: "draft.txt", token: "[redacted]" }, requested_at: at, updated_at: at,
    approval: {
      agent_tool_approval_id: "approval-1", agent_tool_call_id: "tool-call-1", workspace_id: "workspace-1",
      project_id: "project-1", conversation_id: "conversation-1", status: "pending", version: 1,
      title: "Allow project write", reason: "This tool changes project state.", options: ["approve", "reject"],
      subject_snapshot_hash: "hash", requested_at: at,
    },
  };
}
