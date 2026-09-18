// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { VideoBatchDialog } from "./App";
import { api, ApiError } from "./api";
import type { AssetSetSnapshot } from "./types";

function batch(version = 2, sealed = false): AssetSetSnapshot {
  return {
    asset_set: { project_id: "project", asset_set_id: "batch", current_version: version, current_version_id: `v${version}`, status: sealed ? "sealed" : "collecting" },
    version: { asset_set_id: "batch", asset_set_version_id: `v${version}`, version, status: sealed ? "sealed" : "draft", member_count: 1, completeness: { recognized_episode_count: 1, unrecognized_count: 0, missing_episode_numbers: [], duplicate_episode_numbers: [], failed_asset_ids: [], order_confirmed: true } },
    members: [{ asset_id: "video", episode_order: 1, episode_no: 1, episode_label: "Episode 1", filename_candidate: { raw: "Episode 1.mp4" }, included: true }],
  };
}
const props = () => ({ projectID: "project", snapshot: batch(), onChange: vi.fn(), onClose: vi.fn() });
beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("Unexpected fixture network")));
  vi.spyOn(api, "sealVideoBatch").mockResolvedValue(batch(3, true));
  vi.spyOn(api, "getAssetSet").mockResolvedValue(batch(3, true));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const click = (name: string) => act(async () => fireEvent.click(screen.getByRole("button", { name })));

it.each(["remove", "include", "exclude", "reopen"])("retries the original %s operation and reports pending state while hidden", async (action) => {
  const input = { ...props(), onPendingChange: vi.fn() };
  input.snapshot = batch(2, action === "reopen");
  if (action === "include") input.snapshot.members[0].included = false;
  const result = batch(3);
  if (action === "remove") { result.members = []; result.version.member_count = 0; }
  if (action === "exclude") result.members[0].included = false;
  const method = action === "remove" ? "removeVideoBatchAsset" : action === "reopen" ? "reopenVideoBatch" : "setVideoBatchAssetIncluded";
  const mutate = vi.spyOn(api, method).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Unknown operation", 503)).mockResolvedValue(result);
  vi.mocked(api.getAssetSet).mockResolvedValue(result);
  const view = render(<VideoBatchDialog {...input} />);
  if (action === "remove") await click("移除 Episode 1.mp4");
  else if (action === "reopen") await click("重新收集");
  else await act(async () => fireEvent.click(screen.getByRole("checkbox", { name: "纳入 Episode 1.mp4" })));
  expect(input.onPendingChange).toHaveBeenLastCalledWith(true);
  view.rerender(<VideoBatchDialog {...input} open={false} />);
  expect(input.onPendingChange).toHaveBeenLastCalledWith(true);
  view.rerender(<VideoBatchDialog {...input} open />);
  await click("重试原操作");
  expect(mutate.mock.calls[1]).toEqual(mutate.mock.calls[0]);
  if (action === "include" || action === "exclude") expect(mutate.mock.calls[1][2]).toBe(action === "include");
  expect(input.onChange).toHaveBeenCalledWith(result);
  expect(input.onPendingChange).toHaveBeenLastCalledWith(false);
});

it("retains the successful receipt when applying it to parent attachments fails", async () => {
  const input = { ...props(), onChange: vi.fn().mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Attachment read failed", 503)).mockResolvedValue(undefined) };
  render(<VideoBatchDialog {...input} />);
  await click("确认已上传完成");
  expect(input.onClose).not.toHaveBeenCalled();
  await click("重新读取操作结果");
  expect(api.sealVideoBatch).toHaveBeenCalledOnce();
  expect(api.getAssetSet).toHaveBeenCalledTimes(2);
  expect(input.onChange).toHaveBeenCalledTimes(2);
  expect(input.onClose).toHaveBeenCalledOnce();
});

it("keeps a used sealed batch unchanged when reopening is rejected", async () => {
  const input = { ...props(), snapshot: batch(3, true) };
  vi.spyOn(api, "reopenVideoBatch").mockRejectedValue(new ApiError("ASSET_SET_REOPEN_CONFLICT", "Batch already used", 409));
  render(<VideoBatchDialog {...input} />);
  await click("重新收集");
  expect(screen.getByRole("alert").textContent).toBe("Batch already used");
  expect(input.onChange).toHaveBeenCalledWith(batch(3, true));
  expect(input.onClose).not.toHaveBeenCalled();
  expect(screen.getByRole("checkbox", { name: "纳入 Episode 1.mp4" }).hasAttribute("disabled")).toBe(true);
});

it("locks duplicate submissions and refreshes the exact confirmed batch", async () => {
  const input = props(); render(<VideoBatchDialog {...input} />);
  await act(async () => { const button = screen.getByRole("button", { name: "确认已上传完成" }); fireEvent.click(button); fireEvent.click(button); });
  expect(api.sealVideoBatch).toHaveBeenCalledTimes(1);
  expect(api.getAssetSet).toHaveBeenCalledWith("batch");
  expect(input.onChange).toHaveBeenCalledWith(batch(3, true)); expect(input.onClose).toHaveBeenCalledOnce();
});

it("keeps the original command after an unknown result and closing/reopening the dialog", async () => {
  const input = props(); const view = render(<VideoBatchDialog {...input} />);
  vi.mocked(api.sealVideoBatch).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Unknown result", 503));
  await click("确认已上传完成");
  expect(screen.getByRole("alert").textContent).toBe("Unknown result");
  expect(screen.getByRole("button", { name: "确认已上传完成" }).hasAttribute("disabled")).toBe(true);
  await click("关闭");
  view.rerender(<VideoBatchDialog {...input} open={false} />); expect(screen.queryByRole("dialog")).toBeNull();
  view.rerender(<VideoBatchDialog {...input} open />); await click("重试原操作");
  expect(vi.mocked(api.sealVideoBatch).mock.calls[1]).toEqual(vi.mocked(api.sealVideoBatch).mock.calls[0]);
});

it("only rereads after a successful mutation whose refresh failed", async () => {
  const input = props(); render(<VideoBatchDialog {...input} />);
  vi.mocked(api.getAssetSet).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Read unavailable", 503));
  await click("确认已上传完成"); expect(input.onChange).not.toHaveBeenCalled();
  await click("重新读取操作结果");
  expect(api.sealVideoBatch).toHaveBeenCalledOnce(); expect(api.getAssetSet).toHaveBeenCalledTimes(2);
  expect(input.onChange).toHaveBeenCalledWith(batch(3, true));
});

it.each(["project", "set", "version", "state"])("rejects a wrong %s mutation receipt", async (kind) => {
  const receipt = batch(3, true);
  if (kind === "project") receipt.asset_set.project_id = "foreign";
  if (kind === "set") receipt.version.asset_set_id = "foreign";
  if (kind === "version") receipt.version.version = 5;
  if (kind === "state") receipt.asset_set.status = "collecting";
  vi.mocked(api.sealVideoBatch).mockResolvedValue(receipt);
  const input = props(); render(<VideoBatchDialog {...input} />); await click("确认已上传完成");
  expect(screen.getByRole("alert").textContent).toContain("回执与当前操作不一致");
  expect(input.onChange).not.toHaveBeenCalled(); expect(api.getAssetSet).not.toHaveBeenCalled();
});

it("uses the current authoritative version after retrying an older successful seal", async () => {
  const input = props(); vi.mocked(api.getAssetSet).mockResolvedValue(batch(4));
  render(<VideoBatchDialog {...input} />); await click("确认已上传完成");
  expect(input.onChange).toHaveBeenCalledWith(batch(4)); expect(input.onClose).not.toHaveBeenCalled();
});

it("preserves dirty episode numbers on a remote version change until explicit refresh", async () => {
  const input = props(); input.snapshot.members[0].episode_no = null;
  input.snapshot.version.completeness.unrecognized_count = 1;
  input.snapshot.version.completeness.recognized_episode_count = 0;
  const view = render(<VideoBatchDialog {...input} />);
  fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "7" } });
  const changed = structuredClone(input.snapshot); changed.asset_set.current_version = 3; changed.asset_set.current_version_id = "v3"; changed.version.version = 3; changed.version.asset_set_version_id = "v3";
  view.rerender(<VideoBatchDialog {...input} snapshot={changed} />);
  expect(screen.getByRole("spinbutton")).toHaveProperty("value", "7");
  expect(screen.getByRole("spinbutton").hasAttribute("disabled")).toBe(true);
  vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  await click("重新读取批次"); expect(api.getAssetSet).not.toHaveBeenCalled();
  vi.mocked(api.getAssetSet).mockResolvedValue(changed); await click("重新读取批次");
  expect(screen.getByRole("spinbutton")).toHaveProperty("value", "1");
  expect(screen.getByRole("spinbutton").hasAttribute("disabled")).toBe(false);
});

it.each(["viewer", "empty", "duplicates", "unavailable", "foreign"])("does not seal a batch with %s restrictions", async (kind) => {
  const input = props();
  if (kind === "empty") input.snapshot.members[0].included = false;
  if (kind === "duplicates") input.snapshot.version.completeness.duplicate_episode_numbers = [1];
  if (kind === "unavailable") input.snapshot.version.completeness.failed_asset_ids = ["video"];
  if (kind === "foreign") input.snapshot.asset_set.project_id = "foreign";
  render(<VideoBatchDialog {...input} readOnly={kind === "viewer"} />);
  await click("确认已上传完成"); expect(api.sealVideoBatch).not.toHaveBeenCalled();
});

it("rejects huge episode numbers before sending a mutation", async () => {
  const input = props(); input.snapshot.members[0].episode_no = null;
  const update = vi.spyOn(api, "setVideoEpisodeNo"); render(<VideoBatchDialog {...input} />);
  fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "1000000000" } });
  await click("确认Episode 1.mp4的集号");
  expect(screen.getByRole("alert").textContent).toContain("1 到 10000"); expect(update).not.toHaveBeenCalled();
});

it("ignores a late response after access is revoked", async () => {
  const input = props(); let resolve!: (value: AssetSetSnapshot) => void;
  vi.mocked(api.sealVideoBatch).mockImplementation(() => new Promise((done) => { resolve = done; }));
  const view = render(<VideoBatchDialog {...input} />); await click("确认已上传完成");
  view.rerender(<VideoBatchDialog {...input} readOnly />); await act(async () => resolve(batch(3, true)));
  expect(input.onChange).not.toHaveBeenCalled(); expect(input.onClose).not.toHaveBeenCalled();
});

it("keeps another unsubmitted episode draft after confirming one asset", async () => {
  const input = props(); input.snapshot.members[0].episode_no = null;
  input.snapshot.members.push({ ...input.snapshot.members[0], asset_id: "second", episode_order: 2, filename_candidate: { raw: "Second.mp4" } });
  input.snapshot.version.member_count = 2; input.snapshot.version.completeness.unrecognized_count = 2; input.snapshot.version.completeness.recognized_episode_count = 0;
  const latest = structuredClone(input.snapshot); latest.asset_set.current_version = 3; latest.asset_set.current_version_id = "v3"; latest.version.version = 3; latest.version.asset_set_version_id = "v3"; latest.members[0].episode_no = 4;
  latest.version.completeness.unrecognized_count = 1; latest.version.completeness.recognized_episode_count = 1;
  vi.spyOn(api, "setVideoEpisodeNo").mockResolvedValue(latest); vi.mocked(api.getAssetSet).mockResolvedValue(latest);
  const view = render(<VideoBatchDialog {...input} />);
  fireEvent.change(screen.getByRole("spinbutton", { name: "Episode 1.mp4的集号" }), { target: { value: "4" } });
  fireEvent.change(screen.getByRole("spinbutton", { name: "Second.mp4的集号" }), { target: { value: "7" } });
  await click("确认Episode 1.mp4的集号");
  view.rerender(<VideoBatchDialog {...input} snapshot={latest} />);
  expect(screen.getByRole("spinbutton", { name: "Second.mp4的集号" })).toHaveProperty("value", "7");
});

it("blocks a foreign authoritative read after a valid mutation", async () => {
  const input = props(), latest = batch(3, true); latest.asset_set.project_id = "foreign";
  vi.mocked(api.getAssetSet).mockResolvedValue(latest); render(<VideoBatchDialog {...input} />);
  await click("确认已上传完成"); expect(input.onChange).not.toHaveBeenCalled(); expect(input.onClose).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toContain("归属不一致");
});
