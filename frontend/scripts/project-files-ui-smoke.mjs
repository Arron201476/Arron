import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { chromium } from "playwright";

const baseURL = process.env.CONTENT_AGENT_WEB_URL;
if (!baseURL || new URL(baseURL).hostname !== "127.0.0.1" || ["8860", "8880"].includes(new URL(baseURL).port)) {
  throw new Error("Set CONTENT_AGENT_WEB_URL to a separate local UI fixture server.");
}
const outputRoot = fileURLToPath(new URL("../../.tmp/goal-g2-files-ui/", import.meta.url));
const projectID = "prj_files_ui_fixture";
const project = { project_id: projectID, workspace_id: "workspace-fixture", title: "工作文件验收", version: 1, status: "ready", primary_conversation_id: "conv-files-fixture", updated_at: "2026-09-06T00:00:00Z" };
const file = { project_id: projectID, path: "skills/story-checker/SKILL.md", version: 2, content_hash: "fixture", size_bytes: 120, deleted: false, agent_tool_call_id: "call-fixture", created_at: project.updated_at };
const content = "---\nname: story-checker\ndescription: 评审故事大纲中的人物动机和冲突升级。\n---\n\n# 故事评审\n\n1. 读取用户提供的大纲。\n2. 检查人物动机与因果关系。\n3. 按优先级列出修改建议。\n\n<not-executable>仅作为工作文件保存</not-executable>\n";
const snapshot = { project, goal: null, messages: [], artifacts: [], approvals: [], script_candidates: [], final_selection: null, active_run: null, latest_run: null, pending_proposed_actions: [], proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [], agent_tasks: [], agent_turns: [], agent_tool_calls: [] };
const registries = { artifacts: [], navigation: [], tasks: [], approvals: [], interactions: [], composer: [] };
const json = (data) => ({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
await fs.mkdir(outputRoot, { recursive: true });

const draftRoot = "skills/story-checker";
const draft = { project_id: projectID, root_path: draftRoot, snapshot_hash: "fixture-draft-hash", status: "valid", files: [file], diagnostics: [], installations: [], version: `0.0.0+${"abcd1234".repeat(8)}`, execution_mode: "inline", manifest: { name: "story-checker", scope: "project", path: file.path, content_hash: "fixture-content-hash", allow_implicit_invocation: true, interface: {}, dependencies: [], scripts: [] } };
const installRequests = [];
const unhandled = [];
const fixture = createServer(async (request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  let reply;
  if (url.pathname.startsWith("/api/")) {
    if (request.method === "POST" && url.pathname === `/api/v1/projects/${projectID}/skill-drafts/installations`) {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      installRequests.push({ body, idempotencyKey: request.headers["idempotency-key"] });
      const payload = json({ receipt: { receipt_id: "fixture-receipt", project_id: projectID, skill_version_id: "fixture-version" }, installation: { registry_status: "available", enabled: true } });
      response.writeHead(payload.status, { "Content-Type": payload.contentType }).end(payload.body);
      return;
    }
    if (request.method !== "GET") {
      unhandled.push(`${request.method} ${url.pathname}`);
      response.writeHead(405).end();
      return;
    }
    if (url.pathname === "/api/v1/auth/me") reply = json({ kind: "user", user_id: "user-fixture", workspace_id: "workspace-fixture", workspace_name: "本地验收", role: "owner" });
    else if (url.pathname === "/api/v1/skill-management-options") reply = json({ install_scopes: ["project", "user", "workspace"], system_read_only: true });
    else if (url.pathname.endsWith("/skill-drafts/preview")) reply = json(draft);
    else if (url.pathname.endsWith("/workspace-projection")) reply = json({ contract_version: "workspace_projection.v1", registry_revision: "files-fixture", snapshot, registries });
    else if (url.pathname.endsWith("/assets")) reply = json({ items: [] });
    else if (url.pathname.endsWith("/events/stream")) reply = { status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" };
    else if (url.pathname.endsWith("/files")) reply = json([file, { ...file, path: `references/${"long-file-name-".repeat(6)}.txt`, version: 1 }]);
    else if (url.pathname.endsWith("/files/read")) {
      const version = Number(url.searchParams.get("version"));
      const text = version === 1 ? "Previous version" : content;
      reply = json({ file: { ...file, version, path: url.searchParams.get("path") }, content: text, offset: 0, next_offset: [...text].length, truncated: false });
    } else if (url.pathname.endsWith("/files/content")) reply = { status: 200, contentType: "application/octet-stream", headers: { "Content-Disposition": 'attachment; filename="SKILL.md"' }, body: content };
    else {
      unhandled.push(url.pathname);
      reply = { status: 404, contentType: "text/plain", body: "Unhandled fixture API" };
    }
    response.writeHead(reply.status, { "Content-Type": reply.contentType, ...reply.headers }).end(reply.body);
    return;
  }
  try {
    const source = await fetch(new URL(request.url, baseURL));
    response.writeHead(source.status, { "Content-Type": source.headers.get("Content-Type") ?? "application/octet-stream" }).end(Buffer.from(await source.arrayBuffer()));
  } catch { response.writeHead(502).end("UI fixture unavailable"); }
});
fixture.listen(0, "127.0.0.1");
await once(fixture, "listening");
const fixtureURL = `http://127.0.0.1:${fixture.address().port}`;

let browser;
const results = [];
try {
  browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    const context = await browser.newContext({ viewport, acceptDownloads: true });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${fixtureURL}/projects/${projectID}`, { waitUntil: "networkidle" });
    await page.getByRole("button", { name: "工作文件", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "工作文件", exact: true });
    await page.getByRole("button", { name: /skills\/story-checker\/SKILL.md/ }).click();
    await page.getByText("# 故事评审", { exact: false }).waitFor();
    assert.equal(await page.locator("not-executable").count(), 0);
    const metrics = await dialog.evaluate((element) => {
      const box = element.getBoundingClientRect();
      const reader = element.querySelector(".project-file-reader").getBoundingClientRect();
      return { width: innerWidth, scrollWidth: document.documentElement.scrollWidth, left: box.left, right: box.right, bottom: box.bottom, readerHeight: reader.height, readerWidth: reader.width, overflowingText: [...element.querySelectorAll("strong")].some((node) => node.scrollWidth > node.clientWidth + 1) };
    });
    assert(metrics.left >= 0 && metrics.right <= viewport.width && metrics.bottom <= viewport.height);
    assert(metrics.readerWidth > 250 && metrics.readerHeight > 250);
    assert.equal(metrics.scrollWidth, metrics.width);
    assert.equal(metrics.overflowingText, false);
    await page.screenshot({ path: path.join(outputRoot, `files-${viewport.width}.png`), fullPage: true });
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("link", { name: "下载此版本" }).click();
    const download = await downloadPromise;
    assert.equal(download.suggestedFilename(), "SKILL.md");
    assert.equal(await fs.readFile(await download.path(), "utf8"), content);
    await page.getByRole("combobox", { name: "文件版本" }).selectOption("1");
    await page.getByText("Previous version", { exact: true }).waitFor();
    assert.match(await page.getByRole("link", { name: "下载此版本" }).getAttribute("href"), /version=1/);
    assert.equal(await page.getByRole("button", { name: "校验 Skill", exact: true }).count(), 0);
    await page.getByRole("combobox", { name: "文件版本" }).selectOption("2");
    await page.getByRole("button", { name: "校验 Skill", exact: true }).click();
    await page.getByText("校验通过", { exact: true }).waitFor();
    const installCount = installRequests.length;
    assert.match(await page.getByRole("link", { name: "下载 Skill ZIP" }).getAttribute("href"), /snapshot_hash=fixture-draft-hash/);
    const draftMetrics = await page.getByRole("region", { name: "Skill 草稿校验" }).evaluate((element) => ({
      overflow: element.scrollWidth > element.clientWidth + 1,
      clippedText: [...element.querySelectorAll("dd, h3, p")].some((node) => node.scrollWidth > node.clientWidth + 1),
      installButton: element.querySelector(".project-skill-install").getBoundingClientRect().toJSON(),
    }));
    assert.equal(draftMetrics.overflow, false);
    assert.equal(draftMetrics.clippedText, false);
    assert(draftMetrics.installButton.right <= viewport.width);
    await page.screenshot({ path: path.join(outputRoot, `skill-draft-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "确认安装", exact: true }).click();
    await page.getByText("已安装，后续对话可调用。", { exact: true }).waitFor();
    assert.equal(installRequests.length, installCount + 1);
    assert.deepEqual(installRequests.at(-1).body, { root_path: draftRoot, snapshot_hash: draft.snapshot_hash, scope: "project", installation_id: "", expected_active_version_id: "", confirmed: true });
    assert.match(installRequests.at(-1).idempotencyKey, /^[a-f0-9-]{36}$/i);
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "detached" });
    assert.equal(await page.getByRole("button", { name: "工作文件", exact: true }).evaluate((node) => document.activeElement === node), true);
    assert.deepEqual(errors, []);
    assert.deepEqual(unhandled, []);
    results.push({ viewport, metrics, draftMetrics, confirmedSkillInstallation: true, downloaded: true, history: true, escapeAndFocus: true });
    await context.close();
  }
} finally {
  await browser?.close();
  fixture.closeAllConnections();
  await new Promise((resolve) => fixture.close(resolve));
}
await fs.writeFile(path.join(outputRoot, "report.json"), JSON.stringify({ kind: "mocked-api-ui-not-real-agent-acceptance", results }, null, 2));
process.stdout.write(JSON.stringify({ status: "passed", outputRoot, results }, null, 2) + "\n");
