import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.dirname(scriptDir);
const manifestDir = path.join(root, "capabilities", "v1");
const manifestSchemaPath = path.join(manifestDir, "manifest.schema.json");
const adapterRegistryPath = path.join(manifestDir, "response-adapters.json");
const manifestPaths = [
  path.join(manifestDir, "novel-to-script.json"),
  path.join(manifestDir, "non-novel-to-script.json"),
  path.join(manifestDir, "video-reference-creation.json"),
  path.join(manifestDir, "script-continuation.json"),
];
const errors = [];

function readJson(filePath) {
  try {
    return JSON.parse(fs.readFileSync(filePath, "utf8"));
  } catch (error) {
    errors.push(`${path.relative(root, filePath)}: JSON parse failed: ${error.message}`);
    return null;
  }
}

function deepEqual(left, right) {
  return JSON.stringify(left) === JSON.stringify(right);
}

function resolvePointer(document, pointer) {
  if (!pointer || pointer === "#") return document;
  if (!pointer.startsWith("#/")) return undefined;
  return pointer
    .slice(2)
    .split("/")
    .map((part) => part.replaceAll("~1", "/").replaceAll("~0", "~"))
    .reduce((current, part) => current?.[part], document);
}

function matchesType(value, type) {
  if (type === "null") return value === null;
  if (type === "array") return Array.isArray(value);
  if (type === "object") return value !== null && typeof value === "object" && !Array.isArray(value);
  if (type === "integer") return Number.isInteger(value);
  if (type === "number") return typeof value === "number" && Number.isFinite(value);
  return typeof value === type;
}

function validateSchema(value, schema, rootSchema, location) {
  if (!schema || typeof schema !== "object") return;

  if (schema.$ref) {
    const target = resolvePointer(rootSchema, schema.$ref);
    if (!target) {
      errors.push(`${location}: unresolved local schema ref ${schema.$ref}`);
      return;
    }
    validateSchema(value, target, rootSchema, location);
    return;
  }

  if (schema.oneOf) {
    const branchResults = schema.oneOf.map((branch) => {
      const before = errors.length;
      validateSchema(value, branch, rootSchema, location);
      const branchErrors = errors.splice(before);
      return branchErrors;
    });
    const matched = branchResults.filter((branchErrors) => branchErrors.length === 0);
    if (matched.length !== 1) errors.push(`${location}: expected exactly one oneOf branch, matched ${matched.length}`);
    return;
  }

  if (Object.hasOwn(schema, "const") && !deepEqual(value, schema.const)) {
    errors.push(`${location}: expected const ${JSON.stringify(schema.const)}`);
  }
  if (schema.enum && !schema.enum.some((item) => deepEqual(value, item))) {
    errors.push(`${location}: value ${JSON.stringify(value)} is not in enum`);
  }

  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.some((type) => matchesType(value, type))) {
      errors.push(`${location}: expected type ${types.join("|")}`);
      return;
    }
  }

  if (typeof value === "string") {
    if (schema.minLength !== undefined && value.length < schema.minLength) {
      errors.push(`${location}: string is shorter than ${schema.minLength}`);
    }
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) {
      errors.push(`${location}: string does not match ${schema.pattern}`);
    }
  }

  if (typeof value === "number" && schema.minimum !== undefined && value < schema.minimum) {
    errors.push(`${location}: number is below ${schema.minimum}`);
  }

  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems) {
      errors.push(`${location}: array has fewer than ${schema.minItems} items`);
    }
    if (schema.uniqueItems) {
      const unique = new Set(value.map((item) => JSON.stringify(item)));
      if (unique.size !== value.length) errors.push(`${location}: array items must be unique`);
    }
    if (schema.items) {
      value.forEach((item, index) => validateSchema(item, schema.items, rootSchema, `${location}[${index}]`));
    }
  }

  if (value !== null && typeof value === "object" && !Array.isArray(value)) {
    const keys = Object.keys(value);
    if (schema.minProperties !== undefined && keys.length < schema.minProperties) {
      errors.push(`${location}: object has fewer than ${schema.minProperties} properties`);
    }
    for (const required of schema.required ?? []) {
      if (!Object.hasOwn(value, required)) errors.push(`${location}: missing required property ${required}`);
    }
    for (const [key, child] of Object.entries(value)) {
      if (schema.properties?.[key]) {
        validateSchema(child, schema.properties[key], rootSchema, `${location}.${key}`);
      } else if (schema.additionalProperties === false) {
        errors.push(`${location}: unexpected property ${key}`);
      } else if (schema.additionalProperties && typeof schema.additionalProperties === "object") {
        validateSchema(child, schema.additionalProperties, rootSchema, `${location}.${key}`);
      }
    }
  }
}

function resolveExternalRef(baseFile, ref) {
  const [relativePath, pointer = ""] = ref.split("#", 2);
  const targetPath = path.resolve(path.dirname(baseFile), relativePath);
  if (!fs.existsSync(targetPath)) {
    errors.push(`${path.relative(root, baseFile)}: missing ref file ${ref}`);
    return;
  }
  const document = readJson(targetPath);
  if (document && pointer && resolvePointer(document, `#${pointer}`) === undefined) {
    errors.push(`${path.relative(root, baseFile)}: missing JSON Pointer ${ref}`);
  }
}

function checkFileRef(baseFile, ref) {
  const targetPath = path.resolve(path.dirname(baseFile), ref);
  if (!fs.existsSync(targetPath)) {
    errors.push(`${path.relative(root, baseFile)}: missing file ${ref}`);
    return;
  }
  if (fs.statSync(targetPath).size === 0) errors.push(`${path.relative(root, baseFile)}: empty file ${ref}`);
}

const manifestSchema = readJson(manifestSchemaPath);
const adapterRegistry = readJson(adapterRegistryPath);
const adapterIds = new Set(adapterRegistry?.adapters?.map((adapter) => adapter.id) ?? []);
const supportedApprovalActions = new Set([
  "approve",
  "edit_artifact",
  "request_ai_revision",
  "regenerate_artifact",
  "select_adaptation_strategy",
  "select_single_option",
  "ai_revise",
  "manual_edit",
  "confirm_change",
  "accept_with_risk",
]);

if (adapterIds.size !== (adapterRegistry?.adapters?.length ?? 0)) {
  errors.push("capabilities/v1/response-adapters.json: adapter IDs must be unique");
}
for (const adapter of adapterRegistry?.adapters ?? []) {
  resolveExternalRef(adapterRegistryPath, adapter.target_schema_ref);
}

const manifests = manifestPaths.map((filePath) => ({ filePath, value: readJson(filePath) }));
const expectedCapabilityIds = new Set([
  "novel_to_script",
  "non_novel_to_script",
  "video_reference_creation",
  "script_continuation",
]);
const expectedSteps = {
  novel_to_script: [
    "ingest_source",
    "build_source_manifest",
    "build_story_bible",
    "review_volume_fit",
    "split_episodes",
    "build_episode_cards",
    "build_script_contexts",
    "generate_script_units",
    "review_script_set",
    "aggregate_scripts",
  ],
  non_novel_to_script: [
    "ingest_source",
    "build_source_manifest",
    "build_material_bank",
    "review_volume_fit",
    "build_story_seed",
    "build_series_blueprint",
    "build_episode_cards",
    "build_script_contexts",
    "generate_script_units",
    "review_script_set",
    "aggregate_scripts",
  ],
  video_reference_creation: [
    "ingest_source",
    "extract_video_scripts",
    "aggregate_reference_scripts",
    "analyze_reference_scripts",
    "propose_adaptation_options",
    "build_adaptation_brief",
    "build_story_seed",
    "build_series_blueprint",
    "build_episode_cards",
    "build_script_contexts",
    "generate_script_units",
    "review_script_set",
    "aggregate_scripts",
  ],
  script_continuation: [
    "ingest_source",
    "generate_continuation_options",
    "generate_continuation_script",
  ],
};
const actualCapabilityIds = new Set(manifests.map(({ value }) => value?.id).filter(Boolean));
if (!deepEqual([...actualCapabilityIds].sort(), [...expectedCapabilityIds].sort())) {
  errors.push("capabilities/v1: first-release capability IDs do not match the canonical set");
}

for (const { filePath, value: manifest } of manifests) {
  if (!manifest || !manifestSchema) continue;
  const schemaErrorCount = errors.length;
  validateSchema(manifest, manifestSchema, manifestSchema, path.relative(root, filePath));
  if (errors.length !== schemaErrorCount) continue;
  const expectedId = path.basename(filePath, ".json").replaceAll("-", "_");
  if (manifest.id !== expectedId) {
    errors.push(`${path.relative(root, filePath)}: expected capability ID ${expectedId}`);
  }

  const stepIds = manifest.steps.map((step) => step.id);
  if (new Set(stepIds).size !== stepIds.length) {
    errors.push(`${manifest.id}: Step IDs must be unique`);
  }
  if (!deepEqual(stepIds, expectedSteps[manifest.id])) {
    errors.push(`${manifest.id}: Step order does not match the canonical first-release workflow`);
  }
  if (manifest.commands.includes("select_final")) {
    errors.push(`${manifest.id}: select_final is a Project command, not a Capability command`);
  }
  if (manifest.completion.requires_terminal_approval !== false) {
    errors.push(`${manifest.id}: completion must not create a duplicate terminal approval`);
  }
  if (manifest.id === "script_continuation") {
    validateContinuationContract(filePath, manifest);
  }
  if (manifest.id === "novel_to_script") {
    const sourceManifestStep = manifest.steps.find((step) => step.id === "build_source_manifest");
    const storyBibleStep = manifest.steps.find((step) => step.id === "build_story_bible");
    const splitStep = manifest.steps.find((step) => step.id === "split_episodes");
    if (
      manifest.version !== "1.4.0" ||
      sourceManifestStep?.kind !== "system" ||
      sourceManifestStep?.executor_ref !== "runtime.build_source_manifest" ||
      sourceManifestStep?.approval?.type !== "none" ||
      sourceManifestStep?.output_refs?.[0]?.artifact_type !== "source_manifest" ||
      sourceManifestStep?.output_refs?.[0]?.initial_status !== "confirmed" ||
      storyBibleStep?.kind !== "batch" ||
      storyBibleStep?.batch?.preparation?.id !== "source_analysis" ||
      storyBibleStep?.batch?.preparation?.result_mode !== "checkpoint" ||
      storyBibleStep?.batch?.task_stage?.id !== "story_bible_aggregate" ||
      storyBibleStep?.batch?.task_stage?.result_mode !== "artifact" ||
      !storyBibleStep?.input_refs?.some((input) => input.artifact_type === "source_manifest") ||
      !splitStep?.input_refs?.some((input) => input.artifact_type === "source_manifest")
    ) {
      errors.push("novel_to_script: source manifest or source analysis contract is incomplete");
    }
  }
  if (manifest.id === "non_novel_to_script") {
    const sourceManifestStep = manifest.steps.find((step) => step.id === "build_source_manifest");
    const materialBankStep = manifest.steps.find((step) => step.id === "build_material_bank");
    if (
      manifest.version !== "1.3.0" ||
      sourceManifestStep?.kind !== "system" ||
      sourceManifestStep?.executor_ref !== "runtime.build_source_manifest" ||
      sourceManifestStep?.approval?.type !== "none" ||
      sourceManifestStep?.output_refs?.[0]?.artifact_type !== "source_manifest" ||
      sourceManifestStep?.output_refs?.[0]?.initial_status !== "confirmed" ||
      !materialBankStep?.input_refs?.some(
        (input) =>
          input.artifact_type === "source_manifest" &&
          input.version_policy === "exact",
      )
    ) {
      errors.push("non_novel_to_script: source manifest contract is incomplete");
    }
  }
  const producedTypes = new Set();
  for (const step of manifest.steps) {
    for (const output of step.output_refs) {
      producedTypes.add(output.artifact_type);
      resolveExternalRef(filePath, output.schema_ref);
      if (
        output.initial_status === "confirmed" &&
        (!["system", "aggregate"].includes(step.kind) || step.approval.type !== "none")
      ) {
        errors.push(
          `${manifest.id}.${step.id}: only deterministic system/aggregate Steps without approval may create confirmed output`,
        );
      }
    }
    for (const input of step.input_refs) {
      if (input.version_policy === "current_confirmed") {
        errors.push(`${manifest.id}.${step.id}: current_confirmed is forbidden for an executing Step`);
      }
    }
    if (step.prompt_ref) {
      checkFileRef(filePath, step.prompt_ref);
      if (
        step.prompt_ref.includes("novel2script_agent_project") ||
        !step.prompt_ref.includes("../../design/prompts/")
      ) {
        errors.push(`${manifest.id}.${step.id}: Prompt must be local to design/prompts`);
      }
    }
    for (const ruleRef of step.rule_refs) {
      checkFileRef(filePath, ruleRef);
      if (
        ruleRef.includes("novel2script_agent_project") ||
        !ruleRef.includes("../../design/rules/")
      ) {
        errors.push(`${manifest.id}.${step.id}: Rule must be local to design/rules`);
      }
    }
    for (const stage of [step.batch?.preparation, step.batch?.task_stage]) {
      if (!stage) continue;
      checkFileRef(filePath, stage.prompt_ref);
      if (
        stage.prompt_ref.includes("novel2script_agent_project") ||
        !stage.prompt_ref.includes("../../design/prompts/")
      ) {
        errors.push(`${manifest.id}.${step.id}.${stage.id}: batch Prompt must be local to design/prompts`);
      }
    }
    if (step.response_adapter_ref && !adapterIds.has(step.response_adapter_ref)) {
      errors.push(`${manifest.id}.${step.id}: unknown response adapter ${step.response_adapter_ref}`);
    }
    if (step.config_ref !== undefined && step.config_ref !== null && !manifest.config_schema_refs[step.config_ref]) {
      errors.push(`${manifest.id}.${step.id}: unknown config_ref ${step.config_ref}`);
    }
    if (step.approval.condition && step.approval.required !== true) {
      errors.push(`${manifest.id}.${step.id}: conditional approval must be required when its condition is true`);
    }
    for (const action of step.approval.allowed_actions ?? []) {
      if (!supportedApprovalActions.has(action)) {
        errors.push(`${manifest.id}.${step.id}: approval action ${action} has no Runtime/UI handler`);
      }
    }
    if (
      step.id === "review_volume_fit" &&
      !deepEqual(step.approval.allowed_actions, ["approve"])
    ) {
      errors.push(`${manifest.id}.${step.id}: volume-fit confirmation must submit the expansion strategy with approve`);
    }
    if (
      step.id === "propose_adaptation_options" &&
      !deepEqual(step.approval.allowed_actions, ["select_adaptation_strategy", "request_ai_revision"])
    ) {
      errors.push(`${manifest.id}.${step.id}: adaptation confirmation actions do not match the structured UI contract`);
    }
    if (step.kind === "review") {
      if (
        step.id !== "review_script_set" ||
        step.output_refs.length !== 0 ||
        step.result_schema_ref !== "../../schemas/v1/quality-review.schema.json" ||
		step.gate_policy_ref !== "quality.script.v1" ||
		step.approval.type !== "conditional_review" ||
		step.approval.scope !== "quality_review" ||
		step.approval.required_when !== "action_required" ||
		step.prompt_ref !== "../../design/prompts/shared/quality-review-batch.v1.md" ||
		!step.rule_refs.includes("../../design/rules/shared/quality-review.v1.md") ||
		step.batch?.execution !== "parallel" ||
		step.batch?.max_items_per_task !== 5 ||
		step.batch?.failure_policy !== "preserve_success_retry_failed"
      ) {
        errors.push(`${manifest.id}.${step.id}: quality review contract is incomplete`);
      }
      resolveExternalRef(filePath, step.result_schema_ref);
    }
    const transitionTargets = new Map();
    for (const next of step.next) {
      const target = typeof next === "string" ? next : next.to;
      const condition = typeof next === "string" ? "completed" : next.when;
      if (condition === "user_stop") errors.push(`${manifest.id}.${step.id}: user_stop is reserved and not executable; cancellation currently does not run successors`);
      if (transitionTargets.has(condition) && transitionTargets.get(condition) !== target) {
        errors.push(`${manifest.id}.${step.id}: condition ${condition} has multiple targets ${transitionTargets.get(condition)} and ${target}`);
      }
      transitionTargets.set(condition, target);
      if (condition === "failure" && target === step.id) errors.push(`${manifest.id}.${step.id}: failure cannot target itself; use the retry policy for retrying the same step`);
      if (!stepIds.includes(target)) errors.push(`${manifest.id}.${step.id}: unknown transition target ${target}`);
    }
  }

  resolveExternalRef(filePath, manifest.input_schema_ref);
  Object.values(manifest.config_schema_refs).forEach((ref) => resolveExternalRef(filePath, ref));
  if (manifest.default_config_ref !== null && !manifest.config_schema_refs[manifest.default_config_ref]) {
    errors.push(`${manifest.id}: unknown default_config_ref ${manifest.default_config_ref}`);
  }
  for (const terminalStepId of manifest.completion.terminal_step_ids) {
    if (!stepIds.includes(terminalStepId)) errors.push(`${manifest.id}: unknown terminal Step ${terminalStepId}`);
  }
  for (const requiredArtifact of manifest.completion.required_artifacts) {
    if (!producedTypes.has(requiredArtifact.artifact_type)) {
      errors.push(`${manifest.id}: completion requires unproduced artifact ${requiredArtifact.artifact_type}`);
    }
  }

  const reachable = new Set([stepIds[0]]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const step of manifest.steps) {
      if (!reachable.has(step.id)) continue;
      for (const next of step.next) {
        const target = typeof next === "string" ? next : next.to;
        if (!reachable.has(target)) {
          reachable.add(target);
          changed = true;
        }
      }
    }
  }
  for (const stepId of stepIds) {
    if (!reachable.has(stepId)) errors.push(`${manifest.id}: unreachable Step ${stepId}`);
  }
}

function validateContinuationContract(filePath, manifest) {
  const schemaRef = "../../schemas/v1/script-continuation.schema.json#/$defs/";
  const expect = (actual, expected, label) => {
    if (!deepEqual(actual, expected)) {
      errors.push(`script_continuation.${label}: expected ${JSON.stringify(expected)}`);
    }
  };
  expect(manifest.execution_mode, "stateful_workflow", "execution_mode");
  expect(manifest.input_binding, { source_type: "script", asset_role: "primary_source" }, "input_binding");
  expect(manifest.entry_policy?.requires_user_confirmation, true, "start_confirmation");
  expect(manifest.ui?.config_view_key, "script_continuation", "config_view_key");
  expect(manifest.input_schema_ref, `${schemaRef}input`, "input_schema_ref");
  expect(manifest.config_schema_refs?.continuation, `${schemaRef}config`, "config_schema_ref");
  expect(manifest.default_config_ref, "continuation", "default_config_ref");
  expect(manifest.completion.terminal_step_ids, ["generate_continuation_script"], "terminal_step_ids");
  expect(manifest.completion.required_artifacts, [
    { artifact_type: "continuation_script", required_status: "confirmed", coverage: "one" },
  ], "required_artifacts");

  // These are original workflow invariants, not a general JSON Schema implementation.
  const stages = [
    { id: "ingest_source", kind: "system", executor: "runtime.ingest_source", inputs: [],
      output: "source_input", schema: "../../schemas/v1/common.schema.json#/$defs/sourceInput",
      context: ["asset_snapshot", "config_snapshot"], approval: "transition_checkpoint", prompt: null },
    { id: "generate_continuation_options", kind: "model", executor: "worker.structured_content", inputs: ["source_input"],
      output: "continuation_options", schema: `${schemaRef}continuationOptions`,
      context: ["approval_snapshot", "config_snapshot", "user_instruction"], approval: "transition_checkpoint",
      prompt: "../../design/prompts/script-continuation/options.v1.md" },
    { id: "generate_continuation_script", kind: "model", executor: "worker.structured_content", inputs: ["source_input", "continuation_options"],
      output: "continuation_script", schema: `${schemaRef}continuationScript`,
      context: ["approval_snapshot", "config_snapshot", "decision_snapshot", "user_instruction"], approval: "checkpoint",
      prompt: "../../design/prompts/script-continuation/script.v1.md" },
  ];
  for (const [index, stage] of stages.entries()) {
    const step = manifest.steps.find((item) => item.id === stage.id);
    if (!step) continue; // The canonical step-order check reports missing stages.
    expect(step.kind, stage.kind, `${stage.id}.kind`);
    expect(step.executor_ref, stage.executor, `${stage.id}.executor_ref`);
    expect(step.config_ref, "continuation", `${stage.id}.config_ref`);
    expect(step.next, index + 1 < stages.length ? [stages[index + 1].id] : [], `${stage.id}.next`);
    expect(step.input_refs, stage.inputs.map((artifact_type) => ({
      artifact_type, cardinality: "one", required_status: "confirmed", version_policy: "approval_snapshot",
    })), `${stage.id}.input_refs`);
    expect(step.output_refs, [{
      artifact_type: stage.output, cardinality: "one", initial_status: "pending_approval", schema_ref: stage.schema,
    }], `${stage.id}.output_refs`);
    for (const context of stage.context) {
      expect(step.required_context?.includes(context), true, `${stage.id}.required_context.${context}`);
    }
    expect(step.approval.type, stage.approval, `${stage.id}.approval.type`);
    expect(step.approval.required, true, `${stage.id}.approval.required`);
    expect(step.approval.invalidate_on_new_version, true, `${stage.id}.approval.invalidate_on_new_version`);
    expect(step.approval.condition ?? null, null, `${stage.id}.approval.condition`);
    expect(step.prompt_ref, stage.prompt, `${stage.id}.prompt_ref`);
    if (stage.kind === "model") {
      const adapter = adapterRegistry?.adapters?.find((item) => item.id === step.response_adapter_ref);
      expect(adapter?.target_schema_ref, stage.schema, `${stage.id}.response_adapter.target_schema_ref`);
    }
    if (stage.id === "generate_continuation_options") {
      expect(step.approval.scope, "transition", `${stage.id}.approval.scope`);
      expect(step.approval.allowed_actions, ["select_single_option", "request_ai_revision"], `${stage.id}.approval.allowed_actions`);
    }
  }

  const schema = readJson(path.resolve(path.dirname(filePath), schemaRef.split("#")[0]));
  if (!schema) return;
  const definitions = schema.$defs;
  expect(definitions?.input?.properties?.source_type?.const, "script", "schema.source_type");
  expect(definitions?.config?.required?.includes("target_length_chars"), true, "schema.required_target_length");
  const length = definitions?.config?.properties?.target_length_chars;
  expect(length?.type, "integer", "schema.target_length.type");
  expect(length?.minimum, 1000, "schema.target_length.minimum");
  expect(length?.maximum, 50000, "schema.target_length.maximum");
  const options = definitions?.continuationOptions;
  for (const field of ["core_settings", "options", "selection_instructions"]) {
    expect(options?.required?.includes(field), true, `schema.options.required.${field}`);
  }
  expect(options?.properties?.options?.minItems, 5, "schema.options.minItems");
  expect(options?.properties?.options?.maxItems, 5, "schema.options.maxItems");
  expect(options?.properties?.options?.items?.$ref, "#/$defs/continuationOption", "schema.options.items");
  const option = definitions?.continuationOption;
  expect(option?.required?.includes("option_id"), true, "schema.option.required_id");
  expect(option?.properties?.option_id, { type: "string", pattern: "^option_[1-5]$" }, "schema.option.id");
  const script = definitions?.continuationScript;
  for (const field of ["title", "selected_option_id", "script_text"]) {
    expect(script?.required?.includes(field), true, `schema.script.required.${field}`);
  }
  expect(script?.properties?.selected_option_id, { type: "string", pattern: "^option_[1-5]$" }, "schema.script.selected_option_id");
}

const videoManifest = manifests.find(({ value }) => value?.id === "video_reference_creation")?.value;
const briefStep = videoManifest?.steps.find((step) => step.id === "build_adaptation_brief");
if (!briefStep?.required_context?.includes("decision_snapshot")) {
  errors.push("video_reference_creation.build_adaptation_brief: decision_snapshot is required");
}
if (!briefStep?.required_context?.includes("config_snapshot")) {
  errors.push("video_reference_creation.build_adaptation_brief: creation config_snapshot is required");
}

const docsDir = path.join(root, "docs");
const markdownFiles = fs
  .readdirSync(docsDir)
  .filter((name) => name.endsWith(".md"))
  .map((name) => path.join(docsDir, name));
const registryContract = fs.readFileSync(path.join(docsDir, "capability-registry-contract.md"), "utf8");
for (const staleStepId of [
  "confirm_generation_config",
  "confirm_scripts",
  "confirm_adaptation_brief",
  "extract_video_script_units",
  "plan_episode_cards",
  "generate_script_context",
]) {
  for (const markdownFile of markdownFiles) {
    if (fs.readFileSync(markdownFile, "utf8").includes(staleStepId)) {
      errors.push(`${path.relative(root, markdownFile)}: stale Step ID ${staleStepId}`);
    }
  }
}
if (!registryContract.includes("/api/v1/capabilities")) {
  errors.push("docs/capability-registry-contract.md: public Registry API must use /api/v1");
}
for (const forbiddenText of [
  "requires_final_approval",
  "video_reference_creation -> unknown",
  "GET /api/capabilities",
]) {
  for (const markdownFile of markdownFiles) {
    if (fs.readFileSync(markdownFile, "utf8").includes(forbiddenText)) {
      errors.push(`${path.relative(root, markdownFile)}: forbidden stale contract text ${forbiddenText}`);
    }
  }
}

if (errors.length > 0) {
  console.error("Capability contract validation failed:");
  for (const error of errors) console.error(`- ${error}`);
  process.exit(1);
}

const stepCount = manifests.reduce((total, { value }) => total + value.steps.length, 0);
console.log("Capability contract validation passed.");
console.log(`Manifests: ${manifests.length}; Steps: ${stepCount}; Response adapters: ${adapterIds.size}`);
