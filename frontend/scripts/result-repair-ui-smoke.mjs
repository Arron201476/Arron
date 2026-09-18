import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { once } from "node:events";
import { createServer as createViteServer } from "vite";
import react from "@vitejs/plugin-react";
import { chromium } from "playwright";

const root = fileURLToPath(new URL("../", import.meta.url));
const output = fileURLToPath(new URL("../../.tmp/goal-g418-input-attachment-ui/", import.meta.url));
const at = "2026-09-07T00:00:00Z";
const project = { project_id: "prj_repair_fixture", title: "结果修复验收", workspace_id: "fixture-workspace", primary_conversation_id: "repair-conversation", active_write_run_id: "repair-run", version: 1, status: "running", updated_at: at };
const material = { asset_id: "attachment-asset", project_id: project.project_id, current_snapshot_id: "attachment-snapshot", original_filename: "追加材料-".repeat(12) + "notes.txt", kind: "text", status: "available", parse_status: "completed", size_bytes: 42 };
let runStatus, taskStatus, repairStatus;
let inputs = [];
const streams = new Set();
const requests = [], errors = [], reports = [];
const changes = [];
function run() {
  return { run: { run_id: "repair-run", capability_id: "novel_to_script", status: runStatus, current_step_run_id: "repair-step", created_at: at },
    steps: [{ step_run_id: "repair-step", run_id: "repair-run", step_id: "build_story_bible", status: runStatus, attempt_count: 1 }],
    task_items: [{ task_item_id: "repair-task", step_run_id: "repair-step", item_key: "preparation:source_analysis", item_order: 1, status: taskStatus, attempt_count: 1, current_attempt_id: "repair-attempt",
      failure: taskStatus === "failed" ? "OUTPUT_REPAIR_FAILED" : null,
      output_repair: { status: repairStatus, rejection_no: taskStatus === "failed" ? 2 : 1, error_code: "BATCH_COVERAGE_INVALID" } }],
    current_approval: null, available_actions: [
      ...(runStatus === "running" ? [{ action_id: "pause_run" }] : runStatus === "paused" ? [{ action_id: "resume_run" }] : []),
      { action_id: "cancel_run" },
    ].map((action) => ({ ...action, target_type: "run", target_id: "repair-run", enabled: true, disabled_reason: null })) };
}
function projection() {
  return { contract_version: "workspace_projection.v1", registry_revision: "repair-fixture",
    registries: { artifacts: [], navigation: [], approvals: [], composer: [], tasks: [], interactions: [] },
    snapshot: { project, goal: null, active_run: run(), latest_run: null, artifacts: [], approvals: [], script_candidates: [], final_selection: null,
      pending_proposed_actions: [], proposed_actions: [], activities: [], revision_requests: [], target_resolutions: [], agent_tasks: [], agent_turns: [], agent_tool_calls: [],
      messages: [{ message_id: "fixture-message", role: "user", content: "分析原文，保留已有工作文件。", created_at: at }] } };
}
function emit(event = "task.progressed") {
  assert(streams.size > 0, "SSE must be connected");
  for (const stream of streams) stream.write(`event: ${event}\ndata: {}\n\n`);
}
await fs.mkdir(output, { recursive: true });
let vite, fixture, browser;
try {
  vite = await createViteServer({ root, configFile: false, plugins: [react()], server: { host: "127.0.0.1", port: 0, hmr: false, proxy: {} } });
  await vite.listen();
  const vitePort = vite.httpServer.address().port;
  assert(![8860, 8880].includes(vitePort));
  fixture = createServer(async (request, response) => {
    try {
      const url = new URL(request.url, "http://127.0.0.1");
      if (!url.pathname.startsWith("/api/")) {
        const upstream = await fetch(new URL(request.url, `http://127.0.0.1:${vitePort}`));
        response.writeHead(upstream.status, { "Content-Type": upstream.headers.get("Content-Type") ?? "application/octet-stream" }).end(Buffer.from(await upstream.arrayBuffer()));
        return;
      }
      if (url.pathname.endsWith("/events/stream")) {
        response.writeHead(200, { "Content-Type": "text/event-stream" });
        streams.add(response);
        response.on("close", () => streams.delete(response));
        response.write("event: ready\ndata: {}\n\n");
        return;
      }
      let data;
      if (request.method === "GET" && url.pathname === "/api/v1/auth/me") data = { kind: "user", user_id: "fixture-user", display_name: "Fixture", workspace_id: project.workspace_id, workspace_name: "本地隔离验收", role: "owner", auth_method: "fixture" };
      else if (request.method === "GET" && url.pathname.endsWith("/workspace-projection")) data = projection();
      else if (request.method === "GET" && url.pathname.endsWith("/assets")) data = { items: [material] };
      else if (request.method === "POST" && url.pathname === `/api/v1/projects/${project.project_id}/execution-input-changes`) {
        assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
        let raw = "";
        for await (const chunk of request) raw += chunk;
        const body = JSON.parse(raw);
        assert.equal(body.mode, "stateful_workflow"); assert.equal(body.execution_id, "repair-attempt");
        const original = inputs.find((input) => input.input_id === body.input_id);
        assert(original?.can_modify && original.status === "received");
        original.can_modify = false;
        original.status = body.action === "revise" ? "superseded" : "withdrawn";
        data = { mode: body.mode, execution_id: body.execution_id, input_id: body.input_id, status: original.status };
        if (body.action === "revise") {
          data.replacement_input_id = `input-${inputs.length}`;
          original.replacement_input_id = data.replacement_input_id;
          inputs.push({ ...original, input_id: data.replacement_input_id, sequence: inputs.length + 1, content: body.content, status: "received", can_modify: true, replacement_input_id: undefined });
        } else assert.equal(body.action, "withdraw");
        changes.push(data);
      }
      else if (request.method === "GET" && url.pathname === `/api/v1/projects/${project.project_id}/runs/repair-run/input-attempts`) data = { items: [{ attempt_id: "previous-attempt", task_item_id: "repair-task", item_key: "preparation:source_analysis:" + "long-key-".repeat(15), attempt_no: 1, status: "failed", input_count: 1, included_count: 1, current: false }], next_cursor: "" };
      else if (request.method === "GET" && url.pathname === `/api/v1/projects/${project.project_id}/execution-attempts/previous-attempt/inputs`) data = { attempt_id: "previous-attempt", run_id: "repair-run", task_item_id: "repair-task", status: "failed", can_append: false, inputs: [{ input_id: "previous-input", attempt_id: "previous-attempt", user_id: "fixture-user", sequence: 1, content: "上一尝试的追加要求：保留角色原名。", content_hash: "old-input-hash", status: "included", created_at: at }] };
      else if (url.pathname === `/api/v1/projects/${project.project_id}/execution-attempts/repair-attempt/inputs`) {
        if (request.method === "GET") data = { attempt_id: "repair-attempt", run_id: "repair-run", task_item_id: "repair-task", status: taskStatus, can_append: ["running", "paused", "repair_pending"].includes(taskStatus), inputs };
        else if (request.method === "POST") {
          assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
          let raw = "";
          for await (const chunk of request) raw += chunk;
          const body = JSON.parse(raw);
          assert.deepEqual(Object.keys(body), inputs.length === 2 ? ["content", "attachment_refs"] : ["content"]);
          if (body.attachment_refs) assert.deepEqual(body.attachment_refs, [{ asset_id: material.asset_id, asset_snapshot_id: material.current_snapshot_id }]);
          data = { input_id: `input-${inputs.length}`, attempt_id: "repair-attempt", user_id: "fixture-user", sequence: inputs.length + 1, content: body.content, content_hash: "fixture-hash", status: "received", created_at: at, can_modify: true };
          if (body.attachment_refs) data.attachments = [{ asset_id: material.asset_id, asset_snapshot_id: material.current_snapshot_id, name: material.original_filename, kind: material.kind, size_bytes: material.size_bytes, mime_type: "text/plain", checksum: "0".repeat(64) }];
          inputs.push(data);
        } else throw new Error("Unexpected input request");
      }
      else if (request.method === "POST" && ["/api/v1/runs/repair-run/pause", "/api/v1/runs/repair-run/resume"].includes(url.pathname)) {
        assert.match(request.headers["idempotency-key"], /^[a-f0-9-]{36}$/i);
        requests.push(url.pathname);
        runStatus = url.pathname.endsWith("/pause") ? "paused" : "running";
        taskStatus = runStatus;
        data = run();
      } else throw new Error(`Unhandled fixture request: ${request.method} ${url.pathname}`);
      response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ data }));
    } catch (error) { errors.push(error.message); response.writeHead(500).end("Fixture failure"); }
  });
  fixture.listen(0, "127.0.0.1");
  await once(fixture, "listening");
  const port = fixture.address().port;
  assert(![8860, 8880].includes(port));
  browser = await chromium.launch({ channel: "msedge", headless: true });
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    runStatus = "running"; taskStatus = "repair_pending"; repairStatus = "queued";
    inputs = [];
    const context = await browser.newContext({ viewport });
    await context.route("**/*", (route) => {
      const url = new URL(route.request().url());
      if (url.hostname !== "127.0.0.1" || ![port, vitePort].includes(Number(url.port))) {
        errors.push(`Unexpected network origin: ${url.origin}`);
        return route.abort();
      }
      return route.continue();
    });
    const page = await context.newPage();
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${port}/projects/${project.project_id}`, { waitUntil: "domcontentloaded" });
    await page.getByRole("tab", { name: "进度", exact: true }).click();
    await page.getByText("等待修复", { exact: true }).waitFor();
    assert.equal(await page.locator(".task-status .task-spinner").count(), 0);
    taskStatus = "running"; repairStatus = "claimed"; emit();
    await page.getByText("正在修复结果", { exact: true }).waitFor();
    assert.equal(await page.locator(".task-status .task-spinner").count(), 1);
    await page.getByRole("button", { name: "任务追加要求", exact: true }).click();
    await page.getByRole("button", { name: "追加当前处理项要求", exact: true }).click();
    await page.getByRole("textbox", { name: "当前处理项追加要求", exact: true }).fill("保留原文件，只修复缺失章节的覆盖信息。");
    assert.equal(await page.getByRole("combobox", { name: "追加目标任务" }).isDisabled(), true);
    await page.getByRole("button", { name: "提交追加要求", exact: true }).click();
    await page.getByText("追加要求 · 1", { exact: true }).click();
    await page.getByText("已接收，待送入模型", { exact: true }).waitFor();
    const inputMetrics = await page.locator(".stateful-run-inputs").evaluate((element) => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth, overflow: element.scrollWidth > element.clientWidth + 1, select: element.querySelector("select").getBoundingClientRect().toJSON() }));
    assert.equal(inputMetrics.width, inputMetrics.scrollWidth);
    assert.equal(inputMetrics.overflow, false);
    assert(inputMetrics.select.width >= 60);
    await page.screenshot({ path: path.join(output, `inputs-received-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "修订追加要求", exact: true }).click();
    await page.getByRole("textbox", { name: "修订内容", exact: true }).fill("只补充遗漏的章节，不重写已有内容。" + "long-revision-".repeat(18));
    assert.equal(await page.getByRole("combobox", { name: "追加目标任务" }).isDisabled(), true);
    const editor = page.getByRole("group", { name: "修订追加要求", exact: true });
    await editor.scrollIntoViewIfNeeded();
    assert.equal(await editor.evaluate((node) => node.scrollWidth > node.clientWidth + 1), false);
    await page.screenshot({ path: path.join(output, `input-revision-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "提交修订要求", exact: true }).click();
    await page.getByText("追加要求 · 2", { exact: true }).waitFor();
    await page.getByText("已被新修订替代", { exact: true }).waitFor();
    await page.getByRole("button", { name: "撤回追加要求", exact: true }).click();
    await page.getByRole("button", { name: "确认撤回追加要求", exact: true }).click();
    await page.getByText("已撤回，未送入模型", { exact: true }).waitFor();
    await page.getByRole("button", { name: "追加当前处理项要求", exact: true }).click();
    await page.getByRole("textbox", { name: "当前处理项追加要求", exact: true }).fill("保留文件，仅修复章节覆盖。");
    await page.getByRole("button", { name: "选择追加项目材料", exact: true }).click();
    const picker = page.getByRole("dialog");
    await picker.getByRole("checkbox").check();
    await picker.getByRole("button", { name: /使用所选材料/ }).click();
    await page.getByRole("list", { name: "待追加材料", exact: true }).waitFor();
    assert.equal(await page.getByRole("combobox", { name: "追加目标任务" }).isDisabled(), true);
    await page.locator(".input-attachments").scrollIntoViewIfNeeded();
    assert.equal(await page.locator(".input-attachments").evaluate((node) => node.scrollWidth > node.clientWidth + 1), false);
    await page.screenshot({ path: path.join(output, `attachment-draft-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "提交追加要求", exact: true }).click();
    await page.getByText("追加要求 · 3", { exact: true }).waitFor();
    await page.getByRole("button", { name: "暂停任务", exact: true }).click();
    await page.getByText("修复已暂停", { exact: true }).waitFor();
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByRole("tab", { name: "进度", exact: true }).click();
    await page.getByRole("button", { name: "任务追加要求", exact: true }).click();
    await page.getByText("追加要求 · 3", { exact: true }).click();
    await page.getByRole("list", { name: "已接收追加材料", exact: true }).waitFor();
    assert.equal(await page.getByRole("list", { name: "已接收追加材料", exact: true }).innerText(), material.original_filename);
    await page.getByText("已接收，待送入模型", { exact: true }).waitFor();
    await page.getByText("已撤回，未送入模型", { exact: true }).waitFor();
    await page.getByText("已被新修订替代", { exact: true }).waitFor();
    await page.getByText("修复已暂停", { exact: true }).waitFor();
    assert.equal(await page.locator(".task-status .task-spinner").count(), 0);
    const metrics = await page.locator(".run-event").evaluate((element) => {
      const texts = [...element.querySelectorAll(".task-list span, .task-list strong")].map((node) => node.getBoundingClientRect().toJSON());
      const button = element.querySelector(".run-primary-controls button").getBoundingClientRect().toJSON();
      return { width: innerWidth, scrollWidth: document.documentElement.scrollWidth, textOverflow: [...element.querySelectorAll(".task-list span, .task-list strong")].some((node) => node.scrollWidth > node.clientWidth + 1), texts, button };
    });
    const report = { viewport, metrics, inputMetrics, states: [] };
    reports.push(report);
    assert.equal(metrics.width, metrics.scrollWidth);
    assert.equal(metrics.textOverflow, false);
    assert(metrics.texts[0].right <= metrics.texts[1].left + 1);
    assert(metrics.button.width >= 80 && metrics.button.height >= 30);
    await page.screenshot({ path: path.join(output, `repair-paused-${viewport.width}.png`), fullPage: true });
    await page.getByRole("button", { name: "继续任务", exact: true }).click();
    await page.getByText("正在修复结果", { exact: true }).waitFor();
    inputs[2].status = "included"; inputs[2].can_modify = false; emit("execution.inputs_included");
    await page.getByText("已送入模型", { exact: true }).waitFor();
    repairStatus = "closed"; emit();
    await page.getByText("正在校验结果", { exact: true }).waitFor();
    runStatus = "failed"; taskStatus = "failed"; repairStatus = "terminal"; emit("task.failed");
    await page.locator(".task-status").getByText("失败", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "重试失败项", exact: true }).count(), 0);
    await page.screenshot({ path: path.join(output, `repair-failed-${viewport.width}.png`), fullPage: true });
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByRole("tab", { name: "进度", exact: true }).click();
    await page.getByRole("button", { name: "历史追加记录", exact: true }).click();
    const history = page.getByRole("region", { name: "历史追加记录列表", exact: true });
    await history.getByRole("button", { name: /第 1 次尝试/ }).waitFor();
    await history.scrollIntoViewIfNeeded();
    const historyMetrics = await history.evaluate((node) => ({ overflow: node.scrollWidth > node.clientWidth + 1, left: node.getBoundingClientRect().left, right: node.getBoundingClientRect().right }));
    assert(historyMetrics.left >= 0 && historyMetrics.right <= viewport.width + 1 && !historyMetrics.overflow);
    await page.screenshot({ path: path.join(output, `history-list-${viewport.width}.png`), fullPage: true });
    await history.getByRole("button", { name: /第 1 次尝试/ }).click();
    await page.getByText("追加要求 · 1", { exact: true }).click();
    await page.getByText("上一尝试的追加要求：保留角色原名。", { exact: true }).waitFor();
    await page.getByText("已送入模型", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "追加当前处理项要求", exact: true }).count(), 0);
    assert.equal(await page.getByRole("combobox", { name: "追加目标任务" }).inputValue(), "previous-attempt");
    await page.screenshot({ path: path.join(output, `history-selected-${viewport.width}.png`), fullPage: true });
    report.historyMetrics = historyMetrics;
    report.historyAfterReload = true;
    report.states = ["queued", "claimed", "paused", "reload", "resume", "validating", "failed"];
    await context.close();
  }
  assert.deepEqual(errors, []);
  assert.equal(requests.length, 4);
  assert.equal(changes.length, 4);
  await fs.writeFile(path.join(output, "report.json"), JSON.stringify({ evidence: "mock-api actual App UI", modelCalls: 0, userBackendAccess: false, reports, requests, errors }, null, 2));
  console.log(JSON.stringify({ output, viewports: reports.length, requests: requests.length, errors }));
} catch (error) {
  await fs.writeFile(path.join(output, "failure.json"), JSON.stringify({ error: error.message, requests, errors, runStatus, taskStatus, repairStatus, reports }, null, 2));
  if (browser?.isConnected()) {
    const page = browser.contexts().at(-1)?.pages().at(-1);
    if (page) await page.screenshot({ path: path.join(output, "failure.png"), fullPage: true });
  }
  throw error;
} finally {
  if (browser) await browser.close();
  for (const stream of streams) stream.end();
  if (fixture) { fixture.closeAllConnections(); await new Promise((resolve) => fixture.close(resolve)); }
  if (vite) await vite.close();
}
