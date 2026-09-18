import fs from "node:fs";
import crypto from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";

const acceptanceDir = path.dirname(fileURLToPath(import.meta.url));
const planPath = path.join(acceptanceDir, "stage-8-demo-suite.json");
const plan = JSON.parse(fs.readFileSync(planPath, "utf8"));
const matrixPath = path.resolve(acceptanceDir, plan.source_matrix);
const matrix = JSON.parse(fs.readFileSync(matrixPath, "utf8"));
const errors = [];
const casesById = new Map(matrix.cases.map((item) => [item.id, item]));
const assigned = new Map();

for (const suite of plan.suites) {
  let caseIds = suite.case_ids ?? [];
  if (suite.selector) {
    caseIds = matrix.cases
      .filter((item) => Object.entries(suite.selector).every(([key, value]) => item[key] === value))
      .map((item) => item.id);
  }
  if (caseIds.length !== suite.expected_case_count) {
    errors.push(`${suite.id}: expected ${suite.expected_case_count} cases, resolved ${caseIds.length}`);
  }
  for (const caseId of caseIds) {
    if (!casesById.has(caseId)) {
      errors.push(`${suite.id}: unknown case ${caseId}`);
      continue;
    }
    if (assigned.has(caseId)) {
      errors.push(`${caseId}: assigned to both ${assigned.get(caseId)} and ${suite.id}`);
      continue;
    }
    assigned.set(caseId, suite.id);
  }
}

for (const caseId of casesById.keys()) {
  if (!assigned.has(caseId)) errors.push(`${caseId}: missing from Stage 8 disposition`);
}
if (assigned.size !== casesById.size) {
  errors.push(`Stage 8 disposition covers ${assigned.size}/${casesById.size} cases`);
}

for (const required of ["NOVEL-001", "NON-001", "VIDEO-001", "VIDEO-009", "NFR-003"]) {
  if (assigned.get(required) !== "demo_real_sample_and_manual") {
    errors.push(`${required}: real chain/content case must remain in the Demo gate`);
  }
}
for (const environment of plan.environments) {
  if (!environment.id || !environment.purpose) errors.push("Every environment needs id and purpose");
}
const resultTemplate = path.resolve(acceptanceDir, plan.result_template);
if (!fs.existsSync(resultTemplate)) errors.push(`Missing result template ${plan.result_template}`);

const fixtureInventoryPath = path.join(acceptanceDir, "fixtures", "fixture-inventory.json");
const fixtureInventory = JSON.parse(fs.readFileSync(fixtureInventoryPath, "utf8"));
for (const fixture of fixtureInventory.repository_fixtures) {
  const fixturePath = path.resolve(path.dirname(fixtureInventoryPath), fixture.path);
  if (!fs.existsSync(fixturePath)) {
    errors.push(`${fixture.fixture_id}: missing repository fixture ${fixture.path}`);
    continue;
  }
  const content = fs.readFileSync(fixturePath);
  const hash = crypto.createHash("sha256").update(content).digest("hex");
  if (content.length !== fixture.size_bytes) errors.push(`${fixture.fixture_id}: size changed`);
  if (hash !== fixture.sha256) errors.push(`${fixture.fixture_id}: SHA-256 changed`);
}

if (errors.length) {
  console.error("Stage 8 test plan validation failed:");
  errors.forEach((error) => console.error(`- ${error}`));
  process.exit(1);
}

console.log("Stage 8 test plan validation passed.");
console.log(`Cases assigned: ${assigned.size}/${casesById.size}; Suites: ${plan.suites.length}; Environments: ${plan.environments.length}`);
