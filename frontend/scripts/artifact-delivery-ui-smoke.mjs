import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium, expect } from "playwright/test";

const mcpOutputs = process.argv.includes("--mcp-outputs");
const includeOutputs = mcpOutputs || process.argv.includes("--outputs");
const outputRoot = fileURLToPath(new URL(mcpOutputs ? "../../.tmp/goal-g413-mcp-output-ui/" : includeOutputs ? "../../.tmp/goal-g2-output-delivery-ui/" : "../../.tmp/goal-g2-artifact-delivery-ui/", import.meta.url));
const frontendRoot = fileURLToPath(new URL("../", import.meta.url));
const projectID = "prj_artifact_delivery_fixture";
const artifactID = "art_delivery_fixture";
const now = "2026-09-06T00:00:00Z";
const title = `下载验收 ${"long-document-name-".repeat(12)}`;
const versions = [1, 2].map((version) => ({ artifact_version_id: `av_delivery_fixture_${version}`, artifact_id: artifactID, version, status: version === 2 ? "confirmed" : "superseded", payload: { title, content_markdown: `# 精确版本 ${version}\n\n已保存的正文 <&>\n` }, creation_reason: "manual_edit", created_at: now }));
const artifact = { artifact_id: artifactID, artifact_type: "generic_document", scope_key: `artifact:${artifactID}`, title: "下载验收", status: "confirmed", current_version_id: versions[1].artifact_version_id, updated_at: now };
const project = { project_id: projectID, workspace_id: "workspace-fixture", title: "普通产物下载验收", version: 1, status: "ready", primary_conversation_id: "conv-delivery-fixture", current_focus_artifact_version_id: artifact.current_version_id, updated_at: now };
const snapshot = { project, goal: null, messages: [], artifacts: [artifact], approvals: [], script_candidates: [], final_selection: null, active_run: null, latest_run: null, pending_proposed_actions: [], proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [], agent_tasks: [], agent_turns: [], agent_tool_calls: [] };
const outputAsset = { asset_id: "ast_output_fixture", project_id: projectID, current_snapshot_id: "ass_output_fixture", kind: "text", original_filename: `计算结果-${"long-name-".repeat(10)}.csv`, display_name: "计算结果.csv", size_bytes: 12, status: "available", parse_status: "completed", source_type: "hosted_tool", metadata: { agent_tool_call_id: "call_output_fixture", sdk_tool_call_id: "sdk_output_fixture" } };
const outputAssets = [outputAsset, { ...outputAsset, asset_id: "ast_deleted_fixture", display_name: "已删除.csv", original_filename: "已删除.csv", status: "deleted" }, { ...outputAsset, asset_id: "ast_archive_fixture", display_name: "output.bin.zip", original_filename: "output.bin.zip", kind: "archive" }];
if (mcpOutputs) for (const asset of outputAssets) asset.source_type = "mcp_tool";
if (includeOutputs) snapshot.agent_tool_calls = [{ agent_tool_call_id: "call_output_fixture", sdk_tool_call_id: "sdk_output_fixture", project_id: projectID, tool_kind: "hosted", tool_name: "code_interpreter", status: "completed", requested_at: now, citations: [{ type: "url_citation", title: "原始来源", url: "https://example.org/source" }, { type: "file_citation", file_id: "file-source", filename: "材料来源.pdf" }], result_summary: { outputs: [{ filename: "not-authoritative.bin", asset_id: "fake" }] } }];
if (mcpOutputs) Object.assign(snapshot.agent_tool_calls[0], { tool_kind: "mcp", tool_name: "export_fact", citations: [] });
const submittedMaterials = [];
const registries = { artifacts: [{ artifact_type: "generic_document", label: "文档", description: "", view_key: "document", editable: true, preferred_fields: ["content_markdown"], available_actions: ["inspect", "edit"] }], navigation: [], tasks: [], approvals: [], interactions: [], composer: [] };
const json = (data) => ({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
const unhandled = [], requestedDownloads = [];
let failNextDownload = false;
let browser, vite;
const fixture = createServer(async (request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  if (url.pathname.startsWith("/api/")) {
    let reply;
    if (includeOutputs && request.method === "POST" && url.pathname === `/api/v1/conversations/${project.primary_conversation_id}/messages`) {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      submittedMaterials.push(JSON.parse(Buffer.concat(chunks).toString("utf8")));
      response.writeHead(409, { "Content-Type": "application/json" }).end(JSON.stringify({ error: { code: "FIXTURE_CAPTURED", message: "验收已捕获请求，未调用模型。" } })); return;
    }
    if (request.method !== "GET") {
      unhandled.push(`${request.method} ${url.pathname}`);
      response.writeHead(405).end(); return;
    }
    const version = versions.find((version) => url.pathname.startsWith(`/api/v1/artifact-versions/${version.artifact_version_id}`));
    if (url.pathname === "/api/v1/auth/me") reply = json({ kind: "user", user_id: "user-fixture", workspace_id: "workspace-fixture", role: "owner" });
    else if (url.pathname.endsWith("/workspace-projection")) reply = json({ contract_version: "workspace_projection.v1", registry_revision: "fixture", snapshot, registries });
    else if (url.pathname.endsWith("/assets")) reply = json({ items: includeOutputs ? outputAssets : [] });
    else if (includeOutputs && url.pathname === `/api/v1/assets/${outputAsset.asset_id}/download`) { response.writeHead(200, { "Content-Type": "application/octet-stream", "Content-Disposition": "attachment; filename=output.csv" }).end("value\n42\n"); return; }
    else if (url.pathname.endsWith("/events/stream")) reply = { status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" };
    else if (url.pathname === `/api/v1/artifacts/${artifactID}/versions`) reply = json({ items: versions });
    else if (version && url.pathname.endsWith("/delivery")) reply = json({ project_id: projectID, artifact_id: artifactID, artifact_version_id: version.artifact_version_id, version: version.version, status: version.status, title, warnings: [], downloads: ["json", "txt", "md", "docx"].map((format) => ({ format, filename: `fixture-v${version.version}.${format}`, content_type: "application/octet-stream", download_url: `/api/v1/artifact-versions/${version.artifact_version_id}/download?format=${format}` })) });
    else if (version && url.pathname.endsWith("/download")) {
      requestedDownloads.push({ version: version.version, format: url.searchParams.get("format") });
      reply = failNextDownload ? { status: 403, contentType: "application/json", body: JSON.stringify({ error: { code: "FORBIDDEN", message: "下载权限已撤销" } }) } : { status: 200, contentType: "application/octet-stream", body: version.payload.content_markdown };
      failNextDownload = false;
    } else if (version) reply = json(version);
    else { unhandled.push(url.pathname); reply = { status: 404, contentType: "text/plain", body: "Unhandled fixture API" }; }
    response.writeHead(reply.status, { "Content-Type": reply.contentType }).end(reply.body); return;
  }
  try {
    const source = await fetch(new URL(request.url, vite.resolvedUrls.local[0]));
    response.writeHead(source.status, { "Content-Type": source.headers.get("Content-Type") ?? "application/octet-stream" }).end(Buffer.from(await source.arrayBuffer()));
  } catch { response.writeHead(502).end("UI fixture unavailable"); }
});

try {
  await fs.mkdir(outputRoot, { recursive: true });
  // Ignore the project's dev proxy so no request can reach a user's running backend.
  vite = await createViteServer({ root: frontendRoot, configFile: false, plugins: [react()], logLevel: "error", server: { host: "127.0.0.1", port: 0, strictPort: true, hmr: false } });
  await vite.listen();
  assert(!["8860", "8880"].includes(new URL(vite.resolvedUrls.local[0]).port));
  fixture.listen(0, "127.0.0.1"); await once(fixture, "listening");
  const fixtureURL = `http://127.0.0.1:${fixture.address().port}`;
  assert(!["8860", "8880"].includes(new URL(fixtureURL).port));
  browser = await chromium.launch({ channel: "msedge", headless: true });
  const results = [];
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    const context = await browser.newContext({ viewport, acceptDownloads: true });
    await context.route("**/*", (route) => new URL(route.request().url()).origin === fixtureURL ? route.continue() : route.abort());
    const page = await context.newPage(), errors = [], downloads = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("download", (download) => downloads.push(download));
    await page.goto(`${fixtureURL}/projects/${projectID}`, { waitUntil: "networkidle" });
    const workspaceTab = page.getByRole("tab", { name: "工作区", exact: true });
    if (await workspaceTab.isVisible()) await workspaceTab.click();
    const entry = page.getByRole("button", { name: "下载产物", exact: true });
    await expect(entry).toBeEnabled();
    await entry.click();
    const dialog = page.getByRole("dialog", { name: "下载产物", exact: true });
    await expect(dialog.getByText("版本 2 · 已确认", { exact: true })).toBeVisible();
    assert.deepEqual(await dialog.getByRole("combobox", { name: "下载格式" }).locator("option").evaluateAll((options) => options.map((option) => option.value)), ["json", "txt", "md", "docx"]);
    await page.screenshot({ path: `${outputRoot}/document-${viewport.width}.png` });
    const layout = await dialog.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: element.clientWidth, scrollWidth: element.scrollWidth, overflowing: [...element.querySelectorAll("h2,p,button,select")].filter((child) => child.scrollWidth > child.clientWidth + 2).map((child) => child.tagName) };
    });
    assert(layout.left >= 0 && layout.right <= viewport.width && layout.top >= 0 && layout.bottom <= viewport.height);
    assert(layout.scrollWidth <= layout.width + 2); assert.deepEqual(layout.overflowing, []);
    await dialog.getByRole("combobox", { name: "下载格式" }).selectOption("md");
    failNextDownload = true;
    await dialog.getByRole("button", { name: "下载", exact: true }).click();
    await expect(dialog.getByRole("alert")).toHaveText("下载权限已撤销");
    assert.equal(downloads.length, 0);
    const filePromise = page.waitForEvent("download");
    await dialog.getByRole("button", { name: "下载", exact: true }).click();
    const file = await filePromise;
    assert.equal(file.suggestedFilename(), "fixture-v2.md");
    assert.equal(await fs.readFile(await file.path(), "utf8"), versions[1].payload.content_markdown);
    await page.keyboard.press("Escape"); await expect(dialog).toHaveCount(0); await expect(entry).toBeFocused();
    await page.getByRole("textbox", { name: "文档标题", exact: true }).fill("未保存修改");
    await expect(entry).toBeDisabled();
    await page.getByRole("button", { name: "撤销修改", exact: true }).click(); await expect(entry).toBeEnabled();
    await page.getByRole("button", { name: "版本历史", exact: true }).click();
    await page.getByRole("dialog", { name: "版本历史" }).getByRole("button", { name: /版本 1/ }).click();
    await entry.click(); await expect(dialog.getByText("版本 1 · 历史版本")).toBeVisible();
    const historyPromise = page.waitForEvent("download");
    await dialog.getByRole("button", { name: "下载", exact: true }).click();
    const old = await historyPromise;
    assert.equal(old.suggestedFilename(), "fixture-v1.txt");
    assert.equal(await fs.readFile(await old.path(), "utf8"), versions[0].payload.content_markdown);
    await page.keyboard.press("Escape");
    let materialLayout = null;
    if (includeOutputs) {
      const agentTab = page.getByRole("tab", { name: "Agent", exact: true });
      if (await agentTab.isVisible()) await agentTab.click();
      await page.getByRole("tab", { name: "进度", exact: true }).click();
      await page.getByText("未关联的工具调用", { exact: true }).click();
      if (!mcpOutputs) {
        const source = page.getByRole("link", { name: "原始来源", exact: true });
        await expect(source).toHaveAttribute("href", "https://example.org/source");
        await expect(page.getByText("检索文件：材料来源.pdf", { exact: true })).toBeVisible();
      }
      await expect(page.getByText("not-authoritative.bin", { exact: true })).toHaveCount(0);
      await page.screenshot({ path: `${outputRoot}/outputs-${viewport.width}.png` });
      const outputDownload = page.waitForEvent("download");
      await page.getByRole("link", { name: `下载 ${outputAsset.original_filename}`, exact: true }).click();
      const generated = await outputDownload;
      assert.equal(await fs.readFile(await generated.path(), "utf8"), "value\n42\n");
      await page.getByRole("button", { name: `使用 ${outputAsset.original_filename} 作为材料`, exact: true }).click();
      await expect(page.getByRole("button", { name: "移除 计算结果.csv", exact: true })).toBeVisible();
      await expect(page.getByRole("textbox", { name: "给 Agent 的消息", exact: true })).toBeFocused();
      await page.getByRole("button", { name: "项目材料", exact: true }).click();
      const picker = page.getByRole("dialog", { name: "项目材料", exact: true });
      await expect(picker.getByRole("checkbox", { name: /已删除.csv/ })).toBeDisabled();
      await expect(picker.getByRole("checkbox", { name: /output.bin.zip/ })).toBeDisabled();
      await page.screenshot({ path: `${outputRoot}/materials-${viewport.width}.png` });
      materialLayout = await picker.evaluate((element) => { const box = element.getBoundingClientRect(); return { left: box.left, right: box.right, width: element.clientWidth, scrollWidth: element.scrollWidth }; });
      assert(materialLayout.left >= 0 && materialLayout.right <= viewport.width && materialLayout.scrollWidth <= materialLayout.width + 2);
      await picker.getByRole("checkbox", { name: /计算结果.csv/ }).check();
      await picker.getByRole("button", { name: "使用所选材料（1）", exact: true }).click();
      await expect(page.getByRole("button", { name: "移除 计算结果.csv", exact: true })).toHaveCount(1);
      await page.getByRole("textbox", { name: "给 Agent 的消息", exact: true }).fill("读取刚才生成的 CSV，核对数值。");
      await page.getByRole("button", { name: "发送", exact: true }).click();
      await expect(page.getByText("验收已捕获请求，未调用模型。", { exact: true })).toBeVisible();
      assert.deepEqual(submittedMaterials.at(-1).attachment_refs, [{ asset_id: outputAsset.asset_id, asset_snapshot_id: outputAsset.current_snapshot_id, display_name: outputAsset.display_name }]);
    }
    assert.deepEqual(errors, []);
    results.push({ viewport, layout, exactVersionDownloads: true, deniedDownloadNotSaved: true, dirtyDownloadDisabled: true, escapeFocusRestored: true, materialLayout, outputReuseRequestVerified: includeOutputs });
    await context.close();
  }
  assert.deepEqual(unhandled, []);
  await fs.writeFile(`${outputRoot}/report.json`, JSON.stringify({ kind: "mocked-api-ui", modelCalls: 0, userBackendAccess: false, results, requestedDownloads, submittedMaterials }, null, 2));
  process.stdout.write(JSON.stringify({ outputRoot, passed: true, kind: "mocked-api-ui", results }, null, 2));
} finally {
  await browser?.close();
  if (fixture.listening) await new Promise((resolve) => { fixture.close(resolve); fixture.closeAllConnections(); });
  await vite?.close();
}
