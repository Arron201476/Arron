// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { ArtifactDelivery } from "../types";
import { ArtifactDownloads } from "./ArtifactDownloads";

const receipt: ArtifactDelivery = { project_id: "p", artifact_id: "art", artifact_version_id: "av-1", version: 1, status: "superseded", title: "Saved document", downloads: [{ format: "txt", filename: "exact-v1.txt", content_type: "text/plain", download_url: "/api/v1/artifact-versions/av-1/download?format=txt" }, { format: "json", filename: "exact-v1.json", content_type: "application/json", download_url: "/api/v1/artifact-versions/av-1/download?format=json" }], warnings: [] };
const dialogMethods = ["showModal", "close"] as const;
const originals = dialogMethods.map((name) => Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, name));
beforeAll(() => { for (const name of dialogMethods) Object.defineProperty(HTMLDialogElement.prototype, name, { configurable: true, writable: true, value() {} }); });
afterAll(() => { dialogMethods.forEach((name, index) => { const descriptor = originals[index]; if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, name, descriptor); else Reflect.deleteProperty(HTMLDialogElement.prototype, name); }); });
beforeEach(() => {
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (this: HTMLDialogElement) { this.removeAttribute("open"); queueMicrotask(() => this.dispatchEvent(new Event("close"))); });
  vi.spyOn(api, "getArtifactDelivery").mockResolvedValue(receipt);
  vi.spyOn(api, "downloadArtifactVersion").mockResolvedValue(new Blob(["saved text"]));
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  vi.stubGlobal("URL", class extends URL { static createObjectURL = vi.fn(() => "blob:fixture"); static revokeObjectURL = vi.fn(); });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function open() { fireEvent.click(screen.getByRole("button", { name: "下载产物" })); }

describe("artifact downloads", () => {
  it("loads exact version on demand and survives StrictMode dialog cleanup", async () => {
    render(<StrictMode><ArtifactDownloads artifactID="art" versionID="av-1" /></StrictMode>);
    expect(api.getArtifactDelivery).not.toHaveBeenCalled(); open();
    await screen.findByText("Saved document");
    expect(screen.getByRole("dialog", { name: "下载产物" })).toBeTruthy();
    expect(screen.getByText("版本 1 · 历史版本")).toBeTruthy();
    expect(api.getArtifactDelivery).toHaveBeenLastCalledWith("av-1", expect.any(AbortSignal));
    fireEvent.change(screen.getByRole("combobox", { name: "下载格式" }), { target: { value: "json" } });
    fireEvent.click(screen.getByRole("button", { name: "下载", exact: true }));
    await waitFor(() => expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledOnce());
    expect(api.downloadArtifactVersion).toHaveBeenCalledWith("av-1", "json", expect.any(AbortSignal));
    const anchor = vi.mocked(HTMLAnchorElement.prototype.click).mock.instances[0] as HTMLAnchorElement;
    expect(anchor.download).toBe("exact-v1.json");
  });

  it.each(["请先保存或取消修改", "修改稿尚未保存", "产物版本尚未读取完成"])("disables unavailable content: %s", (reason) => {
    render(<ArtifactDownloads artifactID="art" versionID="av-1" disabledReason={reason} />);
    expect(screen.getByRole("button", { name: "下载产物" }).hasAttribute("disabled")).toBe(true); open();
    expect(api.getArtifactDelivery).not.toHaveBeenCalled();
  });

  it("closes stale content on version change and ignores a late response", async () => {
    let resolve!: (result: ArtifactDelivery) => void;
    vi.mocked(api.getArtifactDelivery).mockReturnValueOnce(new Promise((done) => { resolve = done; }));
    const { rerender } = render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    rerender(<ArtifactDownloads artifactID="art" versionID="av-2" />);
    await act(async () => resolve(receipt));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByText("Saved document")).toBeNull();
    vi.mocked(api.getArtifactDelivery).mockResolvedValue({ ...receipt, artifact_version_id: "av-2", version: 2 }); open();
    await screen.findByText("版本 2 · 历史版本");
  });

  it("removes an open download when unsaved edits appear", async () => {
    const { rerender } = render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    await screen.findByText("Saved document");
    rerender(<ArtifactDownloads artifactID="art" versionID="av-1" disabledReason="请先保存或取消修改" />);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("shows permission errors and supports metadata retry without downloading", async () => {
    vi.mocked(api.getArtifactDelivery).mockRejectedValueOnce(new Error("没有访问权限"));
    render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "没有访问权限");
    expect(api.downloadArtifactVersion).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    await screen.findByText("Saved document");
  });

  it("refuses a mismatched receipt", async () => {
    vi.mocked(api.getArtifactDelivery).mockResolvedValue({ ...receipt, artifact_version_id: "wrong-version" });
    render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "下载回执与当前产物版本不符。");
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it.each([
    { downloads: null }, { downloads: [] }, { downloads: [null] },
    { downloads: [{ ...receipt.downloads[0], format: 1 }] },
    { downloads: [{ ...receipt.downloads[0], filename: "" }] },
    { downloads: [receipt.downloads[0], receipt.downloads[0]] },
    { warnings: null }, { warnings: [{}] }, { version: 0 }, { title: {} },
  ])("keeps malformed download metadata recoverable: %j", async (change) => {
    vi.mocked(api.getArtifactDelivery).mockResolvedValueOnce({ ...receipt, ...change } as unknown as ArtifactDelivery);
    render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "下载格式回执无效，请重新读取。");
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(api.downloadArtifactVersion).not.toHaveBeenCalled();
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    await screen.findByRole("combobox", { name: "下载格式" });
  });

  it("does not turn failed download responses into files or follow receipt URLs", async () => {
    vi.mocked(api.getArtifactDelivery).mockResolvedValue({ ...receipt, downloads: [{ ...receipt.downloads[0], download_url: "https://untrusted.invalid/file" }] });
    vi.mocked(api.downloadArtifactVersion).mockRejectedValueOnce(new Error("作品已删除"));
    render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    await screen.findByText("Saved document");
    fireEvent.click(screen.getByRole("button", { name: "下载", exact: true }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "作品已删除");
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "下载", exact: true }));
    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalledOnce());
    expect(api.downloadArtifactVersion).toHaveBeenCalledWith("av-1", "txt", expect.any(AbortSignal));
  });

  it("prevents duplicate requests and ignores a download after closing", async () => {
    let resolve!: (blob: Blob) => void;
    vi.mocked(api.downloadArtifactVersion).mockReturnValue(new Promise((done) => { resolve = done; }));
    render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    await screen.findByText("Saved document");
    const button = screen.getByRole("button", { name: "下载", exact: true });
    fireEvent.click(button); fireEvent.click(button);
    expect(api.downloadArtifactVersion).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "关闭下载" }));
    await act(async () => resolve(new Blob(["late"])));
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });

  it("shows table conversion warnings from the backend", async () => {
    vi.mocked(api.getArtifactDelivery).mockResolvedValue({ ...receipt, downloads: [receipt.downloads[1]], warnings: ["表格列或行结构不完整，仅提供完整 JSON。"] });
    render(<ArtifactDownloads artifactID="art" versionID="av-1" />); open();
    await screen.findByText("表格列或行结构不完整，仅提供完整 JSON。");
    expect(screen.queryByRole("option", { name: "CSV" })).toBeNull();
  });
});
