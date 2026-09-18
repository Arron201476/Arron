import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const baseURL = process.env.CONTENT_AGENT_RUNTIME_URL ?? "http://127.0.0.1:8850";
const outputPath = resolve(
  process.env.CONTENT_AGENT_STAGE9_EVIDENCE ??
    "acceptance/evidence/stage9-core-smoke.json",
);
const clientID = `stage9-smoke-${crypto.randomUUID()}`;

async function request(path, init = {}) {
  const headers = new Headers(init.headers);
  if (init.body && typeof init.body === "string") {
    headers.set("Content-Type", "application/json");
  }
  if (init.method && init.method !== "GET") {
    headers.set("Idempotency-Key", crypto.randomUUID());
    headers.set("X-Client-Instance-ID", clientID);
  }
  const response = await fetch(`${baseURL}${path}`, { ...init, headers });
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(
      `${init.method ?? "GET"} ${path} failed: ${response.status} ${JSON.stringify(payload)}`,
    );
  }
  return payload?.data;
}

const startedAt = new Date().toISOString();
const title = `Stage 9 core smoke ${Date.now()}`;
const checks = [];

function pass(name, details = {}) {
  checks.push({ name, status: "pass", ...details });
}

const health = await request("/healthz");
if (!health?.agent_core_available || !health?.runtime_available) {
  throw new Error(`Runtime is not ready: ${JSON.stringify(health)}`);
}
pass("health", {
  available_capability_count: health.available_capability_count,
});

const capabilityList = await request("/api/v1/capabilities");
const capabilityIDs = capabilityList.items.map((item) => item.capability_id).sort();
if (capabilityIDs.length < 3) {
  throw new Error(`Expected at least three capabilities, got ${capabilityIDs.length}`);
}
pass("capability_registry", { capability_ids: capabilityIDs });

let project = await request("/api/v1/projects", {
  method: "POST",
  body: JSON.stringify({ title }),
});
if (!project?.project_id || !project?.primary_conversation_id) {
  throw new Error(`Project response is incomplete: ${JSON.stringify(project)}`);
}
pass("create_project", { project_id: project.project_id });

const snapshot = await request(`/api/v1/projects/${project.project_id}/snapshot`);
if (snapshot.project?.project_id !== project.project_id) {
  throw new Error("Project snapshot does not match the created project");
}
pass("project_snapshot");

const exchange = await request(
  `/api/v1/conversations/${project.primary_conversation_id}/messages`,
  {
    method: "POST",
    body: JSON.stringify({
      content: "请简短回复：阶段九连接正常。",
      display_content: "请简短回复：阶段九连接正常。",
      capability_ref: null,
      attachment_refs: [],
      selection_snapshot: null,
      client_context: {
        locale: "zh-CN",
        timezone: "Asia/Shanghai",
        surface: "stage9_api_smoke",
        current_view: "workbench",
      },
    }),
  },
);
if (!exchange?.agent_message?.content?.trim()) {
  throw new Error("General Agent returned an empty message");
}
pass("general_agent_chat", {
  intent: exchange.agent_decision?.decision?.intent ?? null,
  response_length: exchange.agent_message.content.length,
});

project = await request(`/api/v1/projects/${project.project_id}`, {
  method: "PATCH",
  body: JSON.stringify({
    title: `${title} renamed`,
    expected_version: project.version,
  }),
});
if (!project.title.endsWith("renamed")) {
  throw new Error("Project rename did not persist");
}
pass("rename_project", { version: project.version });

const preview = await request(
  `/api/v1/projects/${project.project_id}/delete-previews`,
  { method: "POST" },
);
if (!preview?.snapshot_hash || preview.project_id !== project.project_id) {
  throw new Error("Delete impact preview is incomplete");
}
pass("project_delete_preview", {
  source_files_will_be_deleted: preview.source_files_will_be_deleted,
  derived_records_retained_for_audit: preview.derived_records_retained_for_audit,
});

const result = {
  execution_id: crypto.randomUUID(),
  started_at: startedAt,
  finished_at: new Date().toISOString(),
  environment: "trusted_demo_runtime_via_ssh_tunnel",
  base_url: baseURL,
  project_id: project.project_id,
  checks,
  summary: {
    pass: checks.filter((item) => item.status === "pass").length,
    fail: checks.filter((item) => item.status === "fail").length,
  },
};

await mkdir(dirname(outputPath), { recursive: true });
await writeFile(outputPath, `${JSON.stringify(result, null, 2)}\n`, "utf8");
process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
