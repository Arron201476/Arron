// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { RevisionCard } from "./App";
import { ApiError } from "./api";
import type { RevisionRequest, TargetResolution } from "./types";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
const revision: RevisionRequest = { revision_request_id: "revision", project_id: "project", conversation_id: "conversation", request_message_id: "message", target_resolution_id: "target", artifact_id: "artifact", base_artifact_version_id: "base", instruction: "Revise this document", operation: "revise", status: "proposed", execution_policy: "safe_checkpoint", version: 3, created_at: "2026-09-09T00:00:00Z", updated_at: "2026-09-09T00:00:00Z" };
const props = () => ({ revision, resolution: null, onResolveTarget: vi.fn(), onExecute: vi.fn(), onPreview: vi.fn(), onAccept: vi.fn(), onReject: vi.fn(), onCancel: vi.fn() });

it.each(["REVISION_EXECUTION_OWNER_REQUIRED", "REVISION_EXECUTION_OWNER_INVALID", "AUTHENTICATION_REQUIRED", "WORKSPACE_ACCESS_DENIED", "ROLE_FORBIDDEN", "PROJECT_NOT_FOUND", "IDENTITY_INVALID"])("requires explicit execution confirmation after %s", async (failure_code) => {
  const input = props();
  const failed = { ...revision, status: "failed", failure_code };
  input.onExecute.mockResolvedValue(undefined);
  const view = render(<RevisionCard {...input} revision={failed} />);
  expect(screen.getByText("修改执行授权不可用，原版本未受影响。")).toBeTruthy();
  expect(screen.queryByText("修改稿生成失败，原版本未受影响。")).toBeNull();
  expect(input.onExecute).not.toHaveBeenCalled();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认并重新执行" })));
  expect(input.onExecute).toHaveBeenCalledExactlyOnceWith(failed);
  view.rerender(<RevisionCard {...input} revision={failed} readOnly />);
  expect(screen.queryByRole("button", { name: "确认并重新执行" })).toBeNull();
});

it("keeps ordinary model failures distinct from authorization failures", () => {
  render(<RevisionCard {...props()} revision={{ ...revision, status: "failed", failure_code: "SDK_REVISION_EXECUTION_FAILED" }} />);
  expect(screen.getByText("修改稿生成失败，原版本未受影响。")).toBeTruthy();
  expect(screen.getByRole("button", { name: "重新生成修改稿" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "确认并重新执行" })).toBeNull();
});

it("locks competing revision operations while a request is in flight or uncertain", async () => {
  const input = props();
  let fail!: (error: Error) => void;
  input.onAccept.mockImplementationOnce(() => new Promise<void>((_resolve, reject) => { fail = reject; })).mockResolvedValue(undefined);
  render(<RevisionCard {...input} />);
  await act(async () => { const button = screen.getByRole("button", { name: "确认采用" }); fireEvent.click(button); fireEvent.click(button); });
  expect(input.onAccept).toHaveBeenCalledTimes(1);
  expect((screen.getByRole("button", { name: "放弃修改稿" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => fail(new ApiError("UNAVAILABLE", "Response unavailable", 503)));
  expect((screen.getByRole("button", { name: "放弃修改稿" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认采用" })));
  expect(input.onAccept).toHaveBeenCalledTimes(2);
});

it("does not carry a late operation error into a newer revision form", async () => {
  const input = props();
  let fail!: (error: Error) => void;
  input.onAccept.mockImplementation(() => new Promise<void>((_resolve, reject) => { fail = reject; }));
  const view = render(<RevisionCard {...input} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认采用" })));
  view.rerender(<RevisionCard {...input} revision={{ ...revision, version: 4, status: "accepted" }} />);
  await act(async () => fail(new ApiError("UNAVAILABLE", "Old operation error", 503)));
  expect(screen.queryByText("Old operation error")).toBeNull();
  expect(screen.getByRole("heading", { name: "修改稿已采用" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "确认采用" })).toBeNull();
});

it.each(["proposed", "queued", "failed", "waiting_safe_checkpoint"])("keeps %s revisions read-only for a viewer", (status) => {
  render(<RevisionCard {...props()} readOnly revision={{ ...revision, status }} />);
  expect(screen.queryByRole("button", { name: /确认采用|放弃修改稿|取消请求|生成修改稿/ })).toBeNull();
});

it("releases the competing-operation lock after a definitive rejection", async () => {
  const input = props();
  input.onAccept.mockRejectedValue(new ApiError("REVISION_STATE_CONFLICT", "Refresh required", 409));
  render(<RevisionCard {...input} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "确认采用" })));
  expect((screen.getByRole("button", { name: "放弃修改稿" }) as HTMLButtonElement).disabled).toBe(false);
});

const resolution: TargetResolution = { target_resolution_id: "target", project_id: "project", conversation_id: "conversation", request_message_id: "message", status: "ambiguous", source: "semantic", display: {}, candidates: ["one", "two"].map((id) => ({ candidate_id: id, artifact_id: `artifact-${id}`, artifact_version_id: `base-${id}`, artifact_type: "generic_document", scope_key: "global", entity: {}, display: { artifact_label: `Draft ${id}` }, score: 0.85 })), created_at: revision.created_at };

it.each([new ApiError("REQUEST_TIMEOUT", "Unknown target response", 408), new ApiError("COMMAND_IN_PROGRESS", "Unknown target response", 409)])("retains the selected target after an unknown response ($code)", async (error) => {
  const input = props();
  input.onResolveTarget.mockRejectedValueOnce(error).mockResolvedValue(undefined);
  render(<RevisionCard {...input} revision={{ ...revision, status: "waiting_target_confirmation" }} resolution={resolution} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: /Draft one/ })));
  expect((screen.getByRole("button", { name: /Draft two/ }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "取消这次修改" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: /Draft one/ })));
  expect(input.onResolveTarget).toHaveBeenCalledTimes(2);
  expect(input.onResolveTarget).toHaveBeenLastCalledWith(resolution, "one");
});

it.each(["project_id", "conversation_id", "request_message_id", "target_resolution_id", "status"] as const)("rejects mismatched target %s but leaves cancellation available", async (field) => {
  const input = props(); input.onCancel.mockResolvedValue(undefined);
  render(<RevisionCard {...input} revision={{ ...revision, status: "waiting_target_confirmation" }} resolution={{ ...resolution, [field]: "foreign" }} />);
  expect(screen.getByRole("heading", { name: "修改位置待核对" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /Draft one/ })).toBeNull();
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "取消这次修改" })));
  expect(input.onCancel).toHaveBeenCalledTimes(1);
  expect(input.onResolveTarget).not.toHaveBeenCalled();
});

it("keeps missing target resolution cancellation read-only for a viewer", () => {
  render(<RevisionCard {...props()} readOnly revision={{ ...revision, status: "waiting_target_confirmation" }} />);
  expect(screen.queryByRole("button", { name: "取消这次修改" })).toBeNull();
});
