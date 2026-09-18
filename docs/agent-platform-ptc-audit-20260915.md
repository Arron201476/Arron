# PTC Audit Receipt Regression

## G4.225

Scope: programmatic runtime tools must not return successful output when their
durable audit registration, start, or completion is unconfirmed. This is a
targeted implementation review, not the final independent global review.

Changed `agent_tools.py` to require a registration ID, a matching running start
receipt, and a matching completed receipt for tools in `programmatic_tool_ids`.
Completion persistence exceptions propagate instead of being logged and ignored.
The requirement also applies to direct calls to these granted tools. Other tools
retain their existing completion policy. No gateway, database, credentials,
deployment, or user-work modifications were made.

Verification against this change:

- `.tmp/goal-g4225-ptc-audit.xml`: 37 tests, 0 failures, 0 errors, 0 skipped.
- `.tmp/goal-g4225-runtime-audit.xml`: 14 tests, 0 failures, 0 errors, 0 skipped.
  The stdio test was explicitly deselected, not retried or counted as passed.
- New negative cases cover missing registration, invalid start receipts,
  invalid completion receipts, and completion persistence failure.
- These are isolated Python tests. They are not live provider acceptance, Go
  behavioral acceptance, browser acceptance, or production process restart tests.

## G4.226

Added `program_call_id` from SDK ToolContext through the sidecar request and Go
audit model into a separate `agent_program_tool_calls` table. Schema version is
77 in source only; no user database migration was run. The backend validates the
current workspace grant, rejects changed program identity on SDK call-ID reuse,
and checks execution ownership. Existing direct records have no program ID.
The frontend execution history labels these records as programmatic calls.
No program source, fingerprint, or raw program result is added to public audit.

SDK Runner regression exposed that approval callbacks lack caller metadata.
Only explicitly granted read-only, approval-never tools now register immediately
before invocation, when SDK ToolContext has its validated caller. All actual
approval-required tools retain their original approval path.

Verification:

- Initial `.tmp/goal-g4226-program-origin.xml`: 48 passed, 3 failed; retained as
  evidence of the callback metadata mismatch, not accepted as a passing report.
- Final `.tmp/goal-g4226-program-origin-final.xml`: 60 tests, no failures, errors
  or skipped tests. The stdio test remained deselected. Includes isolated SDK
  Runner caller propagation for three execution identities, cache rebinding
  rejection, malformed caller rejection, cancellation and native state recovery.
- `.tmp/goal-g4226-program-origin-ui.xml`: 6 tests, no failures, errors or skips.
- TypeScript build, Go runtime/agenttool vet and full backend build exited 0.
- New Go tests cover origin persistence across database reopening, rebinding and
  grant rejection, but were NOT executed under the existing Go behavior-test
  restriction. Build/vet do not prove migration or persistence behavior.

## G4.227

Background worker program lifecycle events now publish fixed progress messages
through the existing task progress endpoint. A task-local callback carries the
claim and pause signal; heartbeat publication shares a lock and the latest
message, so it cannot overwrite program progress with a stale generic message.
The callback is reset outside the execution task and does not enter RunState.
The existing frontend task view renders `progress_message` in `App.tsx`.

`.tmp/goal-g4227-background-program-final.xml`: 60 tests, no failures, errors or
skips. Includes the actual background worker and SDK streamed Runner with an
isolated Responses model and fake backend, program completion/incompletion,
heartbeat retention, no private program data in progress, pause receipt
propagation, concurrent claim separation and cleanup after publication failure.
Also covers existing background worker behavior and the PTC suite. This is not
a live model/backend/frontend E2E or a database acceptance result.

## G4.228

Stateful worker program events now use the existing authenticated attempt
heartbeat route with an optional fixed `program_status` enum. The backend runs
its existing ownership, token, snapshot, lease and lifecycle checks before
persisting a changed status and appending `task.progressed`. Ordinary heartbeats
do not clear progress. Paused/result-received handoffs return their existing
receipt without changing progress. SDK workers verify the returned program
status for running attempts and stop on a mismatched acknowledgement.

Source schema version 78 adds `execution_program_progress`, keyed to an attempt
with cascading deletion. Attempt and current-task reads project this status;
the frontend renders fixed labels only for running tasks and preserves task
pause/failure/completion and result-repair status precedence. No database was
migrated in this batch.

Verification:

- `.tmp/goal-g4228-program-regression.xml`: 77 tests, zero failures/errors/skips,
  covering PTC plus both worker progress/liveness suites.
- `.tmp/goal-g4228-stateful-program-ui.xml`: 22 tests, zero failures/errors/skips.
- TypeScript build, full Go backend build and runtime/httpapi vet exited 0.
- New Go progress persistence/projection tests were added but NOT run. Existing
  Go behavior-test restrictions remain; static checks do not prove SQL migration,
  lifecycle race handling or event persistence. No live provider/browser E2E.

## Remaining Work

- Program-level status is implemented in all three execution paths in source;
  same-version live frontend/backend/provider acceptance remains outstanding.
- Durable caller attribution is implemented in source but needs actual Go
  persistence/migration acceptance; isolated SDK identity checks do not prove
  the complete production-worker-to-database paths.
- Three production execution modes need integrated acceptance, including
  cancellation, recovery and result delivery.
- Computer and Voice/Realtime remain mandatory, unfinished delivery scope.
- Same-version full regression and independent global review remain open.

Earlier G4.223 added the workspace PTC option and frontend control; G4.224 added
sanitized main-chat program lifecycle projection. Their targeted results were
7 frontend tests plus TypeScript and Go vet for G4.223, and 29 Python tests plus
21 frontend tests and TypeScript for G4.224. These historical results do not
replace regression against subsequent changes. The central progress document
could not be updated due to access denial; its G4.222 entry is not the latest
implementation state.
