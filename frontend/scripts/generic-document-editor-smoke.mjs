import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";
import { resolve } from "node:path";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const outputDir = resolve("../artifacts/browser-smoke");
const projectID = "prj_generic_document_editor_smoke";
const artifactID = "art_generic_document_smoke";
const versionID = "av_generic_document_smoke_v1";
const now = new Date().toISOString();

await mkdir(outputDir, { recursive: true });

const artifact = {
  artifact_id: artifactID,
  project_id: projectID,
  run_id: "run_agent_shell_smoke",
  step_run_id: "step_generic_document_smoke",
  capability_id: "agent_shell",
  artifact_type: "generic_document",
  scope_key: "document:main",
  current_version_id: versionID,
  status: "draft",
  updated_at: now,
};

const snapshot = {
  project: {
    project_id: projectID,
    title: "通用文档编辑器回归",
    version: 1,
    status: "ready",
    primary_conversation_id: "conv_generic_document_smoke",
    current_focus_artifact_version_id: versionID,
    updated_at: now,
  },
  messages: [],
  artifacts: [artifact],
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
  approvals: [],
  capabilities: [],
  script_candidates: [],
  final_selection: null,
  proposed_actions: [],
  pending_proposed_actions: [],
  activities: [],
  revision_requests: [],
  target_resolutions: [],
  active_run: null,
};

const version = {
  artifact_version_id: versionID,
  artifact_id: artifactID,
  version: 1,
  status: "draft",
  payload: {
    title: "短篇人物设定",
    content: Array.from({ length: 30 }, (_, index) => `第 ${index + 1} 段：这是用于验证长文档编辑体验的正文内容。`).join("\n\n"),
    source_kind: "agent",
  },
  schema_id: "generic_document",
  schema_version: "1.0.0",
  creation_reason: "generated",
  created_at: now,
};

const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
page.setDefaultTimeout(15_000);

await page.route(`**/api/v1/projects/${projectID}/snapshot`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: snapshot }) }));
await page.route(`**/api/v1/projects/${projectID}/assets**`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { items: [] } }) }));
await page.route(`**/api/v1/artifact-versions/${versionID}`, (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: version }) }));
await page.route(`**/api/v1/projects/${projectID}/events/stream`, (route) => route.fulfill({ status: 200, contentType: "text/event-stream", body: "" }));

try {
  await page.goto(`${baseURL}/projects/${projectID}`, { waitUntil: "domcontentloaded" });

  const title = page.getByRole("textbox", { name: "文档标题" });
  const body = page.getByRole("textbox", { name: "文档正文" });
  await body.waitFor();

  const bodyBox = await body.boundingBox();
  if (!bodyBox || bodyBox.height < 600) throw new Error(`document body editor is too short: ${bodyBox?.height ?? 0}px`);
  if (await page.locator(".artifact-field-textarea").count()) throw new Error("generic document still uses artifact-field-textarea");
  if (await page.getByRole("button", { name: "编辑内容" }).count()) throw new Error("generic document still requires a separate edit-mode transition");
  if (!(await title.isVisible())) throw new Error("document title editor is missing");
  if (await page.getByRole("button", { name: "保存新版本" }).count()) throw new Error("save action should stay hidden before edits");
  if (await page.getByRole("button", { name: "撤销修改" }).count()) throw new Error("restore action should stay hidden before edits");

  const currentText = await body.textContent();
  await body.fill(`${currentText}\n\n新增人物关系说明。`);
  const save = page.getByRole("button", { name: "保存新版本" });
  const restore = page.getByRole("button", { name: "撤销修改" });
  await save.waitFor();
  if (await save.isDisabled()) throw new Error("save should become enabled after edits");
  if (await restore.isDisabled()) throw new Error("restore should become enabled after edits");

  const overflow = await page.evaluate(() => ({ clientWidth: document.documentElement.clientWidth, scrollWidth: document.documentElement.scrollWidth }));
  if (overflow.scrollWidth > overflow.clientWidth + 1) throw new Error(`horizontal overflow: ${overflow.scrollWidth} > ${overflow.clientWidth}`);

  await page.screenshot({ path: resolve(outputDir, "generic-document-editor.png"), fullPage: true });
  console.log(JSON.stringify({ bodyHeight: bodyBox.height, titleVisible: true, saveStateVerified: true, horizontalOverflow: false }));
} finally {
  await browser.close();
}
