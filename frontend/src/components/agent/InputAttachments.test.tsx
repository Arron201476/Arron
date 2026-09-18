// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import type { Asset } from "../../types";
import { ExecutionInputs } from "./AgentTaskInputs";

const asset = { asset_id: "asset-1", project_id: "project", current_snapshot_id: "snapshot-1", kind: "text", status: "available", parse_status: "completed", original_filename: "notes.txt", display_name: "notes.txt", size_bytes: 12 } as Asset;
const props = { executionID: "attempt", active: true, paused: false, inputs: [], label: "Open additions", inputLabel: "Additional request", changeScope: { projectID: "project", mode: "stateful_workflow" as const } };

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) { this.removeAttribute("open"); });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("submits attachment-only input with the exact snapshot and keeps its nonce after a lost response", async () => {
  vi.spyOn(api, "listAssets").mockResolvedValue([asset]);
  const append = vi.fn().mockRejectedValueOnce(new Error("Lost receipt")).mockResolvedValue(undefined);
  render(<ExecutionInputs {...props} onAppend={append} />);
  fireEvent.click(screen.getByRole("button", { name: "Open additions" }));
  fireEvent.click(screen.getByRole("button", { name: "选择追加项目材料" }));
  const dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("checkbox"));
  fireEvent.click(within(dialog).getByRole("button", { name: /使用所选材料/ }));
  const submit = screen.getByRole("button", { name: "提交追加要求" });
  await waitFor(() => expect(submit).toBeEnabled());
  fireEvent.click(submit);
  expect(await screen.findByText("Lost receipt")).toBeVisible();
  expect(screen.getByRole("list", { name: "待追加材料" })).toHaveTextContent("notes.txt");
  fireEvent.click(submit);
  await waitFor(() => expect(append).toHaveBeenCalledTimes(2));
  expect(append.mock.calls[1]).toEqual(append.mock.calls[0]);
  expect(append.mock.calls[0]).toEqual(["", expect.any(String), [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1", display_name: "notes.txt", kind: "text" }]]);
  await waitFor(() => expect(screen.queryByRole("list", { name: "待追加材料" })).not.toBeInTheDocument());
});

it("locks the target during selection and retains already uploaded files after a later upload fails", async () => {
  vi.spyOn(api, "listAssets").mockResolvedValue([asset]);
  vi.spyOn(api, "uploadFiles").mockResolvedValueOnce([{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1", display_name: "notes.txt" }]).mockRejectedValueOnce(new Error("Second file failed"));
  const dirty = vi.fn();
  render(<ExecutionInputs {...props} onAppend={vi.fn()} onDraftChange={dirty} />);
  fireEvent.click(screen.getByRole("button", { name: "Open additions" }));
  fireEvent.click(screen.getByRole("button", { name: "选择追加项目材料" }));
  await screen.findByRole("dialog");
  await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true));
  expect(screen.getByRole("button", { name: "提交追加要求" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "关闭项目材料" }));
  await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(false));
  fireEvent.change(screen.getByLabelText("上传追加材料", { selector: "input" }), { target: { files: [new File(["first"], "notes.txt"), new File(["second"], "second.txt")] } });
  expect(await screen.findByText("Second file failed")).toBeVisible();
  expect(screen.getByRole("list", { name: "待追加材料" })).toHaveTextContent("notes.txt");
  expect(dirty).toHaveBeenLastCalledWith(true);
});

it("keeps uploaded draft materials when permission is revoked and never submits them", async () => {
  vi.spyOn(api, "listAssets").mockResolvedValue([asset]);
  const append = vi.fn();
  const { rerender } = render(<ExecutionInputs {...props} onAppend={append} />);
  fireEvent.click(screen.getByRole("button", { name: "Open additions" }));
  fireEvent.click(screen.getByRole("button", { name: "选择追加项目材料" }));
  const dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("checkbox"));
  fireEvent.click(within(dialog).getByRole("button", { name: /使用所选材料/ }));
  await screen.findByRole("list", { name: "待追加材料" });
  rerender(<ExecutionInputs {...props} onAppend={undefined} />);
  expect(screen.getByRole("button", { name: "提交追加要求" })).toBeDisabled();
  expect(screen.getByRole("list", { name: "待追加材料" })).toHaveTextContent("notes.txt");
  expect(append).not.toHaveBeenCalled();
});

it("rejects oversized local files before upload", async () => {
  const upload = vi.spyOn(api, "uploadFiles");
  render(<ExecutionInputs {...props} onAppend={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "Open additions" }));
  fireEvent.change(screen.getByLabelText("上传追加材料", { selector: "input" }), { target: { files: [new File([new Uint8Array(5 * 1024 * 1024 + 1)], "oversize.bin")] } });
  expect(await screen.findByRole("alert")).toHaveTextContent("5 MiB");
  expect(upload).not.toHaveBeenCalled();
});

it("sends only exact reference IDs through all three public input APIs", async () => {
  const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: {} }) });
  vi.stubGlobal("fetch", fetcher);
  const refs = [{ asset_id: "a", asset_snapshot_id: "s", display_name: "untrusted", kind: "text", ignored_entries: ["ignored"] }];
  await api.appendAgentTurnInput("turn", "", "nonce", refs);
  await api.appendAgentTaskInput("task", "", "nonce", refs);
  await api.appendExecutionInput("project", "attempt", "", "nonce", refs);
  for (const [, request] of fetcher.mock.calls) {
    expect(JSON.parse(request.body)).toEqual({ content: "", attachment_refs: [{ asset_id: "a", asset_snapshot_id: "s" }] });
    expect(new Headers(request.headers).get("Idempotency-Key")).toBe("nonce");
  }
});
