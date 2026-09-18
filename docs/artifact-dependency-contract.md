# Artifact Dependency Contract

状态：阶段 2 设计稿  
版本：v0.1  
日期：2026-07-24

## 1. 文档目的

本文档定义内容生产 Agent 第一版的 Artifact 版本依赖图和下游影响协议。

它解决：

1. 下游 Artifact Version 精确使用了哪些上游版本；
2. 用户修改一个字段、一集剧本或一段视频解析结果后，哪些内容真正受影响；
3. 用户选择“保留现有后续内容”时，系统如何保持旧版本链路一致；
4. 用户选择“重新生成受影响内容”时，从哪里开始、生成哪些子项；
5. 批量 Artifact、单集剧本和连续性如何局部传播；
6. 视频补传、缺集继续和原文件删除如何影响后续产物；
7. Runtime、Capability 和 Context Assembler 如何使用同一份版本事实。

本合同只处理确定性版本关系。模型可以生成内容，不能自行决定依赖图。

### 1.1 规范性用语

沿用 Runtime Domain Model：

- **必须**：强制要求；
- **禁止**：不得出现；
- **应该**：默认实现；
- **可以**：允许；
- **建议**：当前推荐；
- **示例**：不是完整 Schema。

## 2. 核心原则

### 2.1 依赖绑定 Version，不绑定“最新”

禁止：

```text
script_unit
-> episode_cards latest
```

必须：

```text
script_unit artifact_version_id=SV4
-> episode_cards artifact_version_id=EC2
```

### 2.2 依赖图是有向图

方向：

```text
upstream
-> downstream
```

示例：

```text
episode_cards v2
-> script_unit episode 3 v4
```

### 2.3 依赖图不能跨 Project

第一版所有边必须满足：

```text
upstream.project_id
=
downstream.project_id
```

跨 Project 引用不支持。

### 2.4 依赖图必须无环

Artifact Derivation Graph 必须是 DAG。

版本继承关系 `base_version_id` 与业务依赖边分开，不参与 Derivation DAG 的方向判断。

### 2.5 新版本不改写旧图

创建 Artifact 新 Version 时：

- 旧 Version 依赖边不变；
- 新 Version 创建自己的新依赖边；
- 不能把旧边重新指向新上游；
- 历史 Candidate 始终能还原当时使用的版本链。

### 2.6 没有证据时扩大影响范围

局部重生成必须有确定性 Scope 依据。

如果无法证明只影响某个字段或子项：

```text
impact_scope = whole_artifact
```

禁止让模型凭一句“应该只影响第 3 集”缩小 Runtime 影响范围。

## 3. 图中的节点

依赖图支持三类上游节点：

```text
Artifact Version
Asset Snapshot
Run Config Snapshot
```

### 3.1 Artifact Version Node

```json
{
  "node_kind": "artifact_version",
  "ref_id": "artifact_version_id",
  "project_id": "",
  "artifact_type": "episode_cards",
  "scope_key": "singleton",
  "version": 2
}
```

### 3.2 Asset Snapshot Node

```json
{
  "node_kind": "asset_snapshot",
  "ref_id": "asset_snapshot_id",
  "asset_id": "",
  "checksum": "",
  "episode_no": 3,
  "episode_order": 3
}
```

依赖绑定精确 Run Input Snapshot Version 中的 Asset 状态，而不是只绑定可变 Asset 记录。收集型视频 Run 的每个单集 Task 绑定创建时的 Snapshot Version；整剧聚合绑定 Seal 后的最终 Snapshot Version。

### 3.3 Config Snapshot Node

```json
{
  "node_kind": "config_snapshot",
  "ref_id": "config_snapshot_id",
  "schema_version": "1.0",
  "snapshot_hash": ""
}
```

目标集数、单集时长和扩写策略改变时，可以通过 Config Dependency 计算影响。

## 4. Dependency Edge

### 4.1 结构

```json
{
  "dependency_id": "",
  "project_id": "",
  "run_id": "",
  "downstream_artifact_version_id": "",
  "upstream_kind": "artifact_version",
  "upstream_ref_id": "",
  "relation": "derived_from",
  "upstream_scope": {
    "kind": "artifact",
    "key": "singleton",
    "field_path": null
  },
  "downstream_scope": {
    "kind": "episode",
    "key": "episode:3",
    "field_path": null
  },
  "impact_policy_id": "episode_match",
  "created_at": ""
}
```

### 4.2 Relation

第一版：

| Relation | 用途 | 参与影响传播 |
|---|---|---|
| `derived_from` | 内容由上游生成 | 是 |
| `aggregates` | 聚合子 Artifact | 是 |
| `governed_by` | 受配置、Brief 或确认策略约束 | 是 |
| `references_source` | 来源证据、Asset、时间码 | 视策略 |
| `continuity_from` | 读取上一集连续性状态 | 是 |
| `trace_only` | 仅审计，不改变生成结果 | 否 |

### 4.3 不能使用模糊边

禁止：

```json
{
  "upstream_ref_id": "artifact_id"
}
```

必须保存：

```json
{
  "upstream_ref_id": "artifact_version_id"
}
```

Asset 使用不可变 Snapshot ID 或 Asset ID + Checksum Snapshot。

## 5. Scope

Scope 用于表达 Artifact 内部的稳定业务范围。

### 5.1 Scope Kind

第一版：

```text
artifact
episode
scene
entity
field
asset
time_range
collection_item
continuity
```

### 5.2 Scope 结构

```json
{
  "kind": "episode",
  "key": "episode:3",
  "field_path": "episodes[episode_no=3]"
}
```

### 5.3 稳定选择器

优先：

```text
episodes[episode_no=3]
scenes[scene_id=scene_3_2]
characters[character_id=char_01]
```

禁止只使用数组位置：

```text
episodes[2]
scenes[1]
```

数组顺序变化后，位置选择器会错误定位。

### 5.4 Scope Key

Scope Key 使用稳定格式：

```text
singleton
episode:3
scene:scene_3_2
entity:character:char_01
asset:<asset_id>
continuity:episode:3
```

Scope Key 是业务定位，不替代 Artifact Version ID。

## 6. Change Set

每个新 Artifact Version 必须记录相对 Base Version 的 Change Set。

### 6.1 结构

```json
{
  "change_set_id": "",
  "artifact_version_id": "",
  "base_version_id": "",
  "change_mode": "scoped",
  "changes": [
    {
      "operation": "replace",
      "scope": {
        "kind": "episode",
        "key": "episode:3",
        "field_path": "episodes[episode_no=3]"
      }
    }
  ],
  "derived_signals": {
    "continuity_changed": false,
    "episode_order_changed": false,
    "episode_count_changed": false
  },
  "created_at": ""
}
```

### 6.2 Change Mode

```text
scoped
whole_artifact
unknown
```

`unknown` 在影响计算时等价于 `whole_artifact`。

### 6.3 Change 来源

#### 手动编辑

由前端编辑器和 Runtime 根据稳定 ID 计算差异。

#### AI 局部修改

由结构化 Revision Intent 指定目标，Runtime 应用 Patch 后再次计算实际差异。

模型声明的范围不能替代 Runtime 比对。

#### 重新生成

默认：

```text
whole_artifact
```

如果重新生成明确只针对一个独立 `script_unit` Artifact，则影响范围是该 Artifact 的 `singleton`。

#### 确定性聚合

根据成员变化生成：

```text
collection_item
episode
```

### 6.4 Derived Signals

Runtime 通过确定性比较生成：

- `episode_count_changed`
- `episode_order_changed`
- `episode_no_set_changed`
- `continuity_changed`
- `source_refs_changed`
- `config_ref_changed`
- `brief_constraints_changed`

这些 Signal 用于选择影响策略。

## 7. Scope Overlap

### 7.1 基本规则

```text
whole_artifact
overlaps
all scopes
```

相同稳定 Key：

```text
episode:3
overlaps
episode:3
```

不同集：

```text
episode:3
does not overlap
episode:4
```

父字段与子字段：

```text
characters[character_id=char_01]
overlaps
characters[character_id=char_01].voice
```

### 7.2 不确定选择器

如果旧依赖使用的位置选择器或缺少稳定 ID：

```text
overlap = true
```

以扩大影响范围。

### 7.3 Scope 规范化

保存前：

- Episode Number 转正整数；
- Stable ID 去空格；
- Field Path 使用统一选择器；
- 同一 Scope 去重；
- 父 Scope 已包含子 Scope 时保留父 Scope；
- 无法规范化时降级 `whole_artifact`。

## 8. Impact Policy

Impact Policy 由 Capability/Step Definition 注册，Runtime 执行。

### 8.1 结构

```json
{
  "impact_policy_id": "episode_match",
  "match": "same_episode",
  "propagate_as": "same_episode",
  "regenerate_from_step_id": "build_episode_cards",
  "fallback": "whole_artifact"
}
```

### 8.2 第一版策略

| Policy ID | 用途 |
|---|---|
| `whole_downstream` | 上游任意变化影响整个下游 |
| `same_episode` | 只影响同集子项 |
| `same_entity` | 只影响使用同一稳定实体的下游 |
| `aggregate_member` | 成员变化刷新聚合 |
| `episode_and_following_continuity` | 当前集及连续性影响的后续集 |
| `config_episode_count` | 集数变化影响分集及全部剧本 |
| `asset_exact` | 单个 Asset 只影响对应视频剧本 |
| `source_time_range` | 只影响引用同一时间范围的内容 |
| `trace_only` | 不传播 |

### 8.3 Policy 来源

禁止在 Runtime 中通过 Artifact Type 大量 `switch` 推断。

Policy 应在：

```text
Capability Step Definition
或
Artifact Schema Registry
```

中声明。

## 9. Version Selection Policy

下游 Step 选择输入时必须使用明确策略。

### 9.1 策略

```text
approval_snapshot
lineage_consistent
current_confirmed
explicit_version
```

### 9.2 `approval_snapshot`

使用当前 Step/Approval 已固定版本。

用于：

- Resume；
- Retry；
- Approval 后继续；
- 批量剩余 Task。

### 9.3 `lineage_consistent`

从直接上游 Artifact 的 Dependency Lineage 找到对应祖先版本。

用于用户选择保留下游后继续生成，避免把新故事圣经和旧分集卡任意混合。

### 9.4 `current_confirmed`

只允许：

- 新 Run 的首个业务 Step；
- 用户明确从当前已确认版本创建新分支；
- Capability Definition 明确允许。

不能用于 Resume/Retry。

### 9.5 `explicit_version`

用户或系统明确选择的历史 Version。

必须记录选择原因和 Approval/Command。

### 9.6 禁止 `latest`

API 和 Runtime 不定义无条件：

```text
latest
```

必须明确：

```text
current confirmed version for new branch
或
version pinned by lineage
```

## 10. Active Graph 与 Historical Graph

### 10.1 Historical Graph

包含全部 Version 和 Dependency：

- 历史；
- superseded；
- stale；
- Candidate；
- historical final。

用于审计和版本回溯。

### 10.2 Active Graph

用于影响计算，只包含：

- 当前 Run/Revision Branch；
- 当前有效 Artifact Version；
- 当前 Candidate 所引用 Version；
- 未被 superseded/invalidated 替代的节点。

历史 Candidate 不因当前上游修改被标记受影响。

### 10.3 Candidate Graph

每个 Script Candidate 可以从 `scripts_artifact_version_id` 反向遍历得到完整版本树。

Candidate 创建后，其 Graph 永久固定。

## 11. Impact Preview

新 Artifact Version 创建后，如果旧 Version 有有效下游，Runtime 创建 Impact Preview。

### 11.1 结构

```json
{
  "impact_review_id": "",
  "project_id": "",
  "run_id": "",
  "source_artifact_id": "",
  "old_version_id": "",
  "new_version_id": "",
  "change_set_id": "",
  "status": "pending",
  "affected_items": [],
  "unaffected_summary": {},
  "recommended_regeneration_start": [],
  "snapshot_hash": "",
  "created_at": ""
}
```

### 11.2 Affected Item

```json
{
  "artifact_id": "",
  "artifact_version_id": "",
  "artifact_type": "script_unit",
  "scope_key": "episode:3",
  "impact_path": [
    "episode_cards:v2:episode:3",
    "script_unit:v4:episode:3"
  ],
  "reason_code": "SAME_EPISODE_DEPENDENCY",
  "regenerate_from_step_id": "generate_script_units",
  "regenerate_task_keys": [
    "episode:3"
  ]
}
```

### 11.3 Preview 不立即改变下游

创建 Preview 时：

- 新 Version 为 `pending_approval`；
- 旧下游继续可见；
- 下游不立即 stale；
- 用户看到影响范围；
- 用户选择保留或重生成；
- Preview 与 Approval Subject Snapshot 一起固定。

### 11.4 Preview 失效

以下情况 Preview 标记 `expired`：

- 新 Version 被另一个 Version supersede；
- Subject Version 变化；
- Run 被取消；
- 影响图已经由其他操作改变；
- 用户重新编辑源 Artifact。

## 12. Impact Algorithm

### 12.1 输入

```text
old_version_id
new_version_id
change_set
active_graph_scope
capability impact policies
```

### 12.2 算法

```text
1. 校验 old/new 属于同一 Artifact 和 Project
2. 规范化 Change Set
3. 从 old Version 查找有效直接下游 Edge
4. 检查 Change Scope 与 Edge upstream_scope 是否重叠
5. 根据 Impact Policy 映射 downstream_scope
6. 把受影响 downstream Version 加入结果
7. 继续遍历其有效下游
8. 遇到聚合节点时按成员规则传播
9. 遇到 continuity edge 时按 continuity signal 传播
10. 过滤历史 Candidate 和已失效 Version
11. 按 Step/Task 分组
12. 计算最早可重生成边界
13. 生成稳定排序的 Impact Preview
14. 保存 Snapshot Hash
```

### 12.3 遍历规范

- 使用 BFS 或拓扑遍历；
- 节点去重；
- 同一节点保留全部最短影响路径或至少一条可解释路径；
- 发现循环立即报 `DEPENDENCY_CYCLE_DETECTED`；
- 不允许无限遍历；
- 结果按 Capability Step Order、Task Order、Artifact ID 稳定排序。

### 12.4 最早重生成边界

不是简单选择最早 Artifact Type。

必须根据：

- 受影响 Step；
- 是否存在可复用未受影响输入；
- 批量 Task Scope；
- Capability Definition；
- Approval 状态；
- Shared Workflow Module 边界。

确定一个或多个 regeneration groups。

## 13. 保留现有下游

### 13.1 语义

`keep_downstream` 表示：

```text
接受新上游 Version；
保留已经基于旧上游生成的下游 Version；
不把旧依赖边改到新上游。
```

### 13.2 Runtime 行为

- 新上游 Version 确认为 `confirmed`；
- 旧上游 Version 变为 `superseded`，但仍被旧下游引用；
- 下游 Version 保持原状态；
- Impact Review 记录 `kept`；
- 创建 Dependency Decision；
- 写 Event；
- 不创建假依赖；
- 不自动重生成；
- 不把下游标记为 stale。

### 13.3 Dependency Decision

```json
{
  "dependency_decision_id": "",
  "impact_review_id": "",
  "action": "keep_downstream",
  "old_upstream_version_id": "",
  "new_upstream_version_id": "",
  "preserved_downstream_version_ids": [],
  "resolved_at": ""
}
```

### 13.4 后续继续生成

如果保留下游后还有未生成步骤：

- 以已保留下游的 Lineage 为主要输入；
- 通过 `lineage_consistent` 选择其祖先版本；
- 不把新上游强行混入旧分支；
- 新上游 Version 可以用于未来明确创建的新分支/Revision；
- Context Assembler 不能读取 Project 全局 Current Version 覆盖 Lineage。

### 13.5 用户可见提示

界面必须说明：

- 哪些下游仍基于旧版本；
- 新上游没有应用到哪些内容；
- 以后从新版本重新生成可以得到另一套 Candidate；
- 当前旧 Candidate 不会被删除。

## 14. 重新生成受影响下游

### 14.1 语义

`regenerate_downstream` 表示：

```text
接受新上游 Version；
把实际受影响的当前下游标记待更新；
从最早受影响边界生成替代 Version。
```

### 14.2 Runtime 行为

同一事务：

1. 确认新上游 Version；
2. 校验 Impact Review Snapshot；
3. 受影响 Version 标记 `stale`；
4. 创建 Regeneration Plan；
5. 创建/恢复 Step Run 和 Task Item；
6. 固定新的 Input Version Snapshot；
7. 写 Approval Resolution 和 Event。

### 14.3 Regeneration Plan

```json
{
  "regeneration_plan_id": "",
  "project_id": "",
  "run_id": "",
  "impact_review_id": "",
  "source_new_version_id": "",
  "groups": [
    {
      "step_id": "generate_script_units",
      "task_keys": ["episode:3"],
      "stale_version_ids": [],
      "preserved_version_ids": []
    }
  ],
  "status": "pending"
}
```

### 14.4 替代成功

每个 Task 成功后：

- 创建新 Artifact Version；
- Dependency 指向新上游链；
- 旧 stale Version 变 `superseded`；
- 未受影响 Version 不变；
- 聚合节点刷新；
- 全部必需 Task 成功后进入步骤审批。

### 14.5 替代失败

- 新成功 Task 保留；
- 旧 stale Version 继续可见；
- 失败 Task 可重试；
- 不把聚合视图标记为最终完成；
- 不删除旧 Candidate；
- Run/Step 显示部分失败。

## 15. Partial Regeneration

### 15.1 允许条件

局部重生成必须同时满足：

1. Change Set 有稳定 Scope；
2. Dependency Edge 有匹配 Scope；
3. Impact Policy 支持局部传播；
4. 下游 Artifact 有独立 Task/Scope；
5. 聚合可以确定性刷新；
6. 连续性 Signal 没有要求扩大范围。

任一不满足则扩大范围。

### 15.2 单集 Artifact

`script_unit` 每集是独立 Artifact：

```text
artifact_type=script_unit
scope_key=episode:3
```

修改第 3 集默认只重生成：

```text
episode:3
```

是否向后传播由连续性规则决定。

### 15.3 集合型 Artifact

`episode_cards` 可以继续是一个 Version 化集合，但必须：

- 每个 Item 有稳定且唯一的 `episode_no`；
- Change Set 精确记录变化集；
- Edge 按 Episode Scope 保存；
- Runtime 不因整个 Payload 版本变化就默认全部集受影响。

### 15.4 聚合 Artifact

`scripts`、`reference_scripts` 是确定性聚合：

- 成员变化时刷新聚合 Version；
- 聚合刷新不调用内容模型；
- 聚合 Version 依赖全部成员精确 Version；
- 聚合不成为内容真源；
- 单个成员变化不重写其他成员。

## 16. 剧本连续性传播

### 16.1 连续性依赖

每集 `script_unit N` 可以依赖：

```text
episode_card N
script_context
script_unit N-1 continuity_delta
```

Edge：

```text
script_unit N-1.continuity_delta
-> script_unit N
relation=continuity_from
```

### 16.2 Continuity Delta

每集保存结构化：

```json
{
  "new_facts": [],
  "character_state_changes": [],
  "relationship_changes": [],
  "foreshadowing_opened": [],
  "foreshadowing_resolved": [],
  "hooks_opened": [],
  "hooks_resolved": []
}
```

Runtime 对新旧 Version 的 `continuity_delta` 做规范化 Hash 比较。

### 16.3 不改变连续性

例如：

- 调整对白措辞；
- 修改动作描述；
- 修正格式；
- 不改变事实和人物状态。

如果 `continuity_delta` Hash 不变：

```text
只影响当前 script_unit
和 scripts 聚合
```

不向后传播。

### 16.4 改变连续性

如果 `continuity_delta` 改变：

```text
影响下一集
-> 下一集重新生成后的 continuity_delta 再比较
-> 必要时继续向后
```

第一版采用逐集传播，不预先把所有后续集无条件重生成。

但在无法可靠生成/比较 Delta 时，保守退化为：

```text
当前集及全部后续集
```

### 16.5 分集卡连续性

`episode_cards` 中第 N 集变化：

- 默认影响 `script_unit N`；
- 若改变尾钩、人物状态或跨集承接，设置 `continuity_changed=true`；
- 再按剧本连续性策略向后传播。

## 17. 小说链依赖映射

### 17.1 主图

```text
source_input
-> story_bible
-> episode_split
-> episode_cards
-> script_context
-> script_unit[]
-> scripts
```

### 17.2 建议边

| 上游 | 下游 | Policy |
|---|---|---|
| Source Asset Snapshot | `source_input` | `whole_downstream` |
| `source_input` | `story_bible` | `whole_downstream` |
| `story_bible` | `episode_split` | `whole_downstream` |
| `episode_split episode N` | `episode_cards episode N` | `same_episode` |
| `story_bible` | `episode_cards` | `whole_downstream` 或 Scope-aware entity |
| `episode_cards episode N` | `script_unit N` | `same_episode` |
| `script_context` | 全部 `script_unit` | `whole_downstream` |
| `script_unit N` | `scripts` | `aggregate_member` |
| `script_unit N continuity_delta` | `script_unit N+1` | `episode_and_following_continuity` |

### 17.3 小说原文拆集变化

如果：

- 集数不变；
- 只修改第 N 集来源边界；

可以影响对应 Episode Card 和后续 Script Unit。

如果：

- Episode ID 集合变化；
- 集数变化；
- 大范围重排；

退化为：

```text
episode_split 之后全部重算
```

## 18. 非小说链依赖映射

```text
source_input
-> material_bank
-> story_seed
-> series_blueprint
-> episode_cards
-> script_context
-> script_unit[]
-> scripts
```

建议：

| 上游 | 下游 | Policy |
|---|---|---|
| `source_input` | `material_bank` | `whole_downstream` |
| `material_bank` | `story_seed` | `whole_downstream` |
| `story_seed` | `series_blueprint` | `whole_downstream` |
| `series_blueprint episode/phase` | `episode_cards` | Scope-aware，缺失时 whole |
| `episode_cards episode N` | `script_unit N` | `same_episode` |
| `script_context` | 全部 `script_unit` | `whole_downstream` |
| `script_unit N` | `scripts` | `aggregate_member` |

对核心梗、主角目标或世界观的修改通常影响整部后续，不能仅按字段位置缩小范围。

Impact Policy 可以把这些字段声明为：

```text
semantic_global
-> whole_downstream
```

## 19. 视频参考创作依赖映射

### 19.1 主图

```text
Video Asset Snapshots
-> video_script_unit[]
-> reference_scripts
-> script_analysis
-> adaptation_options
-> adaptation_brief
-> story_seed
-> series_blueprint
-> episode_cards
-> script_unit[]
-> scripts
```

### 19.2 单视频依赖

```text
Asset A episode 3
-> video_script_unit episode 3
```

Policy：

```text
asset_exact
```

### 19.3 聚合

```text
video_script_unit[]
-> reference_scripts
```

单集修改：

- 刷新 `reference_scripts`；
- `script_analysis` 通常受影响；
- `adaptation_brief` 和后续是否重生成由用户选择。

### 19.4 上传完成 Decision

整剧分析还依赖：

```text
upload_complete decision
episode mapping snapshot
incomplete_material decision
```

这些属于 `governed_by` Dependency。

### 19.5 后续补传视频

补传新视频时：

1. 创建新 Asset Snapshot；
2. 创建新 `video_script_unit`；
3. 生成新的 `reference_scripts` Version；
4. 计算 `script_analysis`、`adaptation_brief` 和创作下游影响；
5. 已有单集视频剧本不重做；
6. 用户选择保留或从最早受影响步骤重生成；
7. 不能静默把新集混入旧分析。

### 19.6 人工排序

修改 Episode Mapping：

- Asset 内容没变；
- Mapping Snapshot 创建新版本；
- `reference_scripts` 顺序受影响；
- `script_analysis` 受影响；
- 后续受影响；
- 原 `video_script_unit` 内容通常可复用；
- 只重新聚合和分析，不重新调用视频模型。

### 19.7 视频过期

原视频到期：

- 不改变已有 `video_script_unit` Payload；
- Dependency Source 状态显示 `expired`；
- 已有分析和 Brief 继续可用；
- 不自动 stale；
- 重新解析操作不可用；
- 影响预览标记 `source_reexecution_unavailable`。

## 20. Config Dependency

### 20.1 目标集数变化

影响：

```text
episode_split / series_blueprint
-> episode_cards
-> script_unit[]
-> scripts
```

不能只修改 `scripts.episode_count`。

### 20.2 单集时长变化

通常影响：

- Episode Cards 内容密度；
- Script Context；
- 全部 Script Units。

### 20.3 保留原分集变化

影响小说：

- Episode Split；
- 所有下游。

### 20.4 Config 新版本

Config Snapshot 不原地修改。

用户改变配置：

- 创建新 Config Snapshot；
- 创建新 Run 或 Revision Run；
- Dependency 绑定新 Snapshot；
- 旧 Candidate 保持原配置。

## 21. Adaptation Brief Dependency

`adaptation_brief` 是视频创作下游的强制治理输入：

```text
adaptation_brief confirmed version
-> story_seed
-> all creative downstream
```

Brief 中以下字段变化默认 `whole_downstream`：

- 改编目标；
- 核心保留；
- 替换内容；
- 新增元素；
- 人物关系调整；
- 世界观；
- 目标集数；
- 禁止照搬；
- 强制用户要求。

不能因为只编辑一个字段就默认局部影响。

## 22. Dependency Completeness

Artifact Version 提交前，Runtime 必须校验：

1. Step Definition 的所有必需输入都有 Dependency；
2. Input Version Snapshot 与 Dependency 一致；
3. 所有 Artifact Version 属于同一 Project；
4. 上游状态满足 Step 要求；
5. Asset Snapshot Hash 与 Run Input 一致；
6. Config Snapshot 一致；
7. Scope 格式合法；
8. Impact Policy 已注册；
9. 不产生循环；
10. Aggregate 成员覆盖完整。

缺依赖时 Artifact 不能保存为成功。

## 23. Dependency Graph API

### 23.1 查看 Version Lineage

```http
GET /api/artifact-versions/{artifact_version_id}/lineage
```

返回：

- 直接上游；
- 可选深度的祖先；
- Asset 状态；
- Config Snapshot；
- Capability/Run/Step；
- Dependency Decisions。

### 23.2 创建影响预览

新 Version 保存后由 Runtime 自动创建。

可查询：

```http
GET /api/impact-reviews/{impact_review_id}
```

### 23.3 解决影响

```http
POST /api/impact-reviews/{impact_review_id}/resolve
```

请求：

```json
{
  "action": "keep_downstream | regenerate_downstream",
  "idempotency_key": ""
}
```

### 23.4 前端公开内容

公开：

- 受影响 Artifact 名称；
- 集数；
- 当前版本；
- 原因；
- 保留和重生成的结果；
- 是否存在无法重新执行的过期来源。

不需要向普通用户展示完整图数据库结构。

## 24. 数据表

建议：

```text
artifact_dependencies
artifact_version_change_sets
artifact_version_change_items
impact_reviews
impact_review_items
dependency_decisions
regeneration_plans
regeneration_plan_groups
```

### 24.1 索引

```text
artifact_dependencies(upstream_ref_id)
artifact_dependencies(downstream_artifact_version_id)
artifact_dependencies(project_id, relation)
impact_reviews(project_id, status)
impact_review_items(impact_review_id)
regeneration_plans(run_id, status)
```

### 24.2 唯一约束

同一 Version 的同一规范化 Edge 不重复：

```text
UNIQUE(
  downstream_artifact_version_id,
  upstream_kind,
  upstream_ref_id,
  relation,
  upstream_scope_hash,
  downstream_scope_hash
)
```

## 25. 事务

### 25.1 创建 Version 和 Edge

同一事务：

1. 创建 Artifact Version；
2. 创建 Change Set；
3. 创建 Dependency Edges；
4. 校验 Completeness；
5. 更新 Artifact Current Pointer；
6. 创建 Impact Preview；
7. 创建 Approval；
8. 写 Event。

### 25.2 Resolve Keep

同一事务：

1. 校验 Review Snapshot；
2. 确认新 Version；
3. 保存 Dependency Decision；
4. 更新 Review；
5. 解决 Approval；
6. 写 Event。

### 25.3 Resolve Regenerate

同一事务：

1. 校验 Review Snapshot；
2. 确认新 Version；
3. 标记受影响 Version stale；
4. 创建 Regeneration Plan；
5. 创建 Step/Task；
6. 解决 Approval；
7. 写 Event。

## 26. 错误码

```text
DEPENDENCY_PROJECT_MISMATCH
DEPENDENCY_VERSION_NOT_FOUND
DEPENDENCY_CYCLE_DETECTED
DEPENDENCY_SCOPE_INVALID
DEPENDENCY_POLICY_NOT_FOUND
DEPENDENCY_INCOMPLETE
CHANGE_SET_INVALID
IMPACT_REVIEW_EXPIRED
IMPACT_REVIEW_CONFLICT
IMPACT_SCOPE_UNCERTAIN
REGENERATION_PLAN_CONFLICT
LINEAGE_VERSION_NOT_FOUND
SOURCE_REEXECUTION_UNAVAILABLE
AGGREGATE_MEMBERSHIP_INCOMPLETE
```

## 27. 可观测性

Event 至少记录：

```text
dependency_graph_created
impact_review_created
impact_review_expired
downstream_kept
downstream_regeneration_planned
artifact_marked_stale
artifact_replacement_created
aggregate_refreshed
continuity_propagation_started
continuity_propagation_stopped
```

Trace 需要能回答：

- 为什么第 3 集被重生成；
- 为什么第 4 集没有被重生成；
- 当前剧本使用哪个分集卡版本；
- 为什么视频过期后仍能查看分析；
- 哪次用户决定保留旧下游。

## 28. 不采用的方案

### 28.1 Artifact Type 级粗粒度依赖

不采用。无法支持单集和局部重生成。

### 28.2 自动把旧依赖改到新 Version

不采用。会伪造历史。

### 28.3 新上游一保存就删除下游

不采用。用户尚未选择处理方式。

### 28.4 永远重生成全部后续

不采用。成本高且会丢失用户已确认的无关内容。

### 28.5 永远只重生成当前集

不采用。连续性和全局配置变化可能影响后续。

### 28.6 让模型输出受影响 Artifact 列表

不作为状态依据。模型可以提供内容层建议，但 Runtime 使用确定性图。

### 28.7 使用“最新版本”组装上下文

不采用。Resume、Retry 和保留下游会发生版本串用。

## 29. 测试要求

### 29.1 图完整性

- Version Edge 精确；
- 无跨 Project；
- 无循环；
- 必需依赖齐全；
- Aggregate 成员完整；
- 历史 Version Edge 不变。

### 29.2 Scope

- 稳定 Episode Selector；
- Entity Selector；
- 父子 Field Overlap；
- 不同 Episode 不重叠；
- 非法 Scope 降级 whole；
- 数组重排后仍正确定位。

### 29.3 保留下游

- 新上游确认；
- 旧下游不 stale；
- 旧 Edge 不变；
- Decision 保存；
- 后续使用 lineage-consistent；
- 不混入 Project Current Version。

### 29.4 重生成

- 只标记实际影响 Version；
- 未受影响集数不变；
- 失败 Task 可重试；
- 替代成功后旧 stale -> superseded；
- Aggregate 精确刷新；
- 重复 Resolve 幂等。

### 29.5 连续性

- 当前集对白修改、Delta 不变，不传播；
- 人物状态变化、Delta 改变，传播下一集；
- 下一集新 Delta 相同后停止；
- Delta 无法比较时扩大到后续全部；
- 聚合始终刷新。

### 29.6 小说链

- 修改拆集第 3 集只影响对应范围；
- 修改核心人物设定影响全部相关后续；
- 目标集数变化全量重算；
- 无关 Script Unit Version 不变。

### 29.7 非小说链

- 修改 Story Seed 核心梗影响全部后续；
- 修改单集 Episode Card 只影响相应集和必要连续性；
- Material Bank 局部事实按声明 Policy 传播；
- Context 使用正确 Lineage。

### 29.8 视频链

- 单视频重解析只替换对应 `video_script_unit`；
- 聚合刷新；
- 分析受影响；
- Brief 和后续等待用户选择；
- 补传不重做旧视频；
- 人工排序不重调视频模型；
- 视频过期不删除 Artifact；
- 过期来源不能重新执行。

### 29.9 Candidate

- 历史 Candidate 不被当前影响计算标记；
- Current Candidate 图正确；
- Final Candidate 图固定；
- Revision 形成新图；
- 改选 Final 不重写 Dependency。

## 30. 实施顺序

1. 建 Artifact Version Dependency 表；
2. 保存 Step Input Version Snapshot；
3. 为现有两个 Skills 建粗粒度精确 Version Edge；
4. 加 Change Set；
5. 加 Scope 和 Impact Policy Registry；
6. 实现 Impact Preview；
7. 实现 Keep Decision；
8. 实现 Regeneration Plan；
9. 实现 Batch Scope；
10. 实现 Script Continuity Delta 传播；
11. 实现 Aggregate Refresh；
12. 接视频 Asset 和补传影响；
13. 执行真实链路回归。

实施时先保证版本正确，再逐步缩小影响范围。不能先做局部优化再补依赖真源。

## 31. 完成门禁

Artifact Dependency Contract 实现完成必须满足：

- 所有正式 Artifact Version 有完整精确依赖；
- 依赖绑定 Version，不绑定模糊最新；
- 图不跨 Project 且无环；
- 新 Version 不改写旧图；
- Change Set 有稳定 Scope 或保守降级；
- Impact Preview 在改变下游前生成；
- Keep 不伪造新依赖；
- Regenerate 只处理实际影响范围；
- Lineage-consistent Context 不混用版本；
- 单集修改支持局部影响；
- 连续性变化可向后传播并在稳定后停止；
- 聚合视图确定性刷新；
- 视频补传、排序和过期语义明确；
- 历史 Candidate 和 Final 不受当前修改污染；
- 所有影响决策可审计、可解释、可测试。

## 32. Script Handoff 与 QualityReview 影响规则

权威合同：

```text
content-semantics-and-quality-review-contract.md
```

11A 新增关系：

```text
script_handoff --describes--> script_unit
QualityReview --reads snapshot--> script_unit[]
QualityReview --reads snapshot--> script_handoff[]
QualityReview --reads snapshot--> script_context[]
```

`script_handoff` 是 Artifact Version Dependency 的一部分；QualityReview 通过独立 Review Input Snapshot 保存精确读取版本，不伪装成 Artifact Dependency 节点。

影响规则：

1. `script_unit` 新版本使匹配 Handoff `stale`、旧 Review `superseded`；
2. `script_handoff` 重新生成不改写 `script_unit`；
3. 用户统一确认新的 Script/Handoff 集合后自动创建新 Review；
4. 修改单集固定重审该集、前一集、后一集和全局连续性汇总；
5. 上游事实或分集变化按现有 Impact Preview 扩大范围；
6. Review 返工前必须先展示 Impact Preview；
7. QualityOverride 只适用于固定 Review Input Snapshot，输入变化后自动失效。
