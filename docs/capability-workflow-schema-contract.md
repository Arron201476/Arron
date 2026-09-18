# 三个 Capability 工作流与 Schema 合同

状态：阶段 2 设计稿  
版本：1.0.0  
适用范围：`novel_to_script`、`non_novel_to_script`、`video_reference_creation`

## 1. 目的

本文档固定第一版三个用户级 Skill 的：

- 启动输入；
- 生成配置；
- 执行步骤；
- Prompt 与 Rules 引用边界；
- 主要 Artifact Payload；
- 审批点；
- 批量、重试和聚合规则；
- 旧 Novel2Script 字段到新合同的迁移口径。

本文档不改变已确认的总架构：

```text
OpenAI Agents SDK Main Agent
-> optional Domain Capability
-> Capability Runner
-> Business Runtime
-> deterministic Worker
-> Provider / Tool
```

没有挂载任何 Domain Capability 时，SDK Agent 仍可完成通用对话、文件接收、图片理解、意图判断、上下文维护和能力缺口说明。

## 2. Canonical 约定

### 2.1 用户 Skill 与内部 Workflow

第一版只有三个用户可见 Skill：

| Capability ID | 显示名 |
|---|---|
| `novel_to_script` | 小说转剧本 |
| `non_novel_to_script` | 非小说文本转剧本 |
| `video_reference_creation` | 视频参考创作 |

`script_analysis`、`adaptation_brief`、`script_generation` 都是 Capability 内部步骤或共享 Workflow，不新增用户 Skill。

### 2.2 Prompt Response 与 Artifact Payload 分离

模型可返回执行包装：

```json
{
  "artifact": {},
  "source_trace": {},
  "self_check": {},
  "next_action": "approve"
}
```

Runtime 保存正式 Artifact 时必须规范化：

- 先执行 Step Manifest 声明的 `response_adapter_ref`；
- 只把 Schema 声明的业务字段写入 `payload`；
- `source_trace` 和 `self_check` 若属于产物事实，合并到 Payload 的固定字段；
- `next_action` 属于 Runner 决策，不写入 Artifact Payload；
- 模型返回的 Artifact Type、状态、版本号和审批结果不可信，由 Runtime 生成。

因此，现有 Prompt 顶层的 `story_bible`、`source_trace` 和 `next_action` 只是模型响应包装，不是三个并列 Artifact。

旧 Prompt 必须通过显式、版本化 Adapter 进入新 Schema。Adapter ID 是 Step 合同的一部分；没有 Adapter 的 Step 不允许依赖隐式字段重命名或静默丢字段。

### 2.3 统一命名

新合同统一使用：

- `episode_no`：正整数业务集号；
- `episode_order`：展示顺序；
- `asset_id`：通用上传资产；
- `asset_snapshot_id`：不可变资产快照；
- `artifact_version_id`：不可变产物版本；
- `source_refs`：可追溯来源；
- `source_kind`：仅在共享 Workflow 需要区分输入语义时使用。

旧字段迁移：

| 旧字段 | 新字段/处理 |
|---|---|
| `episode_id` 数字 | `episode_no` |
| `file_id` | `asset_id` |
| `source_mode` | 不再作为全局路由字段；共享 `script_context` 使用 `source_kind` |
| `episode_cards` 数组 | `episodes` |
| `scripts.script_units` 完整副本 | `scripts.unit_refs` 精确引用 |
| Payload 中的 `next_action` | Runner Transition |

Artifact 身份由 Runtime 的 `artifact_id + artifact_version_id + scope_key` 表达，不能再让 Payload 内的业务集号承担数据库身份。

### 2.4 严格 Payload

第一版正式 Artifact 顶层、身份字段、依赖字段、审批相关字段和可编辑 Script 结构默认：

```json
{
  "additionalProperties": false
}
```

少数内容密集型分析表和旧 Prompt 兼容对象允许 Schema 明确声明开放 Map；这类字段不参与路由、状态、依赖和审批判断。不能把未声明的开放对象当作默认规则。

模型在严格对象中产生未知字段时：

1. Schema 校验失败；
2. Worker 可以执行一次结构修复；
3. 修复仍失败则 Task 失败；
4. 不把未知字段静默写进数据库。

### 2.5 主要产物与内部产物

| 类型 | 用户可编辑 | 需要确认 | 说明 |
|---|---:|---:|---|
| `source_input` | 否 | 启动时确认 | Run Input Snapshot 的业务投影 |
| `story_bible` | 是 | 是 | 小说理解 |
| `episode_split` | 是 | 是 | 小说拆集 |
| `material_bank` | 是 | 是 | 非小说事实与素材整理 |
| `story_seed` | 是 | 是 | 新故事种子 |
| `series_blueprint` | 是 | 是 | 整剧结构 |
| `episode_cards` | 子项级 | 是，整步一次 | 分集卡 |
| `script_context` | 否 | 否 | 单集 Worker 最小上下文 |
| `script_unit` | 是 | 是，整步一次 | 新剧本单集真源 |
| `scripts` | 否 | 否 | 已确认单集的只读聚合 |
| `video_script_unit` | 是 | 是，整步一次 | 单视频高还原剧本 |
| `reference_scripts` | 否 | 否 | 参考剧本只读聚合 |
| `script_analysis` | 是 | 是 | 整剧分析 |
| `adaptation_options` | 选择/组合 | 是，转换确认 | 2–3 套改编方案 |
| `adaptation_brief` | 是 | 是 | 后续创作强制依据 |

`video_source` 不是 Artifact。它由 Video Asset Set、Asset Set Version 和 Run Input Snapshot 表达。

## 3. 通用来源与剧本结构

### 3.1 Source Reference

`source_refs` 允许以下来源：

- 文本 Asset 的字符或段落范围；
- 视频 Asset 的秒级时间范围；
- 上游 Artifact Version 的 JSON Pointer；
- 用户确认消息。

每条引用必须明确 `source_type`，并携带该类型所需的稳定 ID。禁止只写“来自原文”“见上一集”等不可定位描述。

### 3.2 Script Document

`script_unit.scenes[].blocks[]` 是结构化编辑真源：

- `scene_id` 在该单集版本内唯一；
- `line_id` 在该单集版本内唯一；
- `block_type` 只允许 `action`、`dialogue`、`transition`、`scene_note`；
- 对白块必须有 `speaker`；
- 动作行文本以 `△` 开头；
- 闪回边界使用 `transition`；
- `script_text` 是结构化内容的可读渲染镜像。

用户编辑通过结构化块保存。服务端必须校验 `script_text` 与结构化内容一致，或由服务端从 Scenes 重新渲染，不能让两份内容独立漂移。

### 3.3 Scripts 聚合

`scripts` 不复制所有 Script Payload，只保存：

- 集数；
- 按 `episode_no` 排序的 `artifact_version_id`；
- 完整度；
- 全局连续性摘要；
- 质量标记。

读取整剧时由 Query Service 批量展开精确版本。这样单集修订不会静默改写历史聚合。

`runtime.aggregate_scripts` 只接受整步确认快照中的全部已确认
`script_unit` Current Versions，按 `episode_no` 确定性生成只读 `scripts`。
它不调用模型、不创建第二次确认，并在同一事务内保存聚合产物和完成 Run。

## 4. 生成配置合同

### 4.1 创作配置

小说、非小说和视频后半段创作共用：

```json
{
  "target_episode_count": 20,
  "episode_duration_minutes": 2,
  "preserve_existing_episode_marks": false,
  "expansion_policy": "confirm_if_needed",
  "user_requirements": []
}
```

约束：

- `target_episode_count` 为正整数；
- `episode_duration_minutes` 第一版允许 0.5–10；
- `expansion_policy` 第一版固定为 `confirm_if_needed`；
- 不允许模型自行新增关键设定或主线；
- 体量不足时创建 `volume_fit` 转换确认，用户确认扩写策略后才能继续。

### 4.2 视频提取配置

视频前半段只使用：

```json
{
  "fidelity_level": "high",
  "timecode_precision": "second",
  "uncertain_content_policy": "mark"
}
```

不在上传和提取阶段询问新剧本目标集数、时长。

### 4.3 同一 Run 的阶段配置快照

`video_reference_creation` 是多阶段 Capability：

1. 视频提取阶段绑定不可变 `extraction_config_snapshot`；
2. 用户确认改编方法、目标集数和时长后，创建不可变 `creation_config_snapshot`；
3. 后续 `story_seed` 到 `scripts` 只绑定第二份快照；
4. 已完成 Task 始终保留原快照引用；
5. 任一快照都不允许原地修改。

这不是切换 Capability，也不是创建第四个 Skill。Runtime 的“一份固定 Config Snapshot”应解释为“每个 Step Run 固定一份精确 Config Snapshot”，而不是强迫长流程只有一份配置。

## 5. `novel_to_script`

### 5.1 启动输入

必填：

- 一个或多个按顺序组成同一本小说的文本/文档 Asset；
- 来源类型已确认为小说原文；
- 创作配置；
- 用户明确启动。

支持 TXT、MD、DOCX、PDF。Input Snapshot 固定 Asset Version、Checksum、顺序和用户请求消息。

### 5.2 步骤

| Step ID | Kind | 输入 | 输出 | 审批 |
|---|---|---|---|---|
| `ingest_source` | `system` | Asset Snapshot | `source_input` | 启动确认 |
| `build_source_manifest` | `system` | confirmed `source_input` + exact Asset Snapshot | confirmed `source_manifest` | none |
| `build_story_bible` | `batch` | confirmed `source_input` + `source_manifest` | `story_bible` | checkpoint |
| `review_volume_fit` | `system/approval` | Bible + Config | 决策记录 | 仅体量不足时 |
| `split_episodes` | `batch` | confirmed Source + Manifest + Bible + Config | `episode_split` | checkpoint |
| `build_episode_cards` | `batch` | confirmed Split + Bible | `episode_cards` | batch checkpoint |
| `generate_script_units` | `shared_workflow` | confirmed Cards + Config | `script_unit[]` | batch checkpoint |
| `review_script_set` | `review` | confirmed `script_unit[]` | `quality_review` | 仅 `action_required` 时 |
| `aggregate_scripts` | `aggregate` | confirmed Units | `scripts` | none |

### 5.3 来源分析与故事圣经聚合

`build_story_bible` 是一个用户只确认最终故事圣经的内部两阶段 Batch：

```text
source_manifest
-> source_analysis Task Checkpoint
-> story_bible_aggregate
-> story_bible（用户确认）
```

- `source_analysis` 读取 Runtime 从精确 Asset Snapshot 重建的完整 Source Units；
- Runtime 校验 Source Unit 数量、顺序、稳定 ID、Asset 引用和覆盖完整性；
- `story_bible_aggregate` 只读取已校验分析，不重复加载原始正文；
- `source_analysis` 不注册用户 Artifact，不新增审批点；
- 任一阶段不得静默截断；超出 Context Pack 上限时失败并保留可恢复 Task。

### 5.4 拆集内部任务

`split_episodes` 内部执行：

```text
global skeleton
-> batches of at most 5 episodes
-> deterministic merge
-> coverage validation
-> episode_split
```

Global Skeleton 和 Batch Result 是 Task Checkpoint Payload，不注册为用户 Artifact。必须校验：

- 集号 1 到目标集数完整且唯一；
- 原文顺序不逆序；
- 不存在未解释的缺失或重复范围；
- 每集边界可定位到原文；
- `actual_episode_count == target_episode_count`。

Manifest 通过 `batch.preparation` 声明 Global Skeleton 的 Prompt/Schema，通过 `batch.task_stage` 声明边界批次的 Prompt/Schema。模型只能选择 Runtime 提供的边界候选；真实 `source_refs`、连续范围和最终覆盖结果由 Runtime 确定性补全与校验。

### 5.5 Artifact 摘要

`story_bible`：

- 故事概览；
- 原文结构单元；
- 人物、关系、世界规则；
- 主线、高潮和伏笔回收；
- 必须保留事实；
- 短剧化资产与风险；
- 来源追溯。

`episode_split`：

- 目标/实际集数；
- 拆集策略；
- 每集来源边界、核心事件、人物变化、尾钩和风险；
- 前后集边界检查；
- 全文覆盖检查。

`episode_cards`：

- 每集功能、开场状态、冲突、反转/兑现、人物变化；
- 尾钩、卡点、节奏和视觉策略；
- 必须保留事实/台词/时刻；
- 场次提纲；
- 连续性增量。

## 6. `non_novel_to_script`

### 6.1 启动输入

必填：

- 一个或多个非小说文本/文档 Asset；
- 用户确认材料类型不是完整小说原文；
- 创作配置；
- 用户明确启动。

可接受故事大纲、分集大纲、人物设定、世界观、桥段、对白素材和混合策划材料。新增推断必须与用户事实分开。

### 6.2 步骤

| Step ID | Kind | 输入 | 输出 | 审批 |
|---|---|---|---|---|
| `ingest_source` | `system` | Asset Snapshot | `source_input` | 启动确认 |
| `build_source_manifest` | `system` | confirmed `source_input` | `source_manifest` | none |
| `build_material_bank` | `model` | confirmed `source_input` + `source_manifest` | `material_bank` | checkpoint |
| `review_volume_fit` | `system/approval` | Bank + Config | 决策记录 | 仅体量不足时 |
| `build_story_seed` | `model` | confirmed Bank | `story_seed` | checkpoint |
| `build_series_blueprint` | `model` | confirmed Seed + Config | `series_blueprint` | checkpoint |
| `build_episode_cards` | `batch` | confirmed Blueprint | `episode_cards` | batch checkpoint |
| `generate_script_units` | `shared_workflow` | confirmed Cards + Config | `script_unit[]` | batch checkpoint |
| `aggregate_scripts` | `aggregate` | confirmed Units | `scripts` | none |

### 6.3 Artifact 摘要

`material_bank`：

- 输入类型标签；
- 用户明确提供的人物、关系、事件、规则、场景、台词和卖点；
- 冲突、情绪、爽点、尾钩和视觉素材；
- 可舍弃内容；
- 推断候选；
- 缺口和问题；
- 体量适配判断；
- 来源追溯；
- 必填 `source_trace.claims`：用户明示内容为带精确 Source Unit 引用的锁定 `FACT`，模型新增为未锁定 `INFERENCE/PROPOSAL/UNKNOWN`；
- 初始素材整理不得生成 `CONFIRMED_CHANGE`，Runtime 对照 Source Manifest 校验 FACT 引用。

`story_seed`：

- Logline 和核心前提；
- 主角、主要人物和关系引擎；
- 中央冲突、世界规则和主线；
- 爽点链、尾钩机制；
- 经确认的新增内容；
- 体量规划和风险；
- 来源追溯，并完整继承 Material Bank Content Claims；新创作只能追加未锁定 `PROPOSAL`。

`series_blueprint`：

- 目标集数和整剧承诺；
- 阶段规划；
- 爽点、尾钩和首个大高潮分布；
- 节奏密度；
- 人物与关系推进；
- 连续性规则；
- 新增内容和适配风险；
- 完整继承 Story Seed Content Claims，规划建议不能升级为用户事实。

## 7. 共享剧本生成 Workflow

内部模块 ID：

```text
shared_script_generation
```

步骤：

```text
for episode in natural order:
  build_script_context
  generate_script_unit
  validate_script_unit
  persist continuity_delta
aggregate_scripts
```

第一版默认顺序生成，后集读取前集已保存的连续性增量。单集失败只重试当前集及尚未开始的后集；不得重做已成功前集。

`script_context` 至少包含：

- `source_kind`：`novel`、`non_novel` 或 `video_reference`；
- 当前集配置；
- 必须遵守事实；
- 允许新增与禁止修改；
- 人物、关系和连续性状态；
- 当前集最小来源材料；
- 风格限制和用户备注。

`script_unit` 至少包含：

- `episode_no`；
- 标题；
- `script_text`；
- 结构化 Scenes/Blocks；
- 来源引用；
- 连续性增量；
- 使用的新增/改编依据；
- 风险和自检。

## 8. `video_reference_creation`

### 8.1 启动输入

必填：

- 一个 `video_reference_source` Asset Set；
- 至少一个 MP4/MOV Asset；
- 用户明确视频用于参考创作。

初始阶段不需要新剧本创作配置。单集视频仍按 Provider 单请求执行，多个视频通过 Task Queue 并发。

### 8.2 步骤

| Step ID | Kind | 输入 | 输出 | 审批 |
|---|---|---|---|---|
| `ingest_source` | `system` | sealed Asset Set Version | confirmed `source_input` | checkpoint |
| `extract_video_scripts` | `batch` | 每个视频精确 Snapshot | `video_script_unit[]` | batch checkpoint |
| `aggregate_reference_scripts` | `aggregate` | confirmed Units | `reference_scripts` | none |
| `analyze_reference_scripts` | `model` | confirmed Aggregate | `script_analysis` | checkpoint |
| `propose_adaptation_options` | `model` | confirmed Analysis | `adaptation_options` | transition checkpoint |
| `build_adaptation_brief` | `model` | selected Options + Selection Snapshot + User Input + Creation Config | `adaptation_brief` | checkpoint |
| `build_story_seed` | `shared_workflow` | confirmed Brief + Analysis | `story_seed` | checkpoint |
| `build_series_blueprint` | `shared_workflow` | confirmed Seed + Config | `series_blueprint` | checkpoint |
| `build_episode_cards` | `shared_workflow` | confirmed Blueprint | `episode_cards` | batch checkpoint |
| `build_script_contexts` | `system` | confirmed Brief + Seed + Blueprint + Cards | `script_context[]` | none |
| `generate_script_units` | `shared_workflow` | confirmed Cards + Brief + Contexts | `script_unit[]` | batch checkpoint |
| `review_script_set` | `review` | confirmed Units + Handoffs | 质量审核产物 | conditional checkpoint |
| `aggregate_scripts` | `aggregate` | confirmed Units | `scripts` | none |

通用上传层每次新增、删除或重排都创建新的 Asset Set Version；用户明确“上传完成”后才 Seal 当前版本。只有已 Seal 的当前 Asset Set Version 可以创建 `video_reference_creation` Run，`ingest_source` 再将其固化为不可变 Run Input Snapshot。Run 内不追加来源视频。

收集和单集提取阶段允许 `episode_no=null`，展示顺序由 `episode_order` 决定。整批确认前，每个纳入聚合的单集必须由用户确认唯一正整数集号；系统不得替无集号视频自动选择业务集号。单视频场景可以建议第 1 集，但仍随批次确认留痕。

Seal 必须检查集号、重复、缺口、失败项和人工顺序。缺集时必须保存用户明确的 `continue_incomplete` 策略，不能靠模型推断同意。逐集提取全部进入成功或失败终态后才生成统一批次确认；只有已确认的逐集版本可进入聚合。

### 8.3 单集视频产物

`video_script_unit` 包含：

- `episode_no` 和展示顺序；
- 原文件名；
- 100–200 字剧情概要；
- 高还原结构化编剧稿；
- 视频时间码来源；
- 不确定标记；
- 提取完整度；
- 源视频是否仍可用于重解析。

质量定位是“可编辑的高还原初稿”，不宣称逐帧 OCR、完整 ASR 或 100% 逐字无遗漏。

### 8.4 Reference Scripts

`reference_scripts` 是只读聚合，包含：

- 封存 Asset Set Snapshot；
- 单集视频剧本精确版本引用；
- 已识别集号、缺失集号、失败集号和排序；
- 完整度；
- 用户是否确认按不完整材料继续。

单集修改后创建新的聚合版本，不重调其他视频模型。

### 8.5 Script Analysis

`script_analysis` 读取已确认的完整参考剧本，结构化覆盖：

1. 基础信息、完整度、概括、剧情简介和世界观；
2. 综合潜力、优势、短板和形态适配；
3. 开头策略；
4. 核心梗与钩子；
5. 爽点、虐点和情绪释放；
6. 剧情驱动和情绪曲线；
7. 人物、关系和语言特征；
8. 逐集信息；
9. 差异化和爆点；
10. 可复制方法；
11. 风险；
12. P0/P1/P2 优化建议；
13. 投流素材方向；
14. 最终结论。

事实判断必须绑定 `episode_no + artifact_version_id + scene_id/line_id` 等证据。分析建议必须标记为建议，不能写成参考剧事实。

`漫剧爆款诊断` 规则应迁入：

```text
design/rules/video-reference-creation/script-analysis.v1.md
```

并只由 `analyze_reference_scripts` 引用。

### 8.6 Adaptation Options

`adaptation_options` 必须生成 2–3 套，分别包含：

- 方案标题与一句话策略；
- 保留模式；
- 替换内容；
- 新增元素建议；
- 人物/关系、世界观和事件链变化；
- 节奏、爽点和尾钩迁移方法；
- 与参考剧的差异要求；
- 风险与适用条件；
- 分析证据。

用户可选择、组合、自定义或要求 AI 修改。最终选择不是字符串索引，而是保存结构化 Adaptation Decision Snapshot。

解决 `propose_adaptation_options` 的转换审批时，单个 Command 必须原子提交：

- 精确 `adaptation_options` Artifact Version；
- 用户选择、组合、自定义和 AI 修订后的 Adaptation Decision Snapshot；
- 目标集数、单集时长等 Creation Config；
- 用户确认消息；
- Snapshot Hash。

Runtime 在同一事务中创建不可变 Adaptation Decision Snapshot 和 Creation Config Snapshot。两者都成功后，Transition 条件 `adaptation_selection_and_creation_config_confirmed` 才成立，才能执行 `build_adaptation_brief`。

### 8.7 Adaptation Brief

`adaptation_brief` 至少包含：

- 改编目标；
- 选定方法；
- 保留的抽象创作模式；
- 必须替换的具体内容；
- 新增元素；
- 人物和关系调整；
- 题材、风格和世界观方向；
- 目标集数和单集时长；
- 禁止照搬内容；
- 用户硬性要求；
- 来源标记；
- 未决问题；
- 版权与合规边界声明。

Brief 必须确认后才能进入 `story_seed`。Brief 新版本使旧确认失效，并触发全部创作下游影响计算。

### 8.8 视频后续复用非小说链

视频链不生成 `material_bank`。其桥接关系为：

```text
confirmed script_analysis
+ confirmed adaptation_brief
+ creation_config_snapshot
-> story_seed
-> series_blueprint
-> episode_cards
-> script_unit[]
-> scripts
```

这些步骤复用相同 Schema、Context Policy 和 Executor，但所有 Run、Event 和 Artifact 的 `capability_id` 仍是 `video_reference_creation`。

## 9. Prompt 与 Rules 引用

第一版引用原则：

| Step | Prompt | Rules |
|---|---|---|
| `build_story_bible` | 小说 Story Bible Prompt | 素材理解、人物关系、结构规划 |
| `split_episodes` | 小说拆集 Prompt | 结构、冲突、尾钩、连续性 |
| `build_material_bank` | 非小说素材库 Prompt | 非小说素材与故事种子 |
| `build_story_seed` | 来源对应 Story Seed Prompt | 人物、结构、非小说素材规则 |
| `build_series_blueprint` | Series Blueprint Prompt | 结构、爽点、尾钩 |
| `build_episode_cards` | Episode Cards Prompt | 结构、连续性、短剧适配 |
| `generate_script_units` | Shared Script Prompt | 写作、对白、格式、短剧适配 |
| `extract_video_scripts` | Video Extract Prompt | 视频高还原规则 |
| `analyze_reference_scripts` | Script Analysis Prompt | 漫剧爆款诊断规则 |
| `build_adaptation_brief` | Adaptation Brief Prompt | 禁止照搬与来源标记规则 |

Rule 只包含约束，不定义 API Key、模型 URL、运行状态或 Capability 路由。Worker 只加载当前 Step 明确引用的 Rules。

## 10. 确定性校验

模型输出通过 JSON Schema 后，Runtime 还必须校验：

### 10.1 全链路

- 所有稳定 ID 在作用域内唯一；
- 所有来源 ID 存在且属于当前 Project；
- Artifact 依赖绑定精确 Version；
- 模型不能创建 Approval；
- Run 已取消或 Cursor 过期时不落库；
- Payload 不包含未声明字段。

### 10.2 集数

- `episode_no` 为 1 到目标集数；
- 每个目标集号恰好出现一次；
- 聚合按 `episode_no`，不按 Task 完成时间；
- `episode_count` 与精确 Unit 引用数量一致。

### 10.3 视频

- 一个视频 Asset 对应一个解析 Task 和一个 Scope；
- `source_refs.asset_id` 必须等于当前 Task 输入；
- 时间码不越过视频时长；
- 缺失/失败集继续必须有用户确认；
- 只有 Sealed Snapshot 能进入整剧分析；
- 源视频过期不删除已有 Artifact，但禁止重解析。

### 10.4 剧本

- Scene/Line ID 唯一；
- Dialogue 有 Speaker；
- 动作行格式合法；
- `script_text` 与 Scenes 一致；
- 后集连续性输入不晚于当前集；
- `scripts.unit_refs` 全部指向已确认版本。

## 11. 旧 Prompt 迁移差异

现有 Novel2Script 设计资产可复用，但实现前必须增加 Prompt Adapter：

| 现状 | 新合同 |
|---|---|
| `source_trace` 有时在模型响应顶层 | 规范化进 Artifact Payload |
| `episode_cards` 数组命名不一致 | 统一为 `episodes` |
| 数字字段叫 `episode_id` | 统一为 `episode_no` |
| `script_unit` 无稳定 `line_id` 的旧输出 | 新 Schema 强制 `line_id` |
| `source_mode` 只有 novel/non_novel | 从正式 Script Payload 删除 |
| Video Prompt 输出普通 `script_unit` | 转为 `video_script_unit` |
| `scripts` 复制所有单集 Payload | 改为精确 Version 引用聚合 |
| Prompt 自报 `next_action` | Runner 根据 Step Definition 决定 |
| 非小说配置补充字段未进入输出示例 | 以 Config Snapshot 为权威，不要求模型重复 |
| 拆集 Prompt 文本要求 `boundary_check` 但示例缺失 | 新 Schema 强制该字段 |

第一版显式 Adapter Registry：

| Adapter ID | 目标 |
|---|---|
| `adapter.legacy_story_bible_to_v1` | 旧小说 Story Bible 响应转 `storyBible` |
| `adapter.legacy_episode_split_to_v1` | 旧拆集响应转 `episodeSplit` |
| `adapter.legacy_material_bank_to_v1` | 旧非小说素材响应转 `materialBank` |
| `adapter.legacy_story_seed_to_v1` | 旧 Story Seed 响应转 `storySeed` |
| `adapter.legacy_series_blueprint_to_v1` | 旧整剧蓝图响应转 `seriesBlueprint` |
| `adapter.legacy_episode_cards_to_v1` | 旧分集卡响应转 `episodeCards` |
| `adapter.legacy_script_unit_to_v1` | 旧单集剧本响应转 `scriptUnit` |
| `adapter.legacy_video_script_unit_to_v1` | 旧视频提取响应转 `videoScriptUnit` |

每个 Adapter 必须拥有输入 Fixture、目标 Schema 测试和不可静默丢失字段清单。Adapter 未注册、版本不匹配或转换后校验失败时，Step 失败，不创建正式 Artifact Version。

迁移时先做适配器和双读验证，不直接修改旧生产数据。

## 12. Schema 文件

机器可读合同：

```text
schemas/v1/common.schema.json
schemas/v1/novel-to-script.schema.json
schemas/v1/non-novel-to-script.schema.json
schemas/v1/video-reference-creation.schema.json
capabilities/v1/novel-to-script.json
capabilities/v1/non-novel-to-script.json
capabilities/v1/video-reference-creation.json
capabilities/v1/response-adapters.json
```

Capability Manifest 的 Schema 引用必须在启动时解析；任何缺失、循环错误或不存在的 `$defs` 都使对应 Capability 状态变为 `unavailable`，但不影响零 Capability 的 Agent Shell 启动。

## 13. 本项完成门禁

- 三个 Capability 都有独立 Input/Config Schema；
- 所有主要 Artifact 都有 Payload Schema；
- 三条链都能由同一 Step Definition 结构表达；
- 视频链不需要提前询问新剧本配置；
- 视频后半段仍属于原 Capability；
- `script_analysis` 不新增用户 Skill；
- Script Unit 有统一结构；
- Prompt Response 与 Artifact Payload 不混用；
- 旧字段有明确迁移映射；
- JSON 文件可解析，所有外部 `$ref` 可定位。

## 14. 阶段 3 第 11 步前 P0 补充

权威合同：

```text
content-semantics-and-quality-review-contract.md
```

11A 对当前工作流做以下确定性升级：

1. 文本链在模型理解前建立内部 `source_manifest`；
2. `source_trace` 升级为五态 `contentClaim`；
3. 当前已定义但未进入执行图的 `script_context` 成为每集内部 Artifact；
4. `generate_script_units` 原子产生 `script_unit + script_handoff`；
5. 两类 Artifact 进入同一个整个剧本 Batch Approval；
6. 用户统一确认后自动运行 `review_script_set`；
7. Review `passed` 后才执行 `aggregate_scripts`；
8. Review `action_required` 时创建 `quality_review` Approval；
9. 视频 Skill 的 `script_analysis` 仍是参考内容分析，不承担新剧本质量审核。

当前机器 Schema、Manifest 与 Compiler 已完成 11A 升级并通过合同校验；真实 `script_handoff` 原子提交和审核执行子图仍以 11E/11F 的运行验收为准。
