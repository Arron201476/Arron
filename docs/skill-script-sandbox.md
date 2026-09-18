# Skill Script Sandbox Contract

Status: implemented locally in W8. No test-machine or production deployment is authorized.

## Boundary

Uploaded script files may be installed, inspected, enabled as Skill content, and
selected by the SDK while script execution remains disabled. Execution is
available only when all of these conditions hold:

1. The active Skill manifest declares the exact script ID and Python path.
2. The Skill is installed and enabled in the authenticated workspace.
3. An administrator has enabled the workspace script policy.
4. The SDK selected that Skill for the current turn.
5. The user approved the exact tool-argument hash for this call.
6. A configured OCI sandbox is available.

There is no host-process fallback. Missing or invalid sandbox configuration is a
startup/configuration error or an unavailable policy state.

## Script Identity

`execute_skill_script` accepts `capability_id`, `skill_name`, `script_id`, and
`input_json`. Prefer the exact selected capability ID. Different scopes can
expose packages with the same display name but different capability IDs; their
script permissions and version/content-hash snapshots are kept separately.

For legacy calls, an omitted or empty `capability_id` is accepted only when the
name and script ID identify one selected package. Ambiguous names are rejected
before registration, not resolved by catalog order. The Go endpoint also checks
explicit identity against the selected snapshot or invocation and the persisted
approval pin. Adding the optional field never rewrites the original argument
payload or an existing approval hash. SDK checkpoints with ambiguous legacy
name-only calls cannot silently select another package; a new explicit call is
required if the original identity cannot be recovered unambiguously.

## Manifest

```json
{
  "schema_version": "1.0.0",
  "id": "report_renderer",
  "version": "1.0.0",
  "execution_mode": "inline",
  "scripts": [
    {
      "id": "render-report",
      "path": "scripts/render_report.py",
      "runtime": "python",
      "description": "Render the confirmed report data."
    }
  ],
  "ui": {}
}
```

The Python process receives two positional arguments: the read-only
`/input/input.json` file and the writable `/output` directory. It also receives
only these environment names: `CONTENT_AGENT_INPUT`, `CONTENT_AGENT_OUTPUT`,
`LANG`, `PATH`, and `PYTHONHASHSEED`.

## OCI Configuration

Configure both required variables together:

```text
CONTENT_AGENT_SCRIPT_OCI_COMMAND=C:\absolute\path\to\docker.exe
CONTENT_AGENT_SCRIPT_PYTHON_IMAGE=registry.example/python@sha256:<64 lowercase hex characters>
```

`CONTENT_AGENT_SCRIPT_SANDBOX_ADAPTER` may be `windows` or `linux`; otherwise it
defaults to the server host OS. The image must already be present in the local
engine because container creation uses `--pull never`.

The container has no network or IPC namespace sharing, a read-only root, all
Linux capabilities dropped, no-new-privileges, a non-root user, read-only Skill
and input mounts, and isolated tmpfs mounts for work, temporary files, and
outputs. Default per-call limits are 30 seconds, 0.5 CPU, 256 MiB memory, 32
processes, 16 MiB output, 16 MiB temporary space, 64 open files, and no core
dumps. Policy limits are validated before they are persisted or used.

## Native Workspace Startup

G4.36 adds host lifecycle wiring for the persistent SDK workspace. It reuses
the configured OCI adapter and digest-pinned image above. It never installs an
engine, pulls an image, changes an administrator policy, or falls back to a host
process. The separate server switch is off by default:

```text
CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED=false
```

An explicit `true` requires the existing isolated OCI adapter. The server binds
the native workspace routes to that engine before serving requests. Only after
the listener and Agent turn manager start does it run bounded cleanup. Each batch handles
at most eight eligible records with a two-minute overall deadline, followed by
a one-minute delay. Cleanup operates only exact recorded environment handles;
unknown engine outcomes stay pending. Shutdown cancels the worker and waits up
to twenty seconds before closing the Store. A timeout is not confirmation that
an environment was removed.

This is not a release-ready enablement instruction. G4.37 wires the three
sidecar entrypoints and native lease-guard ownership behind the same explicit,
default-off switch. G4.38 connects versioned project-file publication and the
existing Skill draft flow in source; real Go/OCI acceptance remains pending.
G4.39 adds an internal PTY engine transport, but its Runtime/SDK approval and
pause wiring, full native input/repair integration and user acceptance remain pending.
No local environment variable or running server
was changed. If an operator later disables the host switch after using native
workspaces, cleanup must first be drained and verified; the disabled server does
not implicitly operate an engine that was configured only for Skill scripts.

The dedicated `POST /internal/v1/native-workspaces/{session_id}/lease/renew`
operation only extends an unexpired current holder. It cannot reserve a new
workspace, change generation, take over a lease, or create an environment.
Pausing executions may renew their existing holder while saving. The transient
sidecar lease guard renews every fifteen seconds with a ten-second request
deadline; failure aborts its Runner owner without retrying or recreating files.
The three entrypoints now use this guard, but real Go/OCI execution remains a
separate acceptance gate.

The sidecar keeps one execution-scoped transport across consecutive Runner
calls and resumes from a confirmed snapshot instead of creating an empty tree.
The private recovery endpoint returns the sealed initial resource manifest and
the exact latest snapshot version/hash, subject to current identity, lease and
policy checks. SDK state must match these trusted references. Skill handoffs
share one session only within the same Runner and execution binding.

Main-turn commits confirm SDK pre-stop cleanup, snapshot persistence and backend
shutdown before committing a terminal Go result. Renewal pauses for that commit
and resumes on failure. Background and stateful results also require confirmed
cleanup. Streaming cancellation waits for the actual run-loop task; persistence
and shutdown are serialized and unknown outcomes are not replayed by subsequent
cleanup callbacks. Isolated SDK/file tests are not real engine acceptance.

## Native PTY Transport

`StartWorkspacePTY` and `WriteWorkspacePTY` are internal OCI engine methods,
not user authorization endpoints. Schema 63 and the internal POST
`/internal/v1/native-workspaces/{session_id}/pty` now bind them to the current
execution, lease, resource policy, original SDK arguments and consumed approval.
Startup uses `runtime:exec_command`; each input or poll uses
`runtime:write_stdin` and also rechecks the original startup approval. Both keep
explicit approval and zero automatic retries. This transport is not yet wired
to SDK `pty_exec_start`, `pty_write_stdin`, `pty_terminate_all` or the approval
pause lifecycle; the sidecar therefore still does not advertise PTY support.

Runtime persists the intent and reserves receipt capacity before engine I/O.
Each process has a session-scoped SDK integer ID, an opaque engine identity,
the original environment binding and a sequence. An input requires the expected
sequence and exact approved characters. Completed requests return their original
hash-bound binary receipt without repeating I/O; running, failed or inconsistent
receipts do not retry. A call already used for a one-shot command cannot also
launch a PTY, and the reverse is rejected. Historical receipts do not assert
current process liveness. Confirmed environment shutdown marks live records lost;
project deletion also erases stored outputs and reservations.

Per-session history is bounded to 128 processes, 512 I/O operations and 128 MiB
of receipts, additionally charged against the existing workspace storage quota.
Each operation reserves 2 MiB until completion. The PTY HTTP request is bounded
to 256 KiB and its response to 2 MiB; other control endpoints keep their limits.
The Python client checks request, process, sequence, exact bytes, exit state and
result hash before exposing a receipt. Unknown responses and redirects are not
retried or followed.

The fixed embedded `workspace_pty.py` bridge runs inside the already verified
container as UID 65532, through `python -I -B -u -c`, with a cleared environment.
Python's standard PTY implementation creates the controlling terminal; the OCI
CLI uses `exec --interactive`, never a host terminal or `exec -t` fallback.
TTY size is 80x24, with `TERM=xterm`; non-TTY commands receive stdin EOF.
The bridge is not copied into an executable workspace file and does not require
relaxing `noexec`. The normal digest, network, mount and resource checks remain.

Process identity is scoped to the complete workspace handle. Existing IDs are
not relaunched; a rebuilt engine reports a missing process rather than treating
a file snapshot as a live process. Total command lifetime is bounded by the
administrator policy (default 30 seconds, at most 300), independently of each
1-30000 ms poll. Inputs are at most 64 KiB and output is bounded to 1 MiB per
receipt, including output produced between polls. Receipts include actual input
bytes written, so early exit does not confirm unwritten bytes. The bridge drains
output and enforces lifetime while idle. Explicit termination signals the owned
process group while its leader is still unreaped, then reaps that child.

Unknown I/O or transport shutdown triggers the existing exact-container cleanup,
not an automatic restart. Failed cleanup remains unconfirmed. Confirmed workspace
deletion closes its streams; finished processes release buffers but retain bounded
identity tombstones. Per-workspace concurrency is at most 16 and additionally
constrained by the process quota; the engine allows at most 128 active terminals
and 4096 recorded identities. These bounds are not a replacement for Runtime
lease, storage quota or durable replay protection.

SDK-managed cleanup currently stops a session on Runner return, which would
terminate a live terminal during an approval interruption. The remaining wiring
must resolve that lifecycle with the public SDK session configuration while
retaining approval and expiry checks. A live session, serialized RunState and
file snapshot are distinct state surfaces; see the [official Sandbox Agents
guide](https://developers.openai.com/api/docs/guides/agents/sandboxes).

Protocol and existing native-runner fixture tests passed in G4.39. G4.40 adds
real loopback transport tests with fake server receipts and Go test source for
the durable approval, input, cancellation, deletion and v62 migration paths.
Go build/vet does not execute that source: Application Control still prevents
Go behavior tests, and neither the new real-OCI tests nor a real POSIX PTY have
run. This is not an instruction to enable or deploy the feature.

## Native File Publication

The native working tree is not automatically a project file or an installed
Skill. `prepare_workspace_publication` selects explicit relative source and
destination paths, saves a checkpoint through the SDK snapshot interface, and
returns the exact snapshot version/hash and expected project file versions.
This checkpoint does not stop the live SDK session. Subsequent edits do not
replace the prepared bytes. An unknown snapshot save prevents cleanup from
silently retrying that save.

`publish_workspace_files` requires durable approval of those exact arguments.
The internal publication endpoint copies only the selected immutable snapshot
bytes into existing project file history in one transaction. It rechecks the
current execution, lease, role, policy, approval, destination versions and
aggregate quotas. It does not read or mutate the live engine. Repeated receipt
reads preserve file ordering and historical versions rather than overwriting
newer edits. `.skills` is reserved for installed resources and cannot be a
project publication destination; an edited copy can be published to a draft.

Publication has an exact-endpoint 256 KiB request and 512 KiB response limit,
not the 4 KiB/64 KiB limits of small control operations. A 256-file loopback
fixture verifies multi-file selections and metadata receipts above those old
limits. This allowance does not change file byte, path, history or Skill limits.

Schema 62 adds binary metadata and permits multiple paths per audited
publication call. It preserves legacy text metadata serialization so unchanged
Skill draft hashes remain compatible. Exact-version downloads return original
bytes with attachment/nosniff headers; the existing Working Files dialog shows
binary metadata and downloads instead of an empty text preview. Publication
completion refreshes that list. The existing validate/export/install Skill
pipeline consumes these project files, including binary resources. Neither
publication nor validation implicitly installs a Skill or saves to a user's
computer.

If installation commits but dependency activation fails, its exact verified
version remains discoverable in that execution with readiness false. A later
explicit `load_skill_instructions` can activate it without reinstalling or
repeating approval. For an upgrade, the old active version and permissions stay
in place until that load succeeds; the new version/hash/scope is rechecked
against the installed selection. This pending activation is not a new tool grant
or an automatic retry of dependency actions.

Native files are bounded to 16 MiB per file; existing project limits remain 256
paths, 4096 versions, and 64 MiB of history plus the workspace storage quota.
Legacy UTF-8 patch writes remain bounded to 1 MiB. Skill package/resource limits
are checked separately and can be lower. This source migration has not been
applied to any user database. Isolated SDK and UI results do not prove a running
Go/OCI publication or Skill installation workflow.

## Audit And Outputs

The Runtime persists the workspace, project, conversation, approved tool call,
Skill version, script ID/path/runtime, adapter, engine, digest-pinned image,
input SHA-256, limits, allowed environment names, timestamps, status, exit code,
bounded stdout/stderr summaries, error code, and output manifest. Raw script
input is not stored in the execution row.

Container output is exported as a bounded tar stream. Extraction rejects
absolute/traversal/device paths, links, non-regular files, duplicate or
case-colliding paths, excessive file counts, and disk overflow. Downloads are
restricted to the recorded manifest and recheck file size and SHA-256 before
serving.

## Verification

Default tests use fake Windows and Linux engine adapters to inspect every OCI
argument and to exercise archive, timeout, path, credential-environment, and
approval boundaries. A real-engine gate is present but opt-in:

```powershell
$env:CONTENT_AGENT_SCRIPT_SANDBOX_INTEGRATION='1'
$env:CONTENT_AGENT_SCRIPT_OCI_COMMAND='C:\absolute\path\to\docker.exe'
$env:CONTENT_AGENT_SCRIPT_PYTHON_IMAGE='registry.example/python@sha256:<digest>'
go test ./internal/scriptsandbox -run OCIIntegration -count=1
```

The real gate verifies blocked network access, absent host credentials, a
read-only Skill mount, process limits, timeout cleanup, and rejection of linked
outputs. If Docker or Podman is unavailable, the policy reports unavailable and
the real-engine gate remains explicitly unverified.
