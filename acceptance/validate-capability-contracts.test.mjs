import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const temporary = fs.realpathSync(path.join(root, ".tmp"));
assert.ok(temporary.toLowerCase().startsWith(`${fs.realpathSync(root)}${path.sep}`.toLowerCase()));
const fixtures = fs.mkdtempSync(path.join(temporary, "goal-g453-contract-fixtures-"));
const continuationPath = "capabilities/v1/script-continuation.json";
const continuationSchemaPath = "schemas/v1/script-continuation.schema.json";
let sequence = 0;

function fixture() {
  const directory = path.join(fixtures, String(++sequence));
  fs.mkdirSync(directory);
  for (const name of ["capabilities", "schemas", "design"]) {
    fs.cpSync(path.join(root, name), path.join(directory, name), {
      recursive: true,
      filter(source) {
        assert.equal(fs.lstatSync(source).isSymbolicLink(), false, "fixture inputs cannot contain links");
        return true;
      },
    });
  }
  fs.mkdirSync(path.join(directory, "acceptance"));
  fs.mkdirSync(path.join(directory, "docs"));
  for (const name of ["acceptance/validate-capability-contracts.mjs", "docs/capability-registry-contract.md"]) {
    fs.copyFileSync(path.join(root, name), path.join(directory, name));
  }
  return directory;
}

function modify(directory, name, change) {
  const file = path.join(directory, name);
  const value = JSON.parse(fs.readFileSync(file, "utf8"));
  change(value);
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

function validate(directory) {
  const child = spawnSync(process.execPath, [path.join(directory, "acceptance/validate-capability-contracts.mjs")], {
    cwd: directory, encoding: "utf8", timeout: 30000, windowsHide: true,
  });
  assert.ifError(child.error);
  assert.equal(child.signal, null);
  return { code: child.status, output: `${child.stdout}${child.stderr}` };
}

test("validates all four original workflows and 37 steps", () => {
  const result = validate(fixture());
  assert.equal(result.code, 0, result.output);
  assert.match(result.output, /Manifests: 4; Steps: 37;/);
});

for (const condition of ["compeleted", "Completed", " completed "]) {
  test(`workflow transition admission rejects an unknown condition: ${condition}`, () => {
    const directory = fixture();
    modify(directory, "capabilities/v1/novel-to-script.json", (value) => {
      const next = value.steps[0].next[0];
      value.steps[0].next[0] = { when: condition, to: typeof next === "string" ? next : next.to };
    });
    const result = validate(directory);
    assert.equal(result.code, 1, result.output);
    assert.match(result.output, /next\[0\].*(when|oneOf)/);
  });
}

test("workflow transition admission rejects a reserved stop condition", () => {
  const directory = fixture();
  modify(directory, "capabilities/v1/novel-to-script.json", (value) => {
    value.steps[0].next.push({ when: "user_stop", to: value.steps.at(-1).id });
  });
  const result = validate(directory);
  assert.equal(result.code, 1, result.output);
  assert.match(result.output, /user_stop.*reserved/);
});

test("workflow transition admission rejects an ignored extra field", () => {
  const directory = fixture();
  modify(directory, "capabilities/v1/novel-to-script.json", (value) => {
    const next = value.steps[0].next[0];
    value.steps[0].next[0] = { when: "completed", to: typeof next === "string" ? next : next.to, when_text: "must not be ignored" };
  });
  const result = validate(directory);
  assert.equal(result.code, 1, result.output);
  assert.match(result.output, /next\[0\]/);
});

test("workflow transition admission rejects two targets for the same condition", () => {
  const directory = fixture();
  modify(directory, "capabilities/v1/novel-to-script.json", (value) => {
    const next = value.steps[0].next[0];
    value.steps[0].next.push({ when: typeof next === "string" ? "completed" : next.when, to: value.steps.at(-1).id });
  });
  const result = validate(directory);
  assert.equal(result.code, 1, result.output);
  assert.match(result.output, /condition.*multiple targets/);
});

test("workflow transition admission rejects an implicit failure retry", () => {
  const directory = fixture();
  modify(directory, "capabilities/v1/novel-to-script.json", (value) => {
    value.steps[0].next.push({ when: "failure", to: value.steps[0].id });
  });
  const result = validate(directory);
  assert.equal(result.code, 1, result.output);
  assert.match(result.output, /failure.*retry policy/);
});

test("workflow transition admission preserves shorthand and same-target conditions", () => {
  const directory = fixture();
  modify(directory, "capabilities/v1/novel-to-script.json", (value) => {
    const next = value.steps[0].next[0];
    const target = typeof next === "string" ? next : next.to;
    value.steps[0].next = [target, { when: "completed", to: target }, { when: "failure", to: target }];
  });
  const result = validate(directory);
  assert.equal(result.code, 0, result.output);
});

for (const name of ["novel-to-script", "non-novel-to-script", "video-reference-creation", "script-continuation"]) {
  test(`rejects a missing original workflow: ${name}`, () => {
    const directory = fixture();
    fs.unlinkSync(path.join(directory, "capabilities/v1", `${name}.json`));
    const result = validate(directory);
    assert.equal(result.code, 1, result.output);
    assert.match(result.output, /validation failed/);
  });
}

const step = (manifest, id) => manifest.steps.find((item) => item.id === id);
const options = (manifest) => step(manifest, "generate_continuation_options");
const script = (manifest) => step(manifest, "generate_continuation_script");
for (const [name, change] of [
  ["wrong identity", (value) => { value.id = "other_workflow"; }],
  ["missing options stage", (value) => { value.steps.splice(1, 1); }],
  ["bypassed option selection", (value) => { value.steps[0].next = ["generate_continuation_script"]; }],
  ["changed execution mode", (value) => { value.execution_mode = "inline"; }],
  ["changed source binding", (value) => { value.input_binding.source_type = "novel"; }],
  ["disabled start confirmation", (value) => { value.entry_policy.requires_user_confirmation = false; }],
  ["wrong trusted view", (value) => { value.ui.config_view_key = "script_generation"; }],
  ["missing selection approval", (value) => { options(value).approval.required = false; }],
  ["selection replaced by plain approval", (value) => { options(value).approval.allowed_actions = ["approve"]; }],
  ["selection remains valid after replacement", (value) => { options(value).approval.invalidate_on_new_version = false; }],
  ["missing choice snapshot", (value) => { script(value).required_context = script(value).required_context.filter((item) => item !== "decision_snapshot"); }],
  ["unconfirmed option input", (value) => { script(value).input_refs[1].required_status = "pending_approval"; }],
  ["wrong input version policy", (value) => { script(value).input_refs[1].version_policy = "latest"; }],
  ["wrong schema for generated script", (value) => { script(value).output_refs[0].schema_ref = value.input_schema_ref; }],
  ["wrong registered response adapter", (value) => { script(value).response_adapter_ref = options(value).response_adapter_ref; }],
  ["missing final checkpoint", (value) => { script(value).approval.required = false; }],
  ["unconfirmed completion", (value) => { value.completion.required_artifacts[0].required_status = "pending_approval"; }],
  ["wrong terminal stage", (value) => { value.completion.terminal_step_ids = ["generate_continuation_options"]; }],
]) {
  test(`rejects broken continuation contract: ${name}`, () => {
    const directory = fixture();
    modify(directory, continuationPath, change);
    const result = validate(directory);
    assert.equal(result.code, 1, result.output);
    assert.match(result.output, /script.continuation/);
  });
}

for (const [name, change] of [
  ["optional target length", (value) => { value.$defs.config.required = []; }],
  ["fractional target length", (value) => { value.$defs.config.properties.target_length_chars.type = "number"; }],
  ["missing minimum length", (value) => { delete value.$defs.config.properties.target_length_chars.minimum; }],
  ["missing maximum length", (value) => { delete value.$defs.config.properties.target_length_chars.maximum; }],
  ["fewer than five choices", (value) => { value.$defs.continuationOptions.properties.options.minItems = 1; }],
  ["more than five choices", (value) => { value.$defs.continuationOptions.properties.options.maxItems = 6; }],
  ["missing selected direction", (value) => { value.$defs.continuationScript.required = ["title", "script_text"]; }],
]) {
  test(`rejects broken continuation schema: ${name}`, () => {
    const directory = fixture();
    modify(directory, continuationSchemaPath, change);
    const result = validate(directory);
    assert.equal(result.code, 1, result.output);
    assert.match(result.output, /script.continuation/);
  });
}

test("rejects a broken continuation prompt reference", () => {
  const directory = fixture();
  fs.unlinkSync(path.join(directory, "design/prompts/script-continuation/script.v1.md"));
  const result = validate(directory);
  assert.equal(result.code, 1, result.output);
  assert.match(result.output, /missing file.*script-continuation/);
});

test("rejects an adapter that silently changes the continuation output schema", () => {
  const directory = fixture();
  modify(directory, "capabilities/v1/response-adapters.json", (value) => {
    value.adapters.find((item) => item.id === "adapter.script_continuation_script_identity_v1").target_schema_ref = "../../schemas/v1/common.schema.json#/$defs/sourceInput";
  });
  const result = validate(directory);
  assert.equal(result.code, 1, result.output);
  assert.match(result.output, /script.continuation/);
});
