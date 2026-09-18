// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, fileKind, uploadMIME } from "./api";
import type { AgentComposerContext, Approval, AssetSetSnapshot, AvailableAction, Capability, Project, ProposedAction, ScriptCandidate, ScriptSandboxPolicy, SkillInstallation } from "./types";

function ok(data: unknown) {
  return Promise.resolve(new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } }));
}

describe("API contracts", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("binds schema reads to the exact proposed action without losing the requested version", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    await api.getProjectCapability("project", "skill/name", "1.0.0", "action&revision");
    const url = new URL(String(fetchMock.mock.calls[0][0]), "http://fixture.invalid");
    expect(url.pathname).toBe("/api/v1/projects/project/capabilities/skill%2Fname");
    expect(Object.fromEntries(url.searchParams)).toEqual({ version: "1.0.0", proposed_action_id: "action&revision" });
  });

  it.each(["bind", "run", "task"])("keeps the explicit %s key on separate transport calls", async (operation) => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const project = { project_id: "project", primary_conversation_id: "conversation" } as Project;
    const action = { proposed_action_id: "proposal", version: 1, snapshot_hash: "hash", capability_ref: { capability_id: "skill", version: "1.0.0" }, input: { source_type: "story" }, config: { payload: {} }, confirmation_message_id: "confirmation" } as ProposedAction;
    const key = "11111111-1111-4111-8111-111111111111";
    for (let attempt = 0; attempt < 2; attempt++) {
      if (operation === "bind") await api.bindProposedActionInput(action, action.input, key);
      else if (operation === "run") await api.startRun(project, action, key);
      else await api.startAgentTask(project, action, key);
    }
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][1]?.body).toBe(fetchMock.mock.calls[0][1]?.body);
    for (const [, options] of fetchMock.mock.calls) expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(key);
  });

  it("preserves the caller's configuration receipt key across separate retry calls", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ version: 2 }));
    const action = { proposed_action_id: "proposal", version: 1 } as ProposedAction;
    const key = "11111111-1111-4111-8111-111111111111";
    for (let attempt = 0; attempt < 2; attempt++) await api.configureProposedAction(action, { source_type: "novel" }, { config_ref: "creation" }, key);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [url, options] of fetchMock.mock.calls) {
      expect(url).toBe("/api/v1/proposed-actions/proposal/configuration");
      expect(new Headers(options?.headers).get("Idempotency-Key")).toBe(key);
      expect(JSON.parse(options?.body as string)).toEqual({ expected_version: 1, input: { source_type: "novel" }, config: { config_ref: "creation" } });
    }
  });

  it.each(["pause", "resume"] as const)("sends an idempotent background %s command", async (action) => {
    const task = { agent_task_id: "task-target", status: action === "pause" ? "pausing" : "queued" };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok(task));
    const result = await (action === "pause" ? api.pauseAgentTask : api.resumeAgentTask)(task.agent_task_id);
    expect(result).toEqual(task);
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe(`/api/v1/agent-tasks/task-target/${action}`);
    expect(options?.method).toBe("POST");
    expect(new Headers(options?.headers).get("Idempotency-Key")).toMatch(/^[a-f0-9-]{36}$/i);
  });

  it("classifies supported files by extension when the browser MIME is empty", () => {
    expect(fileKind({ name: "第01集.mov", type: "" })).toBe("video");
    expect(fileKind({ name: "人物设定.webp", type: "" })).toBe("image");
    expect(fileKind({ name: "故事大纲.docx", type: "" })).toBe("document");
    expect(fileKind({ name: "report.pdf", type: "application/pdf" })).toBe("document");
    expect(fileKind({ name: "全剧.zip", type: "" })).toBe("archive");
  });

  it.each([["notes.md", "text", "text/markdown"], ["table.csv", "text", "text/csv"], ["data.json", "text", "application/json"], ["notes.docx", "document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"], ["notes.pdf", "document", "application/pdf"]])("uploads %s with the backend's declared kind and MIME", async (name, kind, mime) => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input) => String(input).endsWith("/upload-sessions") ? ok({ items: [{ upload_item_id: "item" }] }) : String(input).endsWith("/content") ? ok({}) : ok({ asset: { asset_id: "asset", kind, display_name: name }, asset_snapshot: { asset_snapshot_id: "snapshot" } }));
    expect(uploadMIME({ name, type: "" })).toBe(mime);
    await api.uploadFiles("p", [new File(["fixture"], name)]);
    const body = JSON.parse(fetchMock.mock.calls[0][1]?.body as string);
    expect(body.items[0]).toMatchObject({ kind, declared_mime_type: mime });
    expect(new Headers(fetchMock.mock.calls[1][1]?.headers).get("Content-Type")).toBe(mime);
  });

  it("keeps a ZIP visible while exposing extracted assets to the Harness", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
      const path = String(input);
      if (path.endsWith("/upload-sessions")) return ok({ upload_session_id: "upload-1", items: [{ upload_item_id: "item-1", client_item_key: "zip", original_filename: "全剧.zip", status: "pending" }] });
      if (path.endsWith("/content")) return ok({ status: "validating" });
      return ok({
        asset: { asset_id: "archive-1", display_name: "全剧.zip", kind: "archive" },
        asset_snapshot: { asset_snapshot_id: "archive-snapshot-1" },
        ignored_entries: ["Thumbs.db"],
        extracted_assets: [{
          asset: { asset_id: "video-1", display_name: "正片/第01集.mp4", kind: "video" },
          asset_snapshot: { asset_snapshot_id: "video-snapshot-1" },
        }],
      });
    });

    const results = await api.uploadFiles("project-1", [new File(["zip"], "全剧.zip", { type: "application/zip" })]);

    expect(results).toEqual([
      expect.objectContaining({ asset_id: "archive-1", display_name: "全剧.zip", kind: "archive", ignored_entries: ["Thumbs.db"] }),
      expect.objectContaining({ asset_id: "video-1", display_name: "正片/第01集.mp4", kind: "video", hidden: true, container_asset_id: "archive-1" }),
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("loads global and project Workspace Projection contracts", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input) => ok(
      String(input).includes("/projects/")
        ? { contract_version: "1.0.0", registry_revision: "sha256:test", snapshot: {}, registries: {} }
        : { contract_version: "1.0.0", revision: "sha256:test", registries: {} },
    ));

    await api.getWorkspaceProjection();
    await api.getProjectWorkspaceProjection("project-1");

    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      "/api/v1/workspace-projection",
      "/api/v1/projects/project-1/workspace-projection",
    ]);
  });

  it("uploads Skill ZIP bytes with filename metadata instead of JSON wrapping", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ skill_installation_id: "skill-1" }));
    const archive = new File(["PK\u0003\u0004"], "story-review.zip", { type: "application/zip" });

    const key = "072756a9-4654-4a20-99ec-03a605a39718";
    await api.installSkill(archive, { scope: "workspace" }, key);

    const [path, init] = fetchMock.mock.calls[0];
    const headers = new Headers(init?.headers);
    expect(path).toBe("/api/v1/skills?scope=workspace");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe(archive);
    expect(headers.get("Content-Type")).toBe("application/zip");
    expect(headers.get("Content-Disposition")).toBe("attachment; filename*=UTF-8''story-review.zip");
    expect(headers.get("Idempotency-Key")).toBe(key);
  });

  it("addresses Skill lifecycle operations by installation and version", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ skill_installation_id: "skill-1" }));
    const archive = new File(["zip"], "story-review-v2.zip", { type: "application/zip" });
    const installation = { skill_installation_id: "skill-1", active_version_id: "version-1", status: "installed", enabled: true, events: [{ skill_installation_event_id: "event-1" }] } as SkillInstallation;
    const key = "072756a9-4654-4a20-99ec-03a605a39718";

    await api.upgradeSkill(installation, archive, key);
    await api.activateSkillVersion(installation, "2.0.0+stable", key);
    await api.setSkillEnabled(installation, false, key);
    await api.uninstallSkill(installation, key);

    expect(fetchMock.mock.calls.map(([input, init]) => [String(input), init?.method])).toEqual([
      ["/api/v1/skills/skill-1/versions", "POST"],
      ["/api/v1/skills/skill-1/versions/2.0.0%2Bstable/activate", "POST"],
      ["/api/v1/skills/skill-1/disable", "POST"],
      ["/api/v1/skills/skill-1", "DELETE"],
    ]);
    expect(JSON.parse(new Headers(fetchMock.mock.calls[0][1]?.headers).get("X-Skill-Expected")!)).toEqual({ active_version_id: "version-1", status: "installed", enabled: true, event_count: 1 });
    for (const [, init] of fetchMock.mock.calls.slice(1)) {
      expect(new Headers(init?.headers).get("Idempotency-Key")).toBe(key);
      expect(JSON.parse(String(init?.body))).toEqual({ expected: { active_version_id: "version-1", status: "installed", enabled: true, event_count: 1 } });
    }
  });

  it("preserves package keys, encoded paths and exact directory snapshots", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const key = "472756a9-4654-4a20-99ec-03a605a39718";
    const target = { scope: "project" as const, project_id: "project/one" };
    const installation = { skill_installation_id: "skill/one", active_version_id: "version-1", status: "installed", enabled: false, events: [{ skill_installation_event_id: "event-1" }] } as SkillInstallation;
    const preview = { skill_installation_id: "skill/one", current_version_id: "version-1", capability_id: "skill/cap", version: "2.0.0", content_hash: "sha256:selected", source_path: "never-sent", status: "available" };
    const file = new File([new Uint8Array([0, 255, 128])], "中文 技能.zip", { type: "application/octet-stream" });
    for (let retry = 0; retry < 2; retry++) {
      await api.installSkill(file, target, key);
      await api.upgradeSkill(installation, file, key);
      await api.installDiscoveredSkill(preview.capability_id, preview.version, preview.content_hash, target, key);
      await api.updateSkillFromDirectory(preview, installation, key);
    }
    for (let i = 0; i < 4; i++) {
      const [path, init] = fetchMock.mock.calls[i], [, retryInit] = fetchMock.mock.calls[i + 4];
      expect(fetchMock.mock.calls[i + 4][0]).toBe(path);
      expect(init?.body).toEqual(retryInit?.body);
      expect(new Headers(init?.headers).get("Idempotency-Key")).toBe(key);
      expect(new Headers(retryInit?.headers).get("Idempotency-Key")).toBe(key);
    }
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/skills/skill%2Fone/versions");
    expect(fetchMock.mock.calls[2][0]).toBe("/api/v1/skills/discovered/skill%2Fcap/install");
    expect(fetchMock.mock.calls[3][0]).toBe("/api/v1/skills/skill%2Fone/directory-update");
    expect(fetchMock.mock.calls[0][1]?.body).toBe(file);
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).get("Content-Disposition")).toBe(`attachment; filename*=UTF-8''${encodeURIComponent(file.name)}`);
    expect(JSON.parse(String(fetchMock.mock.calls[3][1]?.body))).toEqual({ expected: { active_version_id: "version-1", status: "installed", enabled: false, event_count: 1 }, expected_active_version_id: "version-1", version: "2.0.0", content_hash: "sha256:selected" });
  });

  it("reads and updates the workspace script sandbox policy", async () => {
    const policy = {
      workspace_id: "workspace-default",
      enabled: false,
      version: 3,
    } as ScriptSandboxPolicy;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok(policy));

    await api.getScriptSandboxPolicy();
    await api.updateScriptSandboxPolicy(policy, true);

    expect(fetchMock.mock.calls.map(([input, init]) => [String(input), init?.method])).toEqual([
      ["/api/v1/script-sandbox-policy", undefined],
      ["/api/v1/script-sandbox-policy", "PUT"],
    ]);
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({ expected_version: 3, enabled: true });
    expect(new Headers(fetchMock.mock.calls[1][1]?.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("sends explicit capability references as structured data", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ user_message: {}, agent_message: {}, message_context: { attachment_refs: [] } }));
    const capability: Capability = { capability_id: "novel_to_script", label: "小说转剧本", version: "1.0.0" };

    await api.sendMessage("conversation-1", "生成剧本", capability);

    const [, init] = fetchMock.mock.calls[0];
    const body = JSON.parse(String(init?.body));
    expect(body.capability_ref).toEqual({ capability_id: "novel_to_script", version: "1.0.0", selection_mode: "explicit" });
    expect(body.content).toBe("生成剧本");
  });

  it("passes the current response abort signal to the message request", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ user_message: {}, agent_message: {}, message_context: { attachment_refs: [] } }));
    const controller = new AbortController();

    await api.sendMessage("conversation-1", "你好", null, [], null, controller.signal);

    expect(fetchMock.mock.calls[0][1]?.signal).toBe(controller.signal);
  });

  it("reuses a durable idempotency key when resuming a homepage message", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ user_message: {}, agent_message: {}, message_context: { attachment_refs: [] } }));

    await api.sendMessage("conversation-1", "继续首条消息", null, [], null, undefined, "11111111-1111-4111-8111-111111111111");

    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get("Idempotency-Key")).toBe("11111111-1111-4111-8111-111111111111");
  });

  it("preserves the original client ID as well as the request key across message recovery", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ user_message: {}, agent_message: {}, message_context: { attachment_refs: [] } }));
    await api.sendMessage("conversation-1", "Original request", null, [], null, undefined, "11111111-1111-4111-8111-111111111111", "original-client");
    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get("Idempotency-Key")).toBe("11111111-1111-4111-8111-111111111111");
    expect(headers.get("X-Client-Instance-ID")).toBe("original-client");
  });

  it("does not retry an intentionally aborted message request", async () => {
    const controller = new AbortController();
    controller.abort();
    const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValue(
      new DOMException("The operation was aborted.", "AbortError"),
    );

    await expect(api.sendMessage("conversation-1", "停止这轮", null, [], null, controller.signal)).rejects.toMatchObject({ name: "AbortError" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("sends the active view and immutable selection to the Agent Shell", async () => {
    vi.stubGlobal("crypto", {
      randomUUID: vi.fn().mockReturnValue("00000000-0000-4000-8000-000000000001"),
      subtle: { digest: vi.fn().mockResolvedValue(new Uint8Array(32).buffer) },
    });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ user_message: {}, agent_message: {}, message_context: { attachment_refs: [] } }));
    const context = {
      view: {
        project_id: "project-1", run_id: "run-1", capability_id: "novel_to_script", artifact_id: "artifact-1",
        artifact_version_id: "version-1", artifact_type: "script_unit", scope_key: "episode:1", artifact_label: "第 1 集",
      },
      selection: {
        schema_version: "1.0.0", artifact_id: "artifact-1", artifact_type: "script_unit", target_scope: "selection",
        field_path: "script_text", text_range: { start: 0, end: 4, selected_text: "目标台词", before_context: "", after_context: "" },
        display: { artifact_label: "第 1 集", selected_text_summary: "目标台词" },
      },
    } as AgentComposerContext;

    await api.sendMessage("conversation-1", "修改这里", null, [], context);

    const body = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(body.client_context).toMatchObject({ current_artifact_id: "artifact-1", current_artifact_version_id: "version-1", viewed_run_id: "run-1" });
    expect(body.selection_snapshot).toMatchObject({ artifact_version_id: "version-1", snapshot_hash: "0".repeat(64) });
  });

  it("swaps adjacent video episodes in one version request", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const snapshot = { asset_set: { asset_set_id: "set-1", current_version: 7, status: "collecting" } } as AssetSetSnapshot;

    await api.swapVideoEpisodes(snapshot, "asset-a", 2, "asset-b", 1);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0];
    const body = JSON.parse(String(init?.body));
    expect(body.expected_asset_set_version).toBe(7);
    expect(body.changes).toEqual([
      { operation: "set_episode_order", asset_id: "asset-a", episode_order: 2 },
      { operation: "set_episode_order", asset_id: "asset-b", episode_order: 1 },
    ]);
  });

  it("preserves the selected DOCX export format", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ export_id: "export-1" }));
    const candidate = { candidate_id: "candidate-1", scripts_artifact_version_id: "version-1" } as ScriptCandidate;

    await api.createScriptExport(candidate, "docx");

    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(String(init?.body))).toEqual({ format: "docx", artifact_version_id: "version-1" });
  });

  it("confirms final selection through its dedicated command", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({ final_selection_id: "selection-2" }));
    const approval = { subject_ref_id: "candidate-2", subject_snapshot_hash: "preview-hash" } as Approval;

    await api.confirmFinalSelection("project-1", approval, "selection-1");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/projects/project-1/final-script-selections");
    const body = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(body).toEqual({ candidate_id: "candidate-2", preview_hash: "preview-hash", expected_current_selection_id: "selection-1", confirmed: true });
  });

  it("accepts quality review risk through the dedicated review command", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const approval = { subject_ref_id: "review-1", subject_snapshot_hash: "review-hash" } as Approval;

    await api.acceptQualityReviewRisk(approval);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/quality-reviews/review-1/actions");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      expected_review_status: "action_required",
      input_snapshot_hash: "review-hash",
      action: "accept_with_risk",
      ignored_issue_ids: [],
    });
  });

  it("requests checkpoint regeneration through its dedicated command", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const approval = {
      approval_request_id: "approval-1",
      version: 3,
      subject_snapshot_hash: "approval-hash",
    } as Approval;

    await api.requestApprovalRegeneration(approval, "request_ai_revision", "强化开场冲突");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/approvals/approval-1/regeneration-requests");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      action: "request_ai_revision",
      instruction: "强化开场冲突",
      expected_approval_version: 3,
      subject_snapshot_hash: "approval-hash",
    });
  });

  it("submits adaptation selection and creation config atomically", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const approval = {
      approval_request_id: "approval-adaptation",
      version: 2,
      subject_snapshot_hash: "adaptation-hash",
    } as Approval;
    const resolution = {
      adaptation_options_artifact_version_id: "version-options",
      selection: { selected_option_ids: ["option-1"], combined_methods: [], custom_changes: ["替换职业"] },
      creation_config: { target_episode_count: 24, episode_duration_minutes: 2, user_requirements: ["节奏紧凑"] },
    };

    await api.resolveApproval(approval, "select_adaptation_strategy", resolution);

    const body = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(body).toEqual({
      action: "select_adaptation_strategy",
      expected_approval_version: 2,
      subject_snapshot_hash: "adaptation-hash",
      resolution_payload: resolution,
    });
  });

  it("submits the confirmed volume expansion strategy", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const approval = {
      approval_request_id: "approval-volume",
      version: 1,
      subject_snapshot_hash: "volume-hash",
    } as Approval;

    await api.resolveApproval(approval, "approve", { expansion_strategy: "只补因果桥段，不新增主线。" });

    const body = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(body.resolution_payload).toEqual({ expansion_strategy: "只补因果桥段，不新增主线。" });
  });

	it("switches episode execution mode at the run boundary", async () => {
		const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));

		await api.setEpisodeExecutionMode("run-1", "continuous");

		expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/runs/run-1/episode-execution-mode");
		expect(fetchMock.mock.calls[0][1]?.method).toBe("PUT");
		expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ mode: "continuous" });
	});

  it("submits quality revision instructions through the review command", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));
    const approval = { subject_ref_id: "review-2", subject_snapshot_hash: "review-hash-2" } as Approval;

    await api.resolveQualityReviewAction(approval, "confirm_change", "确认主角提前知道真相");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/quality-reviews/review-2/actions");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      expected_review_status: "action_required",
      input_snapshot_hash: "review-hash-2",
      action: "confirm_change",
      instruction: "确认主角提前知道真相",
      ignored_issue_ids: [],
    });
  });

  it("completes a script edit with the exact saved version", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));

    await api.completeScriptEdit("run-1", ["version-7"]);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/runs/run-1/script-edit-completions");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      expected_script_version_ids: ["version-7"],
    });
  });

  it("confirms cancellation with the backend contract", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));

    await api.executeRunAction("run-1", { action_id: "cancel_run", target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null });

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/runs/run-1/cancel");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ confirmation: { confirmed: true } });
  });

	it("confirms continuing with partial batch results", async () => {
		const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() => ok({}));

		await api.executeRunAction("run-1", {
			action_id: "continue_with_partial_results",
			target_id: "step-1",
			target_type: "step_run",
			enabled: true,
		} as AvailableAction);

		expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/steps/step-1/partial-continuations");
		expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ confirmation: { confirmed: true } });
	});

  it("retries an uncertain write with the same idempotency key", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockRejectedValueOnce(new TypeError("network interrupted"))
      .mockImplementationOnce(() => ok({ project_id: "project-1" }));

    await api.createProject("重试作品");

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const firstHeaders = fetchMock.mock.calls[0][1]?.headers as Headers;
    const secondHeaders = fetchMock.mock.calls[1][1]?.headers as Headers;
    expect(firstHeaders.get("Idempotency-Key")).toBeTruthy();
    expect(secondHeaders.get("Idempotency-Key")).toBe(firstHeaders.get("Idempotency-Key"));
  });
});
