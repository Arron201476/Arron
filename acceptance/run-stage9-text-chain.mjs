import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { basename, dirname, extname, resolve } from "node:path";

const args = Object.fromEntries(
  process.argv.slice(2).map((item) => {
    const [key, ...value] = item.replace(/^--/, "").split("=");
    return [key, value.join("=")];
  }),
);
const capabilityID = args.capability;
const fixturePath = resolve(args.fixture ?? "");
if (!capabilityID || !["novel_to_script", "non_novel_to_script"].includes(capabilityID)) {
  throw new Error("--capability must be novel_to_script or non_novel_to_script");
}
if (!args.fixture) throw new Error("--fixture is required");

const baseURL = process.env.CONTENT_AGENT_RUNTIME_URL ?? "http://127.0.0.1:8850";
const evidencePath = resolve(
  args.output ?? `acceptance/evidence/stage9-${capabilityID}.json`,
);
const clientID = `stage9-${capabilityID}-${randomUUID()}`;
const source = await readFile(fixturePath);
const timeline = [];
const startedAt = new Date().toISOString();
let currentProjectID = null;
let currentRunID = null;

async function writeFailureEvidence(reason) {
  const evidence = {
    execution_id: randomUUID(),
    started_at: startedAt,
    finished_at: new Date().toISOString(),
    environment: "real_provider_demo_via_ssh_tunnel",
    capability: { id: capabilityID },
    fixture: {
      name: basename(fixturePath),
      extension: extname(fixturePath),
      bytes: source.length,
      sha256: createHash("sha256").update(source).digest("hex"),
    },
    project_id: currentProjectID,
    run_id: currentRunID,
    final_status: "failed",
    error: reason instanceof Error ? reason.message : String(reason),
    timeline,
  };
  await mkdir(dirname(evidencePath), { recursive: true });
  await writeFile(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
}

async function handleFatalFailure(reason) {
  try {
    await writeFailureEvidence(reason);
  } catch (evidenceError) {
    process.stderr.write(`Failed to write failure evidence: ${evidenceError}\n`);
  }
  process.stderr.write(`${reason instanceof Error ? reason.stack : reason}\n`);
  process.exit(1);
}

process.on("unhandledRejection", handleFatalFailure);
process.on("uncaughtException", handleFatalFailure);

async function request(path, init = {}) {
  const headers = new Headers(init.headers);
  if (typeof init.body === "string" && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (init.method && init.method !== "GET") {
    headers.set("Idempotency-Key", randomUUID());
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

function record(event, details = {}) {
  const entry = { at: new Date().toISOString(), event, ...details };
  timeline.push(entry);
  process.stdout.write(`${entry.at} ${event} ${JSON.stringify(details)}\n`);
}

async function upload(projectID) {
  const filename = basename(fixturePath);
  const session = await request(`/api/v1/projects/${projectID}/upload-sessions`, {
    method: "POST",
    body: JSON.stringify({
      items: [{
        client_item_key: `source-${randomUUID()}`,
        kind: "text",
        original_filename: filename,
        declared_mime_type: "text/plain",
        declared_size_bytes: source.length,
      }],
    }),
  });
  const item = session.items[0];
  await request(`/api/v1/upload-items/${item.upload_item_id}/content`, {
    method: "PUT",
    headers: { "Content-Type": "text/plain" },
    body: source,
  });
  return request(`/api/v1/upload-items/${item.upload_item_id}/complete`, {
    method: "POST",
  });
}

async function resolveApproval(approval) {
  if (approval.scope === "quality_review") {
    const review = await request(`/api/v1/quality-reviews/${approval.subject_ref_id}`);
    if (!approval.options.includes("accept_with_risk") || review.issue_counts.blocker > 0) {
      throw new Error(
        `Quality review requires non-automatic action: ${JSON.stringify({ approval, review })}`,
      );
    }
    await request(`/api/v1/quality-reviews/${approval.subject_ref_id}/actions`, {
      method: "POST",
      body: JSON.stringify({
        expected_review_status: "action_required",
        input_snapshot_hash: approval.subject_snapshot_hash,
        action: "accept_with_risk",
        actor_ref: "stage9_automation",
        ignored_issue_ids: [],
      }),
    });
    record("quality_review_accepted", {
      approval_request_id: approval.approval_request_id,
      issue_counts: review.issue_counts,
    });
    return;
  }
  if (!approval.options.includes("approve")) {
    throw new Error(`Approval has no automatic approve action: ${JSON.stringify(approval)}`);
  }
  const resolutionPayload = approval.subject_kind === "transition"
    ? { expansion_strategy: "仅补充必要因果桥段和过渡，不新增关键设定和主线。" }
    : null;
  const resolutionBody = {
    action: "approve",
    expected_approval_version: approval.version,
    subject_snapshot_hash: approval.subject_snapshot_hash,
  };
  if (resolutionPayload) resolutionBody.resolution_payload = resolutionPayload;
  await request(`/api/v1/approvals/${approval.approval_request_id}/resolutions`, {
    method: "POST",
    body: JSON.stringify(resolutionBody),
  });
  record("approval_resolved", {
    approval_request_id: approval.approval_request_id,
    scope: approval.scope,
    subject_kind: approval.subject_kind,
    subject_ref_id: approval.subject_ref_id,
  });
}

const capabilities = await request("/api/v1/capabilities");
const capability = capabilities.items.find((item) => item.capability_id === capabilityID);
if (!capability || capability.status !== "available") {
  throw new Error(`Capability is not available: ${JSON.stringify(capability)}`);
}

let project = await request("/api/v1/projects", {
  method: "POST",
  body: JSON.stringify({ title: `Stage 9 ${capabilityID} ${Date.now()}` }),
});
currentProjectID = project.project_id;
record("project_created", { project_id: project.project_id });

const uploaded = await upload(project.project_id);
const attachment = {
  asset_id: uploaded.asset.asset_id,
  asset_snapshot_id: uploaded.asset_snapshot.asset_snapshot_id,
};
record("source_uploaded", attachment);

const exchange = await request(
  `/api/v1/conversations/${project.primary_conversation_id}/messages`,
  {
    method: "POST",
    body: JSON.stringify({
      content: capabilityID === "novel_to_script" ? "请将上传的小说转成剧本。" : "请将上传的非小说材料转成剧本。",
      display_content: capabilityID === "novel_to_script" ? "请将上传的小说转成剧本。" : "请将上传的非小说材料转成剧本。",
      capability_ref: {
        capability_id: capability.capability_id,
        version: capability.version,
        selection_mode: "explicit",
      },
      attachment_refs: [attachment],
      selection_snapshot: null,
      client_context: {
        locale: "zh-CN",
        timezone: "Asia/Shanghai",
        surface: "stage9_api_e2e",
        current_view: "workbench",
      },
    }),
  },
);
if (!exchange.proposed_action) throw new Error("Agent did not create a proposed action");
record("proposed_action_created", {
  proposed_action_id: exchange.proposed_action.proposed_action_id,
  intent: exchange.agent_decision?.decision?.intent ?? null,
});

let action = exchange.proposed_action;
const sourceType = capabilityID === "novel_to_script" ? "novel" : "non_novel";
action = await request(`/api/v1/proposed-actions/${action.proposed_action_id}/configuration`, {
  method: "POST",
  body: JSON.stringify({
    expected_version: action.version,
    input: {
      ...action.input,
      project_id: project.project_id,
      source_type: sourceType,
      assets: [{ ...attachment, role: "primary_source", order: 1 }],
      user_request_message_id: exchange.user_message.message_id,
      user_notes: [],
    },
    config: {
      config_ref: "creation",
      payload: {
        target_episode_count: 1,
        episode_duration_minutes: 1,
        preserve_existing_episode_marks: true,
        expansion_policy: "confirm_if_needed",
        user_requirements: [],
      },
    },
  }),
});
record("proposed_action_configured", { version: action.version });

let snapshot = await request(`/api/v1/projects/${project.project_id}/runs`, {
  method: "POST",
  body: JSON.stringify({
    capability_id: action.capability_ref.capability_id,
    capability_version: action.capability_ref.version,
    run_kind: "generation",
    conversation_id: project.primary_conversation_id,
    input: action.input,
    config: action.config,
    confirmation: {
      confirmed: true,
      proposed_action_id: action.proposed_action_id,
      action_version: action.version,
      confirmation_message_id: action.confirmation_message_id,
      snapshot_hash: action.snapshot_hash,
    },
  }),
});
const runID = snapshot.run.run_id;
currentRunID = runID;
record("run_started", { run_id: runID, status: snapshot.run.status });

const deadline = Date.now() + Number(args.timeout_ms ?? 30 * 60 * 1000);
let lastState = "";
const resolvedApprovals = new Set();
while (Date.now() < deadline) {
  snapshot = await request(`/api/v1/runs/${runID}/snapshot`);
  const state = JSON.stringify({
    status: snapshot.run.status,
    step: snapshot.steps?.find((item) => item.step_run_id === snapshot.run.current_step_run_id)?.step_id ?? null,
    approval: snapshot.current_approval?.approval_request_id ?? null,
    tasks: (snapshot.task_items ?? []).map((item) => [item.item_key, item.status]),
  });
  if (state !== lastState) {
    record("run_state", JSON.parse(state));
    lastState = state;
  }
  if (snapshot.run.status === "completed") break;
  if (["failed", "cancelled"].includes(snapshot.run.status)) {
    throw new Error(`Run ended with status ${snapshot.run.status}: ${state}`);
  }
  const approval = snapshot.current_approval;
  if (approval && !resolvedApprovals.has(approval.approval_request_id)) {
    await resolveApproval(approval);
    resolvedApprovals.add(approval.approval_request_id);
  }
  if (
    snapshot.run.status === "paused" &&
    snapshot.available_actions?.some(
      (item) => item.action_id === "resume_run" && item.enabled,
    )
  ) {
    await request(`/api/v1/runs/${runID}/resume`, { method: "POST" });
    record("run_resumed", { run_id: runID });
  }
  await new Promise((resolveDelay) => setTimeout(resolveDelay, 2000));
}
if (snapshot.run.status !== "completed") {
  throw new Error(`Run timed out with status ${snapshot.run.status}`);
}

const artifacts = await request(`/api/v1/projects/${project.project_id}/artifacts`);
const candidates = await request(`/api/v1/projects/${project.project_id}/script-candidates`);
const artifactTypes = [...new Set(artifacts.items.map((item) => item.artifact_type))].sort();
if (!artifactTypes.includes("scripts") || candidates.items.length < 1) {
  throw new Error(`Completed run has no final scripts candidate: ${JSON.stringify({ artifactTypes, candidates })}`);
}

const evidence = {
  execution_id: randomUUID(),
  started_at: startedAt,
  finished_at: new Date().toISOString(),
  environment: "real_provider_demo_via_ssh_tunnel",
  capability: { id: capability.capability_id, version: capability.version },
  fixture: {
    name: basename(fixturePath),
    extension: extname(fixturePath),
    bytes: source.length,
    sha256: createHash("sha256").update(source).digest("hex"),
  },
  project_id: project.project_id,
  run_id: runID,
  final_status: snapshot.run.status,
  artifact_types: artifactTypes,
  candidate_count: candidates.items.length,
  timeline,
};
await mkdir(dirname(evidencePath), { recursive: true });
await writeFile(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
process.stdout.write(`${JSON.stringify({
  final_status: evidence.final_status,
  artifact_types: evidence.artifact_types,
  candidate_count: evidence.candidate_count,
  evidence: evidencePath,
}, null, 2)}\n`);
