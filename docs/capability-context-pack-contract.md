# Capability Context Pack Contract

状态：阶段 2 设计稿  
版本：v0.1  
日期：2026-07-24

## 1. 文档目的

本文档定义内容生产 Agent 第一版的上下文分层、选择、组装、预算和追溯协议。

它解决：

1. Main Agent 每轮需要看到什么；
2. Capability 启动和恢复时需要读取什么；
3. Worker 执行某个 Step/Task 时需要读取什么；
4. 用户选区如何形成不可变快照；
5. 如何防止跨 Project、跨 Run 和跨版本串用；
6. 长小说、几十集视频和全集剧本如何避免一次塞进模型；
7. “刚才那一集”“这个人物”“当前场景”等指代如何定位；
8. Context Budget 不足时如何拆分，而不是静默截断关键输入；
9. 如何记录一次模型调用实际使用了哪些来源。

Context Pack 是模型调用输入合同，不是新的业务状态真源。

### 1.1 规范性用语

- **必须**：实现不可偏离；
- **禁止**：不得出现；
- **应该**：默认遵循；
- **可以**：允许；
- **建议**：当前推荐；
- **示例**：用于说明结构，不是完整 Schema。

## 2. 核心原则

### 2.1 分层

```text
Main Agent 看任务地图
Capability 看任务材料
Worker 看最小执行片段
Selection Snapshot 拥有最高定位优先级
```

### 2.2 先定位，再取内容

禁止：

```text
先加载全部作品内容
-> 再让模型自己找目标
```

必须：

```text
确定 Project
-> 确定 Intent
-> 确定 Capability/Run/Step
-> 确定 Target 和 Version
-> 读取最小必要内容
```

### 2.3 精确版本

Context Assembler 只能使用 Runtime 已解析的精确 Version。

禁止：

- 无条件读取最新 Artifact；
- 用 Project Current Pointer 覆盖 Step Input Snapshot；
- Retry 时换成新上游；
- 保留下游后把新上游混入旧 Lineage；
- 从聊天文本猜测版本。

### 2.4 Project 隔离

一个 Context Pack 中所有：

- Message；
- Asset；
- Run；
- Artifact Version；
- Approval；
- Event；

必须属于同一 Project。

第一版不支持跨 Project Context。

### 2.5 最小充分

Context Pack 必须包含完成当前任务的最小充分信息。

不以“还有 Token”作为加入无关内容的理由。

### 2.6 关键输入不静默截断

以下内容不能被静默截断：

- Selection Snapshot；
- Decision Snapshot；
- Target Artifact Scope；
- Step 必需输入；
- Approval Subject；
- Config Snapshot；
- 明确来源范围；
- 输出 Schema；
- 当前 Step 强制 Rules。

预算不足时：

- 拆分任务；
- 缩小范围；
- 使用确定性摘要；
- 或返回 Context Budget Error。

不能悄悄删掉关键字段继续调用模型。

## 3. Context 层级

第一版定义六类 Context Pack：

| Pack | 用途 | 主要消费者 |
|---|---|---|
| `routing` | 理解意图和选择 Capability | Main Agent |
| `capability_start` | 补齐输入、配置和启动确认 | Main Agent + Guard |
| `step_execution` | 执行一个 Step/Task | Worker |
| `revision` | 局部修改或重新生成 | Main Agent + Worker |
| `approval` | 展示并解决确认点 | Guard + Runtime |
| `inspection` | 查看状态、解释来源和影响 | Main Agent |

这些 Pack 使用同一 Envelope，但 Payload 和预算不同。

## 4. Context Pack Envelope

```json
{
  "context_pack_id": "",
  "context_pack_version": "1.0.0",
  "pack_type": "step_execution",
  "project_id": "",
  "conversation_id": "",
  "request_message_id": "",
  "capability": {
    "capability_id": "",
    "capability_version": ""
  },
  "run": {
    "run_id": "",
    "run_kind": "",
    "status": ""
  },
  "step": {
    "step_run_id": "",
    "step_id": "",
    "task_item_id": null
  },
  "intent": {},
  "target": {},
  "selection_snapshot": null,
  "decision_snapshot": null,
  "project_context": {},
  "conversation_context": {},
  "asset_context": [],
  "upstream_context": [],
  "config_snapshot": {},
  "approval_context": null,
  "event_context": [],
  "rules": [],
  "output_contract": {},
  "budget": {},
  "provenance": [],
  "context_hash": "",
  "created_at": ""
}
```

字段不适用时使用 `null` 或空集合，不使用空字符串伪装缺失。

## 5. Context Pack 身份

### 5.1 Context Pack ID

- 服务端生成；
- Opaque；
- 每次有效组装产生新 ID；
- 缓存命中可以复用内容 Blob，但仍记录当前调用 ID；
- 不从 ID 推断 Project、Step 或时间。

### 5.2 Context Hash

Hash 覆盖：

- Pack Version；
- Project ID；
- Intent；
- Target；
- Selection Snapshot Hash；
- Decision Snapshot Hash；
- 所有 Asset Snapshot Hash；
- 所有 Artifact Version ID；
- Config Snapshot Hash；
- Rule Version；
- Output Schema Version；
- 规范化内容片段。

作用：

- Provider Request Fingerprint；
- Retry 一致性校验；
- 缓存；
- 迟到结果校验；
- 审计。

不能把敏感原文直接写入日志作为 Hash 输入展示。

## 6. Routing Context Pack

Routing Pack 用于回答：

```text
用户想做什么？
是否需要 Domain Capability？
需要哪个 Capability？
信息是否足够？
```

### 6.1 包含

- 当前 Project ID、名称和状态；
- 当前主 Conversation；
- 用户当前消息；
- 结构化 Skill 引用；
- 当前附件摘要；
- 最近少量相关消息；
- 当前 Active Run 摘要；
- Pending Approval 摘要；
- 当前焦点 Artifact 摘要；
- 可用 Capability Public Manifest；
- Capability Availability；
- 当前允许动作。

### 6.2 不包含

- 全部小说原文；
- 全部视频；
- 全部 Artifact Payload；
- 全部历史聊天；
- 全部 Prompt/Rules；
- Provider 凭据；
- 其他 Project 内容。

### 6.3 结构

```json
{
  "pack_type": "routing",
  "user_message": {
    "message_id": "",
    "content": "",
    "capability_ref": null,
    "attachment_refs": []
  },
  "project_context": {
    "project_id": "",
    "title": "",
    "status": "",
    "active_run_summary": null,
    "current_focus": null
  },
  "available_capabilities": [],
  "recent_conversation": [],
  "allowed_actions": []
}
```

### 6.4 Routing 输出

Main Agent 只返回结构化 Decision：

```json
{
  "intent": "start_capability",
  "capability_id": "novel_to_script",
  "confidence": 0.94,
  "target": {},
  "missing_inputs": [],
  "next_action": "request_start_confirmation",
  "reply": ""
}
```

Guard 校验 Decision 后才能改变状态。

## 7. Capability Start Context Pack

用于：

- 判断来源是否符合 Skill；
- 提取已经明确的生成配置；
- 找出缺失信息；
- 识别扩写、缺集等需要确认的策略；
- 生成启动确认卡。

### 7.1 包含

- 用户请求；
- 显式或路由 Capability ID；
- Selected Asset Metadata；
- Asset 解析摘要；
- 用户已经明确的目标集数、时长和要求；
- Capability Input/Config Schema；
- Provider Availability；
- Project Write Run 状态；
- 必要风险检查结果。

### 7.2 不创建 Run

Capability Start Pack 可以多轮使用。

只有：

- 输入完整；
- 配置完整；
- 风险策略已确认；
- Guard 通过；

Runtime 才创建 Run。

### 7.3 配置提取

Main Agent 可以从自然语言提取：

```json
{
  "target_episode_count": 20,
  "episode_duration_minutes": 2
}
```

但必须：

- 通过 Config Schema；
- 展示给用户确认；
- 不自动补默认集数；
- 不把模型推测当成用户确认。

## 8. Step Execution Context Pack

用于一个确定 Step 或 Task。

### 8.1 包含

- Capability ID/Version；
- Run/Step/Task；
- Step Intent；
- 精确 Target；
- Step Input Version Snapshot；
- 必需上游最小内容；
- Config Snapshot；
- 当前 Task Cursor；
- Step Prompt；
- Step Rule Refs；
- Output Schema；
- Provider 约束；
- Context Budget；
- Provenance。

### 8.2 不包含

- Main Agent 全部对话；
- 无关 Artifact；
- 其他集剧本全文；
- 其他 Project；
- 未确认新版本；
- 未被 Step Definition 声明的 Rule；
- 前端 UI 状态。

### 8.3 结构

```json
{
  "pack_type": "step_execution",
  "intent": {
    "operation": "generate_artifact",
    "artifact_type": "script_unit"
  },
  "target": {
    "scope_key": "episode:3",
    "episode_no": 3
  },
  "upstream_context": [
    {
      "artifact_version_id": "",
      "artifact_type": "episode_cards",
      "scope": "episode:3",
      "content": {}
    }
  ],
  "config_snapshot": {},
  "rules": [],
  "output_contract": {
    "artifact_type": "script_unit",
    "schema_id": "script_unit",
    "schema_version": "1.0.0"
  }
}
```

## 9. Revision Context Pack

用于：

- 字段修改；
- 实体修改；
- 集合修改；
- 单场修改；
- 选区修改；
- 单集重新生成；
- 整体 Artifact 重新生成。

### 9.1 Revision Intent

```text
patch_field
patch_entity
patch_collection
patch_selection
regenerate_scene
regenerate_episode
regenerate_artifact
```

### 9.2 结构

```json
{
  "pack_type": "revision",
  "intent": {
    "operation": "patch_selection",
    "instruction": "把这句改得更克制"
  },
  "target": {
    "artifact_id": "",
    "artifact_version_id": "",
    "artifact_type": "script_unit",
    "scope_key": "episode:3",
    "field_path": "scenes[scene_id=scene_3_2].blocks[line_id=line_3_2_5].text"
  },
  "selection_snapshot": {},
  "upstream_context": [],
  "version_policy": {
    "base_version_id": "",
    "on_conflict": "reject"
  }
}
```

### 9.3 Sparse Output

局部 Revision Worker 只返回：

- Patch Operation；
- Target Selector；
- Patch Value；
- 必要的 Continuity Delta 变化。

禁止返回完整 Artifact 覆盖不相关字段。

整体 Regeneration 才允许返回完整 Payload。

## 10. Approval Context Pack

用于展示：

- 当前确认对象；
- 精确 Version；
- 影响范围；
- 可用操作；
- 下一步；
- 下游保留/重生成结果。

### 10.1 包含

- Approval Request；
- Approval Subjects；
- Artifact 可读摘要；
- Version；
- Impact Preview；
- Allowed Actions；
- 下一 Step Label；
- 运行状态。

### 10.2 不由模型决定

Approval Pack 可以由 Main Agent解释，但：

- Approval 状态来自 Runtime；
- Allowed Actions 来自 Guard；
- Subject Version 来自 Approval Snapshot；
- 用户自然语言需转成结构化 Resolution 后再校验；
- 模型不能增加未允许操作。

## 11. Inspection Context Pack

用于：

- “现在到哪一步了”；
- “这个剧本用了哪个版本”；
- “为什么第 4 集要重生成”；
- “刚才哪个视频失败了”；
- “这个文件还能重新解析吗”。

### 11.1 包含

- 当前状态快照；
- 相关 Event；
- 相关 Dependency Path；
- 相关 Asset 状态；
- 当前 Artifact 摘要；
- 可用操作。

### 11.2 只读

Inspection Pack 不创建 Run、不修改 Artifact、不解决 Approval。

## 12. Project Context

### 12.1 结构

```json
{
  "project_id": "",
  "title": "",
  "status": "",
  "primary_conversation_id": "",
  "active_write_run_id": null,
  "current_capability_id": null,
  "current_focus": null,
  "current_final_candidate": null,
  "asset_summary": [],
  "artifact_summary": []
}
```

### 12.2 Project Summary

Project Summary 是派生上下文，不是业务真源。

必须记录：

- Summary Version；
- 来源 Event Seq；
- 来源 Artifact Version IDs；
- 生成方式；
- 创建时间。

Summary 过期时不能覆盖真实 Runtime 状态。

### 12.3 零 Capability

即使没有 Domain Capability：

- Project Context 仍存在；
- Conversation 仍存在；
- Asset Summary 仍存在；
- Agent 可以回答通用问题；
- `available_capabilities=[]`；
- Agent 明确说明领域能力不可用。

## 13. Conversation Context

### 13.1 Recent Messages

默认只读取：

- 当前用户消息；
- 最近相关消息；
- 未解决追问；
- 最近一次结构化 Decision；
- 当前 Run 相关消息。

### 13.2 不固定读取最后 N 条

只按最后 N 条可能：

- 丢失仍有效配置；
- 加入无关闲聊；
- 混入旧 Run 指令。

应该使用：

```text
recency
+ relevance
+ unresolved state
+ current run binding
```

### 13.3 Conversation Summary

长对话可以生成 Summary，但必须：

- 区分用户事实和 Agent 建议；
- 不把未确认建议写成已确认要求；
- 保存来源 Message IDs；
- 新消息可以使 Summary 过期；
- 关键配置仍从 Config Snapshot 读取；
- Approval 仍从 Runtime 读取。

### 13.4 消息优先级

```text
当前用户消息
-> 当前未解决追问答案
-> 当前 Run 的结构化决定
-> 近期相关对话
-> Conversation Summary
```

## 14. Target

### 14.1 Target 结构

```json
{
  "kind": "artifact_scope",
  "artifact_id": "",
  "artifact_version_id": "",
  "artifact_type": "script_unit",
  "scope_key": "episode:3",
  "episode_no": 3,
  "scene_id": null,
  "line_id": null,
  "field_path": null,
  "asset_id": null
}
```

### 14.2 Target 优先级

```text
结构化显式 Target
-> Selection Snapshot
-> 用户当前查看的 Focus
-> 当前 Pending Approval Subject
-> 当前 Step Target
-> 指代解析结果
```

### 14.3 显式 Target

来源：

- 用户点击“重新生成本集”；
- 用户在某个字段点击 AI 修改；
- 用户选中文本发送；
- 用户在视频列表选择单集；
- 用户在历史版本中明确选择一个 Version。

显式 Target 优先于模型推断。

### 14.4 Target 冲突

例如：

- 用户选中了第 3 集；
- 文字说“修改第 5 集”。

系统不能任选一个。

必须：

- 检测冲突；
- 询问用户；
- 不执行修改。

## 15. Selection Snapshot

Selection Snapshot 是发送请求时创建的不可变定位对象。

### 15.1 结构

```json
{
  "selection_snapshot_id": "",
  "project_id": "",
  "artifact_id": "",
  "artifact_version_id": "",
  "artifact_type": "script_unit",
  "scope_key": "episode:3",
  "field_path": "scenes[scene_id=scene_3_2].blocks[line_id=line_3_2_5].text",
  "selected_text": "",
  "selection_start": 10,
  "selection_end": 26,
  "before_context": "",
  "after_context": "",
  "content_hash": "",
  "document_revision": "",
  "created_at": ""
}
```

### 15.2 创建时机

用户点击发送时创建。

禁止：

- 只保存浏览器当前 Range；
- Worker 执行时再读取当前编辑器；
- 用户发送后继续编辑影响已发送 Selection；
- 仅靠 selected_text 全文搜索定位。

### 15.3 校验

执行前：

1. Artifact Version 存在；
2. Project 一致；
3. Field Path/Stable IDs 存在；
4. Selected Text 与 Snapshot Hash 一致；
5. Base Version 未变化；
6. Offset 在目标字段范围；
7. 前后文匹配。

失败返回 Version Conflict 或 Selection Stale。

### 15.4 Selected Text 不截断

如果用户选区超过 Revision Worker 预算：

- 要求缩小选区；
- 或拆成明确子任务；
- 不能只传选区前半段。

### 15.5 空选区

删除操作可以产生：

```json
{
  "operation": "delete",
  "new_text": ""
}
```

空字符串是合法 Patch Value，不能被“内容必填”校验误拒绝。

### 15.6 Decision Snapshot

Selection Snapshot 只表示编辑器或 Artifact 内的精确选区。业务方案选择使用独立的不可变 Decision Snapshot，禁止复用选区结构。

```json
{
  "decision_snapshot_id": "ds_...",
  "decision_kind": "adaptation_strategy",
  "project_id": "prj_...",
  "run_id": "run_...",
  "capability_id": "video_reference_creation",
  "step_id": "propose_adaptation_options",
  "approval_request_id": "apr_...",
  "subject_refs": [
    {
      "artifact_version_id": "av_...",
      "artifact_type": "adaptation_options"
    }
  ],
  "decision_payload": {
    "selected_option_ids": ["option_1"],
    "combined_methods": [],
    "custom_changes": []
  },
  "user_instruction_message_id": "msg_...",
  "content_hash": "",
  "created_at": ""
}
```

Decision Snapshot 创建后不可修改。用户改变改编方法时必须创建新 Snapshot、新 Approval Resolution 和受影响的 Brief 新版本；旧 Decision Snapshot 继续作为历史 Lineage。

## 16. Focus Context

Focus 表示用户当前正在查看的位置，不等于执行授权。

```json
{
  "view": "artifact | script | video_list | analysis | brief",
  "artifact_version_id": null,
  "scope_key": null,
  "asset_id": null,
  "updated_at": ""
}
```

作用：

- 帮助解释“这个”；
- 帮助默认打开正确内容；
- 作为 Target 候选；
- 不能覆盖 Selection Snapshot；
- 不能直接启动修改。

## 17. 指代解析

### 17.1 常见指代

```text
刚才那一集
这个人物
上一版
当前场景
刚上传的视频
失败的那几个
这个分析
刚才的方案
```

### 17.2 候选来源

按优先级：

1. Selection Snapshot；
2. 显式 UI Focus；
3. Pending Approval Subject；
4. 当前 Step/Task；
5. 最近结构化操作；
6. 最近相关 Message；
7. 最近 Artifact/Event。

### 17.3 结构化候选

```json
{
  "reference_text": "刚才那一集",
  "candidates": [
    {
      "target": {},
      "reason": "last_opened_script_unit",
      "score": 0.94
    }
  ]
}
```

### 17.4 决策

- 唯一高置信候选：使用；
- 多个接近候选：追问；
- 无候选：追问；
- 用户明确 Episode Number：覆盖模糊指代；
- 模型不能编造不存在的 Target ID。

## 18. Version Resolution

Context Assembler 不自行选择版本。

Runtime 先返回：

```json
{
  "version_resolution": {
    "policy": "approval_snapshot | lineage_consistent | current_confirmed | explicit_version",
    "resolved_refs": []
  }
}
```

### 18.1 Resume/Retry

必须：

```text
approval_snapshot
```

### 18.2 Keep Downstream

必须：

```text
lineage_consistent
```

### 18.3 新 Run

可以使用：

```text
current_confirmed
```

但必须在 Input Snapshot 中固定。

### 18.4 历史版本修改

使用：

```text
explicit_version
```

并创建 Revision Run。

## 19. Upstream Context

### 19.1 结构

```json
{
  "artifact_id": "",
  "artifact_version_id": "",
  "artifact_type": "",
  "scope": {},
  "status": "confirmed",
  "selection_policy": "lineage_consistent",
  "content": {},
  "content_hash": "",
  "provenance_ref": ""
}
```

### 19.2 完整性

Step Definition 声明的必需 Upstream 必须全部存在。

缺失时：

- 不调用模型；
- Step 失败或回到用户确认；
- 返回明确 Missing Upstream Error。

### 19.3 最小 Scope

例如生成第 3 集剧本：

读取：

- 第 3 集 Episode Card；
- Script Context 中必要字段；
- 第 2 集 Continuity Delta；
- 第 3 集 Source Refs；
- 用户对第 3 集的要求。

不读取：

- 全部其他集剧本；
- 全部聊天；
- 无关视频；
- 其他 Candidate。

## 20. Asset Context

### 20.1 文本/文档

```json
{
  "asset_snapshot_id": "",
  "asset_id": "",
  "kind": "document",
  "filename": "",
  "checksum": "",
  "parse_status": "ready",
  "content_refs": [
    {
      "source_unit_id": "",
      "start_offset": 0,
      "end_offset": 0
    }
  ]
}
```

### 20.2 图片

通用图片理解可以构建：

```json
{
  "kind": "image",
  "asset_id": "",
  "provider_input_ref": "",
  "user_intent": "解释图片内容"
}
```

图片不是默认 Domain Run。

### 20.3 视频

单个视频 Task 只包含：

- 当前 Video Asset；
- Provider 可用处理副本；
- Episode Mapping；
- Known Characters；
- 上一集结尾摘要；
- 用户确认事实；
- 视频提取 Prompt/Rule；
- 输出 Schema。

不把全部几十集视频同时发送给单次视频模型调用。

### 20.4 过期 Asset

Asset 过期：

- Context 可以读取元数据和已有派生 Artifact；
- 不能生成 Provider Input Ref；
- 需要重新解析时返回 `ASSET_EXPIRED`；
- 不自动删除已有 Context Provenance。

## 21. Source Context

### 21.1 Source Unit

长文本先确定性切为 Source Units：

```json
{
  "source_unit_id": "S001",
  "asset_id": "",
  "start_offset": 0,
  "end_offset": 5000,
  "heading": "",
  "checksum": ""
}
```

### 21.2 Offset

- 使用 Unicode Code Point 或统一字符标准；
- 前后端必须一致；
- 不混用字节偏移和字符偏移；
- Source Unit Checksum 防止文件变化后错位；
- 任何 Offset 必须绑定 Asset Checksum。

### 21.3 Window

局部窗口包含：

```text
target range
+ boundary padding
+ previous anchor
+ next anchor
```

Window Size 来自 Step Policy，不写死在 Main Agent。

### 21.4 全文任务

Story Bible 等需要全文理解时，不能假设全文一定能放入一个请求。

采用：

```text
source units
-> batch extraction
-> global aggregation
-> deterministic coverage check
```

## 22. Context Budget

### 22.1 Budget 结构

```json
{
  "provider_id": "",
  "model_id": "",
  "max_input_tokens": 0,
  "reserved_output_tokens": 0,
  "reserved_system_tokens": 0,
  "effective_context_tokens": 0,
  "estimator": "provider_tokenizer | conservative_chars",
  "allocation": {}
}
```

### 22.2 Token 优先

有 Provider Tokenizer 时使用 Token。

没有时使用保守字符估算，但必须：

- 配置化；
- 留安全余量；
- 不把中文字符数直接等同于 Token；
- 记录 Estimator。

### 22.3 预算优先级

```text
1. System/Step Prompt 和 Output Schema
2. Selection Snapshot
3. Target Scope
4. Required Upstream
5. Decision/Config/Approval Snapshot
6. Continuity Context
7. Source Window
8. Relevant Recent Conversation
9. Optional Summary
10. Optional Examples
```

低优先级不能挤掉高优先级。

### 22.4 Rule 预算

只加载当前 Step 引用 Rules。

如果 Rules 太大：

- Rule 模块拆分；
- 使用当前 Step 必需章节；
- 通过 Registry 声明 Section Refs；
- 不加载全 Rule 库。

### 22.5 Budget Failure

错误：

```text
CONTEXT_REQUIRED_INPUT_EXCEEDS_BUDGET
CONTEXT_SELECTION_EXCEEDS_BUDGET
CONTEXT_RULES_EXCEED_BUDGET
CONTEXT_PROVIDER_LIMIT_UNKNOWN
```

不得在预算失败后悄悄发一个残缺请求。

## 23. Truncation Policy

### 23.1 允许裁剪

- 已有明确边界的 Recent Conversation；
- Optional Examples；
- Optional Event History；
- 重复摘要；
- 无关字段；
- Source Window Padding。

### 23.2 禁止裁剪

- Selection Text；
- Target Entity；
- 必需 Upstream Scope；
- Config；
- Approval Subject；
- Output Schema 必需字段；
- 强制 Rule；
- Asset Time Range；
- Stable IDs。

### 23.3 裁剪记录

```json
{
  "truncation_log": [
    {
      "section": "recent_conversation",
      "policy": "drop_oldest_irrelevant",
      "original_size": 0,
      "final_size": 0
    }
  ]
}
```

Worker Trace 必须能解释哪些内容被裁剪。

## 24. Summarization

### 24.1 Summary 类型

```text
conversation_summary
project_summary
artifact_digest
episode_end_summary
source_unit_summary
video_episode_summary
```

### 24.2 Summary 不是原文

Summary 必须包含：

- 来源 Refs；
- Source Version/Checksum；
- Summary Version；
- 创建方式；
- 事实/推断区分；
- 生成时间。

### 24.3 关键事实

关键事实不能只存在于 Summary。

例如：

- 用户确认配置；
- Approval；
- Artifact Version；
- Final Selection；
- Asset 删除；

必须读取 Runtime 对象。

### 24.4 Summary 过期

当来源 Version 改变：

- Summary 标记 stale；
- 不能继续作为当前 Context；
- 需要重建或读取旧 Lineage；
- 旧 Summary 仍可供历史 Run 审计。

## 25. Retrieval

### 25.1 第一版优先确定性检索

优先：

- Project ID；
- Artifact Version；
- Stable Scope；
- Episode Number；
- Source Offset；
- Asset ID；
- Event Seq；
- Dependency Lineage。

### 25.2 向量检索

第一版不把向量库作为必需底座。

可以用于：

- 超长材料中寻找相关段落；
- 跨 Source Unit 召回；
- 辅助人物/事件检索。

但检索结果必须：

- 限定当前 Project；
- 返回 Source Refs；
- 绑定 Asset Checksum；
- 经过 Step Policy；
- 不能替代精确 Version Dependency；
- 不能自动加入其他 Project 内容。

### 25.3 Retrieval 结果

```json
{
  "query": "",
  "results": [
    {
      "source_ref": {},
      "score": 0.0,
      "reason": "",
      "content": ""
    }
  ]
}
```

Score 不作为事实正确性证明。

## 26. Context Assembly Pipeline

```text
1. 接收结构化 Action
2. 校验 Project/Conversation
3. 读取 Capability Definition
4. 解析 Intent
5. 解析显式 Target/Selection
6. Runtime 解析精确 Version
7. 读取 Step 必需输入
8. 根据 Scope 编译 Source/Artifact 片段
9. 加载当前 Step Prompt/Rules
10. 加载 Output Schema
11. 分配 Budget
12. 应用允许的裁剪和摘要
13. 校验完整性、隔离和模态
14. 生成 Provenance 和 Context Hash
15. 创建 Execution Attempt
16. 调用 Provider
```

任何一步失败都不能继续调用 Provider。

## 27. Context Validator

调用前必须验证：

1. Pack Version 支持；
2. Project ID 一致；
3. Conversation 属于 Project；
4. Run/Step/Task 属于 Project；
5. Capability ID/Version 与 Run 一致；
6. Target 存在；
7. Selection Snapshot 未过期；
8. Artifact Version 精确存在；
9. Version Resolution Policy 合法；
10. 必需 Upstream 完整；
11. Config Snapshot 一致；
12. Asset 可用；
13. Provider 支持当前模态；
14. Rules 属于当前 Step；
15. Output Schema 存在；
16. Budget 不超限；
17. 没有其他 Project 数据；
18. Provenance 覆盖所有内容片段；
19. Context Hash 生成成功；
20. Attempt 状态允许执行。

## 28. Provenance

### 28.1 结构

```json
{
  "provenance_id": "",
  "context_section": "upstream_context[0]",
  "source_kind": "artifact_version",
  "source_ref_id": "",
  "scope": {},
  "content_hash": "",
  "included_as": "full | scoped | summary | metadata"
}
```

### 28.2 要求

每个传给模型的业务内容片段必须可追踪到：

- Message；
- Asset Snapshot；
- Artifact Version；
- Config Snapshot；
- Rule Version；
- Summary Version。

Provider 输出保存为 Artifact 时，Dependency 使用 Provenance 中的精确输入。

## 29. Prompt、Rules 和 Context 分离

```text
Prompt
= 当前任务说明

Rules
= 当前任务专业约束

Context
= 当前作品的事实和目标材料

Output Schema
= 机器可校验结果合同
```

禁止：

- 把 Project 事实写进共享 Rule；
- 把 Provider Secret 写进 Prompt；
- 把全部 Rules 放入 Main Agent；
- 把 Output Schema 只写成自然语言；
- 把 Context Pack 当成长期记忆文件。

## 30. Main Agent 与 Worker 边界

### 30.1 Main Agent

可以：

- 路由；
- 追问；
- 解释状态；
- 形成结构化 Revision Intent；
- 解析用户指代；
- 生成自然语言回复。

不能：

- 直接生成正式 Artifact 并保存；
- 自行选择未授权 Version；
- 自行读取全部作品；
- 自行跳过 Step；
- 自行确认。

### 30.2 Worker

可以：

- 按 Step Context 生成指定 Artifact/Patch；
- 输出结构化结果；
- 在明确范围内生成内容。

不能：

- 改变 Capability；
- 改变 Target；
- 增加未提供 Asset；
- 决定下一 Step；
- 决定 Approval；
- 从 Project 额外查数据。

Worker 的所有输入必须通过 Context Pack 显式提供。

## 31. 小说链 Context 映射

### 31.1 Story Bible

目标：

```text
完整理解小说
```

如果原文超预算：

```text
Source Units
-> 分批事实提取
-> 故事级聚合
-> Source Coverage Check
```

不能只采样头尾生成 Story Bible。

### 31.2 Episode Split

全局阶段读取：

- Story Bible；
- Source Unit Index；
- Config；
- 原文结构标记；
- 总体体量统计。

边界批次读取：

- 目标 Episode Range；
- 候选边界附近 Source Window；
- 相邻 Episode Anchor；
- 全局 Split Plan。

### 31.3 Episode Cards

每批读取：

- 已确认 Episode Split 对应范围；
- Story Bible 必要人物/主线；
- Config；
- 上一批 Continuity Summary；
- 当前 Batch Episode IDs。

### 31.4 Script Unit N

读取：

- Episode Card N；
- Script Context；
- Source Refs N；
- Script Unit N-1 Continuity Delta；
- 用户对 Episode N 的要求；
- 剧本 Prompt 和 Rules。

不读取全部其他 Episode Script。

## 32. 非小说链 Context 映射

### 32.1 Material Bank

读取：

- 用户选定非小说 Assets；
- 当前请求；
- Config；
- 用户明确事实。

区分：

- 用户原始事实；
- 模型整理；
- 候选推断；
- 缺口。

### 32.2 Story Seed

读取：

- Confirmed Material Bank；
- 用户补充要求；
- Config；
- 已确认新增策略。

### 32.3 Series Blueprint

读取：

- Confirmed Story Seed；
- 必要 Material Bank 摘要；
- Config。

### 32.4 Episode Cards 和 Scripts

沿用 Scope-aware Batch 与单集最小 Context。

不每次重复发送全部原始非小说材料。

## 33. 视频参考创作 Context 映射

### 33.1 单集视频解析

每个 Task：

- 一个 Video Asset；
- Episode Mapping；
- 当前视频时长；
- Known Characters；
- Previous Episode End；
- 用户确认事实；
- 视频提取 Prompt；
- 视频私有 Rule；
- `plot_summary + video_script_unit` Schema。

禁止一次把几十集视频放入一个 Provider 请求。

### 33.2 Reference Scripts Aggregate

系统确定性聚合：

- 所有成功 `video_script_unit`；
- Episode Order Snapshot；
- Incomplete Material Decision。

不调用模型。

### 33.3 Script Analysis

几十集剧本可能超预算。

内部执行：

```text
reference_scripts
-> episode analysis shards
-> phase analysis shards
-> global script_analysis aggregation
-> evidence coverage validation
```

用户仍只看到一个 `script_analysis` 主要步骤和 Artifact。

每个判断保留：

- Episode Ref；
- Event/Scene Ref；
- Short Quote Ref；
- Fact vs Judgment。

### 33.4 Adaptation Options

读取：

- Confirmed Script Analysis；
- 用户已有改编要求；
- 来源和禁止照搬规则；
- 目标用户/题材限制。

### 33.5 Adaptation Brief

读取：

- 用户选择/组合的 Options；
- 用户自定义要求；
- Confirmed Script Analysis；
- 新剧本集数和时长；
- 禁止照搬规则。

### 33.6 Shared Story Development

Brief 确认后：

- Story Seed 以 Brief 为强制上游；
- 参考剧原始全文不默认再次进入每个创作 Step；
- 必要创作方法通过 Brief 和 Analysis Evidence Refs 提供；
- 不让 Worker直接照搬 Reference Scripts。

## 34. 图片通用输入

图片理解属于 Generic Tool。

### 34.1 用户意图明确

例如：

```text
“这张图是什么风格？”
```

使用 Routing/Inspection Context，不创建 Domain Run。

### 34.2 用户希望作为创作参考

图片先作为 Asset 加入 Project。

只有当某个 Domain Capability 的 Input Schema 接受图片参考时，才加入其 Capability Context。

### 34.3 意图不明确

Agent 追问：

```text
希望我解释图片、保存为当前作品参考，还是用于某个创作 Skill？
```

不自动启动剧本生成。

## 35. Context Persistence

### 35.1 保存什么

必须保存：

- Context Pack Metadata；
- 精确 Version Refs；
- Asset Snapshot Refs；
- Selection Snapshot；
- Rule/Prompt/Schema Version；
- Budget Result；
- Truncation Log；
- Provenance；
- Context Hash；
- Attempt ID。

### 35.2 敏感内容

完整编译 Context 可以：

- 加密保存短期调试副本；
- 或只保存可重建 Refs 和 Hash。

第一版默认：

- 业务 Artifact/Asset 正常保存；
- Trace 不重复保存全部原文；
- Provider Request Log 脱敏；
- API Key 永不进入 Context Pack；
- 视频临时 Provider URL 不长期保存。

### 35.3 可重建性

Retry 必须能通过：

- Input Snapshot；
- Artifact Version；
- Context Policy Version；
- Prompt/Rules Version；
- Config Snapshot；

重建同语义 Context。

如果依赖的发布包版本不存在，Run 进入不可恢复错误，不能套用新 Prompt 静默继续。

## 36. Context Cache

可以缓存：

- Artifact Digest；
- Source Unit；
- Token Count；
- Rule 编译结果；
- Summary；
- Provider Upload Variant。

Cache Key 必须包含：

- Project；
- Source Version/Checksum；
- Scope；
- Policy Version；
- Model/Tokenizer；
- Context Hash。

不能只按 Artifact ID 缓存。

## 37. 多模态规范

### 37.1 Modality Capability

Provider Adapter 声明：

```text
text
image
video
audio_in_video
structured_output
max_input
```

Assembler 在调用前验证。

### 37.2 视频处理副本

Context 使用处理副本时：

- 保留 Original Asset Ref；
- 记录 Variant ID；
- 记录压缩/转码参数；
- 不声称处理副本等同原文件；
- 输出 Dependency 仍追溯到 Original + Variant。

### 37.3 不支持模态

明确返回：

```text
CONTEXT_MODALITY_NOT_SUPPORTED
```

不能把视频文件名当成视频内容继续生成。

## 38. 安全与隐私

必须：

- Context 查询强制 Project Filter；
- Asset 访问使用 allowlist；
- Provider 通过公司内网透传；
- 不把 Provider Key 放前端；
- 不把其他 Project Summary 放入检索；
- Trace 脱敏；
- 文件路径不直接暴露；
- MCP Tool 有独立权限和输入范围；
- 用户删除/过期 Asset 后停止创建新 Provider Input。

### 38.1 Cross-project 防线

至少三层：

1. Repository Query 强制 Project ID；
2. Context Validator 再校验所有 Refs；
3. Provider Request Trace 记录 Project/Context Hash。

## 39. API 与内部接口

Context Pack 不作为普通前端完整公开 API。

### 39.1 Internal Interface

```go
type ContextAssembler interface {
    BuildRouting(ctx context.Context, req RoutingContextRequest) (RoutingContextPack, error)
    BuildCapabilityStart(ctx context.Context, req CapabilityStartContextRequest) (CapabilityStartContextPack, error)
    BuildStepExecution(ctx context.Context, req StepExecutionContextRequest) (StepExecutionContextPack, error)
    BuildRevision(ctx context.Context, req RevisionContextRequest) (RevisionContextPack, error)
    BuildApproval(ctx context.Context, req ApprovalContextRequest) (ApprovalContextPack, error)
    BuildInspection(ctx context.Context, req InspectionContextRequest) (InspectionContextPack, error)
}
```

### 39.2 Public Debug Summary

前端可以读取：

```json
{
  "context_pack_id": "",
  "pack_type": "",
  "target_label": "",
  "source_labels": [],
  "version_summary": [],
  "truncated_sections": [],
  "created_at": ""
}
```

不公开完整 System Prompt、Rules 和敏感原文。

## 40. 错误码

```text
CONTEXT_PROJECT_MISMATCH
CONTEXT_CONVERSATION_MISMATCH
CONTEXT_RUN_MISMATCH
CONTEXT_CAPABILITY_MISMATCH
CONTEXT_TARGET_NOT_FOUND
CONTEXT_TARGET_AMBIGUOUS
CONTEXT_TARGET_CONFLICT
CONTEXT_SELECTION_STALE
CONTEXT_SELECTION_EXCEEDS_BUDGET
CONTEXT_VERSION_NOT_FOUND
CONTEXT_LINEAGE_CONFLICT
CONTEXT_REQUIRED_UPSTREAM_MISSING
CONTEXT_ASSET_UNAVAILABLE
CONTEXT_ASSET_EXPIRED
CONTEXT_MODALITY_NOT_SUPPORTED
CONTEXT_REQUIRED_INPUT_EXCEEDS_BUDGET
CONTEXT_RULES_EXCEED_BUDGET
CONTEXT_PROVIDER_LIMIT_UNKNOWN
CONTEXT_PROVENANCE_INCOMPLETE
CONTEXT_BUILD_VERSION_MISSING
```

## 41. 可观测性

Event/Trace：

```text
context_build_started
target_resolved
version_lineage_resolved
context_source_selected
context_section_truncated
context_summary_used
context_budget_checked
context_build_completed
context_build_failed
```

需要能回答：

- 为什么使用 Episode Card v2 而不是 v3；
- 本次修改定位到哪个 Line ID；
- 哪些内容被裁剪；
- 是否使用了 Summary；
- 视频调用使用哪个处理副本；
- 为什么没有读取完整小说；
- 为什么 Context 超预算而没有调用模型。

## 42. 不采用的方案

### 42.1 每轮发送整个 Project

不采用。成本高、噪音大且容易版本串用。

### 42.2 只发送最近 N 条聊天

不采用。可能遗漏结构化状态并混入无关内容。

### 42.3 让模型自行搜索所有 Artifact

不采用。Target 和 Version 必须由 Runtime/Assembler 确定。

### 42.4 使用 Project Current Version 替代 Lineage

不采用。会破坏 Resume、Retry 和 Keep Downstream。

### 42.5 选区只保存文本

不采用。相同文本可能出现多次，必须保存 Version、Stable IDs、Offset 和 Hash。

### 42.6 关键内容超预算时自动截断

不采用。必须拆任务或失败。

### 42.7 第一版先建设全局向量知识库

不采用。优先精确 ID、Scope、Offset 和 Dependency。

### 42.8 Rules 常驻 Main Agent

不采用。Rules 按 Step 加载。

## 43. 测试要求

### 43.1 Project 隔离

- 两个 Project 同名 Artifact 不串用；
- 检索强制 Project Filter；
- Cross-project Ref 被拒绝；
- Summary 不跨 Project；
- Cache Key 包含 Project。

### 43.2 版本

- Resume 使用原 Snapshot；
- Retry 使用原 Snapshot；
- Keep Downstream 使用 Lineage；
- 新 Run 使用当前 Confirmed 并固定；
- 历史 Version 修改使用 Explicit；
- 不存在无条件 Latest。

### 43.3 Target

- 显式 Target 优先；
- Selection 优先于 Focus；
- Focus 不直接授权修改；
- 文本 Target 与 Selection 冲突时追问；
- 模糊指代多候选时追问；
- 不存在目标时不调用 Worker。

### 43.4 Selection

- 发送后编辑不改变 Snapshot；
- Base Version 变化返回 Stale；
- Stable IDs 定位；
- 重复文本不误定位；
- 空字符串删除合法；
- 超大选区不静默截断。

### 43.5 Budget

- 高优先级内容不被低优先级挤出；
- Rule 只加载声明部分；
- Optional Conversation 可裁剪；
- 必需 Upstream 超预算时报错；
- Tokenizer 和字符估算记录；
- Truncation Log 完整。

### 43.6 小说

- Story Bible 长文本分批覆盖；
- Episode Split 只读边界窗口；
- Episode Card Batch 不读全部剧本；
- Script Unit N 只读必要相邻连续性；
- Source Offset 准确。

### 43.7 非小说

- 用户事实与模型新增区分；
- Story Seed 使用 Confirmed Material Bank；
- Script Step 不重复发送全部材料；
- Config Snapshot 一致。

### 43.8 视频

- 单 Task 只发送一个视频；
- 多集分析分层聚合；
- Episode Order 正确；
- 过期视频不创建 Provider Input；
- Brief 后创作不默认照搬 Reference Script；
- Provider Variant 可追溯。

### 43.9 零 Capability

- Routing Pack 正常构建；
- Capability List 为空；
- Project/Conversation/Asset 可读；
- 通用图片解释可用；
- Domain Run 不创建；
- Agent 正确说明能力缺口。

## 44. 实施顺序

1. 建 Context Pack Envelope 和 Version；
2. 建 Project/Conversation/Run Summary Reader；
3. 建 Runtime Version Resolver；
4. 建 Target Resolver；
5. 建 Selection Snapshot；
6. 建 Step Input Resolver；
7. 建 Source Unit/Offset；
8. 建 Budget Manager；
9. 建 Rule/Prompt Loader；
10. 建 Context Validator；
11. 建 Provenance 和 Hash；
12. 迁移现有 Worker Context Assembler；
13. 为小说拆集和剧本生成做定向回归；
14. 实现视频单 Task Context；
15. 实现整剧分析分层 Context。

## 45. 完成门禁

Capability Context Pack 实现完成必须满足：

- Main Agent、Capability 和 Worker 使用不同粒度 Context；
- Registry 为空时 Routing Context 仍可用；
- 所有 Context 限定一个 Project；
- Target 和 Version 在读取内容前确定；
- Selection Snapshot 不可变且可校验；
- Resume/Retry 不替换输入版本；
- Keep Downstream 使用 Lineage；
- Worker 不读取未声明数据；
- 必需输入不被静默截断；
- Budget、裁剪和 Summary 可追踪；
- 长小说和几十集视频可以拆分执行；
- 每个内容片段有 Provenance；
- Prompt、Rules、Context 和 Output Schema 分离；
- Context Hash 可用于幂等和迟到结果校验；
- 所有关键场景有确定性测试。

## 46. Review Execution Context Pack

权威合同：

```text
content-semantics-and-quality-review-contract.md
```

11A 增加 `pack_type: review_execution`。它固定：

- `quality_review_id`；
- `input_snapshot_hash`；
- 本批 Episode Scope；
- 精确 `script_unit/script_handoff/script_context` Version；
- 当前链路必要上游事实；
- Config 与 Decision Snapshot；
- Prompt、Rules、Result Schema 和内容哈希。

单集批次只读取当前范围；全局连续性审核读取单集审核摘要和最小全局规则，不重复发送完整小说、全部原始视频或无关历史对话。

Review Context Pack 不携带可执行 `next_stage`。模型返回 `recommended_route` 后，由 Guard 结合 Capability Definition 和 Dependency 映射真实 Step。
