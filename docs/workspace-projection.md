# Workspace Projection Contract

## Purpose

Workspace Projection is the server-owned view model used by the web client. It keeps Skill discovery, input binding, configuration choices, artifact rendering, navigation, approvals, tasks, and interactions out of product-specific frontend branches.

The contract version is `1.0.0`. Registry revisions are deterministic SHA-256 digests of the public registries.

## Endpoints

- `GET /api/v1/workspace-projection` returns the current workspace registries and `revision`.
- `GET /api/v1/projects/{project_id}/workspace-projection` returns the same registries plus the project snapshot and `registry_revision`.
- The existing project snapshot endpoint remains available during migration, but the web client reads the projected endpoint.

## Registries

The `registries` object contains:

- `composer`: callable capabilities, execution mode, public Skill metadata, accepted inputs, input binding, default Prompt, dependency status, and trusted view keys.
- `artifacts`: artifact labels, renderer keys, editable state, preferred fields, actions, and optional collection member type.
- `navigation`: artifact order, grouping, and visibility.
- `tasks`: task status presentation keys.
- `approvals`: ordered match rules and approval view keys. The final entry is the generic action-list fallback.
- `interactions`: supported command groups for runs, artifacts, tools, and revisions.

Only public option names are exposed for configuration. Server filesystem paths, schema paths, Skill instructions, executable source, and credentials are excluded.

## Trust Boundary

The server may publish new view keys without a frontend release. The client executes only locally registered view keys:

- Composer: `source_materials`, `video_asset_set`.
- Configuration: `script_generation`, `script_continuation`, `video_extraction`, `json_schema`, `none`.
- Artifact: the local artifact renderer registry.
- Approval: `quality_review`, `adaptation_strategy`, `single_option`, `volume_fit`, `action_list`.

An unknown or missing key is not interpreted as HTML, a remote component, or executable code. It is projected to a read-only Inspector. Mutation and run-start actions are blocked until the client explicitly supports that view.

## Adding a Skill

A compatible Skill package provides its UI and input contract in `content-agent/manifest.json`:

```json
{
  "id": "story_review_workflow",
  "execution_mode": "stateful_workflow",
  "input_binding": {
    "source_type": "story",
    "asset_role": "primary_source"
  },
  "accepted_asset_kinds": ["text", "document"],
  "ui": {
    "icon_key": "clipboard_check",
    "sort_order": 900,
    "entry_view_key": "source_materials",
    "config_view_key": "json_schema"
  }
}
```

The package interface or `agents/openai.yaml` may provide the default Prompt. After installation and registry refresh, the Skill appears in the homepage starter and composer menu without an `App.tsx` capability-ID branch. A dependency that is missing or unavailable remains visible with diagnostics but cannot be invoked.

## Skill Administration

The `/skills` route consumes the installation APIs and the public projection. It supports ZIP install and upgrade, active-version switching, enable/disable, uninstall, dependency status, scope, registry status, and install-attempt diagnostics.

ZIP bytes are uploaded directly with `Content-Type: application/zip` and `X-Skill-Filename`. The package security and immutability checks remain server-owned.

## Compatibility Gate

Project SSE now sends a coalesced `stream.snapshot_invalidated` control notice
after each delivered non-delta event batch. The notice contains the project ID
and last changed project sequence, not an artifact payload or a new durable
event ID. The workbench validates its scope and reloads the authoritative
projection, so a new capability event does not require another frontend event
name just to update the workspace. Existing named events remain compatible.

Disconnected workspaces poll read-only snapshots at 3 seconds while an
execution or revision is active and 15 seconds while idle. Revoked stream access
closes the connection and discards pending reads. A snapshot-reset control clears
the native SSE replay ID and permits the authoritative snapshot to replace a
previously higher local revision; normal refresh retains version guards and
neither path automatically resubmits commands. Go behavior and native browser
reconnection remain unverified under the current execution restrictions.

W5 is accepted only when all of the following pass locally:

1. A dynamically named Fixture Skill renders in both entry points without editing `App.tsx`.
2. Unknown view keys become read-only Inspectors.
3. The four existing content workflows retain their input, prompt, configuration, and video-batch interactions.
4. Frontend unit tests and production build pass.
5. Capability, Workspace Projection, HTTP API, and full Go repository tests pass.
