# 通用 Agent 定位与修改设计

状态：设计变更基线，尚未实现  
版本：1.0  
日期：2026-08-11

本文件定义阶段 9 之后提出的通用 Agent 定位与修改增强。它是后续实现的权威设计输入，不代表当前代码已经具备完整闭环，也不自动改变 `project-lifecycle-stage-gates.md` 的阶段结论。

与既有合同的关系：

- 扩展 `agent-context-targeting-contract.md` 的定位协议；
- 复用 `capability-context-pack-contract.md` 的不可变 Selection Snapshot、精确版本和最小上下文原则；
- 复用 `artifact-dependency-contract.md` 的不可变版本、实际差异和下游影响计算；
- 对 `api-event-contract.md` 中“活动写 Run 时锁死全部 Message/Composer”的规则形成定向升级：运行中允许只读问答、定位和排队修订，但仍禁止并发写入和启动第二条主链。

## 0. 当前实现与本设计差距

| 能力 | 当前状态 | 本设计要求 |
|---|---|---|
| View Context、显式 Selection Snapshot | 已有前后端基础实现 | 保留并扩展到所有可编辑 Artifact |
| Selection 归属、版本、Hash 校验 | Runtime 已有 | 继续作为硬校验，不交给模型 |
| Worker Step Context Pack | 已有 | 新增独立 Revision Context Pack，不复用整步生成上下文 |
| 自然语言目标解析 | 未闭环 | Runtime 候选目录 + Agent 受限排序 + 歧义确认 |
| Main Agent 修订动作 | 未闭环 | 增加 Target Resolution 与 Revision Request 动作合同 |
| AI 修改后的版本 | 当前可直接创建 Artifact Version | 先形成 Proposed Version，用户接受后再切 Current Version |
| 运行中自由输入 | 当前 Composer 锁定 | 保持可输入；只读消息即时处理，写请求排队到安全检查点 |
| 修改过程展示 | 未统一 | Agent 时间线展示定位、排队、提案、确认和影响结果 |

## 1. 目标

用户在内容生产的任意阶段，可以通过两种方式向同一个 General Agent 提出修改要求：

1. 显式定位：选中文本、字段、条目、场次或节点后输入要求；
2. 自然语言定位：直接描述“把第三集结尾钩子改强”“修改故事圣经里的主角性格”。

定位、指代、版本校验、确认和审计属于 Agent Shell 与 Runtime，不属于某个 Skill。新增 Skill 复用同一套机制，只注册它支持的 Artifact 类型、上下文需求和修改适配器。

第一版最终闭环：

```text
用户请求
-> Target Resolution
-> Runtime Validation
-> Revision Context Pack
-> Revision Adapter
-> Proposed Artifact Version
-> 用户审阅与确认
-> Current Version 切换
-> 下游影响处理
```

## 2. 非目标

- 不允许模型直接写数据库或自行选择任意 Artifact ID；
- 不允许跨作品定位或修改；
- 不支持一次请求任意修改多个互不相关的 Artifact；
- 不在运行中的 Worker 内热替换输入；
- 不把每个 Skill 做成独立的选区、指代和版本系统；
- 不因文本相似就静默把过期选区迁移到新版本。

## 3. 所有权

```text
Agent Shell
  捕获显式位置、理解自然语言、生成候选、发起澄清

Runtime
  建立可检索目录、校验归属与版本、冻结 Target、管理修改任务与版本

Revision Adapter
  读取已解析 Target 和最小上下文，生成符合 Schema 的修改结果

Skill
  声明可修改 Artifact、领域上下文和适配器，不重建定位系统

Frontend
  展示引用、候选、状态、差异、版本和影响，不自行推断目标
```

该机制不是第四个 Skill，也不属于小说、非小说或视频 Skill。Skill 只注册领域适配器；即使没有选择任何 Skill，General Agent 仍能理解当前 View、回答关于当前产物的问题、完成定位，并在存在适配器时发起修改。

## 4. Target 模型

### 4.1 Resolved Target

```json
{
  "target_id": "tgt_...",
  "schema_version": "1.0.0",
  "project_id": "prj_...",
  "source": "explicit_selection",
  "artifact_id": "art_...",
  "artifact_version_id": "av_...",
  "artifact_type": "episode_cards",
  "scope_key": "global",
  "field_path": "episodes[id=episode_3].ending_hook",
  "entity": {
    "episode_id": "3",
    "scene_id": null,
    "line_id": null,
    "entity_id": "episode_3"
  },
  "text_range": null,
  "display": {
    "artifact_label": "分集规划",
    "location_label": "第 3 集 / 结尾钩子",
    "content_summary": "……"
  },
  "target_hash": "sha256",
  "created_at": ""
}
```

约束：

- `target_id` 由 Runtime 生成；
- `artifact_version_id` 必须在解析完成时冻结；
- `field_path` 使用 Schema Registry 允许的稳定路径，不接收模型任意 JSONPath；
- 数组元素必须优先使用稳定实体 ID，不能只依赖数组下标；
- `text_range` 只用于不可进一步结构化的文本片段；
- `target_hash` 覆盖所有定位字段、版本和内容指纹。

### 4.2 Target Source

允许值及优先级：

```text
explicit_selection       用户选区
explicit_node            用户点击“让 Agent 修改”时的字段或节点
explicit_command         明确按钮，例如“重新生成本集”
semantic_reference       从本轮自然语言解析
conversation_reference   从最近已确认指代解析
view_focus               当前正在查看的 Artifact
approval_subject         当前确认点主体
```

优先级高不代表可以忽略冲突。用户选中第 3 集却输入“修改第 5 集”时，结果必须是 `conflict`。

`explicit_selection`、`explicit_node` 和 `explicit_command` 都属于显式目标。三者之间如内容冲突，不能按优先级静默覆盖，必须进入 `conflict`。优先级只用于没有冲突时补足缺失层级。

### 4.3 Resolution Result

```json
{
  "resolution_id": "tr_...",
  "status": "resolved",
  "intent": "revise",
  "instruction": "强化结尾钩子，但不要新增人物",
  "resolved_target": {},
  "candidates": [],
  "confidence": 1.0,
  "needs_confirmation": false,
  "reason_code": "EXPLICIT_SELECTION",
  "evidence": []
}
```

`status` 允许：

- `resolved`：唯一目标已冻结；
- `ambiguous`：存在多个合理候选；
- `conflict`：显式位置与文字要求冲突；
- `not_found`：没有可验证目标；
- `stale`：位置对应版本已更新；
- `unsupported`：已定位，但没有可用 Revision Adapter。

只有 `resolved` 可以进入修改执行。

## 5. 两条定位通道

### 5.1 显式定位

适用于所有可阅读的中间产物和单集剧本：

- 选中文本；
- 聚焦结构化字段；
- 点击条目级“让 Agent 修改”；
- 在剧本中选择台词、动作、场次；
- 在分集规划中选择某集或某个钩子；
- 在故事圣经中选择人物、关系或世界规则。

发送时生成不可变 `SelectionSnapshot`。Runtime 校验：

1. Project、Artifact、Version 归属；
2. 当前版本是否变化；
3. Field Path 和稳定实体 ID 是否存在；
4. 偏移、选中文本和前后文是否一致；
5. Snapshot Hash 是否一致；
6. 当前 View 是否与 Selection 冲突。

显式 Target 已经确定，模型只解释修改意图，不重新选择位置。

### 5.2 自然语言定位

自然语言定位采用“先检索候选，再让模型排序，最后 Runtime 校验”，禁止把全部 Artifact Payload 塞给 Main Agent。

```text
用户文字
-> 确定性锚点提取
-> Artifact Target Catalog 检索
-> 小候选集
-> 模型结构化排序
-> Runtime 白名单校验
-> resolved / ambiguous / not_found
```

确定性锚点包括：

- 产物名称：故事圣经、素材库、分集规划、剧本分析、改编 Brief、单集剧本；
- 集数：第 3 集、本集、上一集、最后一集；
- 稳定实体：人物名、场次号、字段标签、条目标题；
- 相对指代：这里、这个人物、刚才那一段、上一步；
- 操作词：修改、删除、加强、缩短、重写、恢复。

模型只能返回输入候选中出现的 `candidate_id`，不能返回 Artifact ID、版本 ID 或 Field Path。

### 5.3 Artifact Target Catalog

Runtime 为当前作品构建轻量目录：

```json
{
  "candidate_id": "tc_...",
  "artifact_id": "art_...",
  "artifact_version_id": "av_...",
  "artifact_type": "story_bible",
  "scope_key": "global",
  "capability_id": "novel_to_script",
  "run_id": "run_...",
  "label": "故事圣经",
  "search_terms": ["人物", "关系", "世界观", "林澈"],
  "nodes": [
    {
      "node_id": "node_character_linche",
      "field_path": "characters[id=character_linche]",
      "label": "人物 / 林澈",
      "summary": "……"
    }
  ]
}
```

Catalog 只保存定位摘要和搜索词，不取代 Artifact Version。Artifact 更新后旧 Catalog Entry 失效并重建。

## 6. 歧义、冲突与指代

### 6.1 唯一目标

满足以下条件可直接生成修改稿：

- 显式 Target 校验通过；或
- 自然语言候选唯一且超过阈值；
- 请求中没有互相冲突的集数、字段或 Artifact；
- Revision Adapter 可用。

不额外询问“是否确定位置”，减少交互负担。生成的新版本仍需用户审阅确认。

### 6.2 多候选

Agent 返回位置确认卡，不用开放式追问：

```text
你想修改哪一处？

○ 故事圣经 / 人物 / 林澈 / 性格
○ 故事种子 / 主角 / 林澈
○ 第 3 集剧本 / 场 3-2 / 林澈台词
```

候选最多显示 5 个，超出时要求用户补充产物或集数。

### 6.3 显式冲突

```text
已选位置：第 3 集 / 结尾钩子
文字要求：修改第 5 集结尾
```

必须让用户选择“按选中位置”或“改为第 5 集”，不得按置信度自动覆盖。

### 6.4 对话指代

只允许引用最近已确认的 `target_id` 或当前 View。普通聊天文本不能成为稳定定位依据。

```text
“再短一点” -> 最近一次已确认 Revision Target
“把刚才那个钩子提前” -> 最近一次已确认且类型为 hook 的 Target
```

候选超过一个或目标已经更新时必须追问。

## 7. 过程内与完成后的修改

### 7.1 等待确认、暂停、已完成

可以立即解析目标并生成 Proposed Version。

### 7.2 正在运行

运行中允许发送修改需求，但不直接修改 Worker 正在写入的内容：

1. 立即保存消息和 Target Resolution；
2. 创建 `revision_request`，状态为 `waiting_safe_checkpoint`；
3. 如果目标是尚未生成的未来产物，将要求转成该 Step 的用户约束；
4. 如果目标是已确认产物，在当前 Step 结束后暂停 Run；
5. 到安全检查点重新校验版本；
6. 版本未变则执行，已变则重新定位或提示 stale。

第一版不做 Token 流中途抢占和 Worker 热更新。用户需要立即停止时使用独立“停止运行”操作。

运行中 Message 分为三类：

| 类型 | 行为 |
|---|---|
| `inspect_or_chat` | 只读回答，不改变 Run、Artifact 或 Approval |
| `revision_request` | 定位并保存请求；需要写入时等待安全检查点 |
| `start_or_switch_skill` | 不启动第二条主链；提示先暂停、停止或完成当前 Run |

HTTP 不再因为存在活动写 Run 而在调用 Main Agent 前拒绝全部 Message。它先持久化消息并执行只读路由；任何写动作仍由 Runtime 的单写者锁和版本校验兜底。

### 7.3 单写者规则

同一作品同时只能有一个写入事务：

- Generation Run；
- Revision Job；
- Regeneration Plan。

聊天、定位、候选确认可并行；正式生成修改稿必须排队。

同一作品的 Revision Request 按 `created_at + revision_request_id` 稳定串行。后一个请求执行前必须重新校验 Base Version；如果前一个已被接受并改变同一 Artifact，后一个进入 `stale`，不能自动套用到新版本。用户可以取消 `draft`、`waiting_*` 或 `queued` 请求；已经 `running` 的 Provider 调用允许完成，但结果不得自动形成可采用 Proposal。

## 8. Revision Request

```json
{
  "revision_request_id": "rr_...",
  "project_id": "prj_...",
  "conversation_id": "conv_...",
  "request_message_id": "msg_...",
  "resolution_id": "tr_...",
  "target_id": "tgt_...",
  "instruction": "强化结尾钩子，但不要新增人物",
  "operation": "revise",
  "status": "queued",
  "execution_policy": "safe_checkpoint",
  "created_at": ""
}
```

`operation` 第一版支持：

- `revise`：保留主体并修改；
- `rewrite`：重写目标节点；
- `delete`：删除允许删除的节点；
- `regenerate_scope`：按当前上下文重生成目标范围。

涉及关键设定、主线、集数或来源约束的变更必须进入显式影响确认，不能只做局部文本替换。

## 9. Revision Context Pack

```json
{
  "context_pack_id": "cp_...",
  "context_pack_version": "1.0.0",
  "pack_type": "revision",
  "project_id": "prj_...",
  "conversation_id": "conv_...",
  "request_message_id": "msg_...",
  "revision_request_id": "rr_...",
  "intent": {
    "operation": "revise",
    "instruction": "强化结尾钩子，但不要新增人物"
  },
  "target": {},
  "selection_snapshot": null,
  "target_content": {},
  "adjacent_context": [],
  "required_upstream": [],
  "locked_constraints": [],
  "revision_adapter": {
    "adapter_id": "episode_cards_revision",
    "version": "1.0.0"
  },
  "output_contract": {
    "mode": "full_artifact_payload",
    "schema_ref": "schemas/episode_cards/v1"
  },
  "budget": {},
  "provenance": [],
  "context_hash": ""
}
```

上下文优先级：

```text
Target 原内容
-> 同一实体必要邻接内容
-> 直接上游确认版本
-> 锁定规则和用户已确认决策
-> 最近与本次修改相关的消息
```

禁止默认携带全作品、全部聊天和全部 Run 日志。

## 10. Revision Adapter

### 10.1 注册结构

```json
{
  "adapter_id": "episode_cards_revision",
  "version": "1.0.0",
  "artifact_types": ["episode_cards"],
  "supported_scopes": ["artifact", "entity", "field", "selection"],
  "supported_operations": ["revise", "rewrite", "delete", "regenerate_scope"],
  "context_requirements": {
    "adjacent_entities": 1,
    "required_upstream_types": ["story_bible", "episode_split"]
  },
  "output_mode": "full_artifact_payload",
  "schema_ref": "schemas/episode_cards/v1"
}
```

### 10.2 通用与领域适配

- Shell 提供 Target、版本、上下文和输出校验外壳；
- 通用结构化编辑器可支持安全的字段级改写；
- 剧本、分集规划、故事圣经等领域语义由对应 Adapter 处理；
- 多个 Skill 生产同一 Artifact Type 时可以复用一个共享 Adapter；
- 新 Skill 没有 Adapter 时仍可定位、解释和手动编辑，但不能声称已执行 AI 修改。

第一版需要支持的可修改 Artifact：

- `story_bible`
- `episode_split`
- `material_bank`
- `story_seed`
- `series_blueprint`
- `episode_cards`
- `video_script_unit`
- `script_analysis`
- `adaptation_brief`
- `script_unit`

系统产物 `source_input`、`source_manifest`、`reference_scripts`、`script_context`、`script_handoff`、`scripts` 默认不可直接修改。

## 11. 版本与确认

AI 修改不直接覆盖 Current Version。

```text
Revision Adapter 输出
-> Schema Validation
-> Proposed Artifact Version
-> Diff / 修改摘要
-> 用户确认
-> 切换 Current Version
-> 创建 Impact Review
```

用户可以：

- 确认采用；
- 继续让 Agent 调整；
- 手动编辑修改稿；
- 放弃并保留历史记录。

Proposed Version 必须保存：

- Base Version；
- Target ID；
- Revision Request；
- Context Hash；
- Adapter Version；
- Provider 调用记录；
- 变更摘要；
- Schema 校验结果。

只有确认采用后才更新 Artifact Current Version，并触发下游失效/保留/重生成判断。

`Proposed Artifact Version` 是不可作为生产上游读取的候选版本。它与历史 Current Version 一样需要持久化，但必须带 `lifecycle_status=proposed`；普通生成、Resume、Retry 和下游 Context Assembler 只能读取已接受的版本。用户在修改稿上继续手动编辑或要求 Agent 再改时，形成新的 Proposal Revision，不覆盖上一份提案。

接受操作必须在一个事务中完成：

1. 校验 Proposal 仍基于当前 Base Version；
2. 将 Proposal 标记为 `accepted`；
3. 原子切换 Artifact Current Version；
4. 计算实际 Change Set；
5. 创建 Impact Review；
6. 发布版本与影响事件。

若 Base Version 已变化，返回 `REVISION_BASE_VERSION_CONFLICT`，不得自动 rebase。

### 11.1 审批与确认点

接受的新版本如果替换了已确认 Artifact：

1. 原 Approval Resolution 作为历史保留；
2. 旧 Approval 标记为 `superseded`；
3. 新版本进入对应步骤的 `waiting_approval`；
4. 受影响下游在 Impact Review 解决前不能 Resume；
5. 用户必须重新确认整个剧本步骤或该步骤合同规定的确认粒度。

Proposal 自身的“采用修改”只表示接受这份修改稿，不等同于批准业务步骤继续运行。

### 11.2 Candidate 与 Final

- 修改普通中间产物时沿用原 Run Lineage；
- 修改 `script_unit` 后重新聚合得到新的 Script Candidate；
- 已被选为 Final 的 Candidate 和其 Artifact Version 永远不原地修改；
- 新 Candidate 生成后，当前 Final 保持不变并提示用户是否改选；
- 导出继续读取已确认的 Final Selection，不能静默切到最新 Proposal 或 Candidate。

### 11.3 Target Catalog 生命周期

Target Catalog 是可重建索引，不是内容真源：

- 缓存键至少包含 `project_id + artifact_version_id + schema_version`；
- Artifact 新版本、删除、归档或 Schema 升级立即使对应目录项失效；
- Catalog 只索引当前作品有权读取的 Artifact Version；
- 历史版本只在用户明确进入版本历史时临时加入候选；
- 缓存丢失时从 Artifact Payload 和 Schema Registry 重建，不影响历史 Target 审计。

## 12. Runtime 状态

### 12.1 Target Resolution

```text
pending -> resolved
        -> ambiguous
        -> conflict
        -> not_found
        -> stale
        -> unsupported
```

### 12.2 Revision Request

```text
draft
-> waiting_target_confirmation
-> waiting_safe_checkpoint
-> queued
-> running
-> proposed
-> accepted
-> rejected
-> failed
-> cancelled
```

### 12.3 关键错误码

- `TARGET_NOT_FOUND`
- `TARGET_AMBIGUOUS`
- `TARGET_CONFLICT`
- `TARGET_STALE`
- `TARGET_PROJECT_MISMATCH`
- `TARGET_VERSION_MISMATCH`
- `TARGET_FIELD_NOT_ALLOWED`
- `REVISION_ADAPTER_UNAVAILABLE`
- `REVISION_CONTEXT_EXCEEDS_BUDGET`
- `REVISION_OUTPUT_SCHEMA_INVALID`
- `REVISION_BASE_VERSION_CONFLICT`
- `REVISION_WAITING_SAFE_CHECKPOINT`

## 13. API

```text
POST /api/v1/conversations/{id}/messages
  消息仍是唯一自然语言输入入口；运行中也可调用
  返回 message、resolution、revision_request 和 available_actions 摘要

GET /api/v1/projects/{id}/target-catalog
  调试与管理接口，普通前端不直接依赖它推断

POST /api/v1/target-resolutions/{id}/resolve
  用户从歧义候选中确认目标

GET /api/v1/revision-requests/{id}
  查询修改状态、目标和 Proposed Version

POST /api/v1/revision-requests/{id}/cancel
  取消尚未执行的修改

POST /api/v1/revision-requests/{id}/accept
  接受 Proposed Version，更新 Current Version

POST /api/v1/revision-requests/{id}/revise-again
  基于 Proposed Version 追加修改要求

POST /api/v1/revision-requests/{id}/reject
  放弃 Proposed Version，保留审计记录
```

前端不得直接调用 Provider，也不得自行把候选转换成 Target。

所有修订 Command 都必须携带 `Idempotency-Key`、`expected_revision_version` 和对应的 Base/Proposal Version。Target 候选确认只改变 Resolution，不直接执行修改。

## 14. 事件

```text
target_resolution.created
target_resolution.resolved
target_resolution.clarification_required
target_resolution.stale
revision_request.created
revision_request.queued
revision_request.started
revision_request.proposed
revision_request.accepted
revision_request.rejected
revision_request.failed
revision_request.cancelled
```

事件只展示用户可理解的状态；Context Pack、Provider 原始响应和 JSON Patch 不直接铺到 Agent 面板。

## 15. 前端交互

### 15.0 总体布局原则

- 左侧目录负责产物导航和层级展开，不承担修改操作；
- 中间工作区负责阅读、原位编辑、选区、Diff 和版本切换；
- 右侧 Agent 区保留完整消息与执行时间线，不只显示一次输入和最终输出；
- Composer 始终是同一个入口，不为每个 Skill 重做一套聊天区；
- 不在每个产物顶部常驻“让 Agent 修改”。用户通过选区、节点操作或直接输入自然语言发起；
- 剧本使用专用剧本编辑器和分集目录，中间产物使用结构化 Artifact Renderer，两者共享 Target/Revision 协议，不强行共享同一种编辑器。

### 15.1 显式引用

选中内容后，Composer 顶部显示不可编辑引用：

```text
故事圣经 / 人物 / 林澈 / 性格
“谨慎、隐忍，不轻易信任他人……”
```

用户可移除引用。切换 Artifact 或 Version 时清除不兼容引用。

触发入口：

- 文本选区后的轻量浮动操作“引用给 Agent”；
- 结构化字段或条目的行内菜单“让 Agent 修改”；
- 场次、台词、人物等稳定节点的上下文操作。

入口只在用户选中或打开菜单时出现，不重复铺在每个步骤顶部。

### 15.2 自然语言定位

唯一定位时，Agent 回复中显示目标摘要：

```text
已定位：分集规划 / 第 3 集 / 结尾钩子
正在生成修改稿……
```

不能只显示“正在处理”。目标摘要可点击并将中间工作区定位到对应节点。

### 15.3 歧义确认卡

使用单选候选，不让用户重新输入。确认后原始消息、候选集合和选择结果全部保留。

### 15.4 修改稿

中间工作区显示 Proposed Version，顶部固定操作：

- `采用修改`
- `继续调整`
- `手动编辑`
- `放弃`
- `版本历史`

正文不切换宽度，不弹出全屏 JSON 表单。结构化产物滚动到被改节点并标记变更；剧本定位到对应集、场、行。

操作栏必须吸顶，不能要求用户滚动到长文末尾保存或确认。进入修改稿不改变中间工作区宽度、不收缩编辑器、不导致页面闪动。剧本修改稿仍在剧本编辑器内按集/场展示；中间产物仍按自身结构展示，不降级成原始 JSON。

右侧时间线按一次修订请求至少显示：

```text
用户要求 + 引用目标
-> 已定位目标
-> 等待安全检查点 / 正在生成修改稿
-> 修改稿已生成
-> 用户接受 / 继续调整 / 放弃
-> 下游影响处理结果
```

刷新后这些记录从 Message、Resolution、Revision Request 和 Event 恢复，不能由前端临时文案拼接。

### 15.5 运行中请求

Composer 不再完全禁止输入。发送后显示：

```text
修改需求已记录
目标：分集规划 / 第 3 集
将在当前步骤完成后暂停并处理
```

运行控制仍使用独立暂停/停止按钮，不把“发消息”隐式解释成停止。

Composer 不锁死，但发送区要清楚区分：

- 只读问题可以立即回答；
- 修改请求显示排队状态和目标；
- 新 Skill 请求显示“当前主链运行中”，提供暂停或停止入口；
- 不用模糊的“当前生成未结束，普通输入暂不可用”覆盖所有场景。

### 15.6 歧义与定位反馈

唯一目标不额外打断用户确认，直接生成修改稿；Agent 回复中的目标摘要可点击定位到中间工作区。存在多个合理候选、显式冲突、历史版本或过期选区时，才显示目标确认卡。

确认卡只确认“改哪里”，不混入“修改成什么”；目标确认完成后继续使用原始用户指令，不要求用户重复输入。

### 15.7 无适配器状态

如果 Target 有效但 Artifact 没有 Revision Adapter：

- Agent 仍可解释、定位和给出修改建议；
- 中间工作区仍允许已有的手动编辑；
- AI 修改入口显示“当前产物暂不支持 Agent 直接修改”；
- 不把它误报为定位失败或 Skill 不可用。

## 16. 新 Skill 接入

新增 Skill 不需要实现选区、Target Resolution、版本确认或 Agent 对话。它只需：

1. 在 Manifest 声明生产的 Artifact Type；
2. 注册 Artifact Schema 和稳定实体键；
3. 声明哪些 Artifact 可修改；
4. 注册 Revision Adapter；
5. 声明上下文需求和受保护字段；
6. 提供输出校验和必要的领域 Rules。

未声明修改支持时默认只读，不能使用通用大模型盲改未知 Schema。

接入门禁不是“能生成 Artifact”就算完成。每个声明可编辑 Artifact 的新 Skill 必须通过：显式选区、自然语言定位、歧义确认、历史版本拒绝、运行中排队、Proposal 审阅、接受后影响计算和刷新恢复。

## 17. 验收矩阵

### 17.1 定位

- 选中故事圣经字段后修改；
- 选中分集规划单项后修改；
- 选中单集剧本台词后修改；
- 仅输入“修改第三集结尾钩子”；
- 仅输入“修改林澈的人设”并出现多个候选；
- 选中第三集但文字要求第五集；
- 选区发送后生成 Proposed Version，不切换 Artifact Current Version；
- 指代“再短一点”命中上一次已确认 Target；
- 跨作品 ID、伪造 Candidate ID 和过期版本全部拒绝。

### 17.2 修改

- 中间产物和剧本都生成 Proposed Version；
- Proposed Version 不改变 Current Version；
- 接受后创建新 Current Version；
- 放弃后旧 Current Version 不变；
- 修改关键上游后生成 Impact Review；
- 修改单集后只标记对应依赖范围；
- 无 Adapter 时只提示不支持，不调用 Provider；
- Provider 输出不合 Schema 时不产生可采用版本。

### 17.3 运行中

- 运行中可保存修改请求；
- Worker 执行期间不热改输入；
- 安全检查点后自动暂停并重新校验 Target；
- Target 过期时要求重新定位；
- 取消 Run 后待处理修改请求仍可由用户选择保留或取消。

## 18. 实施顺序

1. Target Catalog 与稳定节点规范；
2. Target Resolution Runtime、数据表和校验；
3. Main Agent `revise` 意图、候选排序和澄清；
4. Revision Request 与安全检查点队列；
5. Revision Context Pack；
6. 首批共享 Revision Adapters；
7. Proposed Version、Diff、确认和 Impact Review；
8. 前端显式引用、候选卡、修改稿工作区；
9. 三条现有链路回归；
10. 新 Skill 接入样板验收。

### 18.1 数据对象

优先复用现有 Message、Selection Snapshot、Artifact Version、Event 和 Impact Review。新增对象保持最少：

- `target_resolutions`：一次用户请求的解析状态；
- `target_resolution_candidates`：Runtime 生成、Agent 排序的候选；
- `revision_requests`：修订生命周期与安全检查点策略；
- `revision_attempts`：Context Hash、Adapter、Provider 与失败记录；
- Artifact Version 增加 Proposal 生命周期字段，避免再建第二套内容表。

### 18.2 代码边界

```text
shell/targetresolver     候选生成编排、Main Agent 受限排序、指代处理
runtime/revision        状态机、单写者、版本、Proposal、Impact Review
runtime/context         Revision Context Pack 组装与预算
capability/revision     Adapter Registry 与领域 Adapter
httpapi                 Message/Resolution/Revision Commands 和 Queries
frontend                引用、目标卡、时间线、Diff、固定操作栏
```

不得把 Artifact Type 的 if/else 重新堆回 Main Agent、HTTP Handler 或通用 Runtime；领域差异从 Registry 和 Adapter 获取。

## 19. 决策结论

该能力是 Agent Shell 的基础能力，不是第四个 Skill，也不是三个现有 Skill 内部各写一套修改流程。

```text
Shell 负责“用户指的是哪里”
Runtime 负责“这个位置是否有效、何时允许写、版本如何变化”
Revision Adapter 负责“这个领域内容应该怎样改”
Skill 负责声明“我生产的产物如何被修改”
```

这使后续分镜、镜头、视频片段和质量审核等 Skill 可以复用同一套定位与修改基础设施。

## 20. 项目变更控制

这是对已通过阶段 5-9 基线的跨层增强，不属于单纯视觉修补：

- 技术方案：新增 Target Resolution、Revision Request、Proposal 与 Adapter 合同；
- 前端：改造 Composer、Agent 时间线、工作区定位、Diff 和确认；
- 后端：新增状态机、持久化、API、事件、Context Pack 和单写者排队；
- 测试：更新验收矩阵并重跑三条主链、刷新恢复、版本冲突和跨 Skill 复用。

在产品负责人确认本设计前，不修改 12 阶段状态；确认后将它作为阶段 5-9 变更包实施，并重新通过受影响门禁。阶段 10 发布在该变更包完成前保持阻塞，不能沿用旧阶段 9 证据直接发布。
