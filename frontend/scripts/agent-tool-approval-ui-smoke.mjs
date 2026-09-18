import { chromium } from "playwright";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const outputRoot = fileURLToPath(new URL("../../.tmp/agent-tool-approval-ui/", import.meta.url));
const projectID = "prj_agent_tool_approval_ui";
const conversationID = "conv_agent_tool_approval_ui";
const turnID = "turn_agent_tool_approval_ui";
const callID = "call_agent_tool_approval_ui";
const approvalID = "approval_agent_tool_approval_ui";
const requestedAt = "2026-09-05T05:00:01Z";

await fs.mkdir(outputRoot, { recursive: true });

const project = {
  project_id: projectID,
  workspace_id: "workspace_local_default",
  owner_user_id: "user_local_default",
  title: "SDK 工具审批验证",
  version: 1,
  status: "ready",
  primary_conversation_id: conversationID,
  active_write_run_id: null,
  current_capability_id: null,
  latest_capability_id: null,
  latest_run_status: null,
  current_focus_artifact_version_id: null,
  updated_at: requestedAt,
};

const registries = {
  artifacts: [],
  navigation: [],
  tasks: [],
  approvals: [],
  interactions: [{
    registry_key: "agent_tool",
    view_key: "agent_tool_approval",
    commands: ["approve", "deny"],
  }],
  composer: [],
};

function json(data, status = 200) {
  return { status, contentType: "application/json", body: JSON.stringify({ data }) };
}

function snapshot(resolved) {
  const approvalStatus = resolved ? "approved" : "pending";
  const callStatus = resolved ? "approved" : "pending_approval";
  const turnStatus = resolved ? "accepted" : "waiting_approval";
  return {
    project,
    goal: null,
    messages: [{
      message_id: "msg_agent_tool_approval_ui",
      role: "user",
      content: "把当前草稿保存到项目文件。",
      created_at: "2026-09-05T05:00:00Z",
    }],
    artifacts: [],
    approvals: [],
    capabilities: [],
    artifact_presentations: [],
    script_candidates: [],
    final_selection: null,
    active_run: null,
    latest_run: null,
    pending_proposed_actions: [],
    proposed_actions: [],
    activities: [],
    revision_requests: [],
    target_resolutions: [],
    agent_tasks: [],
    agent_turns: [{
      agent_turn_id: turnID,
      workspace_id: project.workspace_id,
      project_id: projectID,
      conversation_id: conversationID,
      status: turnStatus,
      request: { content: "把当前草稿保存到项目文件。" },
      user_message_id: "msg_agent_tool_approval_ui",
      created_at: "2026-09-05T05:00:00Z",
      started_at: "2026-09-05T05:00:00Z",
      updated_at: resolved ? "2026-09-05T05:00:03Z" : requestedAt,
    }],
    agent_tool_calls: [{
      agent_tool_call_id: callID,
      workspace_id: project.workspace_id,
      project_id: projectID,
      conversation_id: conversationID,
      agent_turn_id: turnID,
      sdk_tool_call_id: "sdk_call_agent_tool_approval_ui",
      tool_id: "mcp:workspace-files/write_file",
      tool_kind: "mcp",
      server_id: "workspace-files",
      tool_name: "write_file",
      access_mode: "write",
      approval_policy: "always",
      approval_status: approvalStatus,
      status: callStatus,
      arguments_summary: {
        path: "drafts/episode-01.txt",
        bytes: 18420,
        content: "[REDACTED]",
      },
      requested_at: requestedAt,
      updated_at: resolved ? "2026-09-05T05:00:03Z" : requestedAt,
      approval: {
        agent_tool_approval_id: approvalID,
        agent_tool_call_id: callID,
        workspace_id: project.workspace_id,
        project_id: projectID,
        conversation_id: conversationID,
        status: approvalStatus,
        version: resolved ? 2 : 1,
        title: "允许 Agent 写入项目文件？",
        reason: "该操作会创建或覆盖 drafts/episode-01.txt。",
        options: ["approve", "reject"],
        subject_snapshot_hash: "sha256:approval-ui-fixture",
        requested_at: requestedAt,
        resolved_at: resolved ? "2026-09-05T05:00:03Z" : null,
        actor_ref: resolved ? "user_local_default" : null,
      },
    }],
  };
}

async function installRoutes(page, state) {
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (pathname === `/api/v1/projects/${projectID}/workspace-projection`) {
      return route.fulfill(json({
        contract_version: "workspace_projection.v1",
        registry_revision: "approval-ui-fixture",
        snapshot: snapshot(state.resolved),
        registries,
      }));
    }
    if (pathname === `/api/v1/projects/${projectID}/assets`) {
      return route.fulfill(json({ items: [] }));
    }
    if (pathname === `/api/v1/projects/${projectID}/events/stream`) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" });
    }
    if (pathname === `/api/v1/agent-tool-approvals/${approvalID}/resolutions` && request.method() === "POST") {
      const body = request.postDataJSON();
      if (body.action !== "approve" || body.expected_version !== 1 || body.subject_snapshot_hash !== "sha256:approval-ui-fixture") {
        throw new Error(`unexpected approval payload: ${JSON.stringify(body)}`);
      }
      state.resolved = true;
      state.resolveRequests += 1;
      return route.fulfill(json(snapshot(true).agent_tool_calls[0].approval));
    }
    return route.fulfill(json({}));
  });
}

function overlaps(left, right) {
  return left.left < right.right && left.right > right.left && left.top < right.bottom && left.bottom > right.top;
}

async function inspect(page, viewport) {
  const metrics = await page.evaluate(() => {
    const card = document.querySelector(".agent-tool-approval-card")?.getBoundingClientRect();
    const actions = [...document.querySelectorAll(".agent-tool-approval-card .approval-actions button")].map((node) => node.getBoundingClientRect());
    return {
      innerWidth,
      scrollWidth: document.documentElement.scrollWidth,
      card: card ? { left: card.left, right: card.right, top: card.top, bottom: card.bottom, width: card.width } : null,
      actions: actions.map((item) => ({ left: item.left, right: item.right, top: item.top, bottom: item.bottom, width: item.width, height: item.height })),
    };
  });
  if (!metrics.card) throw new Error(`${viewport.width}: approval card is missing`);
  if (metrics.scrollWidth > metrics.innerWidth) throw new Error(`${viewport.width}: horizontal overflow ${metrics.scrollWidth}`);
  if (metrics.card.left < 0 || metrics.card.right > metrics.innerWidth + 1) throw new Error(`${viewport.width}: approval card is outside viewport`);
  if (metrics.actions.length !== 2) throw new Error(`${viewport.width}: expected two approval actions`);
  if (metrics.actions.some((item) => item.width < 96 || item.height < 34)) {
    throw new Error(`${viewport.width}: approval action is undersized: ${JSON.stringify(metrics.actions)}`);
  }
  if (overlaps(metrics.actions[0], metrics.actions[1])) throw new Error(`${viewport.width}: approval actions overlap`);
  return metrics;
}

const browser = await chromium.launch({ channel: "msedge", headless: true });
const report = [];
try {
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    const state = { resolved: false, resolveRequests: 0 };
    await installRoutes(page, state);
    await page.goto(`${baseURL}/projects/${projectID}`, { waitUntil: "networkidle" });
    await page.getByRole("heading", { name: "允许 Agent 写入项目文件？" }).waitFor();
    const metrics = await inspect(page, viewport);
    await page.screenshot({ path: path.join(outputRoot, `approval-${viewport.width}.png`), fullPage: true });

    if (viewport.width === 1440) {
      await page.getByText("查看已脱敏参数", { exact: true }).click();
      await page.getByText("[REDACTED]", { exact: false }).waitFor();
      await page.getByRole("button", { name: "允许调用" }).click();
      await page.getByText("已授权，正在恢复", { exact: false }).waitFor();
      if (state.resolveRequests !== 1) throw new Error(`approval requests = ${state.resolveRequests}, want 1`);
      if (await page.getByRole("button", { name: "允许调用" }).count()) throw new Error("resolved approval still exposes action buttons");
      await page.screenshot({ path: path.join(outputRoot, "approval-approved-1440.png"), fullPage: true });
    }
    report.push({ viewport, metrics });
    await context.close();
  }
} finally {
  await browser.close();
}

await fs.writeFile(path.join(outputRoot, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
process.stdout.write(`${JSON.stringify({ status: "passed", outputRoot, report }, null, 2)}\n`);
