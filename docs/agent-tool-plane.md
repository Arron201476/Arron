# Agent Tool Plane

Status: workspace configuration and configuration-bound execution implemented;
real external integration acceptance remains pending
Contract version: `1.0.0`

## Ownership

The Go Runtime owns the trusted tool catalog, policy, approval state, and audit
ledger. The OpenAI Agents SDK Sidecar owns SDK tool exposure and execution. A
Skill may reference a tool as `server/tool`, but it cannot declare an endpoint,
command, credential, or policy override.

Configure the catalog with `CONTENT_AGENT_TOOL_CONFIG_PATH`. A relative path is
resolved from the project root. See `configs/agent-tools.example.json`.

## Tool Kinds

- `runtime_function`: authoritative Go Runtime query or command exposed as an
  Agents SDK Function Tool.
- `mcp`: operator-managed Streamable HTTP, HTTP/SSE, or allowlisted stdio tool.
- `hosted`: SDK-native `web_search`, `file_search`, `code_interpreter`, and
  `image_generation`, wrapped with `Agent.as_tool` for common approval and audit.
  The native defaults are disabled. Code and image generation require approval.

## Trust Boundary

- Remote MCP endpoints must use HTTPS. Plain HTTP is accepted only for loopback.
- URLs cannot contain credentials, query strings, or fragments.
- Header and process environment values name environment variables; literal
  secrets are not accepted in the JSON catalog.
- stdio is disabled unless an operator enables it and allowlists both the
  absolute command and working-directory roots.
- `allowed_tools` is mandatory. A Skill cannot expand it.
- Write and sensitive tools default to `approval: always`.
- A server containing an approval-gated tool must use `max_retries: 0` to avoid
  repeating a non-idempotent external action.
- Optional `workspace_ids` on an MCP server or Hosted Tool restricts the operator
  grant. An omitted or empty list preserves the legacy shared grant. Use explicit
  workspace IDs for private credentials and vector stores; never treat a shared
  definition as tenant-private.

## Workspace Configuration

`GET /api/v1/agent-tools/configuration` returns public configuration options and
their current workspace version. Administrators can PATCH the same resource:

```json
{
  "expected_version": 0,
  "enabled": {
    "mcp:example-story-data": true,
    "hosted:native-web-search": true
  }
}
```

The patch is atomic and version-checked. Editors/viewers and service credentials
cannot modify it. Settings are persisted separately for each workspace; a
workspace override cannot expose a server or vector store excluded by the
operator grant. Removing a grant makes an old override inert.

These settings select trusted definitions; they are not an endpoint, process,
environment-variable, or credential authoring API. Missing MCP connections still
require operator configuration through `CONTENT_AGENT_TOOL_CONFIG_PATH`. Native
file search cannot be enabled until an operator grants vector stores, either on
an explicitly named tool or an override of the `native-file-search` definition.
The legacy workspace credential-reference inventory remains metadata only.
User-managed connection credentials use the separate encrypted flow below;
legacy `env://` and `vault://` strings are never resolved on behalf of a user.

The Skill management page exposes workspace switches, refresh, permission/error
states, and missing-vector-store diagnostics. Older backends remain read-only.
Saving reloads the tool inventory and Skill dependencies. Main SDK requests and
background callbacks use the persisted execution identity to select the same
workspace catalog. Historic managed/directory Skill execution bindings also
resolve dependencies in that workspace, without changing their package version.

## Execution Contract

### User And Workspace MCP Credentials

An operator can expose named credential fields on a trusted MCP definition:

```json
"credential_headers": { "Authorization": "api-token" }
```

For stdio, use `credential_environment` instead, for example
`{"SERVICE_API_TOKEN": "api-token"}`. These maps contain target names and field
names, not values. Users cannot change endpoints, commands, target header names,
environment-variable names, or the allowlist through the credential API. The
server rejects target collisions, unsafe headers, and interpreter/loader control
variables. Ordinary operator environment references continue to work separately.

At process startup, `CONTENT_AGENT_MCP_CREDENTIAL_KEY` must contain a
base64-encoded random 32-byte key. Supply it through the operator's secret
management process; do not commit it, put it in the catalog, or reuse the Sidecar
service token. AES-256-GCM protects values in `mcp_connections`, with workspace,
owner, server, and record version authenticated as associated data. Preserve the
key separately from database backups. Key rotation/re-encryption is not provided
by this API; changing or losing the key makes existing records unreadable until
the original key is restored or users replace the affected credentials. Without
a key, new credential writes and connections fail closed; revocation remains
available. This code change does not configure a key in a running environment.

`GET /api/v1/mcp-connections` returns only approved field names, scope, status,
versions, and management permissions. `PUT` on the same endpoint accepts
`server_id`, `scope` (`user` or `workspace`), `expected_version`, `request_id`, and
either a complete `values` map or `delete: true`. Reuse the same request ID for a
retry of the exact update. Editors manage their own values; workspace credentials
require an admin/owner, rechecked in the write transaction. User values take
precedence over workspace values. Revoking a personal credential may fall back
to an existing workspace credential, as stated in the confirmation. Revocation
clears the current ciphertext and preserves a version tombstone; it does not erase
historical database backups or revoke a token at its external issuer.

The Skill management page's MCP section exposes these controls. Secret inputs
are write-only, masked, cleared after submission/scope changes, and never
pre-populated from the server. Missing credentials disable effective MCP tools
without preventing unrelated chat. The operator enable switch is distinct from
credential readiness.

The private `POST /internal/v1/mcp-connections/resolve` endpoint requires both
service authentication and an active, exact execution identity. It resolves the
persisted execution owner, role, workspace, credential binding and version in one
transaction. The Sidecar passes resolved values only to native SDK MCP connection
parameters, never process-global environment or model context/checkpoints.
Responses are `no-store`. Credential changes bind new tool configuration hashes;
an old connection cannot start a new audited call with an old approval. Already
dispatched remote requests cannot be recalled by local revocation.

Schema 49 adds encrypted connection records and the execution credential owner
on tool configuration snapshots. Legacy references remain inert. Workspace
deletion clears current ciphertext. Isolated tests cover all three execution
identities over the real Go/SDK/stdio boundary, and separate native SDK HTTP/SSE
transport and UI fixtures. These are not real external-account/model acceptance.

The Sidecar records the SDK `tool_call_id` before execution. Go stores a
redacted argument summary and canonical payload hash. Approval-gated calls stay
in `pending_approval`; transport execution cannot start until the approval is
resolved. Completion, failure, cancellation, result size, and project events are
recorded against the same call.

External tool descriptors contain a configuration hash covering their execution
parameters. The SDK supplies it when registering and starting a call. Runtime
stores the exact hash before approval and includes it in the approval subject.
Changing endpoints, commands, environment references, vector stores, or workspace
settings cannot reuse that approval. A workspace settings revision is included
even when a disable/enable cycle returns to the original settings. A legacy
external call without a bound configuration must be reissued, not silently bound
to the currently configured service. Unchanged runtime function tools retain
their existing contract; Skill script package binding is enforced separately.

Database v38 adds `workspace_agent_tool_settings` and
`agent_tool_config_snapshots`. This is a local implementation change, not proof
that a specific gateway or external service supports every Hosted Tool.

Public endpoints expose descriptors and approval state but never MCP endpoints,
commands, or environment references. The private catalog and execution ledger
write endpoints require the Sidecar bearer token.

## Skill Dependency

```yaml
dependencies:
  tools:
    - type: mcp
      value: story-data/lookup_story_fact
      transport: streamable_http
```

If a server or tool is missing, disabled, or configured with a different
transport, the Skill is marked unavailable with an actionable diagnostic. The
dependency is never silently ignored and is never fetched from a Skill-provided
URL.
