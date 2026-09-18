# 内容语义与剧本质量审核合同

状态：阶段 3 第 11 步 11A 已实施；11E/11F 执行闭环待实施  
版本：1.0.0  
日期：2026-07-29

## 1. 目的与权威边界

本文档固定三条剧本生产 Capability 共用的：

1. 来源单元与事实状态；
2. 用户批准的内容变更；
3. 正式剧本与内部交接分层；
4. 剧本质量审核的触发、状态、返工和重审；
5. Runtime、Context、API、Event 和前端投影；
6. 第 11 步 11A 必须实现的 Manifest 和 Schema 扩展。

本文档是以下合同的 P0 补充：

- `capability-registry-contract.md`
- `capability-workflow-schema-contract.md`
- `runtime-domain-model.md`
- `artifact-dependency-contract.md`
- `capability-context-pack-contract.md`
- `api-event-contract.md`
- `frontend-capability-ui-contract.md`

冲突处理顺序：

```text
本文档的内容语义与质量审核专用条款
-> 上述通用合同
-> 当前设计 Manifest / Schema
```

当前 `capabilities/v1/*.json`、`schemas/v1/*.schema.json`、Compiler、QualityReview Runtime/API 和公开步骤投影已完成 11A 升级。`script_handoff` 原子提交、审核 Worker 子图、自动聚合/Candidate 和返工复审仍分别在 11E/11F 实施；11A 完成不表示质量审核模型已经运行。

## 2. 不变量

1. 第一版用户可见 Skill 仍只有三个：
   - `novel_to_script`
   - `non_novel_to_script`
   - `video_reference_creation`
2. 来源分析、`script_context` 和质量审核是 Capability 内部步骤，不新增 Skill。
3. Runtime 是 Run、Artifact、Approval、Dependency、QualityReview 和 Event 的状态权威。
4. 模型不能确认事实变更、解决 Approval、改变 Run 状态或创建 Candidate。
5. 模型输出的路由只是诊断建议，Guard 必须校验并映射真实 Step。
6. 用户可见内容发生变化必须创建新的 Artifact Version。
7. 审核不能自动修改剧本。
8. 没有适用 Policy Pack 时不能输出确定合规结论。

## 3. 来源清单

### 3.1 Source Manifest

文本和文档 Run 在内容模型执行前必须建立不可变 `source_manifest`：

```json
{
  "manifest_id": "",
  "manifest_version": 1,
  "project_id": "",
  "run_input_snapshot_version_id": "",
  "source_kind": "novel | non_novel",
  "units": [],
  "covered_source_unit_ids": [],
  "missing_or_unread_scope": [],
  "truncation_risk": false,
  "created_at": ""
}
```

`source_manifest` 是内部 Artifact，不创建额外用户审批。它依赖 Sealed Run Input Snapshot 和精确 Asset Snapshot。

### 3.2 Source Unit

```json
{
  "source_unit_id": "SRC-C001-B001",
  "asset_id": "",
  "asset_snapshot_id": "",
  "unit_kind": "chapter_block | story_unit | document_section",
  "parent_label": "第1章",
  "order": 1,
  "summary": "",
  "content_hash": "",
  "characters": [],
  "locations": [],
  "props": []
}
```

稳定 ID 规则：

- 有章节时：`SRC-C{chapter}-B{block}`；
- 无章节时：`SRC-U{unit}-B{block}`；
- ID 创建后不因展示文案、字符偏移或摘要变化重编号；
- Source Unit 新版本通过新的 Manifest Version 表达；
- 字符偏移只能作为解析元数据，不能作为长期引用主键；
- 视频继续使用 `asset_snapshot_id + video_time_range`，不转换为文本 Source Unit。

## 4. 内容事实协议

### 4.1 Content Claim

```json
{
  "claim_id": "FACT-CHAR-001",
  "category": "character | relationship | event | world_rule | information | prop | other",
  "statement": "",
  "status": "FACT",
  "source_refs": [],
  "confidence": "high | medium | low | unknown",
  "locked": true,
  "approval_ref": null,
  "notes": null
}
```

`status` 固定为：

```text
FACT
CONFIRMED_CHANGE
INFERENCE
PROPOSAL
UNKNOWN
```

约束：

- `FACT` 必须有可定位 `source_refs`；
- `CONFIRMED_CHANGE` 必须有有效 `approval_ref`；
- 只有 `FACT/CONFIRMED_CHANGE` 可以 `locked: true`；
- `INFERENCE/PROPOSAL/UNKNOWN` 不能进入 `must_follow_facts`；
- 非小说材料中的用户明示设定属于 `FACT`；
- 模型为补足非小说材料新增的内容先记为 `PROPOSAL`；
- 视频参考创作只能迁移经 Brief 确认的抽象方法，不能把参考剧具体表达记为新故事 `FACT`。

### 4.2 Source Trace 兼容

当前：

```text
source_trace.grounded
source_trace.inferred
```

11A 迁移：

```text
grounded -> FACT
inferred -> INFERENCE
```

旧数据中无法证明用户批准的新增内容不得自动映射为 `CONFIRMED_CHANGE`，统一进入待确认项。

## 5. 内容变更授权

### 5.1 Change Request

修改已锁定事实前必须创建：

```json
{
  "change_request_id": "",
  "project_id": "",
  "run_id": "",
  "requested_by": "user | model",
  "original_claim_ids": [],
  "proposed_change": "",
  "reason": "",
  "affected_artifact_version_ids": [],
  "affected_episode_nos": [],
  "status": "pending | approved | rejected",
  "approval_request_id": "",
  "decision_snapshot_id": null
}
```

### 5.2 状态转换

```text
pending
-> approved: 创建不可变 Decision Snapshot
-> rejected: 保留原事实
```

批准后：

1. 创建新的上游 Artifact Version；
2. 新条目状态为 `CONFIRMED_CHANGE`；
3. 绑定 Approval 和 Decision Snapshot；
4. 生成 Impact Preview；
5. 用户确认影响处理方式后再重生成下游。

模型不能直接完成以上任一步骤。

## 6. 正式剧本与交接

### 6.1 Script Unit

`script_unit` 只保存用户可阅读、编辑和导出的正式剧本：

- 集号和集名；
- 场次；
- 人物；
- 可拍动作；
- 对白、VO、OS、电话音和必要屏显；
- 正式正文渲染；
- 正文内必要来源引用。

`script_unit` 不保存：

- 自检；
- 审核评分；
- 风险清单；
- 制作说明表；
- 下一步路由；
- Runtime 状态。

### 6.2 Script Handoff

`script_handoff` 保存：

```json
{
  "episode_no": 1,
  "script_artifact_version_id": "",
  "source_refs": [],
  "continuity_delta": {},
  "runtime_check": {},
  "critical_presentation_constraints": [],
  "review_focus": [],
  "next_episode_must_address": null,
  "self_check": {}
}
```

### 6.3 原子生成与统一审批

一次 Worker 响应产生一个结构化生成结果。Runtime 在同一事务中创建：

```text
script_unit: pending_approval
script_handoff: pending_approval
```

两个 Version：

- 共享 Attempt ID；
- 共享输入版本快照；
- 建立 `script_handoff describes script_unit` 关系；
- 进入同一个 Batch Approval；
- 用户只执行一次“确认整个剧本步骤”；
- 两类 Version 同时变为 `confirmed`。

任一 Schema、Dependency、写入或审批校验失败时整组回滚。

### 6.4 手动编辑

```text
保存 script_unit 新版本
-> 对应 script_handoff 变 stale
-> 用户点击“完成剧本编辑”
-> 重新生成受影响集的 script_handoff
-> 创建新的整个剧本 Batch Approval
-> 用户统一确认
```

存在缺失、未确认或 `stale` 的 `script_handoff` 时不能开始质量审核。

## 7. 质量审核定位

质量审核：

- 固定 Step ID：`review_script_set`；
- 是三个 Capability 共享的内部 Workflow；
- 不出现在 Skill 菜单；
- 不创建第四个 Domain Capability；
- 不等同于视频 Skill 的 `script_analysis`；
- 不替代用户对剧本步骤的确认；
- 不替代 Final Selection。

## 8. 固定触发流程

```text
全部目标 script_unit/script_handoff 已生成
-> 用户完成编辑
-> 用户统一确认整个剧本步骤
-> Runtime 固定 Review Input Snapshot
-> 自动进入 review_script_set
```

系统不询问“是否审核”。

审核期间：

- Run 状态为 `running`；
- 当前 Step 为 `review_script_set`；
- 输入框按运行中规则禁聊；
- 剧本可读但只读；
- 用户可以离开作品；
- 重启后从持久化 Task Cursor 恢复。

## 9. Review Input Snapshot

Snapshot 必须固定：

- 全部目标 `script_unit` Version；
- 对应 `script_handoff` Version；
- 对应 `script_context` Version；
- 当前链路的故事事实与分集 Artifact Version；
- Config Snapshot；
- Decision Snapshot；
- Capability ID 和 Version；
- Prompt/Rule/Schema 版本与哈希。

任何输入版本变化都使当前 Review `superseded`。旧结果保留，但不能控制新版本流程。

## 10. Review Workflow

```text
review_episode_batches
-> review_global_continuity
-> evaluate_review_gate
```

### 10.1 Episode Batches

- 按自然集序分批；
- 第一版默认每批最多 5 集；
- 批大小来自部署配置，不写死在业务代码；
- 批间可并行；
- 成功批次持久化；
- 失败批次自动重试一次；
- 用户重试只执行失败/未完成批次。

### 10.2 Global Continuity

读取单集审核摘要和最小全局上下文，检查：

- 上下集承接；
- 人物位置、伤势、目标和情绪；
- 道具状态；
- 人物和观众知情范围；
- 伏笔开关；
- 重复场面与重复信息；
- 全剧钩子和结局方向。

### 10.3 Deterministic Gate

Guard 校验：

1. 输出符合 `qualityReviewResult` Schema；
2. 每个问题有集数/场次/证据/影响；
3. 严重级别合法；
4. `recommended_route` 只有一个；
5. 路由存在于当前 Capability；
6. 输入哈希未变化；
7. Run 未取消或替换。

Gate 不调用模型。

## 11. Manifest 目标合同

11A 为 Manifest Compiler 增加：

```json
{
  "id": "review_script_set",
  "kind": "review",
  "executor_ref": "workflow.shared_script_quality_review",
  "result_schema_ref": "../../schemas/v1/quality-review.schema.json#/$defs/result",
  "gate_policy_ref": "quality.script.v1",
  "output_refs": [],
  "approval": {
    "type": "conditional_review",
    "required_when": "action_required",
    "scope": "quality_review"
  }
}
```

新增字段：

```text
StepKind: review
StepDefinition.result_schema_ref
StepDefinition.gate_policy_ref
ApprovalType: conditional_review
ApprovalScope: quality_review
```

`review` Step 结果进入 Task Result Checkpoint 和 QualityReview，不直接创建 Artifact Version。

## 12. QualityReview Runtime

```json
{
  "quality_review_id": "",
  "project_id": "",
  "run_id": "",
  "step_run_id": "",
  "input_snapshot_hash": "",
  "status": "pending",
  "scope": "full_script",
  "issue_counts": {
    "blocker": 0,
    "high": 0,
    "medium": 0,
    "low": 0
  },
  "recommended_route": null,
  "affected_episode_nos": [],
  "result": {},
  "created_at": "",
  "finished_at": null
}
```

状态：

```text
pending
running
passed
action_required
superseded
failed
cancelled
```

QualityReview 是 Runtime 记录，不使用 Artifact Version 的 `confirmed` 语义。

数据库约束：

```text
UNIQUE(run_id, input_snapshot_hash)
```

相同输入的幂等重试返回同一 Review；新输入创建新 Review 并使旧 Review `superseded`。

## 13. Gate 结果

严重级别：

```text
blocker | high | medium | low
```

规则：

- `blocker/high/medium` 都为 0：`passed`；
- 存在任一 `blocker/high/medium`：`action_required`；
- `low` 不打断流程；
- 分数和 A/B/C/D 只作为诊断，不参与 Runtime 状态转换。

### 13.1 Passed

```text
QualityReview passed
-> aggregate_scripts
-> 创建 Candidate
-> Run completed
```

不创建额外用户审批，不自动设置 Final。

### 13.2 Action Required

Runtime 创建 `scope: quality_review` Approval，Subject 固定：

```text
quality_review_id
input_snapshot_hash
review_version
```

Run 进入 `waiting_approval`。

## 14. 用户操作

| 操作 | blocker | high/medium |
|---|---:|---:|
| AI 返修 | 允许 | 允许 |
| 手动修改 | 允许 | 允许 |
| 补充或确认故事变更 | 根因涉及用户决策时允许 | 根因涉及用户决策时允许 |
| 保留当前版本继续 | 不允许 | 允许，必须二次确认 |
| 暂停，以后继续 | 允许 | 允许 |

风险覆盖创建不可变 `QualityOverride Decision Snapshot`：

```json
{
  "quality_override_id": "",
  "quality_review_id": "",
  "input_snapshot_hash": "",
  "ignored_issue_ids": [],
  "actor_ref": "",
  "confirmed_at": ""
}
```

存在有效 Override 时允许聚合和 Candidate，但 Review 状态仍是 `action_required`。

## 15. 唯一返工路由

模型只输出语义路由：

```text
source_analysis
story_bible
episode_plan
script_generation
user
```

Guard 按 Capability 和 Dependency 映射真实 Step：

| 语义路由 | 小说链 | 非小说/视频后续链 |
|---|---|---|
| `source_analysis` | `source_analysis` | `build_material_bank` |
| `story_bible` | `build_story_bible` | 依赖图中 `build_story_seed/build_series_blueprint` 的最上游受影响步骤 |
| `episode_plan` | 依赖图中 `split_episodes/build_episode_cards` 的最上游受影响步骤 | `build_episode_cards` |
| `script_generation` | `generate_script_units` | `generate_script_units` |
| `user` | 用户决策 Approval | 用户决策 Approval |

有多个问题时，Guard 从受影响依赖路径中选择最上游 Step；同层级选择覆盖问题数最多者；仍并列时按 Manifest Step 顺序选择第一个。路由结果因此确定且唯一。

进入返工前必须先生成 Impact Preview。用户确认后才执行失效和重生成。

## 16. 返工与自动重审

- AI 返修：按确认范围重生成；
- 手动修改：保存新 Artifact Version；
- 上游返工：重生成全部受影响下游；
- 所有新剧本版本重新进入整个剧本 Batch Approval；
- 用户统一确认后自动创建新 Review；
- 旧 Review 保留并标记 `superseded`；
- 不允许审核自动调用生成模型形成无人确认循环。

Candidate/Final 后修改使用 Revision Run。

局部修改的重审范围：

```text
修改集
+ 前一集
+ 后一集
+ 一次全局连续性汇总
```

上游变化或 Dependency Impact 扩大时，以 Impact Preview 的完整受影响集为准。

## 17. API

```text
GET  /api/v1/runs/{run_id}/quality-reviews/current
GET  /api/v1/quality-reviews/{quality_review_id}
POST /api/v1/quality-reviews/{quality_review_id}/actions
```

Action Request：

```json
{
  "idempotency_key": "",
  "expected_review_status": "action_required",
  "input_snapshot_hash": "",
  "action": "ai_revise | manual_edit | confirm_change | accept_with_risk | pause",
  "actor_ref": ""
}
```

冲突返回：

```text
QUALITY_REVIEW_STATUS_CONFLICT
QUALITY_REVIEW_INPUT_CHANGED
QUALITY_REVIEW_ACTION_NOT_ALLOWED
QUALITY_REVIEW_NOT_FOUND
```

## 18. Events

```text
quality_review.started
quality_review.batch_completed
quality_review.passed
quality_review.action_required
quality_review.override_confirmed
quality_review.superseded
quality_review.failed
```

Event 只承担审计和 SSE，不替代 QualityReview 当前状态。

## 19. 前端

左侧流程目录固定增加“剧本检查”：

```text
待开始
检查中
需处理
已通过
检查失败
```

### 19.1 检查中

- 显示当前集批次和整体进度；
- 剧本可读但只读；
- 输入框禁聊；
- 可以离开作品；
- 刷新后从 Snapshot 恢复。

### 19.2 已通过

- 显示“剧本检查通过”；
- `low` 问题放在可展开“可选优化”；
- 自动进入 Candidate 结果视图；
- 不要求用户再次确认。

### 19.3 需处理

- 问题按严重级别、集数、场次分组；
- 每项显示证据、影响、必须保留和修改目标；
- 整页只显示一个总体返工目标；
- 操作按钮按第 14 节矩阵渲染；
- 不展示外部 Agent 编号；
- 不用分数代替问题证据。

### 19.4 检查失败

- 明确已生成剧本仍然保留；
- 显示失败批次；
- 提供重试；
- 不创建 Candidate；
- 不要求重新上传材料。

## 20. Context Pack

质量审核使用专用 `review_execution` Context Pack：

```json
{
  "pack_type": "review_execution",
  "quality_review_id": "",
  "input_snapshot_hash": "",
  "episode_scope": [],
  "script_units": [],
  "script_handoffs": [],
  "script_contexts": [],
  "upstream_facts": [],
  "config_snapshot": {},
  "decision_snapshots": [],
  "prompt": {},
  "rules": [],
  "result_contract": {}
}
```

禁止一次向单个 Worker 塞入全部长篇原文。单集批次读取精确集范围；全局审核读取单集摘要和最小全局规则。

## 21. 合规

第一版：

- `compliance_mode` 固定允许 `skip`；
- 没有正式 Policy Pack 时不执行合规模型审核；
- 需要合规判断但缺少 Policy Pack 时状态只能是 `NEEDS_POLICY`；
- UI 显示“未配置适用政策，未执行合规检查”；
- 不阻止质量审核通过和 Candidate 创建；
- 不输出“已合规”。

未来启用时，Compliance Review 必须绑定：

- 被审剧本版本；
- 目标平台和地区；
- `policy_pack_version`；
- QualityReview 或 QualityOverride。

## 22. 第 11 步 11A 交付

11A 必须修改：

1. `capabilities/v1/manifest.schema.json`
2. 三个 Capability Manifest
3. `schemas/v1/common.schema.json`
4. 三个 Capability Schema
5. 新增 `schemas/v1/quality-review.schema.json`
6. Capability Compiler 类型与校验
7. Runtime QualityReview 和 Approval Scope
8. Context Pack
9. API/Event
10. 前端 Registry Projection

当前 `novel_to_script@1.4.0`、`non_novel_to_script@1.3.0` 和 `video_reference_creation@1.2.0` 均已包含 `review_script_set`。在 11E/11F 完成前，不得把“正式剧本与交接原子提交”或“质量审核自动执行闭环”标记为运行通过。

## 23. 完成门禁

- 五态 Content Claim 有唯一字段和状态规则；
- `CONFIRMED_CHANGE` 必须绑定 Approval；
- Source Unit ID 不依赖字符位置；
- `script_unit/script_handoff` 分层且统一审批；
- 剧本统一确认后自动审核，不询问用户是否审核；
- Review 使用精确 Input Snapshot；
- 模型结果不能直接控制 Runtime；
- `passed` 自动进入聚合和 Candidate；
- `action_required` 只显示一个总体返工目标；
- blocker 不能风险覆盖；
- high/medium 风险覆盖有不可变 Decision Snapshot；
- 返工后自动重审且保留旧 Review；
- API、Event 和 UI 状态可确定性验收；
- 无 Policy Pack 不输出合规结论。
