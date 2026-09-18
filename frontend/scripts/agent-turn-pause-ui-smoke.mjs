import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import { chromium } from "playwright";

const root = fileURLToPath(new URL("../", import.meta.url));
const inputsMode = process.argv.includes("--inputs");
const recoveryMode = process.argv.includes("--recovery");
const outputRoot = fileURLToPath(new URL(recoveryMode ? "../../.tmp/goal-g419-main-recovery-ui/" : inputsMode ? "../../.tmp/goal-g47b-main-input-ui/" : "../../.tmp/goal-g46-main-pause-ui/", import.meta.url));
const at = "2026-09-06T00:00:00Z";
const project = { project_id: "prj_pause_fixture", workspace_id: "workspace-fixture", title: "主对话暂停验收", version: 1, status: "ready", primary_conversation_id: "conv-pause", updated_at: at };
const original = "暂停后继续评审人物动机。" + "longword".repeat(35);
const streams = new Set();
const controls = [], unexpected = [], results = [];
let turns, role, rejectResume, rejectAppend;
const inputKeys = new Map();
const json = (data) => ({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
function snapshot() {
  return { project, goal: null, artifacts: [], approvals: [], script_candidates: [], final_selection: null, active_run: null, latest_run: null,
    pending_proposed_actions: [], proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [], messages: [], agent_tasks: [], agent_tool_calls: [], agent_turns: turns };
}
function emit(type) {
  const event = { schema_version: "1.0.0", event_id: `fixture-${Date.now()}`, event_type: type, project_id: project.project_id,
    conversation_id: project.primary_conversation_id, turn_id: turns[0].agent_turn_id, occurred_at: at, terminal: ["agent.turn.committed", "agent.turn.failed", "agent.turn.cancelled"].includes(type), payload: { status: turns[0].status } };
  for (const stream of streams) stream.write(`event: ${type}\ndata: ${JSON.stringify(event)}\n\n`);
}
function makeTurn(id, status, content, extra = {}) {
  return { agent_turn_id: id, user_id: "fixture", workspace_id: project.workspace_id, project_id: project.project_id, conversation_id: project.primary_conversation_id,
    status, request: { content, attachment_refs: [] }, created_at: at, started_at: at, updated_at: at, ...extra };
}
await fs.mkdir(outputRoot, { recursive: true });
let vite, fixture, browser, activePage;
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
      const match = url.pathname.match(/^\/api\/v1\/agent-turns\/([^/]+)\/(pause|resume|cancel|inputs)$/);
      if (request.method === "POST" && match) {
        const target = turns.find((turn) => turn.agent_turn_id === match[1]);
        const action = match[2];
        assert(target && target.user_id === "fixture" && role !== "viewer");
        const chunks = [];
        for await (const chunk of request) chunks.push(chunk);
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        if (action !== "inputs") assert.deepEqual(body, {});
        const key = request.headers["idempotency-key"];
        assert.match(key, /^[a-f0-9-]{36}$/i);
        controls.push({ action, id: target.agent_turn_id, key });
        if (action === "inputs") {
          assert.equal(typeof body.content, "string");
          if (rejectAppend) {
            rejectAppend = false;
            reply = { status: 409, contentType: "application/json", body: JSON.stringify({ error: { code: "AGENT_TURN_STATE_CONFLICT", message: "追加请求未确认，请重试" } }) };
          } else {
            let input = inputKeys.get(key);
            if (!input) {
              target.additional_inputs ??= [];
              input = { input_id: `input-${target.additional_inputs.length+1}`, agent_turn_id: target.agent_turn_id, user_id: "fixture", sequence: target.additional_inputs.length+1, content: body.content, status: "received", created_at: at };
              target.additional_inputs.push(input);
              inputKeys.set(key, input);
            }
            reply = { ...json(input), status: 202 };
          }
        } else if (action === "resume" && rejectResume) {
          rejectResume = false;
          reply = { status: 409, contentType: "application/json", body: JSON.stringify({ error: { code: "AGENT_TURN_STATE_CONFLICT", message: "继续请求未确认，请重试" } }) };
        } else {
          target.status = action === "pause" ? target.status === "running" ? "pausing" : "paused" : action === "resume" ? "accepted" : "cancelled";
          if (action === "resume") { delete target.error_code; delete target.error_message; }
          reply = json(action === "cancel" ? { turn: target, accepted: true } : target);
        }
      } else if (request.method === "GET" && url.pathname === "/api/v1/auth/me") reply = json({ kind: "user", user_id: "fixture", workspace_id: project.workspace_id, workspace_name: "隔离验收", role });
      else if (request.method === "GET" && url.pathname.endsWith("/workspace-projection")) reply = json({ contract_version: "workspace_projection.v1", registry_revision: "pause-fixture", snapshot: snapshot(), registries: { artifacts: [], navigation: [], approvals: [], composer: [], tasks: [], interactions: [] } });
      else if (request.method === "GET" && url.pathname.endsWith("/assets")) reply = json({ items: [] });
      else if (request.method === "GET" && url.pathname.endsWith("/events/stream")) reply = { status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" };
      else { unexpected.push(`${request.method} ${url.pathname}`); reply = { status: 404, contentType: "text/plain", body: "Unhandled fixture API" }; }
      response.writeHead(reply.status, { "Content-Type": reply.contentType });
      if (reply.contentType === "text/event-stream") {
        streams.add(response); response.on("close", () => streams.delete(response)); response.write(reply.body);
      } else response.end(reply.body);
    } catch (error) { unexpected.push(error.message); response.writeHead(500).end("Fixture failed"); }
  });
  fixture.listen(0, "127.0.0.1");
  await once(fixture, "listening");
  assert(![8860, 8880].includes(fixture.address().port));
  const url = `http://127.0.0.1:${fixture.address().port}/projects/${project.project_id}`;
  browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    turns = [makeTurn("old-main", "running", original),
      ...Array.from({ length: 51 }, (_, index) => makeTurn(`done-${index}`, "committed", `历史消息 ${index}`, { created_at: "2026-09-06T01:00:00Z" })),
      makeTurn("other-author", "paused", "协作者的执行", { user_id: "collaborator" })];
    role = "owner"; rejectResume = false; rejectAppend = false; inputKeys.clear();
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    activePage = page;
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(url, { waitUntil: "domcontentloaded" });
    const target = page.locator(".agent-turn-status").first();
    const pause = target.getByRole("button", { name: "暂停本轮", exact: true });
    const resume = target.getByRole("button", { name: "继续本轮", exact: true });
    await pause.waitFor();
    assert.equal(await page.getByRole("button", { name: "继续本轮", exact: true }).count(), 0);
    if (!recoveryMode) {
      await pause.click();
      await target.getByText("等待当前轮完成后暂停", { exact: true }).waitFor();
    }
    assert.equal(await resume.count(), 0);
    assert(await target.getByRole("button", { name: "停止本轮", exact: true }).isEnabled());
    turns[0].status = "paused";
    if (recoveryMode) turns[0].error_code = "SDK_MODEL_RECOVERY_REQUIRED";
    assert(streams.size > 0); emit("agent.turn.paused");
    await resume.waitFor();
    await target.getByText(recoveryMode ? "等待恢复连接" : "本轮已暂停", { exact: true }).waitFor();
    if (recoveryMode) await target.getByText("模型连接中断，原执行状态已保存。", { exact: true }).waitFor();
    await page.locator(".agent-state").getByText("已暂停", { exact: true }).waitFor();
    assert.equal(await page.locator(".presence.running").count(), 0);
    await page.reload({ waitUntil: "domcontentloaded" });
    await resume.waitFor();
    assert.equal(await page.getByRole("button", { name: "继续本轮", exact: true }).count(), 1);
    await target.scrollIntoViewIfNeeded();
    const metrics = await target.evaluate((element) => {
      const box = element.getBoundingClientRect();
      return { left: box.left, right: box.right, overflow: element.scrollWidth > element.clientWidth + 1, documentWidth: document.documentElement.scrollWidth,
        animation: getComputedStyle(element.querySelector("svg")).animationName,
        messages: [...document.querySelectorAll(".timeline-message p")].map((message) => ({ width: message.clientWidth, contentWidth: message.scrollWidth })),
        buttons: [...element.querySelectorAll("button")].map((button) => { const b = button.getBoundingClientRect(); return { width: b.width, height: b.height }; }) };
    });
    assert(metrics.left >= 0 && metrics.right <= viewport.width + 1 && !metrics.overflow);
    assert.equal(metrics.documentWidth, viewport.width);
    assert.equal(metrics.animation, "none");
    assert(metrics.messages.length > 0 && metrics.messages.every((message) => message.contentWidth <= message.width + 1));
    assert(metrics.buttons.every((button) => button.width >= 36 && button.height >= 36));
    await page.screenshot({ path: path.join(outputRoot, `main-paused-${viewport.width}.png`), fullPage: true });
    rejectResume = true;
    await resume.click();
    await target.getByRole("alert").filter({ hasText: "继续请求未确认，请重试" }).waitFor();
    await page.screenshot({ path: path.join(outputRoot, `main-resume-error-${viewport.width}.png`), fullPage: true });
    await resume.click();
    await target.getByText("等待恢复执行", { exact: true }).waitFor();
    assert.equal(controls.at(-1).key, controls.at(-2).key);
    turns[0].status = "waiting_approval"; emit("agent.turn.waiting_approval");
    await target.locator("strong").filter({ hasText: "等待工具授权" }).waitFor();
    await pause.click();
    await resume.waitFor();
    if (inputsMode) {
      assert.equal(await page.getByRole("button", { name: "追加本轮要求", exact: true }).count(), 1);
      await page.getByRole("button", { name: "追加本轮要求", exact: true }).click();
      const text = "保留原内容，再核对人物动机。" + "LongRequirement".repeat(24);
      await page.getByRole("textbox", { name: "本轮追加要求", exact: true }).fill(text);
      rejectAppend = true;
      await page.getByRole("button", { name: "提交追加要求", exact: true }).click();
      await page.getByRole("alert").filter({ hasText: "追加请求未确认，请重试" }).waitFor();
      assert.equal(await page.getByRole("textbox", { name: "本轮追加要求", exact: true }).inputValue(), text);
      await page.screenshot({ path: path.join(outputRoot, `main-input-error-${viewport.width}.png`), fullPage: true });
      await page.getByRole("button", { name: "提交追加要求", exact: true }).click();
      await page.getByRole("textbox", { name: "本轮追加要求", exact: true }).waitFor({ state: "detached" });
      assert.equal(controls.at(-1).key, controls.at(-2).key);
      assert.equal(turns[0].additional_inputs.length, 1);
      await page.reload({ waitUntil: "domcontentloaded" });
      await page.getByText("追加要求 · 1", { exact: true }).click();
      await page.getByText("已接收，待送入模型", { exact: true }).waitFor();
      const inputPanel = page.locator(".agent-turn-inputs").first();
      await inputPanel.scrollIntoViewIfNeeded();
      assert(await inputPanel.evaluate((element) => element.scrollWidth <= element.clientWidth + 1));
      assert((await inputPanel.locator("button").evaluateAll((buttons) => buttons.map((button) => button.getBoundingClientRect().width))).every((width) => width >= 36));
      await page.screenshot({ path: path.join(outputRoot, `main-input-received-${viewport.width}.png`), fullPage: true });
      turns[0].additional_inputs[0].status = "included"; emit("agent.turn.inputs_included");
      await page.getByText("已送入模型", { exact: true }).waitFor();
      turns[0].additional_inputs.push({ input_id: "late", agent_turn_id: turns[0].agent_turn_id, user_id: "fixture", sequence: 2, content: "尚未送入的追加", status: "received", created_at: at });
      emit("agent.turn.input_received");
      await page.getByText("追加要求 · 2", { exact: true }).waitFor();
    }
    role = "viewer";
    await page.reload({ waitUntil: "domcontentloaded" });
    await target.getByText("本轮已暂停", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "继续本轮", exact: true }).count(), 0);
    assert.equal(await page.getByRole("button", { name: "暂停本轮", exact: true }).count(), 0);
    if (inputsMode) assert.equal(await page.getByRole("button", { name: "追加本轮要求", exact: true }).count(), 0);
    role = "owner";
    await page.reload({ waitUntil: "domcontentloaded" });
    await resume.waitFor();
    if (inputsMode) {
      await page.getByRole("button", { name: "追加本轮要求", exact: true }).click();
      await page.getByRole("textbox", { name: "本轮追加要求", exact: true }).fill("尚未提交的草稿");
    }
    await target.getByRole("button", { name: "停止本轮", exact: true }).click();
    await resume.waitFor({ state: "detached" });
    assert.equal(turns[0].status, "cancelled");
    if (inputsMode) {
      const draft = page.getByRole("textbox", { name: "本轮追加要求", exact: true });
      assert.equal(await draft.inputValue(), "尚未提交的草稿");
      assert(await page.getByRole("button", { name: "提交追加要求", exact: true }).isDisabled());
      await page.getByText("追加要求 · 2", { exact: true }).click();
      await page.getByText("未确认送入模型", { exact: true }).waitFor();
      await draft.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(outputRoot, `main-input-terminal-draft-${viewport.width}.png`), fullPage: true });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), viewport.width);
      await page.getByRole("button", { name: "收起追加输入", exact: true }).click();
      await page.getByRole("button", { name: "查看未提交草稿", exact: true }).click();
      assert.equal(await draft.inputValue(), "尚未提交的草稿");
    }
    assert.deepEqual(errors, []); assert.deepEqual(unexpected, []);
    results.push({ viewport, metrics, pauseRequested: !recoveryMode, modelRecoveryCheckpoint: recoveryMode, ssePaused: true, reloadRetained: true, oldTurnVisible: true, retryKeyRetained: true, permissions: true, cancelled: true, inputReceiptsAndDraft: inputsMode });
    await context.close();
  }
} catch (error) {
  if (activePage && !activePage.isClosed()) {
    await activePage.screenshot({ path: path.join(outputRoot, "failure.png"), fullPage: true });
    await fs.writeFile(path.join(outputRoot, "failure.json"), JSON.stringify({ error: error.message, controls, unexpected, text: await activePage.locator("body").innerText() }, null, 2));
  }
  throw error;
} finally {
  await browser?.close();
  if (fixture) { fixture.closeAllConnections(); await new Promise((resolve) => fixture.close(resolve)); }
  await vite?.close();
}
await fs.writeFile(path.join(outputRoot, "report.json"), JSON.stringify({ kind: "mocked-api-ui-not-real-agent-acceptance", modelCalls: 0, userBackendAccess: false, results, controls }, null, 2));
process.stdout.write(JSON.stringify({ status: "passed", outputRoot, results }, null, 2) + "\n");
