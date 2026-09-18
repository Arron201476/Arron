# Agent Context & Targeting Contract

Status: implemented baseline; deterministic target validation, natural-language candidate resolution and revision closure are available

Full revision lifecycle and frontend behavior are defined in `agent-targeting-and-revision-design.md`. This file remains the base contract for context capture, immutable selections and validation.

## 1. Ownership

Context capture, target resolution, stale checks and reference presentation belong to the General Agent Shell. A Skill may declare domain-specific context requirements, but it must not own a separate selection or positioning system.

```text
Agent Shell  -> capture, normalize, display and route context
Runtime      -> validate ownership, version, hash and persistence
Skill        -> interpret a resolved target in its domain
Rules        -> constrain the selected execution step
```

## 2. Context layers

1. `Conversation Context`: project and recent relevant messages.
2. `View Context`: current project, Artifact, Artifact Version and scope supplied by the client.
3. `Target Context`: current artifact, structured field/entity, script episode/scene/line, or text range.
4. `Execution Context`: the minimum confirmed upstream material required by a Worker.
5. `Runtime Context`: server-owned active Run, Capability, current Step, approval and available actions.

`client_context` is never workflow authority. The client may provide `viewed_run_id` and `viewed_capability_id` only as a view hint; Runtime validates project ownership before use. Legacy `current_run_id/current_capability_id` fields are normalized into the same view hint and then cleared. The server-owned `Active Run` is loaded independently and can never be overwritten by the viewed record. The Agent classifies every message as ordinary chat, progress inspection, artifact revision or a new Skill invocation.

`Skill Invocation` and `Business Run` are different scopes. Every recognized Skill call may create an Invocation record. Only `execution_mode=stateful_workflow`, after required validation and user confirmation, may delegate that Invocation to a Run. Recent context is assembled from Project messages plus the explicitly targeted Invocation/Run/Artifact; unrelated historical Runs are represented only by compact `Run Index` metadata unless explicitly selected.

Priority:

```text
immutable explicit selection
-> focused structured node
-> active artifact version
-> active/viewed Run
-> project conversation
-> clarification when the target remains ambiguous
```

This priority fills missing context; it never resolves contradictory explicit evidence. If a text selection, structured node or explicit command conflicts with the user's written target, the result is `conflict` and requires clarification. Natural-language resolution may only rank Runtime-provided candidates and cannot invent Artifact IDs, field paths or versions.

## 3. Generic selection payload

`selection_snapshot.selection` uses a Shell-owned schema:

```json
{
  "schema_version": "1.0.0",
  "artifact_id": "art_...",
  "artifact_type": "script_unit",
  "target_scope": "selection",
  "scope_key": "episode:3",
  "field_path": "script_text",
  "entity": {
    "episode_id": "3",
    "scene_id": "scene-3-2",
    "line_id": "scene-3-2-line-4",
    "block_type": "dialogue"
  },
  "text_range": {
    "start": 120,
    "end": 136,
    "selected_text": "...",
    "before_context": "...",
    "after_context": "..."
  },
  "display": {
    "artifact_label": "单集剧本",
    "location_label": "第 3 集 / 场 2 / 台词 4"
  }
}
```

The outer snapshot binds this payload to one immutable `artifact_version_id` and a canonical SHA-256 `snapshot_hash`.

## 4. Validation

Runtime rejects a message before any Provider call when:

- the Artifact Version does not belong to the current Project;
- the selection points to a different Artifact than the version;
- the View Context references a foreign Run or Artifact;
- the canonical snapshot hash does not match;
- the selected version is no longer the Artifact current version;
- a text selection is empty or its offsets are invalid.

Stale selections return `SELECTION_SNAPSHOT_STALE`. The Shell keeps the user instruction but requires a fresh selection. Similar text is never used as a silent fallback.

## 5. Frontend behavior

- Opening an Artifact establishes passive View Context.
- Selecting text establishes an explicit immutable Selection Snapshot.
- The Composer shows the exact reference above the input and provides a remove action.
- Switching Artifact or Artifact Version clears an incompatible selection.
- Sent user messages retain a visible reference summary after refresh.
- Aggregate script views are read-only; edits and selections that can mutate content are made against a single `script_unit`.
- A resolved explicit selection outside an active Run is generated, accepted and saved as a new Artifact Version in one request; no second “generate draft” or “confirm adopt” step is shown.
- A revision submitted during an active Run waits for the Runtime safe checkpoint and never mutates an in-flight step directly.

## 6. Capability adapter boundary

A Skill may map generic targets to domain context requirements, for example `scene_id -> adjacent scenes + character state`. It cannot redefine hashing, version checks, message persistence, Composer references or cross-project authorization.

Target Resolution and Revision are Shell/Runtime foundations, not a Skill. A Skill may register Artifact schemas, stable entity keys, editable scopes, protected fields and a Revision Adapter. A Skill without an Adapter still receives View/Target Context for inspection, but cannot claim an AI revision was executed.

## 7. Acceptance cases

- active Artifact without text selection;
- structured field selection;
- single-line and multi-line script selection;
- duplicate selected text at different offsets;
- selection cleared on Artifact switch;
- stale Artifact Version rejection;
- cross-project and cross-Run rejection;
- reference survives message reload;
- no Skill selected while the Agent still receives valid View/Target Context.
- client Run/Capability claims cannot override Runtime state;
- active Run plus ordinary greeting remains generic chat;
- progress questions use the persisted current Step and approval;
- “continue” never silently approves a pending checkpoint;
- a new Skill cannot start while another write Run is active;
- explicit selected micro-edit persists a new version and a durable completion reply.
