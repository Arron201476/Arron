// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Composer } from "../../App";
import { ProjectMaterialPicker } from "./ProjectMaterialPicker";
import { AgentExecutionHistory } from "../agent/AgentExecutionHistory";
import { citationLink, materialAttachment, toolOutputAssets } from "./projectMaterials";
import type { AgentComposerContext, AgentToolCall, Asset, ComposerRegistryEntry } from "../../types";

const asset: Asset = { asset_id: "ast", project_id: "p", current_snapshot_id: "ass", kind: "text", display_name: "table.csv", original_filename: "table.csv", status: "available", parse_status: "completed", source_type: "hosted_tool", metadata: { agent_tool_call_id: "call", sdk_tool_call_id: "sdk" } };
const call = { agent_tool_call_id: "call", sdk_tool_call_id: "sdk", project_id: "p", status: "completed", tool_kind: "hosted", tool_name: "code_interpreter", requested_at: "2026-09-06T00:00:00Z" } as AgentToolCall;
const context: AgentComposerContext = { view: { project_id: "p", run_id: null, capability_id: null, artifact_id: null, artifact_version_id: null, artifact_type: null, scope_key: null, artifact_label: null }, selection: null };
const methods = ["showModal", "close"] as const;
const originals = methods.map((name) => Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, name));
beforeAll(() => { for (const name of methods) Object.defineProperty(HTMLDialogElement.prototype, name, { configurable: true, writable: true, value() {} }); });
afterAll(() => { methods.forEach((name, index) => { const original = originals[index]; if (original) Object.defineProperty(HTMLDialogElement.prototype, name, original); else Reflect.deleteProperty(HTMLDialogElement.prototype, name); }); });
beforeEach(() => {
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (this: HTMLDialogElement) { this.setAttribute("open", ""); });
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (this: HTMLDialogElement) { this.removeAttribute("open"); queueMicrotask(() => this.dispatchEvent(new Event("close"))); });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("project material delivery", () => {
  it("renders only authoritative files for the same project and SDK call", () => {
    const foreign = { ...asset, asset_id: "foreign", project_id: "other" };
    const wrong = { ...asset, asset_id: "wrong", metadata: { ...asset.metadata, sdk_tool_call_id: "wrong" } };
    expect(toolOutputAssets(call, [asset, foreign, wrong])).toEqual([asset]);
    expect(toolOutputAssets({ ...call, tool_kind: "mcp" }, [asset])).toEqual([]);
    const onUse = vi.fn();
    const { container } = render(<AgentExecutionHistory turns={[]} calls={[{ ...call, result_summary: { outputs: [{ asset_id: "fake", filename: "fake.bin" }] }, citations: [{ type: "url_citation", title: "Source", url: "https://example.org/source" }, { type: "file_citation", filename: "source.pdf", file_id: "file-source" }, { type: "url_citation", url: "javascript:alert(1)" }] }]} assets={[asset, foreign, wrong]} onUseAsset={onUse} />);
    container.querySelector("details")?.setAttribute("open", "");
    expect(screen.getByRole("link", { name: "下载 table.csv" }).getAttribute("href")).toBe("/api/v1/assets/ast/download");
    expect(screen.queryByText("fake.bin")).toBeNull();
    expect(screen.getByRole("link", { name: "Source" }).getAttribute("rel")).toBe("noopener noreferrer");
    expect(screen.getByText("检索文件：source.pdf")).toBeTruthy();
    expect(screen.getByText("引用地址不可用")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "使用 table.csv 作为材料" }));
    expect(onUse).toHaveBeenCalledWith(asset);
  });

  it("does not offer unavailable files for download or use", () => {
    const { container } = render(<AgentExecutionHistory turns={[]} calls={[call]} assets={[{ ...asset, status: "deleted" }]} onUseAsset={vi.fn()} />);
    container.querySelector("details")?.setAttribute("open", "");
    expect(screen.getByRole("button", { name: "下载 table.csv" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "使用 table.csv 作为材料" }).hasAttribute("disabled")).toBe(true);
  });

  it("shows MCP files only from the matching call and reuses their exact asset", () => {
    const mcpCall = { ...call, tool_kind: "mcp", tool_name: "export_fact" } as AgentToolCall;
    const output = { ...asset, source_type: "mcp_tool" };
    const foreign = { ...output, asset_id: "foreign", metadata: { ...output.metadata, agent_tool_call_id: "another" } };
    expect(toolOutputAssets(mcpCall, [asset, output, foreign])).toEqual([output]);
    expect(toolOutputAssets(call, [output])).toEqual([]);
    const onUse = vi.fn();
    const { container } = render(<AgentExecutionHistory turns={[]} calls={[mcpCall]} assets={[asset, output, foreign]} onUseAsset={onUse} />);
    container.querySelector("details")?.setAttribute("open", "");
    expect(screen.getAllByRole("link", { name: "下载 table.csv" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "使用 table.csv 作为材料" }));
    expect(onUse).toHaveBeenCalledWith(output);
  });

  it.each(["javascript:alert(1)", "//example.org/x", "https://user:pass@example.org/", "https://example.org/?access_token=secret", "https://example.org/#access_token=secret", "file:///private"])("rejects unsafe citation link %s", (url) => { expect(citationLink(url)).toBeNull(); });

  it("keeps selection explicit, searchable, scoped, and excludes unsupported archives", async () => {
    const onConfirm = vi.fn();
    render(<StrictMode><ProjectMaterialPicker projectID="p" assets={[asset, { ...asset, asset_id: "arch", original_filename: "output.bin.zip", display_name: "output.bin.zip", kind: "archive" }, { ...asset, asset_id: "foreign", project_id: "other", display_name: "foreign" }]} capability={null} onClose={vi.fn()} onConfirm={onConfirm} /></StrictMode>);
    expect(screen.queryByText("foreign")).toBeNull();
    expect(screen.getByRole("checkbox", { name: /output.bin.zip/ }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "使用所选材料" }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByRole("textbox", { name: "搜索项目材料" }), { target: { value: "table" } });
    fireEvent.click(screen.getByRole("checkbox", { name: /table.csv/ }));
    fireEvent.click(screen.getByRole("button", { name: "使用所选材料（1）" }));
    expect(onConfirm).toHaveBeenCalledWith([asset]);
  });

  it("invalidates selected files that become deleted before confirmation", () => {
    const props = { projectID: "p", capability: null, onClose: vi.fn(), onConfirm: vi.fn() };
    const { rerender } = render(<ProjectMaterialPicker {...props} assets={[asset]} />);
    fireEvent.click(screen.getByRole("checkbox", { name: /table.csv/ }));
    rerender(<ProjectMaterialPicker {...props} assets={[{ ...asset, status: "deleted" }]} />);
    expect(screen.getByRole("button", { name: "使用所选材料" }).hasAttribute("disabled")).toBe(true);
  });

  it("passes existing exact snapshot references to the next message without reuploading", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    render(<Composer projectID="p" projectAssets={[asset]} capabilities={[]} runSnapshot={null} context={context} onClearSelection={vi.fn()} onSend={onSend} />);
    fireEvent.click(screen.getByRole("button", { name: "项目材料" }));
    fireEvent.click(screen.getByRole("checkbox", { name: /table.csv/ }));
    fireEvent.click(screen.getByRole("button", { name: "使用所选材料（1）" }));
    fireEvent.change(screen.getByRole("textbox", { name: "给 Agent 的消息" }), { target: { value: "Analyze this output" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("Analyze this output", null, [materialAttachment(asset)], null, expect.any(AbortSignal), expect.stringMatching(/^[0-9a-f-]{36}$/)));
  });

  it("handles one-click tool output reuse once in StrictMode", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined), handled = vi.fn();
    render(<StrictMode><Composer projectID="p" projectAssets={[asset]} capabilities={[]} runSnapshot={null} context={context} onClearSelection={vi.fn()} onSend={onSend} materialRequest={{ id: "request", asset }} onMaterialHandled={handled} /></StrictMode>);
    expect(screen.getAllByRole("button", { name: "移除 table.csv" })).toHaveLength(1);
    expect(handled).toHaveBeenCalledOnce();
    fireEvent.change(screen.getByRole("textbox", { name: "给 Agent 的消息" }), { target: { value: "Reuse" } });
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("Reuse", null, [materialAttachment(asset)], null, expect.any(AbortSignal), expect.stringMatching(/^[0-9a-f-]{36}$/)));
  });

  it("rejects a stale or foreign one-click reference", () => {
    render(<Composer projectID="p" projectAssets={[asset]} capabilities={[]} runSnapshot={null} context={context} onClearSelection={vi.fn()} onSend={vi.fn()} materialRequest={{ id: "request", asset: { ...asset, current_snapshot_id: "old" } }} />);
    expect(screen.getByText("材料版本已更新，请重新选择。")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "移除 table.csv" })).toBeNull();
  });

  it("enforces Skill accepted kinds in the material picker", () => {
    const capability = { label: "Image Skill", accepted_asset_kinds: ["image"] } as ComposerRegistryEntry;
    render(<ProjectMaterialPicker projectID="p" assets={[asset]} capability={capability} onClose={vi.fn()} onConfirm={vi.fn()} />);
    expect(screen.getByRole("checkbox", { name: /table.csv/ }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByText("Image Skill 不接受这类材料")).toBeTruthy();
  });
});
