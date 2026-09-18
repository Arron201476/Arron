# 共享 Schema / 类型合同

本文档定义 Novel2Script Agent 前端 TypeScript、后端 Go、mock runtime、未来 Eino runtime 共同遵守的数据类型。它承接 `api-event-contract.md`，重点解决字段命名、枚举、选区上下文、artifact patch 和前后端类型对齐问题。

## 设计原则

1. 字段使用 `snake_case` 作为 API JSON 标准。
2. TypeScript 可以在 client 内映射为 camelCase，但接口层必须保持 snake_case。
3. Go struct 使用明确 `json` tag，不依赖字段名自动转换。
4. 所有枚举值集中定义，不在前端或后端临时写字符串。
5. 版本字段必须参与更新，避免覆盖旧版本。
6. 局部修改必须携带 selection snapshot，不能只传 selected_text。
7. Artifact payload 可以随类型变化，但 artifact envelope 必须稳定。

## 基础类型

```text
ID: string
ISODateTime: string
JSONValue: null | boolean | number | string | JSONValue[] | object
```

## 枚举

### SourceMode

```text
unknown
novel
non_novel
```

### ProjectStatus

```text
idle
running
waiting_approval
paused
failed
completed
```

### RunStatus

```text
pending
running
waiting_approval
paused
completed
failed
cancelled
```

### StepStatus

```text
pending
running
waiting_approval
completed
failed
skipped
```

### ArtifactStatus

```text
draft
pending_approval
confirmed
superseded
invalidated
failed
```

### ArtifactType

```text
source_input
story_bible
episode_split
material_bank
story_seed
series_blueprint
episode_cards
script_context
script_unit
scripts
```

说明：

- `script_unit` 表示单个剧本单元。
- `scripts` 表示多个 `script_unit` 的聚合视图。

### MessageRole

```text
user
agent
system
```

### IntentType

```text
chat
generate_novel
generate_non_novel
revise_artifact
revise_selection
project_control
unknown
```

### ApprovalStatus

```text
pending
approved
revised
paused
rejected
expired
```

### ApprovalAction

```text
approve
revise_instruction
pause
reject
rerun_step
auto_continue
```

### FileStatus

```text
uploaded
parsed
failed
deleted
```

### RunEventType

```text
run_started
run_resumed
run_paused
run_completed
run_failed
run_cancelled
intent_detected
step_started
step_completed
step_failed
llm_input_prepared
llm_output_received
artifact_created
artifact_updated
artifact_invalidated
approval_requested
approval_resolved
message_created
file_attached
script_document_saved
script_suggestion_created
script_suggestion_resolved
script_comment_created
script_comment_updated
```

### EventLevel

```text
info
warning
error
```

### ScriptBlockType

```text
action
dialogue
transition
scene_note
```

### ScriptSuggestionStatus

```text
suggested
accepted
rejected
stale
superseded
```

### ScriptCommentStatus

```text
open
resolved
stale
deleted
```

### ScriptSaveStatus

```text
saved
dirty
saving
conflict
```

## TypeScript 类型草案

```ts
export type SourceMode = "unknown" | "novel" | "non_novel";
export type ProjectStatus = "idle" | "running" | "waiting_approval" | "paused" | "failed" | "completed";
export type RunStatus = "pending" | "running" | "waiting_approval" | "paused" | "completed" | "failed" | "cancelled";
export type StepStatus = "pending" | "running" | "waiting_approval" | "completed" | "failed" | "skipped";
export type ArtifactStatus = "draft" | "pending_approval" | "confirmed" | "superseded" | "invalidated" | "failed";
export type MessageRole = "user" | "agent" | "system";

export interface Project {
  project_id: string;
  title: string;
  source_mode: SourceMode;
  status: ProjectStatus;
  active_run_id?: string;
  current_focus_artifact_id?: string;
  active_artifacts: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface Artifact<TPayload = unknown> {
  artifact_id: string;
  artifact_type: ArtifactType;
  project_id: string;
  run_id?: string;
  version: number;
  status: ArtifactStatus;
  source_mode: SourceMode;
  derived_from: ArtifactRef[];
  payload: TPayload;
  created_at: string;
  updated_at: string;
}
```

TypeScript client 内部是否转 camelCase 后续再定；API 边界必须保留 snake_case。

## Go 类型草案

```go
type SourceMode string

const (
    SourceModeUnknown SourceMode = "unknown"
    SourceModeNovel SourceMode = "novel"
    SourceModeNonNovel SourceMode = "non_novel"
)

type Project struct {
    ProjectID              string            `json:"project_id"`
    Title                  string            `json:"title"`
    SourceMode             SourceMode        `json:"source_mode"`
    Status                 ProjectStatus     `json:"status"`
    ActiveRunID            string            `json:"active_run_id,omitempty"`
    CurrentFocusArtifactID string            `json:"current_focus_artifact_id,omitempty"`
    ActiveArtifacts        map[string]string `json:"active_artifacts"`
    CreatedAt              string            `json:"created_at"`
    UpdatedAt              string            `json:"updated_at"`
}

type Artifact struct {
    ArtifactID   string          `json:"artifact_id"`
    ArtifactType ArtifactType    `json:"artifact_type"`
    ProjectID    string          `json:"project_id"`
    RunID        string          `json:"run_id,omitempty"`
    Version      int             `json:"version"`
    Status       ArtifactStatus  `json:"status"`
    SourceMode   SourceMode      `json:"source_mode"`
    DerivedFrom  []ArtifactRef   `json:"derived_from"`
    Payload      json.RawMessage `json:"payload"`
    CreatedAt    string          `json:"created_at"`
    UpdatedAt    string          `json:"updated_at"`
}
```

Go 端可以对不同 `artifact_type` 定义 payload struct，但持久层和 API envelope 必须能保存 `json.RawMessage`。

## ArtifactRef

```json
{
  "artifact_id": "artifact_xxx",
  "artifact_type": "episode_cards",
  "version": 1,
  "node_id": "episode_01"
}
```

用途：

- 记录 artifact 来源。
- 记录 run_event 关联产物。
- 记录 approval 影响产物。
- 记录 selection context 的目标节点。

## SelectionContext

局部修改必须携带完整上下文。

```json
{
  "artifact_id": "artifact_xxx",
  "artifact_type": "scripts",
  "version": 1,
  "node_id": "line_03",
  "episode_id": 1,
  "scene_id": "scene_01",
  "line_id": "line_03",
  "block_type": "dialogue",
  "selection_start": 12,
  "selection_end": 46,
  "selected_text": "",
  "before_context": "",
  "after_context": "",
  "selection_hash": "sha256_xxx"
}
```

字段规则：

- `artifact_id`、`version` 必填。
- `selected_text` 必填，除非用户选中的是完整节点。
- `before_context` / `after_context` 用于模型理解，不用于定位。
- `selection_hash` 用于后端判断用户发送后选区是否已经变化。
- 如果 artifact 已更新导致 version 不一致，后端返回 `CONFLICT`。

## ScriptEditor 数据对象

`ScriptEditor` 的详细交互规格见 `script-editor-lexical-spec.md`。本节只定义前端 TypeScript、Go 后端、mock runtime 必须共同承认的数据对象。

### ScriptDocument

```json
{
  "artifact_id": "artifact_xxx",
  "version": 1,
  "source_mode": "novel | non_novel",
  "episodes": [
    {
      "episode_id": 1,
      "episode_no": 1,
      "title": "",
      "scenes": [
        {
          "scene_id": "scene_01",
          "scene_no": 1,
          "title": "",
          "location": "",
          "time_of_day": "",
          "lines": [
            {
              "line_id": "line_03",
              "block_type": "dialogue",
              "speaker": "女主",
              "text": "",
              "marks": []
            }
          ]
        }
      ]
    }
  ]
}
```

规则：

- `ScriptDocument` 是业务可读结构，不等同于 Lexical JSON。
- Lexical JSON 可以作为 `editor_state` 保存编辑状态，但不能替代 `ScriptDocument`。
- `episode_id`、`scene_id`、`line_id` 是跨版本定位基础，不能使用数组下标代替。

### ScriptTextMark

```json
{
  "type": "bold | italic | underline | strike",
  "offset_range": [0, 4]
}
```

### ScriptSuggestion

```json
{
  "suggestion_id": "suggestion_xxx",
  "artifact_id": "artifact_xxx",
  "base_version": 1,
  "target": {
    "episode_id": 1,
    "scene_id": "scene_01",
    "line_id": "line_03",
    "selection_start": 12,
    "selection_end": 46,
    "selection_hash": "sha256_xxx"
  },
  "before": "",
  "after": "",
  "reason": "",
  "status": "suggested",
  "created_at": "",
  "resolved_at": ""
}
```

规则：

- `suggested` 只表示候选建议，不写入正式正文。
- 接受后才生成新的 `scripts` artifact version。
- `base_version` 或 `selection_hash` 不匹配时，必须标记 `stale`，不能直接接受。

### ScriptComment

```json
{
  "comment_id": "comment_xxx",
  "artifact_id": "artifact_xxx",
  "version": 1,
  "target": {
    "episode_id": 1,
    "scene_id": "scene_01",
    "line_id": "line_03",
    "selection_start": 12,
    "selection_end": 46,
    "selection_hash": "sha256_xxx"
  },
  "author": "user",
  "body": "",
  "status": "open",
  "created_at": "",
  "updated_at": ""
}
```

规则：

- 正文改变导致 `selection_hash` 失效时，批注标记为 `stale`。
- 第一版不要求多人协作，但必须保留 `author` 和时间字段。

### ScriptDocumentSaveRequest

```json
{
  "base_version": 1,
  "document": {},
  "editor_state": {},
  "change_summary": "",
  "client_generated_ids": []
}
```

### ScriptSuggestionResolveRequest

```json
{
  "suggestion_id": "suggestion_xxx",
  "base_version": 1,
  "action": "accept | reject"
}
```

## ArtifactPatch

用户直接编辑 artifact 字段时使用 patch。

```json
{
  "base_version": 1,
  "patch": [
    {
      "op": "replace",
      "path": "/payload/episode_cards/0/title",
      "value": "第 1 集：被弃贵女重回白昼"
    }
  ],
  "note": "用户手动修改标题"
}
```

### PatchOp

```text
add
replace
remove
```

第一版只支持 JSON Pointer 路径，不支持文本 diff。剧本正文的局部 AI 修改通过 message + selection_context，不走 artifact patch。

## Message

```json
{
  "message_id": "message_xxx",
  "project_id": "project_xxx",
  "run_id": "run_xxx",
  "role": "user",
  "content": "",
  "attachments": [],
  "selection_context": null,
  "intent": "chat",
  "created_at": ""
}
```

`attachments` 只保存 `FileRef` 或 `file_id`，不把文件内容直接塞进 message。

## ApprovalRequest

```json
{
  "approval_request_id": "approval_xxx",
  "run_id": "run_xxx",
  "project_id": "project_xxx",
  "step_id": "step_xxx",
  "status": "pending",
  "title": "",
  "reason": "",
  "affected_artifacts": [],
  "risk_notes": [],
  "options": [],
  "created_at": "",
  "resolved_at": ""
}
```

Approval option：

```json
{
  "type": "approve",
  "label": "确认继续",
  "danger": false
}
```

## RunEvent

```json
{
  "event_id": "event_xxx",
  "run_id": "run_xxx",
  "project_id": "project_xxx",
  "step_id": "step_xxx",
  "type": "step_started",
  "level": "info",
  "message": "正在整理素材库",
  "artifact_refs": [],
  "approval_request_id": "",
  "payload": {},
  "created_at": ""
}
```

前端不能把 `RunEvent.message` 当作唯一 UI 文案来源。关键状态应由 `type`、`run.status`、`approval_request` 和 artifact status 共同决定。

## Artifact Payload 第一版要求

详细字段仍以 `artifact-schemas.md` 为准。共享 schema 第一版只强制：

- 每个 payload 顶层必须是 object。
- payload 内如果有列表项，必须有稳定 id，例如 `episode_id`、`scene_id`、`block_id`。
- 可被局部修改的节点必须有 `node_id` 或可推导的稳定路径。
- UI 不能依赖数组下标作为长期定位。

## 版本与冲突

### 读

前端读取 artifact 时拿到 `version`。

### 写

前端修改 artifact 时必须提交 `base_version`。

### 冲突

如果 `base_version` 小于当前版本，后端返回：

```json
{
  "error": {
    "code": "CONFLICT",
    "message": "当前内容已有新版本，请刷新后再修改。",
    "recoverable": true,
    "retryable": false
  }
}
```

## Go / TypeScript 对齐要求

进入正式开发前需要做：

1. 在 Go 后端定义上述枚举和 struct。
2. 在 TypeScript 前端定义对应 API types。
3. 写一组 schema fixture，前后端共同读取。
4. API response 必须通过 fixture 校验。
5. mock runtime 返回的数据必须使用这些类型。

## 禁止事项

- 前端临时创建不在枚举里的状态。
- 后端返回 camelCase JSON。
- 用 artifact 标题代替 artifact_id。
- 局部修改只传 selected_text。
- 用数组下标作为跨版本节点定位。
- 把文件正文直接塞进 message。
