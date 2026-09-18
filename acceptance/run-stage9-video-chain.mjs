import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { basename, dirname, extname, resolve } from "node:path";

const args = Object.fromEntries(
  process.argv.slice(2).map((item) => {
    const [key, ...value] = item.replace(/^--/, "").split("=");
    return [key, value.join("=")];
  }),
);
if (!args.fixture) throw new Error("--fixture is required");

const fixturePath = resolve(args.fixture);
const source = await readFile(fixturePath);
if (source.length > 10 * 1024 * 1024) {
  throw new Error("Stage 9 video fixture must be prepared to 10 MiB or less");
}

const baseURL = process.env.CONTENT_AGENT_RUNTIME_URL ?? "http://127.0.0.1:8850";
const evidencePath = resolve(
  args.output ?? "acceptance/evidence/stage9-video-real-e2e.json",
);
const clientID = `stage9-video-${randomUUID()}`;
const timeline = [];
let startedAt = new Date().toISOString();
let currentProjectID = null;
let currentRunID = null;

async function writeFailureEvidence(reason) {
  const evidence = {
    execution_id: randomUUID(),
    started_at: startedAt,
    finished_at: new Date().toISOString(),
    environment: "real_provider_demo_via_ssh_tunnel",
    capability: { id: "video_reference_creation" },
    fixture: fixtureIdentity(),
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

function fixtureIdentity() {
  return {
    name: basename(fixturePath),
    extension: extname(fixturePath),
    bytes: source.length,
    sha256: createHash("sha256").update(source).digest("hex"),
  };
}

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

async function uploadVideo(projectID) {
  const session = await request(`/api/v1/projects/${projectID}/upload-sessions`, {
    method: "POST",
    body: JSON.stringify({
      items: [{
        client_item_key: `video-${randomUUID()}`,
        kind: "video",
        original_filename: basename(fixturePath),
        declared_mime_type: "video/mp4",
        declared_size_bytes: source.length,
      }],
    }),
  });
  const item = session.items[0];
  await request(`/api/v1/upload-items/${item.upload_item_id}/content`, {
    method: "PUT",
    headers: { "Content-Type": "video/mp4" },
    body: source,
  });
  return request(`/api/v1/upload-items/${item.upload_item_id}/complete`, {
    method: "POST",
  });
}

async function createSealedVideoSet(projectID, assetID) {
  let set = await request(`/api/v1/projects/${projectID}/asset-sets`, {
    method: "POST",
    body: JSON.stringify({
      purpose: "video_reference_source",
      display_name: "Stage 9 参考剧视频",
      created_by_message_id: null,
    }),
  });
  set = await request(`/api/v1/asset-sets/${set.asset_set.asset_set_id}/versions`, {
    method: "POST",
    body: JSON.stringify({
      expected_asset_set_version: set.asset_set.current_version,
      changes: [{ operation: "add_asset", asset_id: assetID }],
    }),
  });
  return request(`/api/v1/asset-sets/${set.asset_set.asset_set_id}/seal`, {
    method: "POST",
    body: JSON.stringify({
      expected_asset_set_version: set.asset_set.current_version,
      continuation_policy: null,
      user_confirmed_upload_complete: true,
    }),
  });
}

function firstAdaptationOption(payload) {
  const options = payload?.payload?.adaptation_options?.options
    ?? payload?.payload?.options
    ?? payload?.adaptation_options?.options
    ?? payload?.options;
  const optionID = Array.isArray(options) ? options[0]?.option_id : null;
  if (!optionID) {
    throw new Error(`Adaptation option is missing: ${JSON.stringify(payload)}`);
  }
  return optionID;
}

async function resolveApproval(approval) {
  if (approval.scope === "quality_review") {
    const review = await request(`/api/v1/quality-reviews/${approval.subject_ref_id}`);
    if (!approval.options.includes("accept_with_risk") || review.issue_counts.blocker > 0) {
      throw new Error(
        `Quality review requires user action: ${JSON.stringify({ approval, review })}`,
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

  if (approval.options.includes("select_adaptation_strategy")) {
    const artifactVersion = await request(
      `/api/v1/artifact-versions/${approval.subject_ref_id}`,
    );
    const optionID = firstAdaptationOption(artifactVersion);
    await request(`/api/v1/approvals/${approval.approval_request_id}/resolutions`, {
      method: "POST",
      body: JSON.stringify({
        action: "select_adaptation_strategy",
        expected_approval_version: approval.version,
        subject_snapshot_hash: approval.subject_snapshot_hash,
        resolution_payload: {
          adaptation_options_artifact_version_id: approval.subject_ref_id,
          selection: {
            selected_option_ids: [optionID],
            combined_methods: [],
            custom_changes: ["保留结构方法，替换人物、关系和事件，不照搬原内容"],
          },
          creation_config: {
            target_episode_count: 1,
            episode_duration_minutes: 1,
            user_requirements: ["形成可独立成立的一集漫剧剧本"],
          },
        },
      }),
    });
    record("adaptation_strategy_selected", {
      approval_request_id: approval.approval_request_id,
      option_id: optionID,
    });
    return;
  }

  if (!approval.options.includes("approve")) {
    throw new Error(`Approval has no automatic action: ${JSON.stringify(approval)}`);
  }
  await request(`/api/v1/approvals/${approval.approval_request_id}/resolutions`, {
    method: "POST",
    body: JSON.stringify({
      action: "approve",
      expected_approval_version: approval.version,
      subject_snapshot_hash: approval.subject_snapshot_hash,
    }),
  });
  record("approval_resolved", {
    approval_request_id: approval.approval_request_id,
    scope: approval.scope,
    subject_kind: approval.subject_kind,
    subject_ref_id: approval.subject_ref_id,
  });
}

const capabilities = await request("/api/v1/capabilities");
const capability = capabilities.items.find(
  (item) => item.capability_id === "video_reference_creation",
);
if (!capability || capability.status !== "available") {
  throw new Error(`Capability is not available: ${JSON.stringify(capability)}`);
}

let project;
let snapshot;
let runID;
if (args.resume_run_id) {
  snapshot = await request(`/api/v1/runs/${args.resume_run_id}/snapshot`);
  runID = snapshot.run.run_id;
  project = await request(`/api/v1/projects/${snapshot.run.project_id}`);
  try {
    const previousEvidence = JSON.parse(await readFile(evidencePath, "utf8"));
    if (previousEvidence.run_id === runID && Array.isArray(previousEvidence.timeline)) {
      timeline.push(...previousEvidence.timeline);
      startedAt = previousEvidence.started_at ?? startedAt;
    }
  } catch {
    // The resumed run can still produce independent evidence when no prior file exists.
  }
  currentProjectID = project.project_id;
  currentRunID = runID;
  record("existing_run_attached", {
    project_id: project.project_id,
    run_id: runID,
    status: snapshot.run.status,
  });
} else {
project = await request("/api/v1/projects", {
  method: "POST",
  body: JSON.stringify({ title: `Stage 9 video ${Date.now()}` }),
});
currentProjectID = project.project_id;
record("project_created", { project_id: project.project_id });

const uploaded = await uploadVideo(project.project_id);
const attachment = {
  asset_id: uploaded.asset.asset_id,
  asset_snapshot_id: uploaded.asset_snapshot.asset_snapshot_id,
};
record("video_uploaded", {
  ...attachment,
  prepared_bytes: uploaded.asset.size_bytes,
  duration_seconds: uploaded.asset.duration_seconds ?? null,
});

const sealedSet = await createSealedVideoSet(project.project_id, attachment.asset_id);
record("video_set_sealed", {
  asset_set_id: sealedSet.asset_set.asset_set_id,
  asset_set_version_id: sealedSet.version.asset_set_version_id,
  episode_count: sealedSet.version.member_count,
});

const exchange = await request(
  `/api/v1/conversations/${project.primary_conversation_id}/messages`,
  {
    method: "POST",
    body: JSON.stringify({
      content: "解析上传的视频并参考创作一集新的漫剧剧本。",
      display_content: "解析上传的视频并参考创作一集新的漫剧剧本。",
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
action = await request(`/api/v1/proposed-actions/${action.proposed_action_id}/configuration`, {
  method: "POST",
  body: JSON.stringify({
    expected_version: action.version,
    input: {
      project_id: project.project_id,
      source_type: "video_reference",
      asset_set_id: sealedSet.asset_set.asset_set_id,
      asset_set_version_id: sealedSet.version.asset_set_version_id,
      collection_state: "sealed",
      user_request_message_id: exchange.user_message.message_id,
      user_notes: [],
    },
    config: {
      config_ref: "extraction",
      payload: {
        fidelity_level: "high",
        timecode_precision: "second",
        uncertain_content_policy: "mark",
      },
    },
  }),
});
record("proposed_action_configured", { version: action.version });

snapshot = await request(`/api/v1/projects/${project.project_id}/runs`, {
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
runID = snapshot.run.run_id;
currentRunID = runID;
record("run_started", { run_id: runID, status: snapshot.run.status });
}

const deadline = Date.now() + Number(args.timeout_ms ?? 45 * 60 * 1000);
let lastState = "";
const resolvedApprovals = new Set();
while (Date.now() < deadline) {
  snapshot = await request(`/api/v1/runs/${runID}/snapshot`);
  const state = JSON.stringify({
    status: snapshot.run.status,
    step: snapshot.steps?.find(
      (item) => item.step_run_id === snapshot.run.current_step_run_id,
    )?.step_id ?? null,
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
const requiredArtifactTypes = [
  "reference_scripts",
  "script_analysis",
  "adaptation_brief",
  "scripts",
];
const missingArtifactTypes = requiredArtifactTypes.filter(
  (artifactType) => !artifactTypes.includes(artifactType),
);
if (missingArtifactTypes.length > 0 || candidates.items.length < 1) {
  throw new Error(
    `Completed run is incomplete: ${JSON.stringify({ missingArtifactTypes, artifactTypes, candidates })}`,
  );
}

const evidence = {
  execution_id: randomUUID(),
  started_at: startedAt,
  finished_at: new Date().toISOString(),
  environment: "real_provider_demo_via_ssh_tunnel",
  capability: { id: capability.capability_id, version: capability.version },
  fixture: fixtureIdentity(),
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
