import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import { chromium } from "playwright";

const root = fileURLToPath(new URL("../", import.meta.url));
const outputRoot = fileURLToPath(new URL("../../.tmp/goal-g43-queue-ui/", import.meta.url));
const at = "2026-09-06T00:00:00Z";
const project = { project_id: "prj_queue_fixture", workspace_id: "workspace-fixture", title: "消息队列验收", version: 1, status: "ready", primary_conversation_id: "conv-queue", updated_at: at };
const streams = new Set();
let turns, conflict;
const requests = [], unhandled = [], results = [];
function turn(id, content, extra = {}) {
  return { agent_turn_id: id, user_id: "fixture", project_id: project.project_id, workspace_id: project.workspace_id, conversation_id: project.primary_conversation_id,
    status: "accepted", request: { content, attachment_refs: [] }, created_at: at, updated_at: at, ...extra };
}
function snapshot() {
  return { project, goal: null, artifacts: [], approvals: [], script_candidates: [], final_selection: null, active_run: null, latest_run: null,
    pending_proposed_actions: [], proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [], messages: [], agent_tasks: [], agent_tool_calls: [], agent_turns: turns };
}
function emitUpdate() { for (const stream of streams) stream.write("event: agent.turn.queued_updated\ndata: {}\n\n"); }
const json = (data) => ({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
await fs.mkdir(outputRoot, { recursive: true });
let vite, fixture, browser;
try {
  vite = await createViteServer({ root, configLoader: "native", server: { host: "127.0.0.1", port: 0, hmr: false } });
  await vite.listen();
  const vitePort = vite.httpServer.address().port;
  assert(![8860, 8880].includes(vitePort));
  fixture = createServer(async (request, response) => {
    const url = new URL(request.url, "http://127.0.0.1");
    try {
      if (!url.pathname.startsWith("/api/")) {
        const source = await fetch(new URL(request.url, `http://127.0.0.1:${vitePort}`));
        response.writeHead(source.status, { "Content-Type": source.headers.get("Content-Type") ?? "application/octet-stream" }).end(Buffer.from(await source.arrayBuffer()));
        return;
      }
      let reply;
      if (request.method === "PATCH" && url.pathname.endsWith("/queued-message")) {
        const target = turns.find((item) => url.pathname === `/api/v1/agent-turns/${item.agent_turn_id}/queued-message`);
        assert(target && target.user_id === "fixture" && !target.started_at);
        const chunks = [];
        for await (const chunk of request) chunks.push(chunk);
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        assert.equal(body.expected_content, target.request.content);
        assert.deepEqual(Object.keys(body).sort(), ["content", "expected_content"]);
        assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
        requests.push({ method: request.method, body });
        if (conflict) {
          reply = { status: 409, contentType: "application/json", body: JSON.stringify({ error: { code: "AGENT_TURN_STATE_CONFLICT", message: "消息已开始处理" } }) };
        } else { target.request.content = body.content; reply = json(target); }
      } else if (request.method === "POST" && url.pathname.endsWith("/cancel")) {
        const target = turns.find((item) => url.pathname === `/api/v1/agent-turns/${item.agent_turn_id}/cancel`);
        assert(target);
        target.status = "cancelled";
        requests.push({ method: request.method, cancelled: target.agent_turn_id });
        reply = json({ turn: target, accepted: true });
      } else if (request.method === "GET" && url.pathname === "/api/v1/auth/me") reply = json({ kind: "user", user_id: "fixture", workspace_id: project.workspace_id, workspace_name: "本地验收", role: "owner" });
      else if (request.method === "GET" && url.pathname.endsWith("/workspace-projection")) reply = json({ contract_version: "workspace_projection.v1", registry_revision: "queue-fixture", snapshot: snapshot(), registries: { artifacts: [], navigation: [], approvals: [], composer: [], tasks: [], interactions: [] } });
      else if (request.method === "GET" && url.pathname.endsWith("/assets")) reply = json({ items: [] });
      else if (request.method === "GET" && url.pathname.endsWith("/events/stream")) reply = { status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" };
      else { unhandled.push(`${request.method} ${url.pathname}`); reply = { status: 404, contentType: "text/plain", body: "Unhandled fixture API" }; }
      response.writeHead(reply.status, { "Content-Type": reply.contentType });
      if (reply.contentType === "text/event-stream") {
        streams.add(response); response.on("close", () => streams.delete(response)); response.write(reply.body);
      } else response.end(reply.body);
    } catch (error) { unhandled.push(error.message); response.writeHead(500).end("Fixture failed"); }
  });
  fixture.listen(0, "127.0.0.1");
  await once(fixture, "listening");
  assert(![8860, 8880].includes(fixture.address().port));
  const url = `http://127.0.0.1:${fixture.address().port}/projects/${project.project_id}`;
  browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    turns = [turn("running", "正在执行的原始请求", { status: "running", started_at: at }), turn("queued", "稍后评审故事大纲"),
      turn("collaborator", "协作者的排队请求", { user_id: "collaborator" }), turn("resuming", "已有 SDK 恢复状态的请求", { started_at: at })];
    conflict = false;
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(url, { waitUntil: "domcontentloaded" });
    const edit = page.getByRole("button", { name: "编辑排队消息", exact: true });
    await edit.waitFor();
    assert.equal(await edit.count(), 1);
    await page.getByRole("button", { name: "加入下一轮队列", exact: true }).waitFor();
    await page.getByText("等待恢复执行", { exact: true }).waitFor();
    await edit.click();
    const draft = page.getByRole("textbox", { name: "编辑排队消息内容" });
    const revised = "改为只评审人物动机，不改写正文。" + "longword".repeat(30);
    await draft.fill(revised);
    await draft.scrollIntoViewIfNeeded();
    const metrics = await draft.evaluate((element) => {
      const card = element.closest(".agent-turn-status");
      const box = card.getBoundingClientRect();
      return { left: box.left, right: box.right, overflow: card.scrollWidth > card.clientWidth + 1, documentWidth: document.documentElement.scrollWidth,
        buttons: [...card.querySelectorAll("button")].map((button) => { const b = button.getBoundingClientRect(); return { width: b.width, height: b.height }; }) };
    });
    assert(metrics.left >= 0 && metrics.right <= viewport.width + 1 && !metrics.overflow);
    assert.equal(metrics.documentWidth, viewport.width);
    assert(metrics.buttons.every((button) => button.width >= 36 && button.height >= 36));
    await page.screenshot({ path: path.join(outputRoot, `queue-editor-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "保存排队消息" }).click();
    await draft.waitFor({ state: "detached" });
    await page.reload({ waitUntil: "domcontentloaded" });
    await edit.click();
    assert.equal(await draft.inputValue(), revised);
    await draft.fill("保留我的未保存草稿");
    turns[1].request.content = "另一个窗口修改了消息";
    assert(streams.size > 0); emitUpdate();
    await page.getByText("消息已变化或开始执行，未覆盖你的草稿。", { exact: true }).waitFor();
    assert.equal(await draft.inputValue(), "保留我的未保存草稿");
    assert(await page.getByRole("button", { name: "保存排队消息" }).isDisabled());
    await page.getByRole("button", { name: "放弃修改" }).click();
    await edit.click();
    assert.equal(await draft.inputValue(), "另一个窗口修改了消息");
    conflict = true;
    await draft.fill("冲突时仍保留的草稿");
    await page.getByRole("button", { name: "保存排队消息" }).click();
    await page.getByRole("alert").filter({ hasText: "消息已开始处理" }).waitFor();
    assert.equal(await draft.inputValue(), "冲突时仍保留的草稿");
    await page.screenshot({ path: path.join(outputRoot, `queue-conflict-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "放弃修改" }).click();
    await page.locator(".agent-turn-status").filter({ has: edit }).getByRole("button", { name: "取消排队消息" }).click();
    await edit.waitFor({ state: "detached" });
    assert.equal(turns[1].status, "cancelled");
    assert.deepEqual(errors, []); assert.deepEqual(unhandled, []);
    results.push({ viewport, metrics, savedReload: true, changedSSE: true, conflictDraftRetained: true, permissions: true, cancelled: true });
    await context.close();
  }
} finally {
  await browser?.close();
  if (fixture) { fixture.closeAllConnections(); await new Promise((resolve) => fixture.close(resolve)); }
  await vite?.close();
}
await fs.writeFile(path.join(outputRoot, "report.json"), JSON.stringify({ kind: "mocked-api-ui-not-real-agent-acceptance", modelCalls: 0, userBackendAccess: false, results, requests }, null, 2));
process.stdout.write(JSON.stringify({ status: "passed", outputRoot, results }, null, 2) + "\n");
