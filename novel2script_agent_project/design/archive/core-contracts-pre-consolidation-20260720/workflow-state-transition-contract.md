# 用户流程与状态机合同

本文档定义 Novel2Script Agent 的用户流程、project_state、run_state、artifact_status、approval_status 与 run_event 之间的状态转移关系。

## 状态分层

```text
ProjectStatus：项目对用户呈现的总状态
RunStatus：一次 Agent 执行的生命周期
StepStatus：单个步骤状态
ArtifactStatus：产物状态
ApprovalStatus：审批状态
RunEventType：可回放事件
```

前端展示必须以后端状态为准，不能本地猜测流程是否完成。

## ProjectStatus

```text
idle
running
waiting_approval
paused
failed
completed
```

### idle

含义：

- 当前没有运行中的 run。
- 可以是空项目，也可以是完成后待用户继续操作。

用户可做：

- 闲聊。
- 上传文件。
- 输入小说 / 灵感。
- 查看已有 artifact。

### running

含义：

- 有 active_run 正在执行。

用户可做：

- 查看已生成产物。
- 发送补充说明。
- 暂停。

用户不可做：

- 静默启动第二个 active run。

### waiting_approval

含义：

- active_run 等待用户确认。

用户可做：

- 确认继续。
- 补充要求。
- 暂停。
- 取消。

用户不可做：

- 在同一 project 下静默开启新 run。

### paused

含义：

- run 被用户或系统暂停。

用户可做：

- 继续。
- 补充说明。
- 取消。

补充说明不会自动继续，除非用户明确说继续。

### failed

含义：

- 当前 run 或 step 失败，等待恢复。

用户可做：

- 重试。
- 补充信息后重试。
- 暂停。
- 取消。

### completed

含义：

- 当前主流程完成，已有最终 scripts 或可查看的最终产物。

用户可做：

- 查看剧本。
- 局部修改。
- 查看过程产物。
- 新建任务或继续改当前项目。

## RunStatus

```text
pending
running
waiting_approval
paused
completed
failed
cancelled
```

## 主状态转移表

| From | Trigger | To | Event | 说明 |
|---|---|---|---|---|
| idle | chat message | idle | message_created | 闲聊不创建 run |
| idle | upload file | idle | file_attached | 文件不自动启动 run |
| idle | generate request | running | run_started | 明确生成才启动 |
| running | step completed, no checkpoint | running | step_completed | 继续下一步 |
| running | checkpoint reached | waiting_approval | approval_requested | 例如分集卡确认 |
| waiting_approval | approve | running | approval_resolved, run_resumed | 继续生成 |
| waiting_approval | revise_instruction | running | approval_resolved, step_started | 当前 run 内调整 |
| waiting_approval | pause | paused | run_paused | 暂停 |
| running | pause | paused | run_paused | 暂停 |
| paused | continue | running | run_resumed | 从 next_action 继续 |
| paused | cancel | cancelled | run_cancelled | 取消 run |
| running | step failed | failed | step_failed | 不清空已生成 artifact |
| failed | retry | running | step_started | 重试失败步骤 |
| running | final artifact persisted | completed | run_completed | 生成完成 |
| completed | revise request | running | run_started | 局部修改可产生新 run |

## 用户输入处理矩阵

| 当前状态 | 用户输入 | 行为 |
|---|---|---|
| idle | 你好 / ? | 只回复，不创建 run |
| idle | 上传文件 | 创建 FileRef，不创建 run |
| idle | 明确生成 | 创建 run |
| running | 补充要求 | 记录到当前 run，上下文相关时影响后续步骤 |
| running | 暂停 | run -> paused |
| waiting_approval | 确认继续 | resolve approval approve |
| waiting_approval | 补充要求 | resolve approval revise_instruction，不创建新 run |
| waiting_approval | 暂停 | run -> paused |
| paused | 补充要求 | 记录 pending instruction，不自动继续 |
| paused | 继续 | run -> running |
| failed | 重试 | retry failed step |
| completed | 选区修改 | revise_selection |

## ArtifactStatus

```text
draft
pending_approval
confirmed
superseded
invalidated
failed
```

### 状态转移

| From | Trigger | To |
|---|---|---|
| draft | checkpoint required | pending_approval |
| pending_approval | user approve | confirmed |
| draft | newer version created | superseded |
| confirmed | newer version created after approval | superseded |
| any | upstream changed | invalidated |
| draft | validation failed | failed |

前端显示规则：

- 没有用户确认动作不能显示“已确认”。
- `confirmed` 是内部状态，UI 可根据语境显示“已确认”或“确认版本”。
- `invalidated` 必须提示下游需要复查或重生成。

## ApprovalStatus

```text
pending
approved
revised
paused
rejected
expired
```

| From | Trigger | To | Run impact |
|---|---|---|---|
| pending | approve | approved | run resumes |
| pending | revise_instruction | revised | current step reruns or updates |
| pending | pause | paused | run paused |
| pending | reject | rejected | run cancelled or returns to previous state |
| pending | timeout | expired | run paused or failed by policy |

## StepStatus

```text
pending
running
waiting_approval
completed
failed
skipped
```

Step completed 不等于用户确认。内部步骤完成不能用绿色确认勾表示。

## Dependency Invalidation

基础规则：

```text
story_bible changed
-> episode_split, episode_cards, script_context, script_unit/scripts

episode_split changed
-> episode_cards, script_context, script_unit/scripts

material_bank changed
-> story_seed, series_blueprint, episode_cards, script_context, script_unit/scripts

story_seed changed
-> series_blueprint, episode_cards, script_context, script_unit/scripts

series_blueprint changed
-> episode_cards, script_context, script_unit/scripts

episode_cards changed
-> script_context, script_unit/scripts

script_unit changed
-> only current script_unit unless continuity_delta changes
```

## Event 映射

| 状态变化 | 必须事件 |
|---|---|
| run created | run_started |
| intent decided | intent_detected |
| step starts | step_started |
| LLM input ready | llm_input_prepared |
| LLM output received | llm_output_received |
| artifact created | artifact_created |
| artifact updated | artifact_updated |
| artifact invalidated | artifact_invalidated |
| approval requested | approval_requested |
| approval resolved | approval_resolved |
| run paused | run_paused |
| run resumed | run_resumed |
| step failed | step_failed |
| run failed | run_failed |
| run completed | run_completed |

## 前端状态消费

左侧目录：

- 使用 artifact status。
- 使用 project.source_mode 决定链路。
- 使用 run.status 标记运行中 / 等待确认 / 失败。

中间内容：

- 使用 current_focus_artifact。
- artifact 不存在时显示空态。
- artifact invalidated 时显示复查提示。

右侧 Agent：

- 使用 messages。
- 使用 run_event 的摘要。
- 使用 approval_request 渲染审批卡。

## 禁止事项

- 前端自行把 project.status 改成 completed。
- 前端根据按钮点击假设 approval 已解决。
- waiting_approval 时补充说明创建新 run。
- paused 时补充说明自动继续。
- failed 时清空已生成 artifact。
- 上游变更后下游仍显示为稳定可用。
