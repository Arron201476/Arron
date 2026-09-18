import { chromium } from "playwright";
import { randomUUID } from "node:crypto";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const apiURL = process.env.CONTENT_AGENT_API_URL ?? "http://127.0.0.1:8850";
const browser = await chromium.launch({ channel: "msedge", headless: true });
const created = [];

async function api(path, options = {}) {
	const write = options.method && options.method !== "GET";
  const response = await fetch(`${apiURL}${path}`, {
    ...options,
    headers: { "Content-Type": "application/json", ...(write ? { "Idempotency-Key": randomUUID() } : {}), ...(options.headers ?? {}) },
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(`${options.method ?? "GET"} ${path}: ${response.status} ${JSON.stringify(payload)}`);
  return payload.data;
}

async function createProject(label) {
  const project = await api("/api/v1/projects", {
    method: "POST",
    body: JSON.stringify({ title: `Registry smoke ${label} ${Date.now()}` }),
  });
  created.push(project.project_id);
  return project;
}

async function send(project, content, capability = null) {
  return api(`/api/v1/conversations/${project.primary_conversation_id}/messages`, {
    method: "POST",
    body: JSON.stringify({
      content,
      display_content: content,
      capability_ref: capability,
      attachment_refs: [],
      client_context: { locale: "zh-CN", timezone: "Asia/Shanghai", surface: "web", current_view: "conversation" },
    }),
  });
}

async function removeProject(projectID) {
  const preview = await api(`/api/v1/projects/${projectID}/delete-previews`, { method: "POST", body: "{}" });
  await api(`/api/v1/projects/${projectID}/delete-confirmations`, {
    method: "POST",
    body: JSON.stringify({ preview_hash: preview.snapshot_hash, confirmed: true }),
  });
}

try {
  const catalog = (await api("/api/v1/capabilities")).items;
  const byID = new Map(catalog.map((item) => [item.capability_id, item]));
  const modes = [
    ["novel_to_script", "小说转剧本", "请将小说转成剧本"],
    ["non_novel_to_script", "非小说文本转剧本", "请将故事大纲转成剧本"],
    ["video_reference_creation", "视频参考创作", "请解析视频并还原剧本"],
  ];
  const report = { skills: {}, generic: null };
  for (const [id, label, request] of modes) {
    const capability = byID.get(id);
    if (!capability) throw new Error(`missing capability ${id}`);
    const project = await createProject(id);
    const exchange = await send(project, request, {
      capability_id: id,
      version: capability.version,
      selection_mode: "explicit",
    });
    if (exchange.agent_decision.decision.intent !== "propose_capability") throw new Error(`${id}: wrong intent`);
    if (exchange.proposed_action?.action_type !== "collect_run_configuration") throw new Error(`${id}: configuration action missing`);
    if (exchange.run_ref) throw new Error(`${id}: run created before confirmation`);
    report.skills[id] = { label, intent: "propose_capability", createsRunBeforeConfirmation: false };
  }

  const genericProject = await createProject("generic");
  const genericExchange = await send(genericProject, "请创建并保存一份通用文档，标题为角色备忘录，正文写：林舟是一名谨慎的剑修。不要调用任何 Skill。");
  if (genericExchange.agent_decision.decision.intent !== "create_artifact") throw new Error(`generic: wrong intent ${genericExchange.agent_decision.decision.intent}`);
  if (!genericExchange.generic_artifact || genericExchange.generic_artifact.artifact_type !== "generic_document") throw new Error("generic: persisted artifact missing");
  const snapshot = await api(`/api/v1/projects/${genericProject.project_id}/snapshot`);
  if (snapshot.project.active_write_run_id) throw new Error("generic: unexpectedly occupied active Skill run");
  const presentation = snapshot.artifact_presentations.find((item) => item.artifact_type === "generic_document");
  if (!presentation || presentation.renderer !== "document") throw new Error("generic: presentation registry missing");

  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(`${baseURL}/projects/${genericProject.project_id}`, { waitUntil: "domcontentloaded" });
  await page.locator(".artifact-workspace").waitFor({ timeout: 15_000 });
  await page.getByText("文档", { exact: true }).first().waitFor();
  await page.locator(".artifact-document").waitFor();
  const shellLabel = await page.locator(".rail-run-selector strong").innerText();
  if (shellLabel !== "通用 Agent") throw new Error(`generic: rail label is ${shellLabel}`);
  report.generic = { intent: "create_artifact", artifactType: "generic_document", renderer: "document", activeSkillRun: false, railLabel: shellLabel };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
} finally {
  await browser.close();
  for (const projectID of created.reverse()) {
    try { await removeProject(projectID); } catch (error) { process.stderr.write(`cleanup ${projectID}: ${error}\n`); }
  }
}
