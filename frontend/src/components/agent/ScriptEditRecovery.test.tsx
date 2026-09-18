// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ScriptEditRecovery } from "./ScriptEditRecovery";
import { RunEvent } from "../../App";
import { ApiError } from "../../api";
import type { PendingScriptEdit } from "../../types";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const pending: PendingScriptEdit = { project_id: "project", run_id: "run", step_run_id: "step", expected_script_version_ids: ["saved"], pending_scopes: ["episode:1"], can_complete: true };

it("blocks duplicate completion clicks while keeping the saved version set unchanged", async () => {
  let complete!: () => void;
  const submit = vi.fn(() => new Promise<void>((resolve) => { complete = resolve; }));
  render(<ScriptEditRecovery pending={pending} onComplete={submit} />);
  await act(async () => { const button = screen.getByRole("button", { name: "更新交接" }); fireEvent.click(button); fireEvent.click(button); });
  expect(submit).toHaveBeenCalledExactlyOnceWith(pending);
  await act(async () => complete());
});

it("does not attach an old completion failure to newly projected versions", async () => {
  let fail!: (error: Error) => void;
  const submit = vi.fn(() => new Promise<void>((_resolve, reject) => { fail = reject; }));
  const view = render(<ScriptEditRecovery pending={pending} onComplete={submit} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "更新交接" })));
  view.rerender(<ScriptEditRecovery pending={{ ...pending, expected_script_version_ids: ["new-version"] }} onComplete={submit} />);
  await act(async () => fail(new ApiError("UNAVAILABLE", "Old version failure", 503)));
  expect(screen.queryByText("Old version failure")).toBeNull();
  expect((screen.getByRole("button", { name: "更新交接" }) as HTMLButtonElement).disabled).toBe(false);
});

it("keeps both recovery and run controls read-only for viewers", () => {
  const submit = vi.fn();
  render(<><ScriptEditRecovery pending={pending} readOnly onComplete={submit} /><RunEvent readOnly snapshot={{ run: { run_id: "run", capability_id: "test", status: "running", current_step_run_id: "step" }, current_approval: null, available_actions: ["pause_run", "cancel_run"].map((action_id) => ({ action_id, target_id: "run", target_type: "run", enabled: true, disabled_reason: null })) }} onRunAction={submit} /></>);
  expect(screen.queryByRole("button", { name: /更新交接|暂停任务|更多运行操作/ })).toBeNull();
  expect(submit).not.toHaveBeenCalled();
});
