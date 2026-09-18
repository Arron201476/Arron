import { chromium } from "playwright";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const out = fileURLToPath(new URL("../../artifacts/stage-6-frontend/", import.meta.url));
await fs.mkdir(out, { recursive: true });

const project = {
  project_id: "project-demo",
  title: "穷剑修",
  version: 4,
  status: "active",
  primary_conversation_id: "conversation-demo",
  active_write_run_id: null,
  current_capability_id: "video_reference_creation",
  current_focus_artifact_version_id: "version-3",
  updated_at: new Date().toISOString(),
};
const projects = [
  { ...project, project_id: "p1", title: "凡骨问仙", version: 1, current_capability_id: "novel_to_script", current_focus_artifact_version_id: null },
  { ...project, project_id: "p2", title: "山门风云", current_capability_id: "novel_to_script", status: "waiting_approval" },
  { ...project, project_id: "p3", title: "逆袭计划", current_capability_id: "non_novel_to_script", status: "waiting_approval" },
  project,
  { ...project, project_id: "p5", title: "旧城协议", current_capability_id: null, status: "active" },
];
const messages = [
  { message_id: "m1", role: "user", content: "把这组参考视频整理成完整剧本。", created_at: new Date().toISOString() },
  { message_id: "m2", role: "assistant", content: "已按文件名排序并完成 9 集解析。当前需要确认是否按不完整材料继续。", created_at: new Date().toISOString() },
];
const artifacts = [
  { artifact_id: "a1", artifact_type: "reference_script", status: "confirmed", current_version_id: "v1", updated_at: new Date().toISOString() },
  { artifact_id: "a2", artifact_type: "script_analysis", status: "confirmed", current_version_id: "v2", updated_at: new Date().toISOString() },
  { artifact_id: "a3", artifact_type: "adaptation_brief", status: "draft", current_version_id: "v3", updated_at: new Date().toISOString() },
];
const approvals = [{ approval_request_id: "approval-1", scope: "artifact", status: "pending", version: 2, title: "确认改编方向", reason: "Brief 已生成。确认后才会进入新剧本生成。", options: ["approve", "revise"], subject_kind: "artifact", subject_ref_id: "a3", subject_version: 3, subject_snapshot_hash: "hash" }];
const capabilities = [
  { capability_id: "novel_to_script", label: "小说转剧本", version: "1.0.0", description: "上传小说原文并生成分集剧本" },
  { capability_id: "non_novel_to_script", label: "非小说文本转剧本", version: "1.0.0", description: "从大纲、集纲等文本生成剧本" },
  { capability_id: "video_reference_creation", label: "视频参考创作", version: "1.0.0", description: "解析参考视频并按确认方向生成新剧本" },
];
const candidates = [{ candidate_id: "candidate-1", source_capability_id: "video_reference_creation", scripts_artifact_version_id: "v3", status: "ready", label: "视频参考改编稿 · 版本 1", updated_at: new Date().toISOString() }];

function json(data, status = 200) { return { status, contentType: "application/json", body: JSON.stringify({ data }) }; }

async function mock(page, options = {}) {
  let runStarted = false;
  let runStatus = "running";
  let finalPending = false;
  let finalSelected = false;
  const activeRunSnapshot = () => ({
    run: { run_id: "run-1", capability_id: "novel_to_script", status: runStatus, current_step_run_id: "step-1", config_snapshot: { payload: { target_episode_count: 12, episode_duration_minutes: 2 } } },
    steps: [{ step_run_id: "step-1", run_id: "run-1", step_id: "build_story_bible", status: runStatus === "paused" ? "paused" : "running", attempt_count: 1 }],
    task_items: [{ task_item_id: "task-1", step_run_id: "step-1", item_key: "aggregate:story_bible_aggregate", item_order: 1, status: runStatus === "paused" ? "paused" : "running", attempt_count: 1, failure: null }],
    available_actions: runStatus === "paused"
      ? [{ action_id: "resume_run", target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null }, { action_id: "cancel_run", target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null }]
      : runStatus === "cancelled"
        ? []
        : [{ action_id: "pause_run", target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null }, { action_id: "cancel_run", target_type: "run", target_id: "run-1", enabled: true, disabled_reason: null }],
    current_approval: null,
  });
  const videoSnapshot = (status = "collecting") => ({
    asset_set: { asset_set_id: "video-set-1", current_version: status === "sealed" ? 3 : 2, status },
    version: { asset_set_version_id: status === "sealed" ? "video-set-version-3" : "video-set-version-2", version: status === "sealed" ? 3 : 2, member_count: 1, status, completeness: { recognized_episode_count: 1, unrecognized_count: 0, duplicate_episode_numbers: [], missing_episode_numbers: [], failed_asset_ids: [], order_confirmed: status === "sealed" } },
    members: [{ asset_id: "video-asset-1", episode_order: 1, episode_no: 1, episode_label: "第1集", filename_candidate: { raw: "第01集.mp4" }, included: true }],
  });
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    if (path === "/api/v1/projects" && request.method() === "GET") return route.fulfill(json({ items: projects }));
    if (path === "/api/v1/capabilities") return route.fulfill(json({ items: capabilities }));
    if (path === "/api/v1/projects/project-demo/snapshot") return route.fulfill(json({
      project: runStarted && runStatus !== "cancelled" ? { ...project, status: runStatus, active_write_run_id: "run-1", current_capability_id: "novel_to_script" } : project,
      messages,
      artifacts: options.quality ? [{ artifact_id: "quality-script-1", run_id: "quality-run-1", step_run_id: "quality-script-step", artifact_type: "script_unit", scope_key: "episode:1", status: "confirmed", current_version_id: "quality-script-version-1", updated_at: new Date().toISOString() }] : options.stress ? Array.from({ length: 50 }, (_, index) => ({ artifact_id: `stress-${index + 1}`, run_id: "stress-run-1", artifact_type: "script_unit", scope_key: `episode:${index + 1}`, status: index < 34 ? "confirmed" : "draft", current_version_id: `stress-version-${index + 1}`, updated_at: new Date().toISOString() })) : artifacts,
      approvals: options.quality ? [{ approval_request_id: "quality-approval-1", scope: "quality_review", status: "pending", version: 1, title: "剧本质量审核需要处理", reason: "审核发现会影响后续生产的问题，请确认处理方式。", options: ["ai_revise", "manual_edit", "confirm_change", "accept_with_risk"], subject_kind: "quality_review", subject_ref_id: "quality-review-1", subject_version: 1, subject_snapshot_hash: "quality-hash" }] : finalPending ? [...approvals, { approval_request_id: "final-approval-1", scope: "final_selection", status: "pending", version: 1, title: "确认设为最终稿", reason: "确认后将该候选剧本设为作品当前唯一最终稿。", options: ["select_final"], subject_kind: "script_candidate", subject_ref_id: "candidate-1", subject_version: 1, subject_snapshot_hash: "final-preview-hash" }] : approvals,
      capabilities,
      script_candidates: candidates,
      final_selection: finalSelected ? { final_selection_id: "selection-1", project_id: "project-demo", candidate_id: "candidate-1", status: "active" } : null,
      active_run: runStarted && runStatus !== "cancelled" ? activeRunSnapshot() : null,
      pending_proposed_actions: [],
      activities: [],
      revision_requests: [],
      target_resolutions: [],
    }));
    if (path === "/api/v1/projects/project-demo/assets") return route.fulfill(json({ items: [{ asset_id: "source-asset-1", current_snapshot_id: "source-snapshot-1", kind: "document", display_name: "测试小说.docx", original_filename: "测试小说.docx", status: "available", parse_status: "completed" }] }));
    if (path.endsWith("/events/stream")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" });
    if (path.endsWith("/messages") && request.method() === "GET") return route.fulfill(json({ items: messages }));
    if (path.endsWith("/messages") && request.method() === "POST") {
      const body = request.postDataJSON();
      const capabilityID = body.capability_ref?.capability_id;
      if (!["novel_to_script", "video_reference_creation"].includes(capabilityID) || body.capability_ref?.selection_mode !== "explicit") throw new Error("explicit capability_ref contract failed");
      return route.fulfill(json({
        user_message: { message_id: "flow-user", role: "user", content: body.content, created_at: new Date().toISOString() },
        agent_message: { message_id: "flow-agent", role: "assistant", content: capabilityID === "video_reference_creation" ? "视频批次已接收，请确认解析配置。" : "已识别为小说转剧本，请先确认剧本体量。", created_at: new Date().toISOString() },
        message_context: { attachment_refs: body.attachment_refs },
        proposed_action: { proposed_action_id: "proposal-1", version: 1, snapshot_hash: "proposal-hash", status: "proposed", action_type: "collect_run_configuration", capability_ref: { capability_id: capabilityID, version: "1.0.0" }, input: {}, config: {}, confirmation_message_id: "flow-agent" },
      }));
    }
    if (path.endsWith("/artifacts")) return route.fulfill(json({ items: options.quality ? [{ artifact_id: "quality-script-1", run_id: "quality-run-1", step_run_id: "quality-script-step", artifact_type: "script_unit", scope_key: "episode:1", status: "confirmed", current_version_id: "quality-script-version-1", updated_at: new Date().toISOString() }] : options.stress ? Array.from({ length: 50 }, (_, index) => ({ artifact_id: `stress-${index + 1}`, artifact_type: "script_unit", status: index < 34 ? "confirmed" : "draft", current_version_id: `stress-version-${index + 1}`, updated_at: new Date().toISOString() })) : artifacts }));
    if (path.includes("/artifact-versions/")) return route.fulfill(json(options.quality ? { artifact_version_id: "quality-script-version-1", artifact_id: "quality-script-1", version: 1, status: "confirmed", creation_reason: "generated", created_at: new Date().toISOString(), payload: { script: "第一集剧本正文" } } : { artifact_version_id: "v3", artifact_id: "a3", version: 3, status: "draft", creation_reason: "generated", created_at: new Date().toISOString(), payload: { content: "改编目标：保留主角以弱胜强的核心结构。\n\n新元素：将宗门试炼替换为城防危机，并强化师徒关系。\n\n改编边界：不照搬原剧台词、角色名与标志性桥段。" } }));
    if (path.endsWith("/approvals")) return route.fulfill(json({ items: options.quality ? [{ approval_request_id: "quality-approval-1", scope: "quality_review", status: "pending", version: 1, title: "剧本质量审核需要处理", reason: "审核发现会影响后续生产的问题，请确认处理方式。", options: ["ai_revise", "manual_edit", "confirm_change", "accept_with_risk"], subject_kind: "quality_review", subject_ref_id: "quality-review-1", subject_version: 1, subject_snapshot_hash: "quality-hash" }] : finalPending ? [...approvals, { approval_request_id: "final-approval-1", scope: "final_selection", status: "pending", version: 1, title: "确认设为最终稿", reason: "确认后将该候选剧本设为作品当前唯一最终稿。", options: ["select_final"], subject_kind: "script_candidate", subject_ref_id: "candidate-1", subject_version: 1, subject_snapshot_hash: "final-preview-hash" }] : approvals }));
    if (path.endsWith("/quality-reviews/quality-review-1")) return route.fulfill(json({ quality_review_id: "quality-review-1", project_id: "project-demo", run_id: "quality-run-1", step_run_id: "quality-review-step", input_snapshot_hash: "quality-hash", status: "action_required", scope: "full_script", review_version: 1, issue_counts: { blocker: 1, high: 0, medium: 0, low: 0 }, recommended_route: "user", affected_episode_nos: [1], result: { issues: [{ issue_id: "QR-1", severity: "blocker", episode_nos: [1], evidence: "第1集场次1-3：主角突然知道师父身份。", impact: "人物知情范围与前文冲突。", revision_target: "确认是否采用该故事变更。" }] } }));
    if (path.endsWith("/script-candidates")) return route.fulfill(json({ items: candidates }));
    if (path.endsWith("/final-script-selection")) return finalSelected ? route.fulfill(json({ final_selection_id: "selection-1", project_id: "project-demo", candidate_id: "candidate-1", approval_request_id: "final-approval-1", selection_no: 1, status: "active", selected_at: new Date().toISOString(), replaced_selection_id: null })) : route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { code: "FINAL_SELECTION_NOT_FOUND", message: "尚未选择最终稿。" } }) });
    if (path.endsWith("/final-script-selection-previews") && request.method() === "POST") { finalPending = true; return route.fulfill(json({ preview: { preview_hash: "final-preview-hash" } }), 201); }
    if (path.endsWith("/final-script-selections") && request.method() === "POST") { const body = request.postDataJSON(); if (body.preview_hash !== "final-preview-hash" || body.candidate_id !== "candidate-1" || !body.confirmed) throw new Error("final selection confirm contract failed"); finalPending = false; finalSelected = true; return route.fulfill(json({ final_selection_id: "selection-1", candidate_id: "candidate-1" }), 201); }
    if (path.includes("/proposed-actions/proposal-1/input-binding")) {
      const body = request.postDataJSON();
      return route.fulfill(json({ proposed_action_id: "proposal-1", version: 1, snapshot_hash: "bound-hash", status: "proposed", action_type: "collect_run_configuration", capability_ref: { capability_id: "video_reference_creation", version: "1.0.0" }, input: body.input, config: {}, confirmation_message_id: "flow-agent" }));
    }
    if (path.includes("/proposed-actions/proposal-1/configuration")) {
      const body = request.postDataJSON();
      const video = body.config?.config_ref === "extraction";
      if (!video && (body.config?.payload?.target_episode_count !== 12 || body.config?.payload?.episode_duration_minutes !== 2)) throw new Error("run configuration contract failed");
      if (video && body.config?.payload?.fidelity_level !== "high") throw new Error("video extraction config contract failed");
      return route.fulfill(json({ proposed_action_id: "proposal-1", version: 2, snapshot_hash: "configured-hash", status: "proposed", action_type: "start_run", capability_ref: { capability_id: video ? "video_reference_creation" : "novel_to_script", version: "1.0.0" }, input: body.input, config: body.config, confirmation_message_id: "flow-agent" }));
    }
    if (path.endsWith("/upload-sessions") && request.method() === "POST") return route.fulfill(json({ upload_session_id: "upload-1", items: [{ upload_item_id: "upload-item-1", client_item_key: "video-1", original_filename: "第01集.mp4", status: "pending" }] }));
    if (path.endsWith("/upload-items/upload-item-1/content") && request.method() === "PUT") return route.fulfill(json({ accepted: true }));
    if (path.endsWith("/upload-items/upload-item-1/complete")) return route.fulfill(json({ asset: { asset_id: "video-asset-1", display_name: "第01集.mp4", kind: "video" }, asset_snapshot: { asset_snapshot_id: "video-snapshot-1" } }));
    if (path.endsWith("/asset-sets") && request.method() === "POST") return route.fulfill(json({ ...videoSnapshot(), asset_set: { asset_set_id: "video-set-1", current_version: 1, status: "collecting" }, version: { ...videoSnapshot().version, asset_set_version_id: "video-set-version-1", version: 1, member_count: 0 }, members: [] }));
    if (path.endsWith("/asset-sets/video-set-1/versions")) return route.fulfill(json(videoSnapshot()));
    if (path.endsWith("/asset-sets/video-set-1/seal")) return route.fulfill(json(videoSnapshot("sealed")));
    if (path.endsWith("/runs") && request.method() === "POST") {
      const body = request.postDataJSON();
      if (!body.confirmation?.confirmed || body.confirmation?.action_version !== 2) throw new Error("run confirmation contract failed");
      runStarted = true;
      runStatus = "running";
      return route.fulfill(json(activeRunSnapshot()));
    }
    if (path.endsWith("/runs/run-1/snapshot")) return route.fulfill(json(activeRunSnapshot()));
    if (path.endsWith("/runs/run-1/pause") && request.method() === "POST") { runStatus = "paused"; return route.fulfill(json(activeRunSnapshot())); }
    if (path.endsWith("/runs/run-1/resume") && request.method() === "POST") { runStatus = "running"; return route.fulfill(json(activeRunSnapshot())); }
    if (path.endsWith("/runs/run-1/cancel") && request.method() === "POST") { runStatus = "cancelled"; return route.fulfill(json(activeRunSnapshot())); }
    if (path.endsWith("/steps/step-1/tasks")) return route.fulfill(json({ items: [{ task_item_id: "task-1", step_run_id: "step-1", item_key: "episode-1", item_order: 1, status: "completed", attempt_count: 1, failure: null }, { task_item_id: "task-2", step_run_id: "step-1", item_key: "episode-2", item_order: 2, status: "running", attempt_count: 1, failure: null }, { task_item_id: "task-3", step_run_id: "step-1", item_key: "episode-3", item_order: 3, status: "failed", attempt_count: 1, failure: "provider timeout" }] }));
    if (path.includes("delete-previews")) return route.fulfill(json({ snapshot_hash: "preview-hash", source_files_will_be_deleted: true, impact: { message_count: 12, asset_count: 9, artifact_count: 3, candidate_count: 0, has_final_selection: false } }, 201));
    if (/\/api\/v1\/projects\/[^/]+$/.test(path)) return route.fulfill(json(runStarted ? { ...project, active_write_run_id: "run-1" } : project));
    return route.fulfill(json({}));
  });
}

const browser = await chromium.launch({ channel: "msedge", headless: true });
const report = [];
for (const viewport of [{ width: 1280, height: 800 }, { width: 1440, height: 900 }, { width: 1920, height: 1080 }]) {
  const context = await browser.newContext({ viewport });
  const page = await context.newPage();
  await mock(page);
  await page.goto("http://127.0.0.1:8860/", { waitUntil: "networkidle" });
  await page.screenshot({ path: path.join(out, `entry-${viewport.width}.png`), fullPage: true });
  const entry = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth, rows: document.querySelectorAll(".project-row:not(.table-head)").length }));
  if (viewport.width === 1440) {
    await page.locator('.project-row:not(.table-head)').first().locator('.icon-button').click();
    if (!(await page.locator('.anchor-menu').isVisible())) throw new Error("project menu did not open");
    if (await page.locator('.modal-layer').count()) throw new Error("project menu incorrectly opened a modal");
    await page.screenshot({ path: path.join(out, "entry-menu-1440.png"), fullPage: true });
    await page.locator('.anchor-menu .danger').click();
    await page.getByRole('button', { name: '查看删除影响' }).click();
    await page.screenshot({ path: path.join(out, "delete-impact-1440.png"), fullPage: true });
    await page.keyboard.press("Escape");
    if (await page.locator(".modal-layer").count()) throw new Error("Escape did not close modal dialog");
  }
  await page.goto("http://127.0.0.1:8860/projects/project-demo", { waitUntil: "networkidle" });
  const metrics = await page.evaluate(() => {
    const composer = document.querySelector(".composer").getBoundingClientRect();
    const panel = document.querySelector(".agent-panel").getBoundingClientRect();
    const send = document.querySelector(".send-action").getBoundingClientRect();
    return { scrollWidth: document.documentElement.scrollWidth, innerWidth, composerHeight: composer.height, composerBottomGap: Math.round(panel.bottom - composer.bottom), panelWidth: panel.width, sendWidth: send.width, sendHeight: send.height };
  });
  if (metrics.scrollWidth > metrics.innerWidth) throw new Error(`${viewport.width}: horizontal overflow ${metrics.scrollWidth}`);
  if (metrics.composerHeight !== 160 || metrics.composerBottomGap !== 0) throw new Error(`${viewport.width}: composer contract failed`);
  if (metrics.sendWidth !== metrics.sendHeight) throw new Error(`${viewport.width}: send button not square`);
  await page.screenshot({ path: path.join(out, `workbench-${viewport.width}.png`), fullPage: true });
  if (viewport.width === 1440) {
    await page.getByRole("button", { name: "Skill", exact: true }).click();
    if ((await page.locator(".capability-menu button").count()) !== 3) throw new Error("capability menu mismatch");
    await page.screenshot({ path: path.join(out, "workbench-capability-1440.png"), fullPage: true });
    await page.locator(".capability-menu button").first().click();
    if ((await page.getByLabel("给 Agent 的消息").inputValue()) !== "请将我提供的小说改编成短剧剧本。") throw new Error("novel Skill default prompt mismatch");
    await page.getByLabel("给 Agent 的消息").fill("把这本小说生成 12 集短剧剧本");
    await page.getByRole("button", { name: "发送" }).click();
    await page.getByRole("button", { name: "保存配置" }).click();
    await page.getByRole("button", { name: "确认并开始" }).click();
    await page.getByText("生成中", { exact: true }).first().waitFor();
    await page.getByRole("button", { name: "暂停任务" }).click();
    await page.getByRole("button", { name: "继续任务" }).waitFor();
    await page.screenshot({ path: path.join(out, "workbench-run-paused-1440.png"), fullPage: true });
    await page.getByRole("button", { name: "继续任务" }).click();
    await page.getByRole("button", { name: "暂停任务" }).waitFor();
    await page.getByRole("button", { name: "更多运行操作" }).click();
    await page.screenshot({ path: path.join(out, "workbench-run-more-1440.png"), fullPage: true });
    await page.getByRole("menuitem", { name: "结束本次运行" }).click();
    await page.getByRole("heading", { name: "结束本次运行" }).waitFor();
    await page.screenshot({ path: path.join(out, "workbench-run-end-dialog-1440.png"), fullPage: true });
    await page.getByRole("button", { name: "保留运行" }).click();
    await page.screenshot({ path: path.join(out, "workbench-run-started-1440.png"), fullPage: true });
    await page.getByRole("button", { name: "候选稿" }).click();
    await page.getByRole("button", { name: "设为最终稿" }).click();
    await page.getByRole("button", { name: "确认设为最终稿" }).click();
    await page.getByRole("button", { name: /最终稿/ }).waitFor();
    await page.getByRole("button", { name: /导出剧本/ }).click();
    if ((await page.locator(".export-menu button").count()) !== 2) throw new Error("export format menu mismatch");
    await page.screenshot({ path: path.join(out, "workbench-export-1440.png"), fullPage: true });
  }
  report.push({ viewport, entry, metrics });
  await context.close();
}
const videoContext = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const videoPage = await videoContext.newPage();
await mock(videoPage);
await videoPage.goto("http://127.0.0.1:8860/projects/project-demo", { waitUntil: "networkidle" });
await videoPage.getByRole("button", { name: "Skill", exact: true }).click();
await videoPage.locator(".capability-menu button").filter({ hasText: "视频参考创作" }).click();
if ((await videoPage.getByLabel("给 Agent 的消息").inputValue()) !== "请解析我提供的参考视频，并基于解析结果改编生成新的短剧剧本。") throw new Error("video Skill default prompt mismatch");
await videoPage.getByLabel("添加文件").click();
await videoPage.locator('input[type="file"]').setInputFiles({ name: "第01集.mp4", mimeType: "video/mp4", buffer: Buffer.from("video-fixture") });
await videoPage.getByRole("heading", { name: "确认视频批次" }).waitFor();
await videoPage.getByRole("button", { name: "确认已上传完成" }).click();
await videoPage.getByText("视频批次 · 1 集 · 已确认").waitFor();
await videoPage.getByLabel("给 Agent 的消息").fill("逐字提取这一集的剧情概要和剧本");
await videoPage.getByRole("button", { name: "发送" }).click();
await videoPage.getByRole("button", { name: "确认并开始" }).click();
await videoPage.getByText("生成中", { exact: true }).first().waitFor();
await videoPage.screenshot({ path: path.join(out, "workbench-video-run-started-1440.png"), fullPage: true });
await videoContext.close();
const stressContext = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const stressPage = await stressContext.newPage();
await mock(stressPage, { stress: true });
await stressPage.goto("http://127.0.0.1:8860/projects/project-demo", { waitUntil: "networkidle" });
if ((await stressPage.locator(".artifact-episode-list button").count()) === 0) {
  await stressPage.locator(".artifact-group-button").filter({ hasText: "单集剧本" }).click();
}
if ((await stressPage.locator(".artifact-episode-list button").count()) !== 50) throw new Error("50-item stress fixture failed");
await stressPage.screenshot({ path: path.join(out, "workbench-50-items-1440.png"), fullPage: true });
await stressContext.close();
const qualityContext = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const qualityPage = await qualityContext.newPage();
await mock(qualityPage, { quality: true });
await qualityPage.goto("http://127.0.0.1:8860/projects/project-demo", { waitUntil: "networkidle" });
await qualityPage.locator(".quality-summary").waitFor();
if (await qualityPage.getByRole("button", { name: "确认保留风险" }).count()) throw new Error("blocker quality review exposed risk override");
if (await qualityPage.getByRole("button", { name: "让 Agent 返修" }).count()) throw new Error("user-routed quality review exposed AI revision");
if (!(await qualityPage.getByRole("button", { name: "手动修改" }).isVisible()) || !(await qualityPage.getByRole("button", { name: "确认修改" }).isVisible())) throw new Error("quality review action matrix mismatch");
await qualityPage.screenshot({ path: path.join(out, "workbench-quality-blocker-1440.png"), fullPage: true });
await qualityContext.close();
await browser.close();
await fs.writeFile(path.join(out, "visual-qa-report.json"), JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
