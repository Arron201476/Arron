import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import { chromium } from "playwright";

const root = fileURLToPath(new URL("../", import.meta.url));
const outputRoot = fileURLToPath(new URL("../../.tmp/goal-g415-rule-approval-ui/", import.meta.url));
const at = "2026-09-06T00:00:00Z";
const project = { project_id: "prj_origin_fixture", workspace_id: "workspace-fixture", title: "执行归属验收", version: 1, status: "ready", primary_conversation_id: "conv-origin", updated_at: at };
const runID = `run_${"long-id-".repeat(10)}`;
const origins = {
  workflow: { mode: "stateful_workflow", run_id: runID, step_run_id: "step-origin", step_id: "review_story", task_item_id: "task-origin", item_key: `episode:${"long-item-".repeat(8)}`, attempt_id: "workflow-attempt", attempt_no: 1, capability_id: "story_review" },
  background: { mode: "background_task", agent_task_id: "background-task", attempt_id: "background-attempt", attempt_no: 2, capability_id: "story_research" },
  control: { mode: "conversation", agent_turn_id: "main-turn" },
  rules: { mode: "conversation", agent_turn_id: "main-turn" },
};
const ruleText = "用户明确要求保存的个人规则。" + "unbroken-long-rule-".repeat(30);
const decisions = new Map();
let backgroundStatus;
let additionalInputs = [];
const streams = new Set();
const requests = [];
const unhandled = [];
const json = (data) => ({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
function call(kind) {
  const action = decisions.get(kind);
  const approvalStatus = action === "approve" ? "approved" : action === "reject" ? "rejected" : "pending";
  return {
    agent_tool_call_id: `call-${kind}`, project_id: project.project_id, workspace_id: project.workspace_id, conversation_id: project.primary_conversation_id,
    execution: origins[kind], sdk_tool_call_id: `sdk-${kind}`, tool_id: kind === "rules" ? "runtime:update_saved_instructions" : `${kind}-tool`, tool_kind: "runtime_function", tool_name: kind === "rules" ? "update_saved_instructions" : `${kind}_tool`, arguments_hash: `arguments-${kind}`,
    access_mode: "write", approval_policy: "always", approval_status: approvalStatus, status: action ? approvalStatus : "pending_approval",
    requested_at: at, updated_at: at, arguments_summary: kind === "control" ? { target_type: "run", target_id: runID, action_id: "pause_run", snapshot_hash: "control-snapshot" } : { path: "skills/review/SKILL.md" },
    approval: { agent_tool_approval_id: `approval-${kind}`, agent_tool_call_id: `call-${kind}`, project_id: project.project_id,
      workspace_id: project.workspace_id, conversation_id: project.primary_conversation_id, status: approvalStatus, version: 7,
      subject_snapshot_hash: `hash-${kind}`, title: kind === "rules" ? "确认保存 Agent 规则" : kind === "workflow" ? "安装工作流创建的 Skill" : kind === "control" ? `允许暂停运行：${runID}` : "保存后台调研文件",
      reason: "此操作会写入当前作品。", options: ["approve", "reject"], requested_at: at },
  };
}
function snapshot() {
  return {
    project, goal: null, artifacts: [], approvals: [], script_candidates: [], final_selection: null, active_run: null, latest_run: null,
    pending_proposed_actions: [], proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [],
    messages: Array.from({ length: 15 }, (_, index) => ({ message_id: `message-${index}`, role: "user", content: `最近消息 ${index + 1}`, created_at: `2026-09-06T01:${String(index).padStart(2, "0")}:00Z` })),
    agent_tasks: [{ agent_task_id: "background-task", user_id: "fixture", project_id: project.project_id, workspace_id: project.workspace_id, conversation_id: project.primary_conversation_id,
      skill_invocation_id: "invocation", capability_id: "story_research", capability_version: "1.0.0", status: backgroundStatus ?? (decisions.has("background") ? "running" : "waiting_approval"),
      additional_inputs: additionalInputs, progress_current: 0, progress_total: 1, progress_message: backgroundStatus === "pausing" ? "等待当前轮完成后暂停" : backgroundStatus === "paused" ? "已暂停" : "待授权的后台调研", attempt_count: 2, max_attempts: 3, cancel_requested: false, input: {}, config: {}, created_at: at, queued_at: at, updated_at: at },
      { agent_task_id: "unsafe-retry", project_id: project.project_id, workspace_id: project.workspace_id, conversation_id: project.primary_conversation_id,
        skill_invocation_id: "unsafe-invocation", capability_id: "story_research", capability_version: "1.0.0", status: "failed",
        progress_current: 1, progress_total: 2, progress_message: "已有部分结果", attempt_count: 1, max_attempts: 3, cancel_requested: false, input: {}, config: {}, created_at: at, queued_at: at, updated_at: at,
        retry_blocked_reason: "已有写入调用或执行恢复状态，请先核对已有结果，不能直接从头重新执行。" }],
    agent_turns: [{ agent_turn_id: "main-turn", status: "committed", request: { content: "检查草稿" }, created_at: at, updated_at: at, observation: { usage: { total_tokens: 120 } } }],
    agent_tool_calls: [call("workflow"), call("background"), call("control"), call("rules"), { agent_tool_call_id: "main-call", execution: { mode: "conversation", agent_turn_id: "main-turn" }, tool_name: "read_workspace_file", tool_kind: "runtime_function", status: "completed", requested_at: at }],
  };
}
const registries = { artifacts: [], navigation: [], approvals: [], composer: [],
  tasks: ["waiting_approval", "running", "pausing", "paused"].map((status) => ({ status, view_key: "task_progress" })).concat([{ status: "failed", view_key: "task_failure" }]),
  interactions: [{ registry_key: "agent_tool", view_key: "agent_tool_approval", commands: ["approve", "deny"] }] };

await fs.mkdir(outputRoot, { recursive: true });
let vite, fixture, browser;
const results = [];
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
      const kind = ["workflow", "background", "control", "rules"].find((value) => url.pathname === `/api/v1/agent-tool-approvals/approval-${value}/resolutions`);
      const taskAction = ["pause", "resume"].find((action) => url.pathname === `/api/v1/agent-tasks/background-task/${action}`);
      if (request.method === "POST" && url.pathname === "/api/v1/agent-tasks/background-task/inputs") {
        const chunks = [];
        for await (const chunk of request) chunks.push(chunk);
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        assert.equal(Object.keys(body).join(","), "content");
        assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
        const input = { input_id: "input-fixture", agent_task_id: "background-task", user_id: "fixture", sequence: 1, content: body.content, status: "received", created_at: at };
        additionalInputs.push(input); backgroundStatus = "pausing";
        requests.push({ input: body }); reply = json(input);
      } else if (taskAction && request.method === "POST") {
        assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
        requests.push({ taskAction });
        if (taskAction === "pause") backgroundStatus = decisions.has("background") ? "pausing" : "paused";
        else { assert.equal(backgroundStatus, "paused"); backgroundStatus = undefined; }
        reply = json(snapshot().agent_tasks[0]);
      } else if (kind && request.method === "POST") {
        const chunks = [];
        for await (const chunk of request) chunks.push(chunk);
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        assert.deepEqual(body, { action: kind === "background" ? "reject" : "approve", expected_version: 7, subject_snapshot_hash: `hash-${kind}` });
        assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
        requests.push({ kind, body });
        decisions.set(kind, body.action);
        reply = json(call(kind).approval);
      } else if (request.method === "GET" && url.pathname === "/api/v1/auth/me") reply = json({ kind: "user", user_id: "fixture", workspace_id: project.workspace_id, workspace_name: "本地验收", role: "owner" });
      else if (request.method === "GET" && url.pathname === "/api/v1/projects") reply = json({ items: [project] });
      else if (request.method === "GET" && url.pathname === "/api/v1/agent-tool-calls/call-rules/instruction-proposal") reply = json({ agent_tool_call_id: "call-rules", project_id: project.project_id, user_id: "fixture", arguments_hash: "arguments-rules", arguments: { scope: "user", expected_version: 1, content: ruleText, enabled: true }, current: { scope: "user", scope_ref: "fixture", version: 1, content: "已有规则", enabled: true, content_hash: "old" }, can_approve: true, can_reject: true });
      else if (request.method === "GET" && url.pathname.endsWith("/workspace-projection")) reply = json({ contract_version: "workspace_projection.v1", registry_revision: "origin-fixture", snapshot: snapshot(), registries });
      else if (request.method === "GET" && url.pathname.endsWith("/assets")) reply = json({ items: [] });
      else if (request.method === "GET" && url.pathname.endsWith("/events/stream")) reply = { status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" };
      else { unhandled.push(`${request.method} ${url.pathname}`); reply = { status: 404, contentType: "text/plain", body: "Unhandled fixture API" }; }
      response.writeHead(reply.status, { "Content-Type": reply.contentType });
      if (reply.contentType === "text/event-stream") {
        streams.add(response);
        response.on("close", () => streams.delete(response));
        response.write(reply.body);
      }
      else response.end(reply.body);
    } catch (error) { unhandled.push(error.message); response.writeHead(500).end("Fixture failed"); }
  });
  fixture.listen(0, "127.0.0.1");
  await once(fixture, "listening");
  const url = `http://127.0.0.1:${fixture.address().port}/projects/${project.project_id}`;
  browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    decisions.clear();
    backgroundStatus = undefined;
    additionalInputs = [];
    const requestCount = requests.length;
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(url, { waitUntil: "domcontentloaded" });
    assert.equal(await page.getByRole("link", { name: "作品规则", exact: true }).getAttribute("href"), `/instructions?project_id=${project.project_id}`);
    await page.getByRole("button", { name: "取消任务", exact: true }).first().waitFor();
    assert.equal(await page.getByRole("button", { name: "取消任务", exact: true }).count(), 1, "older waiting task must remain actionable");
    await page.getByRole("button", { name: "暂停后台任务", exact: true }).click();
    await page.getByRole("button", { name: "继续后台任务", exact: true }).waitFor();
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "继续后台任务", exact: true }).click();
    await page.getByRole("button", { name: "暂停后台任务", exact: true }).waitFor();
    await page.getByRole("tab", { name: "进度", exact: true }).click();
    const rules = page.getByRole("region", { name: "规则变更确认", exact: true });
    await rules.getByText(ruleText, { exact: true }).waitFor();
    await rules.getByText("当前规则 · v1", { exact: true }).click();
    await rules.scrollIntoViewIfNeeded();
    const ruleMetrics = await rules.evaluate((element) => {
      const box = element.getBoundingClientRect();
      const buttons = [...element.querySelectorAll("button")].map((node) => node.getBoundingClientRect().toJSON());
      return { left: box.left, right: box.right, overflow: element.scrollWidth > element.clientWidth + 1, buttons };
    });
    assert(ruleMetrics.left >= 0 && ruleMetrics.right <= viewport.width + 1 && !ruleMetrics.overflow);
    assert(ruleMetrics.buttons.every((a, i, items) => items.every((b, j) => i === j || !(a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top))));
    await page.screenshot({ path: path.join(outputRoot, `rule-approval-${viewport.width}.png`), fullPage: true });
    await rules.getByRole("button", { name: "确认保存规则", exact: true }).click();
    await rules.getByText(ruleText, { exact: true }).waitFor({ state: "detached" });
    const retryReason = page.getByText("已有写入调用或执行恢复状态，请先核对已有结果，不能直接从头重新执行。", { exact: true });
    await retryReason.waitFor();
    assert.equal(await page.getByRole("button", { name: "重新执行", exact: true }).count(), 0);
    const card = page.locator(".agent-tool-approval-card").filter({ has: page.getByRole("heading", { name: "安装工作流创建的 Skill" }) });
    await card.getByText("执行标识", { exact: true }).click();
    await card.getByText(runID, { exact: true }).waitFor();
    await card.scrollIntoViewIfNeeded();
    const metrics = await card.evaluate((element) => {
      const box = element.getBoundingClientRect();
      const buttons = [...element.querySelectorAll(".approval-actions button")].map((node) => node.getBoundingClientRect().toJSON());
      return { left: box.left, right: box.right, width: innerWidth, scrollWidth: document.documentElement.scrollWidth,
        overflowingText: [...element.querySelectorAll("dd, code, h3")].some((node) => node.scrollWidth > node.clientWidth + 1 && getComputedStyle(node).display !== "inline"), buttons };
    });
    assert(metrics.left >= 0 && metrics.right <= viewport.width + 1);
    assert.equal(metrics.scrollWidth, metrics.width);
    assert.equal(metrics.overflowingText, false);
    assert.equal(metrics.buttons.length, 2);
    assert(metrics.buttons.every((button) => button.width >= 96 && button.height >= 34));
    const [a, b] = metrics.buttons;
    assert(!(a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top));
    await page.screenshot({ path: path.join(outputRoot, `origin-approval-${viewport.width}.png`), fullPage: true });
    await card.getByRole("button", { name: "允许调用", exact: true }).click();
    const background = page.locator(".agent-tool-approval-card").filter({ has: page.getByRole("heading", { name: "保存后台调研文件" }) });
    await background.getByRole("button", { name: "拒绝调用", exact: true }).click();
    await page.getByRole("button", { name: "暂停后台任务", exact: true }).click();
    await page.getByText("暂停中", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "继续后台任务", exact: true }).count(), 0);
    backgroundStatus = "paused";
    assert(streams.size > 0);
    for (const stream of streams) stream.write("event: agent_task.paused\ndata: {}\n\n");
    const resume = page.getByRole("button", { name: "继续后台任务", exact: true });
    await resume.waitFor();
    await resume.scrollIntoViewIfNeeded();
    const pauseMetrics = await resume.evaluate((element) => {
      const button = element.getBoundingClientRect();
      const card = element.closest(".agent-task-card");
      const box = card.getBoundingClientRect();
      return { width: button.width, height: button.height, left: box.left, right: box.right, overflow: card.scrollWidth > card.clientWidth + 1 };
    });
    assert(pauseMetrics.width >= 32 && pauseMetrics.height >= 32);
    assert(pauseMetrics.left >= 0 && pauseMetrics.right <= viewport.width + 1 && !pauseMetrics.overflow);
    await page.screenshot({ path: path.join(outputRoot, `background-paused-${viewport.width}.png`), fullPage: true });
    await resume.click();
    await page.getByRole("button", { name: "暂停后台任务", exact: true }).waitFor();
    const control = page.locator(".agent-tool-approval-card").filter({ has: page.getByRole("heading", { name: `允许暂停运行：${runID}`, exact: true }) });
    await page.getByRole("button", { name: "追加后台任务要求", exact: true }).click();
    const inputBox = page.getByRole("textbox", { name: "后台任务追加要求", exact: true });
    const appended = "请只检查人物动机，不修改已有文件。" + "longword".repeat(20);
    await inputBox.fill(appended);
    await page.locator(".agent-task-card").filter({ has: inputBox }).scrollIntoViewIfNeeded();
    const inputMetrics = await inputBox.evaluate((element) => {
      const parent = element.closest(".agent-task-card");
      const box = parent.getBoundingClientRect();
      return { left: box.left, right: box.right, overflow: parent.scrollWidth > parent.clientWidth + 1 };
    });
    assert(inputMetrics.left >= 0 && inputMetrics.right <= viewport.width + 1 && !inputMetrics.overflow);
    await page.screenshot({ path: path.join(outputRoot, `background-input-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "提交追加要求", exact: true }).click();
    await page.getByText("追加要求 · 1", { exact: true }).click();
    await page.getByText("已接收，待送入模型", { exact: true }).waitFor();
    assert.equal(additionalInputs[0].content, appended);
    backgroundStatus = "paused";
    for (const stream of streams) stream.write("event: agent_task.paused\ndata: {}\n\n");
    await page.getByRole("button", { name: "继续后台任务", exact: true }).waitFor();
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByRole("tab", { name: "进度", exact: true }).click();
    await page.getByText("追加要求 · 1", { exact: true }).click();
    await page.getByText("已接收，待送入模型", { exact: true }).waitFor();
    await page.getByRole("note").filter({ hasText: "先处理已批准的工具" }).waitFor();
    additionalInputs[0].status = "included";
    backgroundStatus = "running";
    for (const stream of streams) stream.write("event: agent_task.progressed\ndata: {}\n\n");
    await page.getByText("已送入模型", { exact: true }).waitFor();
    await control.scrollIntoViewIfNeeded();
    const controlMetrics = await control.evaluate((element) => {
      const box = element.getBoundingClientRect();
      return { left: box.left, right: box.right, overflow: element.scrollWidth > element.clientWidth + 1 };
    });
    assert(controlMetrics.left >= 0 && controlMetrics.right <= viewport.width + 1 && !controlMetrics.overflow);
    await page.screenshot({ path: path.join(outputRoot, `control-approval-${viewport.width}.png`), fullPage: true });
    await control.getByRole("button", { name: "允许调用", exact: true }).click();
    await retryReason.scrollIntoViewIfNeeded();
    const retryMetrics = await retryReason.evaluate((element) => ({ overflow: element.scrollWidth > element.clientWidth + 1 }));
    assert.equal(retryMetrics.overflow, false);
    await page.screenshot({ path: path.join(outputRoot, `retry-blocked-${viewport.width}.png`), fullPage: true });
    await page.getByText("未关联的工具调用", { exact: true }).waitFor({ state: "detached" });
    const history = page.getByRole("region", { name: "Agent 执行记录", exact: true });
    const summaries = history.locator(":scope > .execution-turn > summary");
    assert.equal(await summaries.count(), 3);
    for (const summary of await summaries.all()) await summary.click();
    await history.getByText("workflow_tool", { exact: true }).waitFor();
    await history.getByText("background_tool", { exact: true }).waitFor();
    await history.getByText("read_workspace_file", { exact: true }).waitFor();
    const historyMetrics = await history.evaluate((element) => ({ overflow: element.scrollWidth > element.clientWidth + 1, groups: element.querySelectorAll(":scope > .execution-turn").length }));
    assert.equal(historyMetrics.overflow, false);
    await history.locator(".execution-turn").last().scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(outputRoot, `origin-history-${viewport.width}.png`), fullPage: true });
    assert.equal(requests.length, requestCount + 9);
    await page.goto(new URL("/", url).href, { waitUntil: "domcontentloaded" });
    await page.getByRole("link", { name: "Agent 规则", exact: true }).waitFor();
    const headerMetrics = await page.locator(".global-header").evaluate((element) => {
      const boxes = [...element.querySelectorAll(".brand, .workspace-label, .header-link, .result-button")].filter((node) => getComputedStyle(node).display !== "none").map((node) => node.getBoundingClientRect().toJSON());
      const brand = element.querySelector(".brand").getBoundingClientRect();
      return { overflow: document.documentElement.scrollWidth > innerWidth, height: element.getBoundingClientRect().height, brandHeight: brand.height, boxes };
    });
    await page.screenshot({ path: path.join(outputRoot, `global-navigation-${viewport.width}.png`), fullPage: true });
    assert.equal(headerMetrics.overflow, false);
    assert(headerMetrics.brandHeight <= 34);
    assert(headerMetrics.boxes.every((a, i, boxes) => boxes.every((b, j) => i === j || !(a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top))));
    assert(await page.locator(".recent-projects .page-heading h2").evaluate((node) => node.getBoundingClientRect().width >= 84));
    assert.deepEqual(errors, []);
    assert.deepEqual(unhandled, []);
    results.push({ viewport, metrics, historyMetrics, controlMetrics, retryMetrics, pauseMetrics, inputMetrics, ruleMetrics, headerMetrics, inputReceiptReload: true, inputIncludedSSE: true, preservedWaitingTask: true, pausedReload: true, pauseSSE: true, approvalAndRejection: true, unsafeRetryHidden: true });
    await context.close();
  }
} finally {
  await browser?.close();
  if (fixture) { fixture.closeAllConnections(); await new Promise((resolve) => fixture.close(resolve)); }
  await vite?.close();
}
await fs.writeFile(path.join(outputRoot, "report.json"), JSON.stringify({ kind: "mocked-api-ui-not-real-agent-acceptance", results }, null, 2));
process.stdout.write(JSON.stringify({ status: "passed", outputRoot, results }, null, 2) + "\n");
