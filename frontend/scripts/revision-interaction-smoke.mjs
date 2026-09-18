import { chromium } from "playwright";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8960";
const outputDir = resolve("../design/audits/2026-08-11-revision-interaction");
await mkdir(outputDir, { recursive: true });

const now = new Date().toISOString();
const projectID = "prj_revision_ui";
const artifact = {
  artifact_id: "art_script_12", project_id: projectID, run_id: "run_active",
  step_run_id: "step_script", capability_id: "novel_to_script",
  artifact_type: "script_unit", scope_key: "episode:12",
  current_version_id: "av_script_12_v1", status: "draft", updated_at: now,
};
const resolutionAmbiguous = {
  target_resolution_id: "tr_ambiguous", project_id: projectID, conversation_id: "conv_ui",
  request_message_id: "msg_revision_1", status: "ambiguous", source: "semantic_reference",
  display: {}, created_at: now,
  candidates: [
    { candidate_id: "tc_1", artifact_id: "art_script_11", artifact_version_id: "av_11", artifact_type: "script_unit", scope_key: "episode:11", entity: {}, display: { artifact_label: "单集剧本", location_label: "第 11 集" }, score: 0.8 },
    { candidate_id: "tc_2", artifact_id: artifact.artifact_id, artifact_version_id: artifact.current_version_id, artifact_type: "script_unit", scope_key: "episode:12", entity: {}, display: { artifact_label: "单集剧本", location_label: "第 12 集" }, score: 0.76 },
  ],
};
const resolutionProposed = {
  target_resolution_id: "tr_proposed", project_id: projectID, conversation_id: "conv_ui",
  request_message_id: "msg_revision_2", status: "resolved", source: "explicit_selection",
  artifact_id: artifact.artifact_id, artifact_version_id: artifact.current_version_id,
  artifact_type: artifact.artifact_type, scope_key: artifact.scope_key,
  display: { artifact_label: "单集剧本", location_label: "第 12 集" }, candidates: [], created_at: now,
};
const snapshot = {
  project: { project_id: projectID, title: "定位与修改交互回归", version: 1, status: "running", primary_conversation_id: "conv_ui", active_write_run_id: "run_active", current_capability_id: "novel_to_script", latest_capability_id: "novel_to_script", latest_run_status: "running", current_focus_artifact_version_id: artifact.current_version_id, updated_at: now },
  messages: [
    { message_id: "msg_revision_1", role: "user", content: "把后面冲突改得更强", created_at: now },
    { message_id: "msg_agent_1", role: "assistant", content: "找到了多个可能位置，请确认。", created_at: now },
  ],
  artifacts: [artifact], approvals: [],
  capabilities: [{ capability_id: "novel_to_script", label: "小说转剧本", version: "1.4.0", description: "将小说改编为短剧剧本" }],
  script_candidates: [], final_selection: null, pending_proposed_actions: [], activities: [],
  active_run: { run: { run_id: "run_active", project_id: projectID, status: "running", capability_id: "novel_to_script" }, steps: [], artifacts: [artifact], approvals: [], available_actions: [{ action_id: "pause_run", label: "暂停", enabled: true }, { action_id: "cancel_run", label: "结束", enabled: true }], task_items: [] },
  target_resolutions: [resolutionAmbiguous, resolutionProposed],
  revision_requests: [
    { revision_request_id: "rr_ambiguous", project_id: projectID, conversation_id: "conv_ui", request_message_id: "msg_revision_1", target_resolution_id: resolutionAmbiguous.target_resolution_id, instruction: "把后面冲突改得更强", operation: "revise", status: "waiting_target_confirmation", execution_policy: "safe_checkpoint", version: 1, created_at: now, updated_at: now },
    { revision_request_id: "rr_proposed", project_id: projectID, conversation_id: "conv_ui", request_message_id: "msg_revision_2", target_resolution_id: resolutionProposed.target_resolution_id, artifact_id: artifact.artifact_id, base_artifact_version_id: artifact.current_version_id, instruction: "把这句台词改得更有压迫感", operation: "revise", status: "proposed", execution_policy: "safe_checkpoint", version: 3, proposal_payload: { script_text: "林川（压低声音）：你再往前一步，今天就别想走出这里。" }, proposal_summary: "增强主角台词的压迫感。", created_at: now, updated_at: now },
  ],
};

const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
page.setDefaultTimeout(15_000);
await page.route(`**/api/v1/projects/${projectID}/snapshot`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: snapshot }) }));
await page.route("**/api/v1/artifact-versions/av_script_12_v1", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { artifact_version_id: "av_script_12_v1", artifact_id: artifact.artifact_id, version: 1, status: "draft", payload: { script_text: "林川：你别再往前了。" }, schema_id: "script_unit", schema_version: "1.0.0", creation_reason: "generated", created_at: now } }) }));
await page.route(`**/api/v1/projects/${projectID}/events/stream`, (route) => route.fulfill({ status: 200, contentType: "text/event-stream", body: "" }));

const report = {};
try {
  await page.goto(`${baseURL}/projects/${projectID}`, { waitUntil: "domcontentloaded" });
  await page.locator(".artifact-workspace").waitFor();
  const composer = page.getByRole("textbox", { name: "给 Agent 的消息" });
  if (!(await composer.isVisible()) || await composer.isDisabled()) throw new Error("composer is unavailable during an active run");
  if (!(await page.getByRole("button", { name: "暂停运行" }).isVisible())) throw new Error("pause control is not separate from send");
  if (!(await page.getByRole("button", { name: "发送" }).isVisible())) throw new Error("send control is missing during an active run");
  await page.getByText("你指的是哪一项？").waitFor();
  if (await page.locator(".target-options > button").count() !== 2) throw new Error("target candidate options are incomplete");
  await page.getByText("修改稿已生成").waitFor();
  await page.getByRole("button", { name: "查看修改稿" }).click();
  await page.getByText("修改预览，不会自动覆盖当前版本").waitFor();
  if (!(await page.getByText("你再往前一步，今天就别想走出这里。").isVisible())) throw new Error("proposal payload is not visible in artifact workspace");
  report.runningComposerAvailable = true;
  report.separateRunControls = true;
  report.targetCandidates = 2;
  report.proposalPreview = true;
  await page.screenshot({ path: resolve(outputDir, "revision-interaction.png"), fullPage: true });
  await writeFile(resolve(outputDir, "report.json"), JSON.stringify(report, null, 2));
} finally {
  await Promise.race([browser.close(), new Promise((resolveClose) => setTimeout(resolveClose, 2_000))]);
}

console.log(JSON.stringify(report));
process.exit(0);
