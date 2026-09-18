// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createHash, webcrypto } from "node:crypto";
import { Blob as NodeBlob } from "node:buffer";
import { CandidateControls } from "./App";
import { api, ApiError } from "./api";
import type { Artifact, ArtifactVersion, FinalSelectionPreviewResult, Project, ScriptCandidate, ScriptExport } from "./types";

const stamp = "2026-09-09T00:00:00Z";
const candidate: ScriptCandidate = { candidate_id: "candidate", project_id: "project", source_run_id: "run", source_capability_id: "novel_to_script", scripts_artifact_version_id: "version-1", status: "candidate", label: "First draft", updated_at: stamp };
const other = { ...candidate, candidate_id: "other", scripts_artifact_version_id: "other-version", label: "Second draft" };
const artifact: Artifact = { artifact_id: "artifact", project_id: "project", run_id: "run", artifact_type: "scripts", current_version_id: "version-2", updated_at: stamp };
const version: ArtifactVersion = { artifact_id: "artifact", artifact_version_id: "version-1", version: 1, status: "superseded", payload: {}, creation_reason: "generated", created_at: stamp };
const preview: FinalSelectionPreviewResult = {
  preview: { final_selection_preview_id: "preview", project_id: "project", candidate_id: "candidate", approval_request_id: "approval", current_selection_id: null, expected_current_selection_id: null, proposed_selection_no: 1, preview_hash: "hash", status: "pending" },
  approval: { approval_request_id: "approval", project_id: "project", run_id: "run", scope: "final_selection", status: "pending", version: 1, title: "Confirm final draft", reason: "Selection", options: ["select_final"], subject_kind: "script_candidate", subject_ref_id: "candidate", subject_version: 1, subject_snapshot_hash: "hash", requested_at: stamp },
};
const body = "Exact saved script";
const exported: ScriptExport = { export_id: "export", project_id: "project", candidate_id: "candidate", artifact_version_id: "version-1", format: "txt", status: "ready", filename: "script.txt", size_bytes: Buffer.byteLength(body), checksum: createHash("sha256").update(body).digest("hex"), content_type: "text/plain", expires_at: "2026-09-10T00:00:00Z" };
const props = () => ({ project: { project_id: "project" } as Project, candidates: [candidate, other], artifacts: [artifact], selection: null, capabilities: [], onChanged: vi.fn().mockResolvedValue(undefined), onPreview: vi.fn().mockReturnValue(true) });
beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("Unexpected fixture network")));
  vi.stubGlobal("crypto", webcrypto);
  vi.spyOn(api, "getArtifactVersion").mockResolvedValue(version);
  vi.spyOn(api, "previewFinalSelection").mockResolvedValue(preview);
  vi.spyOn(api, "createScriptExport").mockResolvedValue(exported);
  vi.spyOn(api, "downloadScriptExport").mockResolvedValue(new NodeBlob([body]) as Blob);
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  vi.stubGlobal("URL", class extends URL { static createObjectURL = vi.fn(() => "blob:export-fixture"); static revokeObjectURL = vi.fn(); });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const openCandidates = () => act(async () => fireEvent.click(screen.getByRole("button", { name: "候选稿" })));
const openExports = () => act(async () => fireEvent.click(screen.getByRole("button", { name: "导出剧本" })));
const clickFinal = () => act(async () => fireEvent.click(screen.getByRole("button", { name: "设为最终稿" })));
const clickTXT = () => act(async () => fireEvent.click(screen.getByRole("menuitem", { name: "TXT 文本" })));

it("loads the exact historical candidate only on an explicit preview selection", async () => {
  const input = props(); render(<CandidateControls {...input} />);
  expect(api.getArtifactVersion).not.toHaveBeenCalled();
  await openCandidates();
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  expect(api.getArtifactVersion).toHaveBeenCalledWith("version-1");
  expect(input.onPreview).toHaveBeenCalledWith(version);
});

it("does not switch the selected candidate when the workbench declines to discard edits", async () => {
  const input = props(); input.onPreview.mockReturnValue(false);
  vi.mocked(api.getArtifactVersion).mockResolvedValue({ ...version, artifact_version_id: "other-version" });
  render(<CandidateControls {...input} />); await openCandidates();
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /Second draft/ })));
  expect(input.onPreview).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("menuitem", { name: /First draft/ }).className).toBe("selected");
});

it("requires explicit preview of a remotely updated candidate before export or final selection", async () => {
  const input = props(); const view = render(<CandidateControls {...input} />);
  await openCandidates();
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  view.rerender(<CandidateControls {...input} candidates={[{ ...candidate, scripts_artifact_version_id: "version-2" }, other]} />);
  expect(screen.getByRole("alert").textContent).toContain("候选稿已有新版本");
  expect(screen.getByRole("button", { name: "设为最终稿" }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "导出剧本" }).hasAttribute("disabled")).toBe(true);
  await clickFinal(); expect(api.previewFinalSelection).not.toHaveBeenCalled();
  vi.mocked(api.getArtifactVersion).mockResolvedValue({ ...version, artifact_version_id: "version-2" });
  input.onPreview.mockReturnValue(false);
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  expect(screen.getByRole("button", { name: "导出剧本" }).hasAttribute("disabled")).toBe(true);
  input.onPreview.mockReturnValue(true);
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  expect(screen.queryByRole("alert")).toBeNull();
  await clickFinal();
  expect(vi.mocked(api.previewFinalSelection).mock.calls[0][4]).toBe("version-2");
});

it.each(["version", "artifact", "project", "run"])("rejects a candidate preview with mismatched %s", async (field) => {
  const input = props();
  if (field === "project") input.artifacts = [{ ...artifact, project_id: "foreign" }];
  if (field === "run") input.artifacts = [{ ...artifact, run_id: "foreign" }];
  if (field === "artifact") vi.mocked(api.getArtifactVersion).mockResolvedValue({ ...version, artifact_id: "foreign" });
  if (field === "version") vi.mocked(api.getArtifactVersion).mockResolvedValue({ ...version, artifact_version_id: "foreign" });
  render(<CandidateControls {...input} />); await openCandidates();
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  expect(input.onPreview).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toContain("候选稿内容与所选版本不一致");
});

it("retries an unknown final preview with the exact candidate version and original key", async () => {
  const input = props();
  vi.mocked(api.previewFinalSelection).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Preview response lost", 503));
  render(<CandidateControls {...input} />); await openCandidates();
  await act(async () => { const button = screen.getByRole("button", { name: "设为最终稿" }); fireEvent.click(button); fireEvent.click(button); });
  expect(api.previewFinalSelection).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("menuitem", { name: /Second draft/ }).hasAttribute("disabled")).toBe(true);
  await clickFinal();
  expect(api.previewFinalSelection).toHaveBeenCalledTimes(2);
  expect(vi.mocked(api.previewFinalSelection).mock.calls[0]).toEqual(["project", "candidate", null, expect.stringMatching(/^[a-f0-9-]{36}$/i), "version-1"]);
  expect(vi.mocked(api.previewFinalSelection).mock.calls[1]).toEqual(vi.mocked(api.previewFinalSelection).mock.calls[0]);
  expect(input.onChanged).toHaveBeenCalledTimes(2);
});

it("rejects a mismatched final preview receipt and keeps the original command retryable", async () => {
  vi.mocked(api.previewFinalSelection).mockResolvedValueOnce({ ...preview, approval: { ...preview.approval, subject_ref_id: "foreign" } });
  render(<CandidateControls {...props()} />); await openCandidates(); await clickFinal();
  expect(screen.getByRole("alert").textContent).toContain("最终稿预览回执不一致");
  await clickFinal();
  expect(vi.mocked(api.previewFinalSelection).mock.calls[1]).toEqual(vi.mocked(api.previewFinalSelection).mock.calls[0]);
});

it("retains export failure in the open menu and retries the original byte-producing command", async () => {
  const input = props();
  vi.mocked(api.downloadScriptExport).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Download response lost", 503));
  render(<CandidateControls {...input} />); await openExports(); await clickTXT();
  expect(screen.getByRole("alert").textContent).toBe("Download response lost");
  expect(screen.getByRole("menuitem", { name: "DOCX 文档" }).hasAttribute("disabled")).toBe(true);
  await clickTXT();
  await waitFor(() => expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1));
  expect(vi.mocked(api.createScriptExport).mock.calls[1]).toEqual(vi.mocked(api.createScriptExport).mock.calls[0]);
  expect(api.downloadScriptExport).toHaveBeenCalledWith("export", expect.any(AbortSignal));
  const anchor = vi.mocked(HTMLAnchorElement.prototype.click).mock.instances[0];
  expect(anchor.download).toBe("script.txt"); expect(anchor.href).toBe("blob:export-fixture");
});

it.each(["receipt", "size", "checksum"])("does not download mismatched export %s", async (kind) => {
  if (kind === "receipt") vi.mocked(api.createScriptExport).mockResolvedValue({ ...exported, artifact_version_id: "foreign" });
  else vi.mocked(api.downloadScriptExport).mockResolvedValue(new NodeBlob([kind === "size" ? "short" : "Other saved script"]) as Blob);
  render(<CandidateControls {...props()} />); await openExports(); await clickTXT();
  await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
  expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled();
  if (kind === "receipt") expect(api.downloadScriptExport).not.toHaveBeenCalled();
});

it("ignores a late export receipt after candidate version replacement", async () => {
  const input = props(); let resolve!: (value: ScriptExport) => void;
  vi.mocked(api.createScriptExport).mockImplementation(() => new Promise((done) => { resolve = done; }));
  const view = render(<CandidateControls {...input} />); await openExports(); await clickTXT();
  view.rerender(<CandidateControls {...input} candidates={[{ ...candidate, scripts_artifact_version_id: "version-2" }, other]} />);
  await act(async () => resolve(exported));
  expect(api.downloadScriptExport).not.toHaveBeenCalled();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("allows viewer preview while hiding both mutation controls", async () => {
  const input = props(); render(<CandidateControls {...input} readOnly />); await openCandidates();
  expect(screen.queryByRole("button", { name: "设为最终稿" })).toBeNull();
  expect(screen.queryByRole("button", { name: "导出剧本" })).toBeNull();
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  expect(input.onPreview).toHaveBeenCalledWith(version);
});

it("allows explicitly reselecting and exporting a historical final candidate", async () => {
  const input = props();
  render(<CandidateControls {...input} candidates={[{ ...candidate, status: "historical_final" }, other]} selection={{ final_selection_id: "current", candidate_id: other.candidate_id, selection_no: 2, status: "active" }} />);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "最终稿" })));
  await act(async () => fireEvent.click(screen.getByRole("menuitem", { name: /First draft/ })));
  expect(screen.getByRole("button", { name: "导出剧本" }).hasAttribute("disabled")).toBe(false);
  expect(screen.getByRole("button", { name: "设为最终稿" }).hasAttribute("disabled")).toBe(false);
  await clickFinal();
  expect(api.previewFinalSelection).toHaveBeenCalledWith("project", "candidate", "current", expect.any(String), "version-1");
});

it("handles projection refresh failure without losing an accepted preview or throwing an unhandled error", async () => {
  const input = props(); input.onChanged.mockRejectedValue(new ApiError("UNAVAILABLE", "Refresh unavailable", 503));
  render(<CandidateControls {...input} />); await openCandidates(); await clickFinal();
  expect(screen.getByRole("alert").textContent).toBe("Refresh unavailable");
  expect(api.previewFinalSelection).toHaveBeenCalledTimes(1);
  input.onChanged.mockResolvedValue(undefined);
  await clickFinal();
  expect(vi.mocked(api.previewFinalSelection).mock.calls[1]).toEqual(vi.mocked(api.previewFinalSelection).mock.calls[0]);
});
