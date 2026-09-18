# OpenAI Agents SDK runtime

This Sidecar is the required Agent Harness for the content-production product.
It exposes one production turn path, `execute-stream`; the former read-only,
decision-only, and synchronous spike routes have been removed. There is no
Eino fallback.

## Ownership

The OpenAI Agents SDK owns:

- Agent and Runner execution;
- persistent model-facing Session history and tool items;
- `OpenAIResponsesCompactionSession` input compaction;
- intent, context selection, target discovery, and tool selection;
- optional Skill selection and specialist Agent orchestration;
- image input, video-frame input, and Artifact revision patches.

The Go Runtime remains authoritative for projects, durable messages, assets,
Goals, Runs, tasks, approvals, Artifact versions, lineage, authorization,
schema validation, transactions, and idempotency. SDK tools can write only
through the authenticated Runtime command API.

Declared Skill scripts are exposed as an SDK function tool only for Skills
selected in the current turn. The SDK owns tool selection, but the Go Runtime
owns administrator policy, exact user approval, package integrity, OCI sandbox
execution, and output audit. Uploaded code is never executed by the Sidecar or
directly on the host.

## Memory

Each project conversation maps to an SDK `SQLiteSession`. It is bootstrapped
once from the complete Go conversation and is then owned by the SDK. Before
history enters a model request, the Session invokes SDK-native Responses input
compaction. A gateway that cannot expose `/responses/compact` fails the
compatibility/release gate; the application does not replace SDK compaction
with a local summary or recent-message truncation.

### Private Memory Generation and Archive Recovery

Private project Memory is separate from SDK conversation Session history.
The Go Runtime owns per-user consent, immutable source references, generation
jobs, approval and publication. The Sidecar uses SDK extraction/consolidation
stages; enabling a worker never grants user consent or bypasses tool approval.

| Setting | Default | Effect |
| --- | --- | --- |
| `CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED` | `false` | Starts the private Memory generation worker. Requires the configured model and native workspace execution dependencies. |
| `CONTENT_AGENT_SIDECAR_MEMORY_ARCHIVE_DB_PATH` | empty | An absolute, dedicated SQLite file enables write-ahead capture and archive recovery. Empty disables this local recovery queue, not ordinary consent-bound archival. |

Archive recovery starts when its database is configured, independently of
generation and task-worker flags. It does not call a model. Generation can
remain disabled while completed sources are archived. Conversely, enabling
generation without the archive database does not provide durable local retries
for an interrupted source upload. Existing queued Go jobs remain pending when
the generation worker is disabled; a user's generation preference alone does
not start that worker.

Before enabling these settings in an authorized environment:

- Use matched Go and Sidecar revisions: recovery requires the internal
  `/internal/v1/agent-memory/archive-recover` endpoint and current schema.
- Provision a private directory for the dedicated service account. The factory
  does not create directories or configure Windows ACLs. Do not place the file
  in a shared project, upload area or publicly served directory. Direct symbolic
  links and a path or hard-link alias of the SDK Session file are rejected;
  this validation is not a filesystem sandbox or a substitute for permissions.
- Keep the directory and any backups under the same private retention policy.
  The queue contains frozen private rollout text and original consent metadata,
  not old execution tokens. Default limits are 64 MiB, 256 entries and 24 hours;
  the latter is an expiry deadline checked while the queue is running, not a
  background deletion guarantee while the service is offline.
- Verify a real completed source, exact recovery receipt, generation job and
  approved publication. `/healthz` alone does not prove these paths or gateway
  compatibility. Current repository evidence does not establish disk/ACL,
  cross-process, real Go/OCI/model or browser acceptance for this integration.

Recovery rechecks the original successful execution, current owner access,
Memory version and original consent revision. Unknown responses, service-auth
failures and `AGENT_MEMORY_ARCHIVE_NOT_READY` retain the pending item. Explicit
source/consent revocation permits cleanup. A matching receipt deletes the exact
pending key/hash; failure to delete remains retryable. Workers stop before the
queue closes. Deleted SQLite pages use `secure_delete=ON`; this does not erase
historical journals, filesystem copies, backups or already transmitted data.

For rollback, coordinate an authorized service stop, preserve the previous
code/configuration and any required private data snapshot, then restore the
compatible versions. Disabling the queue path stops recovery but does not erase
its file; disabling generation stops new claims but does not cancel durable Go
jobs. Neither switch revokes user consent. Do not delete the queue or clear SDK
checkpoints to make recovery appear successful. Old phase-less Memory workspaces
remain rejected during ordinary access. Explicit owner resume can bind a legacy
workspace only after its lease expires and the original checkpoint proves the
same phase, owner, source, model and exact stored snapshot. A conflicting target
workspace is never overwritten. Consolidation also requires its saved input
plan. Missing evidence remains a recovery blocker; this path still needs real
Go and SDK joint acceptance.

These instructions do not authorize deployment, restarting the current local
stack, modifying user databases, or changing machine security policy. The
repository's current acceptance restrictions remain in force.

## Run

The standard local stack starts Sidecar, Go, and the frontend together:

```powershell
.\scripts\start-local-demo.ps1
```

Defaults:

- frontend: `http://127.0.0.1:8860`
- Go Runtime: `http://127.0.0.1:8850`
- SDK Sidecar: `http://127.0.0.1:8871`

Stop the managed stack with:

```powershell
.\scripts\stop-local-demo.ps1
```

For standalone Sidecar development, install `.[dev]`, ensure
`backend/.env.local` contains the model and internal-token configuration, and
run:

```powershell
.\experiments\openai-agents-sidecar\run.ps1 -Port 8871
```

The health endpoint does not return credentials:

```powershell
Invoke-RestMethod http://127.0.0.1:8871/healthz
```

## Cancellation and concurrency

Cancelling a durable Agent turn cancels the SDK Runner and rolls the Session
back to the pre-turn checkpoint. Streaming exposes exactly one terminal event:
`agent.turn.committed`, `agent.turn.cancelled`, or `agent.turn.failed`.
An SDK tool interruption emits nonterminal `agent.turn.waiting_approval`; its
serialized `RunState` is sent only to the authenticated Runtime. Approval or
rejection resumes that exact state and exact SDK tool call instead of rerunning
the turn from user input. Privacy-filtered usage, latency, and correlation
telemetry is checkpointed with the private context and remains cumulative after
resume.
Turns sharing one conversation Session are serialized; the independent SDK
task worker can continue a Skill Run while the main Agent answers another
conversation turn.

## Local release gate

Run the full local-only gate from the repository root:

```powershell
.\scripts\run-agent-platform-local-gate.ps1
```

This validates pinned SDK surfaces, a real local stdio MCP call, Go/Sidecar/UI
regressions, the Agent eval contract, and the database rollback drill. It never
runs live gateway calls or deploys to a remote environment.
