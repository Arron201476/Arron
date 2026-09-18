# Workflow State

本文档定义 Novel2Script Agent 的项目状态、运行状态、步骤状态、事件流、审批请求和失效传播规则。

## 状态分层

```text
project_state：项目当前稳定状态
run_state：一次 Agent 执行的生命周期
step_status：某个步骤的执行状态
run_event：给前端和日志使用的追加事件
approval_request：等待用户处理的审批卡
dependency_invalidation：artifact 变更造成的下游影响
```

## project_state

```json
{
  "project_id": "",
  "source_mode": "novel | non_novel | unknown",
  "active_artifacts": {},
  "confirmed_artifacts": {},
  "latest_run_id": "",
  "current_focus_artifact": "",
  "approval_mode": "auto | checkpoint | every_step",
  "status": "idle | running | waiting_approval | paused | failed | completed"
}
```

## run_state

```json
{
  "run_id": "",
  "project_id": "",
  "intent": "",
  "status": "pending | running | waiting_approval | paused | completed | failed | cancelled",
  "current_step_id": "",
  "next_action": {},
  "created_artifacts": [],
  "updated_artifacts": [],
  "invalidated_artifacts": [],
  "approval_request_id": "",
  "started_at": "",
  "ended_at": ""
}
```

## step_status

```json
{
  "step_id": "",
  "capability_id": "",
  "status": "pending | running | waiting_approval | completed | failed | skipped",
  "prompt_path": "",
  "selected_skills": [],
  "input_artifacts": [],
  "output_artifacts": [],
  "started_at": "",
  "ended_at": "",
  "error": null
}
```

## run_event

`run_event` 是追加日志，前端右侧 Agent 面板按事件流展示进度。

```json
{
  "event_id": "",
  "run_id": "",
  "step_id": "",
  "type": "llm_input_prepared",
  "message": "",
  "artifact_refs": [],
  "payload": {},
  "created_at": ""
}
```

事件类型第一版与 `shared-schema-contract.md` 保持一致：

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
```

## approval_request

审批请求必须说明要执行什么、会影响什么、用户有哪些操作。

```json
{
  "approval_request_id": "",
  "run_id": "",
  "step_id": "",
  "title": "",
  "reason": "",
  "proposed_action": {},
  "affected_artifacts": [],
  "risk_notes": [],
  "options": [
    "approve",
    "revise_instruction",
    "pause",
    "reject",
    "rerun_step",
    "auto_continue"
  ],
  "status": "pending | approved | revised | paused | rejected | expired",
  "user_response": "",
  "created_at": "",
  "resolved_at": ""
}
```

## dependency_invalidation

```json
{
  "source_artifact_id": "",
  "changed_scope": "full | episode | scene | selection | metadata",
  "affected_artifacts": [],
  "invalidation_level": "review | regenerate_required",
  "reason": ""
}
```

基础规则：

```text
story_bible changed
-> episode_split, episode_cards, script_context, script_unit

episode_split changed
-> episode_cards, script_context, script_unit

material_bank changed
-> story_seed, series_blueprint, episode_cards, script_context, script_unit

story_seed changed
-> series_blueprint, episode_cards, script_context, script_unit

series_blueprint changed
-> episode_cards, script_context, script_unit

episode_cards changed
-> script_context, script_unit

script_context changed
-> script_unit

single script_unit changed
-> 默认只影响该 script_unit；如果 continuity_delta 改变，再影响后续集
```

## 暂停、继续、重跑

- 暂停只冻结当前 run，不删除 artifact。
- 继续时从 `run_state.next_action` 和最新 confirmed artifacts 恢复。
- 重跑某一步必须创建新 step 和新 artifact version。
- 重跑上游步骤时，下游 artifact 不能自动覆盖，只能标记失效并等待用户确认。
