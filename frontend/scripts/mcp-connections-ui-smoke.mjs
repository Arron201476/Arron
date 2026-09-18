import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium, expect } from "playwright/test";

const outputRoot = fileURLToPath(new URL("../../.tmp/goal-g414-connections-ui/", import.meta.url));
const frontendRoot = fileURLToPath(new URL("../", import.meta.url));
const serverID = "story-connection-with-a-long-but-valid-name-for-layout";
const descriptor = { id: `mcp:${serverID}/lookup`, kind: "mcp", server_id: serverID, name: "lookup", description: "Lookup", enabled: true, access: "read", approval: "never" };
const empty = { scope: "user", version: 0, status: "missing" };
let personal = { ...empty }, workspace = { scope: "workspace", version: 1, status: "active" };
let failSave = false, browser, vite;
const unhandled = [], mutations = [];
const fixture = createServer(async (request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  if (url.pathname.startsWith("/api/")) {
    let data;
    if (request.method === "PUT" && url.pathname === "/api/v1/mcp-connections") {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      assert.equal(body.server_id, serverID);
      assert.equal(typeof body.request_id, "string");
      const current = body.scope === "user" ? personal : workspace;
      assert.equal(body.expected_version, current.version);
      if (failSave) {
        failSave = false;
        response.writeHead(409, { "Content-Type": "application/json" }).end(JSON.stringify({ error: { code: "MCP_CREDENTIAL_CONFLICT", message: "凭据已变化，请刷新后重试。" } })); return;
      }
      if (!body.delete) assert.equal(body.values["api-token"], "Bearer isolated-ui-secret");
      else assert.equal(body.values, undefined);
      mutations.push({ scope: body.scope, version: current.version, remove: Boolean(body.delete) });
      data = { scope: body.scope, version: current.version + 1, status: body.delete ? "deleted" : "active" };
      if (body.scope === "user") personal = data; else workspace = data;
    } else if (request.method !== "GET") {
      unhandled.push(`${request.method} ${url.pathname}`); response.writeHead(405).end(); return;
    } else if (url.pathname === "/api/v1/auth/me") data = { kind: "user", user_id: "fixture-owner", workspace_id: "fixture-workspace", workspace_name: "隔离验收", role: "owner" };
    else if (["/api/v1/skills", "/api/v1/skills/install-attempts", "/api/v1/projects"].includes(url.pathname)) data = { items: [] };
    else if (url.pathname === "/api/v1/skill-management-options") data = { install_scopes: ["user", "workspace", "project"], system_read_only: true };
    else if (url.pathname === "/api/v1/script-sandbox-policy") data = { version: 0, enabled: false, sandbox: { available: false, enabled: false, provider: "unconfigured" } };
    else if (url.pathname === "/api/v1/workspace-projection") data = { contract_version: "workspace_projection.v1", registry_revision: "fixture", registries: { composer: [], artifacts: [], navigation: [], tasks: [], approvals: [], interactions: [] } };
    else if (url.pathname === "/api/v1/agent-tools") data = { tools: [descriptor] };
    else if (url.pathname === "/api/v1/agent-tools/configuration") data = { workspace_id: "fixture-workspace", version: 0, options: [{ id: `mcp:${serverID}`, kind: "mcp", name: serverID, description: "Connection fixture", enabled: true, configurable: true, transport: "streamable_http" }] };
    else if (url.pathname === "/api/v1/mcp-connections") data = { storage_available: true, can_manage_personal: true, can_manage_workspace: true, items: [{ server_id: serverID, display_name: serverID, fields: ["api-token"], enabled: true, effective_scope: personal.status === "active" ? "user" : workspace.status === "active" ? "workspace" : "none", personal, workspace }] };
    else { unhandled.push(url.pathname); response.writeHead(404).end(); return; }
    response.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" }).end(JSON.stringify({ data })); return;
  }
  try {
    const source = await fetch(new URL(request.url, vite.resolvedUrls.local[0]));
    response.writeHead(source.status, { "Content-Type": source.headers.get("Content-Type") ?? "application/octet-stream" }).end(Buffer.from(await source.arrayBuffer()));
  } catch { response.writeHead(502).end(); }
});

try {
  await fs.mkdir(outputRoot, { recursive: true });
  vite = await createViteServer({ root: frontendRoot, configFile: false, plugins: [react()], logLevel: "error", server: { host: "127.0.0.1", port: 0, strictPort: true, hmr: false } });
  await vite.listen();
  assert(!["8860", "8880"].includes(new URL(vite.resolvedUrls.local[0]).port));
  fixture.listen(0, "127.0.0.1"); await once(fixture, "listening");
  const origin = `http://127.0.0.1:${fixture.address().port}`;
  assert(!["8860", "8880"].includes(new URL(origin).port));
  browser = await chromium.launch({ channel: "msedge", headless: true });
  const results = [];
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    personal = { ...empty }; workspace = { scope: "workspace", version: 1, status: "active" };
    const context = await browser.newContext({ viewport });
    await context.route("**/*", (route) => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    const page = await context.newPage(), errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/skills`, { waitUntil: "networkidle" });
    await page.getByText("MCP", { exact: true }).click();
    const connections = page.getByLabel("MCP 连接凭据", { exact: true });
    await expect(connections.getByText("使用工作区凭据", { exact: true })).toBeVisible();
    await connections.getByRole("button", { name: "配置凭据", exact: true }).click();
    const input = connections.getByLabel("api-token", { exact: true });
    await input.fill("Bearer isolated-ui-secret");
    assert.equal(await input.getAttribute("type"), "password");
    await connections.scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${outputRoot}/credentials-${viewport.width}.png` });
    const layout = await connections.evaluate((element) => ({
      width: element.clientWidth, scrollWidth: element.scrollWidth,
      overflowing: [...element.querySelectorAll("strong,button,label")].filter((child) => child.scrollWidth > child.clientWidth + 2).map((child) => child.tagName),
      inputBounds: [...element.querySelectorAll("input")].map((child) => ({ left: child.getBoundingClientRect().left, right: child.getBoundingClientRect().right })),
      right: element.getBoundingClientRect().right, left: element.getBoundingClientRect().left,
    }));
    assert(layout.left >= 0 && layout.right <= viewport.width && layout.scrollWidth <= layout.width + 2);
    assert.deepEqual(layout.overflowing, []);
    assert(layout.inputBounds.every((bounds) => bounds.left >= 0 && bounds.right <= viewport.width));
    await connections.getByRole("button", { name: "保存凭据", exact: true }).click();
    await expect(connections.getByText("使用个人凭据", { exact: true })).toBeVisible();
    await expect(input).toHaveCount(0);
    assert(!(await page.content()).includes("Bearer isolated-ui-secret"));
    await page.reload({ waitUntil: "networkidle" });
    await page.getByText("MCP", { exact: true }).click();
    await expect(connections.getByText("已保存 · v1", { exact: true })).toBeVisible();
    await connections.getByRole("button", { name: "更新凭据", exact: true }).click();
    await expect(input).toHaveValue("");
    failSave = true;
    await input.fill("Bearer isolated-ui-secret");
    await connections.getByRole("button", { name: "保存凭据", exact: true }).click();
    await expect(connections.getByRole("alert")).toHaveText("凭据已变化，请刷新后重试。");
    await expect(input).toHaveCount(0);
    page.once("dialog", (dialog) => dialog.accept());
    await connections.getByRole("button", { name: "撤销凭据", exact: true }).click();
    await expect(connections.getByText("使用工作区凭据", { exact: true })).toBeVisible();
    await expect(connections.getByText("已撤销", { exact: true })).toBeVisible();
    await connections.getByRole("radio", { name: "工作区", exact: true }).check();
    page.once("dialog", (dialog) => dialog.accept());
    await connections.getByRole("button", { name: "撤销凭据", exact: true }).click();
    await expect(connections.getByText("未配置凭据", { exact: true })).toBeVisible();
    await page.screenshot({ path: `${outputRoot}/revoked-${viewport.width}.png` });
    assert.deepEqual(errors, []);
    results.push({ viewport, layout, errors });
    await context.close();
  }
  assert.deepEqual(unhandled, []);
  assert.equal(mutations.length, 6);
  await fs.writeFile(`${outputRoot}/report.json`, JSON.stringify({ status: "passed", backend: "isolated API fixture", sdk: "not exercised by this UI test", results, mutations, unhandled }, null, 2));
  process.stdout.write("MCP connection UI fixture passed at 1440px and 390px\n");
} finally {
  await browser?.close();
  fixture.closeAllConnections();
  if (fixture.listening) await new Promise((resolve) => fixture.close(resolve));
  await vite?.close();
}
