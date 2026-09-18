import { chromium } from "playwright";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const targetProject = "prj_agent_continuity_smoke";
const now = new Date().toISOString();

const project = {
  project_id: targetProject,
  title: "Agent 连续对话回归",
  version: 1,
  status: "ready",
  primary_conversation_id: "conv_agent_continuity_smoke",
  active_write_run_id: null,
  current_capability_id: null,
  latest_capability_id: "non_novel_to_script",
  latest_run_status: "completed",
  current_focus_artifact_version_id: "av_run_2",
  updated_at: now,
};

const artifacts = [
  {
    artifact_id: "art_run_1",
    project_id: targetProject,
    run_id: "run_1",
    step_run_id: "step_run_1",
    capability_id: "novel_to_script",
    artifact_type: "generic_document",
    scope_key: "document:outline",
    current_version_id: "av_run_1",
    status: "confirmed",
    updated_at: "2026-08-24T10:00:00Z",
  },
  {
    artifact_id: "art_run_2",
    project_id: targetProject,
    run_id: "run_2",
    step_run_id: "step_run_2",
    capability_id: "non_novel_to_script",
    artifact_type: "generic_document",
    scope_key: "document:episode-1",
    current_version_id: "av_run_2",
    status: "draft",
    updated_at: "2026-08-24T11:00:00Z",
  },
];

const messages = [
  { message_id: "msg_1", role: "user", content: "先生成故事大纲", created_at: "2026-08-24T09:59:00Z" },
  { message_id: "msg_2", role: "assistant", content: "大纲已经生成。", created_at: "2026-08-24T10:01:00Z" },
  { message_id: "msg_3", role: "user", content: "再生成第一集", created_at: "2026-08-24T10:59:00Z" },
  { message_id: "msg_4", role: "assistant", content: "第一集已经生成。", created_at: "2026-08-24T11:01:00Z" },
];

const snapshot = {
  project,
  messages,
  artifacts,
  approvals: [],
  capabilities: [],
  artifact_presentations: [{
    artifact_type: "generic_document",
    label: "文档",
    description: "通用 Agent 生成的文档",
    renderer: "document",
    editable: true,
    preferred_fields: ["title", "content"],
    navigation: { order: 1, group_mode: "single", visibility: "user" },
    available_actions: ["manual_edit", "agent_revision", "version_history"],
  }],
  script_candidates: [],
  final_selection: null,
  active_run: null,
  pending_proposed_actions: [],
  proposed_actions: [],
  activities: [
    { event_id: "event_1", event_type: "run.completed", run_id: "run_1", capability_id: "novel_to_script", step_id: null, occurred_at: "2026-08-24T10:01:00Z" },
    { event_id: "event_2", event_type: "run.completed", run_id: "run_2", capability_id: "non_novel_to_script", step_id: null, occurred_at: "2026-08-24T11:01:00Z" },
  ],
  revision_requests: [],
  target_resolutions: [],
};

const versions = {
  av_run_1: { artifact_version_id: "av_run_1", artifact_id: "art_run_1", version: 1, status: "confirmed", payload: { artifact_label: "故事大纲", title: "故事大纲", content: "大纲正文" }, creation_reason: "generated", created_at: "2026-08-24T10:00:00Z" },
  av_run_2: { artifact_version_id: "av_run_2", artifact_id: "art_run_2", version: 1, status: "draft", payload: { artifact_label: "第一集", title: "第一集", content: "第一集正文" }, creation_reason: "generated", created_at: "2026-08-24T11:00:00Z" },
};

const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.setDefaultTimeout(15_000);

await page.route(`**/api/v1/projects/${targetProject}/snapshot`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: snapshot }) }));
await page.route(`**/api/v1/projects/${targetProject}/assets**`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { items: [] } }) }));
await page.route("**/api/v1/artifact-versions/*", (route) => {
  const id = route.request().url().split("/").at(-1);
  return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: versions[id] }) });
});
await page.route(`**/api/v1/projects/${targetProject}/events/stream`, (route) => route.fulfill({ status: 200, contentType: "text/event-stream", body: "" }));

try {
  await page.goto(`${baseURL}/projects/${targetProject}`, { waitUntil: "domcontentloaded" });
  await page.locator(".artifact-workspace").waitFor();
  const composer = page.locator(".composer-input textarea");
  if (!(await composer.isVisible()) || await composer.isDisabled()) throw new Error("project composer is unavailable");
  const timeline = page.locator(".agent-timeline");
  const messageCountBefore = await timeline.locator(".timeline-message").count();
  if (messageCountBefore !== messages.length) throw new Error(`project conversation count = ${messageCountBefore}, want ${messages.length}`);
  const conversationBefore = await timeline.innerText();

  const runSelector = page.locator(".rail-run-selector");
  await runSelector.click();
  const runOptions = page.locator(".rail-run-menu > button");
  const runOptionCount = await runOptions.count();
  if (runOptionCount !== 2) throw new Error(`run option count = ${runOptionCount}, want 2`);
  await runOptions.nth(0).click();
  await page.waitForTimeout(300);

  const messageCountAfter = await timeline.locator(".timeline-message").count();
  const conversationAfter = await timeline.innerText();
  if (messageCountAfter !== messageCountBefore || conversationAfter !== conversationBefore) {
    throw new Error("switching generation records replaced or hid the project conversation");
  }
  const railText = await page.locator(".artifact-list").innerText();
  if (railText.includes("来源清单")) throw new Error("internal source_manifest leaked into the artifact rail");

  const capabilities = await page.request.get("http://127.0.0.1:8850/api/v1/capabilities");
  if (!capabilities.ok()) throw new Error(`capability registry request failed: ${capabilities.status()}`);
  const payload = await capabilities.json();
  const items = payload?.data?.items ?? [];
  if (!items.length || items.some((item) => item.kind !== "domain_workflow" || item.execution_mode !== "stateful_workflow" || item.creates_run !== true)) {
    throw new Error("public capability execution contract is incomplete");
  }

  process.stdout.write(`${JSON.stringify({
    project: targetProject,
    runOptions: runOptionCount,
    messageCount: messageCountAfter,
    conversationPreserved: true,
    internalArtifactsHidden: true,
    composerAvailable: true,
    capabilityExecutionContract: true,
  }, null, 2)}\n`);
} finally {
  await browser.close();
}
