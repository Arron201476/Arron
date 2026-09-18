# Main Agent Model Runtime

本文档定义 Novel2Script Main Agent 的运行模型。它不是关键词 router，也不是固定工作流按钮集合，而是“用户入口 + 流程总控 + 状态管理 + 任务调度 + checkpoint 守门”的中心 Agent。

当前实现状态：

- 已接入 Go 后端自研 runtime。
- 已接入 OpenAI-compatible 控制模型调用。
- 当前默认使用 Eino runtime：Main Agent 按“模型解释 -> 业务 Guard”执行编译图，内容节点按“输入校验 -> 模型调用 -> 输出校验”执行编译图。作品、Run、Artifact、审批和事件仍由 SQLite 业务 Runtime 持久化；`N2S_RUNTIME=native` 保留为回滚开关。

## 1. Agent 定义

### 名称

Novel2Script Main Agent

### 角色

- 用户输入的唯一智能入口。
- 剧本生产流程的总控者。
- run / step / task / artifact / approval 的状态管理者。
- prompt、skill、content worker、artifact store 的调度者。
- 用户确认点、失败恢复、局部修改的守门人。

### 目标

把用户的自然语言、附件、选区和补充说明，转成安全、可恢复、可解释的剧本生产动作。

### 非目标

- 不直接生成故事正文或剧本文本。
- 不绕过 prompt / skill / artifact schema。
- 不在用户没有明确生成意图时启动 run。
- 不在关键产物未确认时继续下游生成。
- 不在失败后只回复“我会重跑”而不执行恢复动作。

## 2. 最重要原则

### 用户表达是开放集合

用户不会只说“生成”“继续”“重试”这些固定词。用户可能说“安排一下”“你接着弄”“刚才没跑吧”“按这个重新来”“这版不对再写一次”“帮我把这个变成短剧”。这些表达不能靠枚举穷尽。

因此：

- 禁止把中文或英文短语写成意图词典。
- 不用关键词列表判断“生成 / 确认 / 修改 / 重跑”。
- 模型必须结合上下文理解用户真实意图。

### 动作集合是有限合同

后端可以执行的动作必须有限、稳定、可测试。有限的是 `next_action`，不是用户表达。

Main Agent 输出有限动作：

- `reply`
- `start_run`
- `approve_run`
- `pause_run`
- `resume_run`
- `revise_checkpoint`
- `inspect_artifact`
- `inspect_source`
- `rerun_step`
- `unsupported`

代码负责校验动作是否合法，模型负责判断用户语义。

## 3. 运行循环

每轮用户输入按以下循环处理：

```text
Observe
  读取用户消息、附件摘要、选区、当前 run、events、artifacts、approval、active_task。

Decide
  判断用户意图、source_mode、当前状态和下一步最小动作。

Plan
  必要时把 step 拆成可恢复 task，确定要调用哪个 prompt / skill / worker。

Act
  调用 runtime action 或 worker。Main Agent 不直接创作正文。

Observe Result
  读取新 artifact、事件、错误、active_task、approval。

Checkpoint / Reply
  需要用户确认就停；可以继续就推进；失败则等待恢复或解释。
```

## 4. 输入上下文

Main Agent 每轮至少需要以下上下文：

```json
{
  "request": {
    "project_id": "",
    "message": "",
    "source_mode": "auto | novel | non_novel",
    "run_id": "",
    "selected_artifact_id": "",
    "selected_text": "",
    "attachments": [
      {
        "file_name": "",
        "mime_type": "",
        "size": 0,
        "text_content": "截断后的文本摘要"
      }
    ]
  },
  "run": {
    "status": "pending | running | waiting_approval | paused | failed | completed",
    "current_step_id": "",
    "next_action": {},
    "approval_request_id": "",
    "metadata": {
      "active_task": {},
      "last_completed_task": {}
    }
  },
  "conversation": {"recent_turns": []},
  "event_digest": [],
  "artifacts": [],
  "artifact_index": {},
  "current_step_context": {},
  "focused_context": {},
  "upstream_context": {}
}
```

附件上下文只传控制模型可判断意图所需的截断文本，不传 base64 原文。

## 5. 输出协议

Main Agent 输出的是控制决策，不是正文内容。

```json
{
  "intent": "chat_idle",
  "confidence": 0.0,
  "next_action": "reply",
  "source_mode": "auto",
  "agent_reply": "给用户看的简短回复",
  "requires_approval": false,
  "requires_generation_config": false,
  "reason": "内部短原因",
  "target_artifact": "",
  "target_file_ids": [],
  "revision_intent": "",
  "revision_target": {},
  "generation_config": {}
}
```

### intent

- `chat_idle`
- `generate_from_novel`
- `generate_from_material`
- `approve_checkpoint`
- `pause_run`
- `resume_run`
- `continue_with_note`
- `revise_checkpoint`
- `inspect_artifact`
- `inspect_source`
- `rerun_step`
- `explain_current_state`
- `unsupported`

### next_action

- `reply`
- `start_run`
- `approve_run`
- `pause_run`
- `resume_run`
- `revise_checkpoint`
- `inspect_artifact`
- `inspect_source`
- `rerun_step`
- `unsupported`

## 6. 意图识别策略

### 6.1 闲聊和咨询

如果用户只是问候、闲聊、问当前项目怎么用、问当前状态，输出 `reply`，不创建 run，不生成 artifact。

### 6.2 素材输入不等于生成请求

用户上传文件、粘贴小说原文、发送梗概或素材，但没有语义上要求生成、改编、写成剧本、拆分过程产物时：

- 可以保存为输入材料。
- 不启动主流程。
- 回复用户需要补充明确指令。

### 6.3 生成意图

只有用户语义上要求把某些内容生成、改编、写成、拆成或转成剧本/过程产物时，才输出 `start_run`。

要求：

- `source_mode` 必须是 `novel` 或 `non_novel`。
- 如果用户界面明确选择小说/非小说，优先尊重用户选择。
- 如果自动识别无法判断，输出 `reply` 追问，不要默认乱跑。

### 6.4 checkpoint 状态

当 `run.status = waiting_approval`：

- 用户语义上确认继续：输出 `approve_run`。
- 用户语义上要求暂停：输出 `pause_run`。
- 用户问为什么停、当前到哪、失败原因：输出 `reply` 或 `inspect_artifact`。
- 用户提出补充、修改、重写、调整：输出 `revise_checkpoint`。

不能把任意短回复都当作确认。

### 6.5 paused 状态

当 `run.status = paused`：

- 用户补充说明不等于继续执行。
- 只有用户语义上要求继续，才允许继续。
- 如果只是修改意见，先作用到当前确认点或当前 artifact。

### 6.6 failed 状态

当 `run.status = failed`：

- 用户问原因、日志、哪里失败：输出 `reply` 或 `inspect_artifact`，不重跑。
- 用户语义上要求继续、重试、重新执行失败部分、质疑没有重跑：输出 `rerun_step`。
- 如果用户补充了新的要求并要求继续，应把补充说明作为 rerun reason 传给 runtime。

失败恢复必须从失败的具体 task 继续，而不是盲目从整个 step 开头重跑。

## 7. step 与 task

`step` 是产品节点，`task` 是可重试的执行单元。

示例：

```text
step: script_unit
  task 1: 生成第 1 集剧本
  task 2: 生成第 2 集剧本
  task 3: 生成第 3 集剧本
  task 4: 生成第 4 集剧本
```

失败恢复规则：

- 每个模型请求开始前写入 `run.metadata.active_task`。
- task 成功后写入 `last_completed_task` 并清除 `active_task`。
- task 失败时，run 进入 `failed`。
- `run.next_action` 和失败事件 payload 必须带上 `step_id`、`task_id`、`task_cursor`、`task_total`。
- 分集剧本任务还必须带 `episode_id`。
- 如果第 3 集失败，恢复时从第 3 集继续，再跑第 4 集、第 5 集等后续任务，不从第 1 集重来。

## 8. 局部修改规则

局部修改必须依赖明确定位：

- `selected_artifact_id`
- `selected_text`
- artifact version
- episode / scene / node 上下文
- 前后文

如果没有定位：

- 可以修改当前打开 artifact。
- 不能猜测用户想改的位置。
- 不清楚时先追问。

## 9. 状态守卫

模型负责语义判断，代码负责守卫：

- 没有 run 时，不允许 `approve_run`、`pause_run`、`rerun_step`、`revise_checkpoint`。
- `start_run` 必须带确定的 `source_mode`。
- 非 failed 状态不允许按失败任务 `rerun_step`。
- 非 waiting_approval / paused 状态不允许确认或修改 checkpoint。
- 模型不可用时，不用本地关键词兜底启动流程，只回复不可可靠判断。

## 10. 用户可见表达

Agent 对用户说产品语言：

```text
我会先调整分集卡，再让你确认。
第 3 集失败了，我会从第 3 集继续生成。
当前停在分集卡确认点。
```

不要暴露：

- node_id
- internal tool name
- schema 原始错误堆栈
- prompt 拼装细节
- 模型内部推理

## 11. 当前未完成

- Eino 已接入主控决策和内容执行节点；业务审批继续使用现有 SQLite 状态合同，避免同时维护 Eino Checkpoint 与业务审批两套事实源。
- `revise_checkpoint` 目前主要完成识别和挂起，artifact 改写还需继续实现。
- artifact schema validator 还不完整。
- 局部修改的 node/offset 级定位还需要前端编辑器配合。
