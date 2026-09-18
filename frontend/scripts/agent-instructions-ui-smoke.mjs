import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium, expect } from "playwright/test";

const output = fileURLToPath(new URL("../../.tmp/goal-g415-instructions-ui/", import.meta.url));
const root = fileURLToPath(new URL("../", import.meta.url));
let browser, vite, failSave = false;
let documents;
const unhandled = [], mutations = [], results = [];
const fixture = createServer(async (request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  if (url.pathname.startsWith("/api/")) {
    let data;
    if (url.pathname === "/api/v1/agent-instructions" && request.method === "PUT") {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      const current = documents.find((item) => item.scope === body.scope);
      assert(current.can_edit && body.expected_version === current.version && typeof body.request_id === "string");
      if (body.scope === "project") assert.equal(body.project_id, "p");
      if (failSave) {
        failSave = false;
        response.writeHead(409, { "Content-Type": "application/json" }).end(JSON.stringify({ error: { code: "AGENT_INSTRUCTIONS_CONFLICT", message: "规则已变化，请刷新。" } })); return;
      }
      data = { ...current, version: current.version + 1, content: body.content, enabled: body.enabled };
      documents = documents.map((item) => item.scope === body.scope ? data : item);
      mutations.push({ scope: body.scope, version: data.version, enabled: data.enabled });
    } else if (request.method !== "GET") {
      unhandled.push(`${request.method} ${url.pathname}`); response.writeHead(405).end(); return;
    } else if (url.pathname === "/api/v1/auth/me") data = { kind: "user", user_id: "u", workspace_id: "w", workspace_name: "隔离规则验收", display_name: "测试编辑者", role: "editor" };
    else if (url.pathname === "/api/v1/projects") data = { items: [{ project_id: "p", title: "超长作品名称用于检查移动端选择器边界与文本布局" }] };
    else if (url.pathname === "/api/v1/agent-instructions") data = { workspace_id: "w", project_id: url.searchParams.get("project_id") ?? "", documents: documents.filter((item) => item.scope !== "project" || url.searchParams.get("project_id") === "p"), applies_to: "new_executions" };
    else { unhandled.push(url.pathname); response.writeHead(404).end(); return; }
    response.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" }).end(JSON.stringify({ data })); return;
  }
  try {
    const source = await fetch(new URL(request.url, vite.resolvedUrls.local[0]));
    response.writeHead(source.status, { "Content-Type": source.headers.get("Content-Type") ?? "application/octet-stream" }).end(Buffer.from(await source.arrayBuffer()));
  } catch { response.writeHead(502).end(); }
});
try {
  await fs.mkdir(output, { recursive: true });
  vite = await createViteServer({ root, configFile: false, plugins: [react()], logLevel: "error", server: { host: "127.0.0.1", port: 0, strictPort: true, hmr: false } });
  await vite.listen();
  assert(!["8860", "8880"].includes(new URL(vite.resolvedUrls.local[0]).port));
  fixture.listen(0, "127.0.0.1"); await once(fixture, "listening");
  const origin = `http://127.0.0.1:${fixture.address().port}`;
  assert(!["8860", "8880"].includes(new URL(origin).port));
  browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    documents = [
      { scope: "workspace", scope_ref: "w", version: 1, content: "输出必须保留事实和来源。", enabled: true, can_edit: false, content_hash: "fixture" },
      { scope: "user", scope_ref: "u", version: 0, content: "", enabled: false, can_edit: true, content_hash: "fixture" },
      { scope: "project", scope_ref: "p", version: 1, content: "角色名沿用原文。", enabled: true, can_edit: true, content_hash: "fixture" },
    ];
    const context = await browser.newContext({ viewport });
    await context.route("**/*", (route) => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    const page = await context.newPage(), errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/instructions?project_id=p`, { waitUntil: "networkidle" });
    await expect(page.getByLabel("规则内容")).toHaveValue("角色名沿用原文。");
    await page.getByRole("tab", { name: "个人", exact: true }).click();
    await page.getByLabel("规则内容").fill("回答使用中文。\n最终交付只保留正文，不添加未核实事实。");
    await page.getByLabel("启用", { exact: true }).check();
    await page.getByRole("button", { name: "保存规则", exact: true }).click();
    await expect(page.getByText("v1 · 已启用", { exact: true })).toBeVisible();
    await page.screenshot({ path: `${output}/personal-${viewport.width}.png`, fullPage: true });
    await page.reload({ waitUntil: "networkidle" });
    await page.getByRole("tab", { name: "个人", exact: true }).click();
    await expect(page.getByLabel("规则内容")).toHaveValue("回答使用中文。\n最终交付只保留正文，不添加未核实事实。");
    failSave = true;
    await page.getByLabel("规则内容").fill("冲突中的草稿");
    await page.getByRole("button", { name: "保存规则", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("规则已变化");
    await expect(page.getByLabel("规则内容")).toHaveValue("冲突中的草稿");
    await expect(page.getByRole("button", { name: "保存规则", exact: true })).toBeDisabled();
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "刷新规则", exact: true }).click();
    await expect(page.getByLabel("规则内容")).not.toHaveValue("冲突中的草稿");
    await page.getByRole("tab", { name: "工作区", exact: true }).click();
    await expect(page.getByText("只读", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "保存规则", exact: true })).toHaveCount(0);
    await page.getByRole("tab", { name: "作品", exact: true }).click();
    await expect(page.getByLabel("规则作品")).toHaveValue("p");
    await page.screenshot({ path: `${output}/project-${viewport.width}.png`, fullPage: true });
    const layout = await page.locator(".instructions-main").evaluate((element) => ({
      left: element.getBoundingClientRect().left, right: element.getBoundingClientRect().right,
      width: element.clientWidth, scrollWidth: element.scrollWidth,
      overflow: [...element.querySelectorAll("h1,h2,button,label,dd,textarea,select")].filter((child) => {
        const box = child.getBoundingClientRect();
        return box.left < 0 || box.right > innerWidth + 1 || (!(child instanceof HTMLTextAreaElement || child instanceof HTMLSelectElement) && child.scrollWidth > child.clientWidth + 2);
      }).map((child) => child.tagName),
    }));
    assert(layout.left >= 0 && layout.right <= viewport.width && layout.width + 2 >= layout.scrollWidth);
    assert.deepEqual(layout.overflow, []);
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "清空并停用", exact: true }).click();
    await expect(page.getByText("v2 · 已停用", { exact: true })).toBeVisible();
    await expect(page.getByLabel("规则内容")).toHaveValue("");
    assert.deepEqual(errors, []);
    results.push({ viewport, layout, errors });
    await context.close();
  }
  assert.deepEqual(unhandled, []);
  assert.equal(mutations.length, 4);
  await fs.writeFile(`${output}/report.json`, JSON.stringify({ status: "passed", backend: "isolated API fixture", sdk: "not exercised by this UI test", results, mutations }, null, 2));
  process.stdout.write("Agent instructions UI passed at 1440px and 390px\n");
} finally {
  await browser?.close();
  fixture.closeAllConnections();
  if (fixture.listening) await new Promise((resolve) => fixture.close(resolve));
  await vite?.close();
}
