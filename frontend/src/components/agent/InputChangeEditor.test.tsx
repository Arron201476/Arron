// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import { InputChangeEditor } from "./InputChangeEditor";
import { ExecutionInputs } from "./AgentTaskInputs";
import type { InputChangeReceipt } from "../../types";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const scope = { projectID: "p", mode: "conversation" as const };
const input = { input_id: "input", content: "Original", status: "received" as const, can_modify: true };
const receipt: InputChangeReceipt = { mode: "conversation", execution_id: "turn", input_id: "input", status: "superseded", replacement_input_id: "new" };
it("preserves revision text and the same request key across lost responses", async () => {
  const change = vi.spyOn(api, "changeExecutionInput").mockRejectedValueOnce(new Error("Lost response")).mockResolvedValueOnce(receipt);
  const saved = vi.fn();
  render(<InputChangeEditor scope={scope} executionID="turn" input={input} action="revise" allowed onClose={vi.fn()} onSaved={saved} />);
  fireEvent.change(screen.getByRole("textbox", { name: "修订内容" }), { target: { value: "Replacement" } });
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "提交修订要求" })));
  expect(screen.getByRole("alert").textContent).toBe("Lost response");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Replacement");
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "提交修订要求" })));
  expect(change.mock.calls[0]).toEqual(change.mock.calls[1]);
  expect(saved).toHaveBeenCalledWith(receipt);
});
it("requires confirmation for withdrawal and does not label it model-consumed", async () => {
  const change = vi.spyOn(api, "changeExecutionInput").mockResolvedValue({ ...receipt, status: "withdrawn", replacement_input_id: undefined });
  const dirty = vi.fn();
  render(<ExecutionInputs executionID="turn" active paused={false} inputs={[input]} label="Append" inputLabel="Input" changeScope={scope} onDraftChange={dirty} />);
  fireEvent.click(screen.getByText("追加要求 · 1"));
  fireEvent.click(screen.getByRole("button", { name: "撤回追加要求", exact: true }));
  expect(change).not.toHaveBeenCalled();
  expect(dirty).toHaveBeenLastCalledWith(true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认撤回追加要求" })));
  expect(screen.getByText("已撤回，未送入模型")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "修订追加要求" })).toBeNull();
  expect(dirty).toHaveBeenLastCalledWith(false);
});
it("preserves an open correction but disables it after claim or permission changes", () => {
  const props = { scope, executionID: "turn", input, action: "revise" as const, onClose: vi.fn(), onSaved: vi.fn() };
  const ui = render(<InputChangeEditor {...props} allowed />);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Draft" } });
  ui.rerender(<InputChangeEditor {...props} allowed={false} />);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Draft");
  expect(screen.getByRole("button", { name: "提交修订要求" })).toHaveProperty("disabled", true);
});
it("rejects a response belonging to a different execution", async () => {
  vi.spyOn(api, "changeExecutionInput").mockResolvedValue({ ...receipt, execution_id: "other" });
  const saved = vi.fn();
  render(<InputChangeEditor scope={scope} executionID="turn" input={input} action="revise" allowed onClose={vi.fn()} onSaved={saved} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "提交修订要求" })));
  expect(screen.getByRole("alert").textContent).toContain("回执不匹配");
  expect(saved).not.toHaveBeenCalled();
});
