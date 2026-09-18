# Agent 工程启动契约

## 技术路线决策：定 Eino

当前项目技术路线定为：

```text
前端：TypeScript
后端：Go
Agent Runtime：Go + CloudWeGo Eino
模型层：Claude / OpenAI / 其他 provider 可替换
```

这条路线替代此前文档里提到的“TypeScript Agent Worker + Claude Agent SDK”方案。原因是后端主体已经确定为 Go，Eino 是 Go 原生 LLM / Agent 框架，更适合把 `project_state`、`run_state`、artifact store、run events、interrupt/resume 和审批机制放在同一个后端运行时里。

Claude 仍然可以作为模型 provider 使用，但不把 Claude Agent SDK 作为主 Agent 框架。

## Mock 阶段

在接入真实 Eino Agent 前，先做 Go mock agent runtime：

```text
frontend TypeScript
-> backend Go
-> mock agent runtime
-> mock run_events / artifacts / approval_request
```

mock runtime 必须模拟 Eino 将来会提供的行为：

```text
按 next_action 推进
流式返回 run_event
生成或更新 artifact
能在 approval_request 暂停
能从 run_state.next_action 继续
能返回 step_failed 供前端 retry
```

这样后续从 mock 切到 Eino 时，只替换 Agent 执行层，不推翻前端和 Go 状态机。

本文档用于启动 Novel2Script Agent 新项目的第一层工程契约。目标是先固定 Agent 运行所需的 artifact、状态、路由、调度和剧本生成拼参边界，再进入 TypeScript 前端、Go 后端和 Eino Agent Runtime 的工程实现。

## 1. Artifact Schemas

### 命名原则

- 小说链拆集主产物统一命名为 `episode_split`。
- 不再把 `source_chunks` 作为主 artifact；它只作为 `episode_split.episodes[].source_refs` 或 `source_spans` 的内部字段出现。
- 小说链和非小说链都在 `episode_cards` 后汇合。
- 公共剧本生成前必须先形成 `script_context`，再生成 `script_unit`。

### 主 artifact 类型

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
```

### 统一 artifact 外壳

每个 artifact 都必须有统一元信息，payload 内部才是具体业务字段。

```json
{
  "artifact_id": "",
  "artifact_type": "",
  "project_id": "",
  "run_id": "",
  "version": 1,
  "status": "draft | pending_approval | confirmed | superseded | invalidated",
  "source_mode": "novel | non_novel",
  "derived_from": [],
  "payload": {},
  "created_at": "",
  "updated_at": ""
}
```

## 2. Workflow State

状态必须分层，不能把项目状态、执行状态和步骤状态混在一个字段里。

```text
project_state：项目当前总状态
run_state：一次 Agent 执行的状态
step_status：某一步 prompt/tool 的状态
dependency_invalidation：artifact 变更后造成的下游失效关系
```

### project_state

记录当前项目的稳定事实：

```text
project_id
source_mode
active_artifacts
confirmed_artifacts
latest_run_id
current_focus_artifact
approval_mode
```

### run_state

记录一次 Agent 执行的生命周期：

```text
run_id
project_id
intent
status: pending | running | waiting_approval | paused | completed | failed | cancelled
current_step_id
next_action
created_artifacts
updated_artifacts
invalidated_artifacts
events
```

### dependency_invalidation

局部修改后必须明确哪些下游产物失效。

```text
story_bible changed
-> episode_split, episode_cards, script_context, script_unit may be invalidated

episode_split changed
-> episode_cards, script_context, script_unit may be invalidated

episode_cards changed
-> script_context, script_unit may be invalidated

single script_unit dialogue changed
-> only that script_unit version changes unless continuity_delta changes
```

## 3. Router

第一版 router 只做最小意图识别，不做复杂自主决策。

```text
generate_novel
generate_non_novel
revise_artifact
revise_selection
chat
project_control
unknown
```

Router 输出只负责描述意图，不直接执行。

```json
{
  "intent": "",
  "confidence": 0.0,
  "source_mode": "novel | non_novel | null",
  "target_artifact": "",
  "selection_ref": null,
  "recommended_next_action": "",
  "needs_user_confirmation": false,
  "reason": ""
}
```

## 4. Runner

Runner 不采用固定 Step1-4 按钮流，而是按 `next_action` 推进。

```json
{
  "next_action": {
    "capability_id": "",
    "prompt_path": "",
    "selected_skills": [],
    "input_artifacts": [],
    "output_artifacts": [],
    "approval_policy": "auto | checkpoint | every_step"
  }
}
```

每一步执行都必须落运行事件，前端右侧 Agent 面板根据事件展示 loading、完成态和审批卡。

```text
llm_input_prepared
llm_output_received
tool_call_started
tool_call_completed
artifact_created
artifact_updated
approval_requested
approval_resolved
step_failed
run_completed
```

## 5. script_generate 拼参器

`script_generate` 不负责判断输入来自小说还是非小说。必须先由 `script_context_adapter` 把不同链路产物转成统一 `script_context`。

### 小说链输入

```text
story_bible
episode_split
episode_cards
source_refs
user_notes
```

转成：

```text
script_context
```

### 非小说链输入

```text
material_bank
story_seed
series_blueprint
episode_cards
user_notes
```

转成：

```text
script_context
```

### 公共剧本生成输入

公共 `script_generate` 只消费：

```json
{
  "episode_card": {},
  "script_context": {},
  "user_notes": []
}
```

这样剧本生成层只负责把单集卡写成剧本，不承担 source_mode 判断、上游规划修改或拆集调整。

## 技术栈边界

- 前端使用 TypeScript，负责编辑器、Agent 聊天、过程状态、审批卡和 artifact 展示。
- 后端使用 Go，负责项目、run/checkpoint、artifact store、权限、SSE/WebSocket 和内部 API。
- Agent Runtime 使用 Go + CloudWeGo Eino。
- 浏览器端不直接持有模型 API key，不直接调用模型 provider。
- Go 后端负责 project_state、run_state、artifact store、run events、审批和 Eino runtime 调度。

## 执行顺序

1. 固定本启动契约，并让 `preview.html` 作为唯一预览入口。
2. 补 artifact schema 和 workflow state 文档。
3. 建 TypeScript 前端壳、Go 后端接口、Go mock agent runtime 的最小目录。
4. mock 闭环稳定后，再把 mock runtime 替换为 Eino runtime。
