# Skill Package Management Contract

Status: implemented locally through W8. This contract does not authorize deployment.

2026-09-10: lifecycle and package command protection is implemented in local source. Go
behavior tests remain unrun under the current application-control restriction;
build and static checks are not end-to-end acceptance.

## Package shape

A package is either a directory or ZIP containing exactly one root `SKILL.md`.
ZIP files may be rootless or wrapped in a directory whose name matches the
`name` in `SKILL.md`.

Allowed package files:

- `SKILL.md`
- `agents/openai.yaml`
- `content-agent/manifest.json`
- `content-agent/workflow.json`
- files below `references/`, `assets/`, `schemas/`, and `prompts/`
- Python source and declarative data below `scripts/`

Executable entrypoints must be declared in `content-agent/manifest.json` under
`scripts`. Each declaration has a lowercase kebab-case `id`, a normalized `.py`
path below `scripts/`, runtime `python`, and a description. Script files are
installable while execution remains disabled by default. Unsupported executable
formats are rejected with `SKILL_SCRIPT_FILE_UNSUPPORTED`.

## Limits and validation

- Compressed ZIP: 20 MiB
- Expanded package: 50 MiB
- Single file: 10 MiB
- Files: 256
- Compression ratio: 100:1 for files or archives at least 64 KiB

Import rejects absolute paths, traversal, Windows device paths, symbolic links,
non-regular files, encryption, duplicate paths, case-only collisions, multiple
package roots, unsupported files, and invalid metadata. Uploads are first copied
to `data/skills/quarantine`; a validated version is moved into immutable storage
identified by workspace, Skill name, semantic version, and SHA-256 content hash.

The same semantic version cannot be replaced with different content. Activation
verifies Skill name, capability ID, version, execution mode, and content hash
before the file swap and again against the refreshed Registry. Interrupted
attempts and stale activation directories are recovered on startup. A tampered
active package is marked `broken` and removed from the active Registry projection.

## HTTP API

All ZIP write routes accept `application/zip`,
`application/x-zip-compressed`, or `application/octet-stream`. The filename may
be sent as `X-Skill-Filename` or in `Content-Disposition`.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/v1/skills` | List active installation records |
| `GET` | `/api/v1/skills?include_uninstalled=true` | Include retained uninstall history |
| `POST` | `/api/v1/skills` | Install a ZIP or reinstall by package identity |
| `GET` | `/api/v1/skills/{installation_id}` | Read versions, Registry status, and audit events |
| `POST` | `/api/v1/skills/{installation_id}/versions` | Validate and install an upgrade ZIP for that identity |
| `POST` | `/api/v1/skills/{installation_id}/versions/{version}/activate` | Roll forward or back to an immutable version |
| `POST` | `/api/v1/skills/{installation_id}/enable` | Enable an execution-ready Skill |
| `POST` | `/api/v1/skills/{installation_id}/disable` | Disable a Skill without deleting history |
| `DELETE` | `/api/v1/skills/{installation_id}` | Uninstall while retaining versions and audit history |
| `GET` | `/api/v1/skills/install-attempts` | Read recent success/failure diagnostics |
| `GET` | `/api/v1/script-sandbox-policy` | Read the workspace policy and sandbox availability |
| `PUT` | `/api/v1/script-sandbox-policy` | Enable, disable, or update limits as an administrator |
| `GET` | `/api/v1/projects/{project_id}/skill-script-executions` | Read execution audit records |
| `GET` | `/api/v1/skill-script-executions/{execution_id}` | Read one execution result |
| `GET` | `/api/v1/skill-script-executions/{execution_id}/artifact?path=...` | Download a hash-verified output |

Directory import is available through the Runtime store for trusted local
administration. The public HTTP API intentionally does not accept an arbitrary
host filesystem path.

### Lifecycle commands

The enable, disable, version-activation, and uninstall routes require a UUID
`Idempotency-Key` and this JSON body, including for `DELETE`:

```json
{
  "expected": {
    "active_version_id": "skv_observed_version",
    "status": "installed",
    "enabled": true,
    "event_count": 3
  }
}
```

All values come from the same observed installation; `event_count` is the length
of its `events` array. The transaction checks the active version, status, enabled
flag, and event count before changing files or records. Counting retained events
also rejects an old confirmation when the state changed away and back again.
Changing the selected version preserves the existing enabled/disabled setting;
it is not an implicit enable command.

The response's `data` contains `receipt` and `installation`. The immutable receipt
includes `request_id`, `skill_installation_id`, `skill_installation_event_id`,
`skill_version_id`, `action`, and `actor_ref`. It is committed together with the
installation event through the existing idempotency store. The event binds the
request ID and canonical request hash. `installation` is the current record, not
a claim that the original action is still its latest state.

Repeat the identical command with its original key after an unknown response.
The server rechecks current user/scope authority before returning a stored result
and verifies that result against the durable audit event. A later uninstall or
reinstall does not replay the old action. Reusing a key with different arguments
is rejected. A new command based on stale state receives
`SKILL_INSTALLATION_STATE_CONFLICT` and requires a fresh observation.

`GET /api/v1/skill-management-options` advertises `lifecycle_commands: true`.
Clients must not use the old empty-body lifecycle protocol; the updated UI
disables these controls on older servers. It persists the original request before
dispatch and permits read-only refresh or an explicit retry of that same request.
A confirmed command whose follow-up refresh failed is refreshed without replay.
The recovery implementation and its verification limits are described below.

### Package commands

`GET /api/v1/skill-management-options` also advertises `package_commands: true`.
The four public package writes require an original UUID `Idempotency-Key`:

| Path | Request |
| --- | --- |
| `POST /api/v1/skills?scope=...&project_id=...` | Raw ZIP; installs an absent package or reinstalls a previously uninstalled identity |
| `POST /api/v1/skills/{installation_id}/versions` | Raw ZIP plus `X-Skill-Expected`, containing the JSON snapshot shown above without the `expected` wrapper |
| `POST /api/v1/skills/discovered/{capability_id}/install` | JSON `version`, `content_hash`, `scope`, and optional `project_id` |
| `POST /api/v1/skills/{installation_id}/directory-update` | JSON `version`, `content_hash`, `expected_active_version_id`, and full `expected` snapshot; the two active-version fields must match |

The UI uses RFC 5987 `Content-Disposition: attachment; filename*=UTF-8''...`
for filenames, including Chinese names, instead of non-ASCII header values.
The backend derives upgrade scope from the existing installation. Directory
commands resolve registered, visible sources; none accepts a caller's host path.
New install/adoption cannot silently overwrite an existing installed identity:
the user must select that installation and explicitly submit an upgrade.

All four return `data: { receipt, installation }`. The receipt contains the
lifecycle receipt fields plus `workspace_id`, `scope`, `scope_ref`, `skill_name`,
`capability_id`, `version`, `content_hash`, and `source_hash`. Actions are
`install_zip`, `upgrade_zip`, `adopt_directory`, and `update_directory`.
`source_hash` is the lowercase, unprefixed SHA-256 of the exact uploaded ZIP
bytes, or an empty string for directory commands. It is distinct from the
normalized package `content_hash`. The backend computes the archive hash itself;
it does not trust a caller-supplied hash in place of reading the bounded upload.

The existing quarantine/immutable-version/activation transaction commits both
the installation event and the receipt. It rechecks current scope authority and
the full expected installation snapshot before file activation, including event
count to reject changes away and back. The legacy trusted upgrade/directory
helpers also recheck their loaded snapshot inside this transaction. Project
Skill draft installation retains its separate approval and receipt contract.

A successful original-key retry validates the persisted audit binding and reads
current installation state, rather than replaying installation. This also applies
after a later upgrade, uninstall or reinstall, or after a directory source changes
or disappears. Cached retries do not start another validation attempt; requests
that were already validating concurrently may finish a separate diagnostic
attempt without writing another installation event. An error reading after a
committed installation must not relabel its completed attempt as failed.

The UI preserves the original File, scope, version snapshot and key during an
unknown response, checks the receipt against archive bytes and installation
events, and separates committed-but-unrefreshed results from unconfirmed writes.
It does not issue a competing package/lifecycle command while one is pending.
Old servers without the
capability flag have package writes disabled. Runtime and HTTP behavior test
sources exist, but have not run under the current Go application-control block.

### Local pending-command recovery

All eight management commands use the same IndexedDB journal, keyed by the
authenticated workspace and user. The database is
`content-agent-skill-commands`, version 1, store `pending`. This is browser-origin
storage, not a Skill installation directory or a new backend database.

The journal stores the exact UUID, target, observed installation snapshot and
directory version/hash. ZIP commands also preserve the original binary bytes,
filename, MIME type and last-modified value. Metadata is limited to 2 MiB and
archives to 20 MiB; both have SHA-256 checks. Reads validate the owner, schema,
request shape and hashes before reconstructing a File.

The initial claim must commit before the HTTP write is sent. The read/claim/delete
decision runs inside a single IndexedDB transaction; another page cannot replace
an outstanding request, and late cleanup cannot delete a different command.
Recovery never automatically submits on page mount. A user retry verifies that
the original saved record still exists and sends its original parameters/key,
independently of the new page's selected scope or the current remote version.

Unavailable/corrupt storage locks new writes but leaves a successfully loaded
catalog readable. There is no silent in-memory fallback, expiry, or automatic
deletion of a corrupt record. Once this page validates a success receipt or a
definite rejection, failed local cleanup is retried locally only, even if the
user's role was subsequently reduced. After a page reload, a remaining record
is conservatively treated as unknown and requires the original server receipt;
the page-local settled flag is not itself persisted as proof.

Tests use fake-indexeddb 6.2.5 and fresh component mounts to exercise all eight
commands, original ZIP bytes, owner/target isolation, competing claims, aborted
transactions and storage failures. They do not prove physical browser disk
durability, real browser reload behavior, or real Go/SDK integration. Those
acceptance gates remain open. Clearing browser data or changing the origin is
not a supported recovery path. Other application commands are outside this
journal's scope.

## Current execution boundary

`inline`, `background_task`, and `stateful_workflow` are selected from package
metadata and dispatched through their generic executors. Request principals and
workspace predicates own the tenant boundary; client-supplied actor fields are
not authoritative.

A declared script is exposed to the SDK only when its Skill is selected for the
current turn. Every execution additionally requires an enabled administrator
policy and an exact, one-time user approval for the tool arguments. See
`docs/skill-script-sandbox.md` for the isolation and operations contract.
