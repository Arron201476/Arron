# Skill / Capability Registry 契约

状态：阶段 2 设计合同  
版本：v1.0  
日期：2026-07-27

## 1. 文档目的

本文档定义内容生产 Agent 第一版的 Skill / Capability Registry。当前三个内置 Skill 都是 Domain Workflow，但 Registry 协议不能把所有未来 Skill 等同为有状态工作流。

它解决：

1. Agent 如何发现和调用小说、非小说、视频三个 Skills；
2. 每个 Skill 如何声明输入、配置、步骤、Artifact、审批和执行依赖；
3. Runtime 如何在不硬编码三条流程的前提下启动和恢复 Run；
4. 前端如何获得 Skill 菜单和流程展示信息；
5. Agent 没有任何 Domain Capability 时如何继续提供通用能力。

本文档不重复定义每个 Artifact 的完整字段。字段合同和机器可读 JSON Schema 见 `capability-workflow-schema-contract.md`、`../schemas/v1/` 与 `../capabilities/v1/`。

## 2. 核心结论

```text
产品界面的 Skill
=
后端架构的 Capability Definition
```

但以下概念不能混用：

| 概念 | 含义 |
|---|---|
| Agent Core | 对话、意图理解、追问、上下文、能力发现和 Guard |
| Generic Tool | 文件解析、图片理解、模型调用、MCP 等通用执行资源 |
| Agent Skill | 当前 Turn 内完成或委托单目标后台任务的轻量能力 |
| Domain Capability | 一条完整、可持久化、可审批的领域生产流程 |
| Rule | 某个 Capability 步骤按需加载的专业规则 |
| Subagent | 具备独立上下文或自主循环的专业执行器 |
| Runtime | Project、Run、Step、Artifact、Approval、Event 的业务状态权威 |

Registry 注册可调用的 Skill 定义，不注册 Agent Core。Skill 分为轻量 `agent_skill` 与有状态 `domain_workflow`。

即使 Registry 为空：

- Agent 仍然可以对话；
- Agent 仍然可以理解和澄清意图；
- Agent 仍然可以管理 Project、Conversation 和 Asset；
- Agent 仍然可以使用已经启用的 Generic Tools；
- Agent 必须说明缺少完成领域任务所需的 Capability。

## 3. 设计原则

### 3.1 宿主与能力分离

General Content Agent 是长期宿主。Agent Skills 和 Domain Capabilities 都是按需发现和调用的扩展能力。

不能把三个 Skills 实现成三个独立产品 Agent，也不能为每个 Skill 重复建设聊天、Project、文件、Run、审批、版本和编辑器。

### 3.2 声明与执行分离

Capability Definition 声明“有什么步骤、需要什么、产生什么”。Capability Runner 负责执行声明。

Definition 不能直接保存业务状态；Runner 不能绕过 Runtime。

### 3.3 模型与状态分离

模型可以建议路由和下一动作，但不能：

- 注册新 Capability；
- 修改 Capability Definition；
- 自行确认审批；
- 自行把 Run 标记为完成；
- 把自然语言回复当成正式 Artifact；
- 决定跳过 Schema 校验。

### 3.4 第一版内置注册

第一版 Registry 使用“受控源码 Manifest + 启动编译”的方式：

- 三个 Skills 由工程代码随版本发布；
- `capabilities/v1/*.json` 是唯一源码真源；
- `manifest.schema.json` 是源码 Manifest 的机器合同；
- 启动时校验并编译成只读 Runtime Capability Definition；
- Go 结构是编译结果，不是另一份可独立维护的配置；
- 普通用户不能上传可执行 Skill；
- 不建设公开插件市场和远程代码加载；
- 未来可以扩展成插件，但第一版不为未知插件牺牲可控性。

这种方式保留主流 Host + Capabilities 结构，同时适合当前公司内部生产系统。

### 3.5 Invocation 不等于 Run；一个 Run 一条主 Capability

每次识别到 Skill 调用可以创建轻量 `Skill Invocation` 记录。只有 `execution_mode=stateful_workflow` 且完成必要确认后才创建 Business Run。`inline` 与 `background_task` 不创建 Run。

每个生成 Run 固定：

```text
capability_id
capability_version
```

Run 启动后不能中途改成另一个 Capability。

视频参考创作后半段复用非小说的故事开发和剧本生成子图，但 Run 的 `capability_id` 仍然是：

```text
video_reference_creation
```

这属于内部 Workflow Module 复用，不是用户 Skill 跳转。

## 4. Registry 组成

```text
Skill / Capability Registry
├─ Capability Definitions
├─ Step Definitions
├─ Artifact Type References
├─ Config Schema References
├─ Context Policies
├─ Approval Policies
├─ Executor References
├─ Provider Requirements
├─ Public UI Metadata
└─ Availability State
```

Registry 不保存：

- Project；
- Conversation；
- Run 实例；
- Step Run 实例；
- Artifact 实例；
- Approval 实例；
- Event；
- 模型 Key；
- 用户上传文件；
- 运行 cursor。

## 5. Capability ID

第一版固定三个用户级 Domain Capability：

| Capability ID | 用户显示名 | 用途 |
|---|---|---|
| `novel_to_script` | 小说转剧本 | 小说原文生成分集剧本 |
| `non_novel_to_script` | 非小说文本转剧本 | 大纲、集纲、设定等生成分集剧本 |
| `video_reference_creation` | 视频参考创作 | 视频反推剧本、分析、确定改编方法并生成新剧本 |

命名规则：

- 只使用小写 ASCII；
- 使用 `snake_case`；
- ID 一经发布不能改义；
- 显示名可以调整，ID 不随文案调整；
- Artifact Type、Step ID 和 Executor ID 使用独立命名空间；
- Rule 文件名不作为 Capability ID。

## 6. Capability Definition

源码 Manifest 编译后的内部合同：

```go
type CapabilityDefinition struct {
    ID                   string
    Version              string
    Label                string
    Description          string
    Kind                 CapabilityKind
    ExecutionMode        ExecutionMode
    EntryPolicy          EntryPolicy
    Routing              RoutingDefinition
    AcceptedAssetKinds   []AssetKind
    RequiredProviders    []ProviderRequirement
    InputSchemaRef       string
    ConfigSchemaRefs     map[string]string
    DefaultConfigRef     *string
    DefaultRetry         RetryPolicy
    Steps                []StepDefinition
    Commands             []CommandDefinition
    ContextPolicyRef     string
    UI                   CapabilityUIManifest
    Completion           CompletionDefinition
}
```

`ArtifactTypes`、完整 Command Definition、默认 Approval 字段和可用性状态由编译器根据 Manifest、Artifact Registry 和部署环境生成。它们不作为第二份手写真源。

### 6.1 顶层字段

| 字段 | 必填 | 说明 |
|---|---|---|
| `id` | 是 | 稳定 Capability ID |
| `version` | 是 | Definition 语义版本 |
| `label` | 是 | 用户可见中文名称 |
| `description` | 是 | 给用户和路由模型使用的能力描述 |
| `kind` | 是 | `agent_skill` 或 `domain_workflow` |
| `execution_mode` | 是 | `inline`、`background_task` 或 `stateful_workflow` |
| `entry_policy` | 是 | 启动前条件和是否允许自动路由 |
| `routing` | 是 | 显式引用和自然语言路由信息 |
| `accepted_asset_kinds` | 是 | 可作为本 Skill 来源输入的 Asset 类型 |
| `required_providers` | 是 | 执行需要的 Provider/Tool |
| `input_schema_ref` | 是 | 启动输入 Schema |
| `config_schema_refs` | 是 | 阶段名到配置 Schema 的映射 |
| `default_config_ref` | 是 | 默认阶段配置；多阶段 Capability 可为 `null` |
| `default_retry` | 是 | Step 未覆写时的重试策略 |
| `steps` | 是 | 有序或图结构步骤 |
| `commands` | 是 | 当前 Capability 可接受的命令 |
| `context_policy_ref` | 是 | 上下文组装策略 |
| `ui` | 是 | Skill 菜单和工作台公开元数据 |
| `completion` | 是 | Run 完成判定 |

源码 Manifest 的 `ui` 是 UI Seed，只声明 `icon_key`、`sort_order` 和 `entry_view_key`。启动编译器根据 Step Outputs、Artifact View Registry、Approval Type 和安全 Action Registry 生成完整 Public Server UI Manifest；前端再把受控 View Key 映射到编译进产品的组件。

导航和 Artifact 类型不得再手写一份与 `steps` 平行的流程清单。UI Seed 和编译后的 Public Manifest 都不能包含任意 React 组件路径、脚本、HTML、CSS 或 API URL，完整安全边界以 `frontend-capability-ui-contract.md` 为准。

### 6.2 版本

使用语义版本：

```text
MAJOR.MINOR.PATCH
```

- MAJOR：步骤、Artifact 或状态语义不兼容；
- MINOR：增加兼容字段、步骤能力或可选操作；
- PATCH：Prompt、Rule、文案或不改变协议的修复。

Run 启动时固定 `capability_version`。恢复旧 Run 时必须读取与其匹配的 Definition，不能静默套用最新版本。

第一版发布包至少保留仍有未完成 Run 使用的旧 Definition。

## 7. Entry Policy

```json
{
  "explicit_invocation": true,
  "auto_route": true,
  "requires_user_confirmation": true,
  "input_collection_modes": ["fixed"]
}
```

含义：

- 用户可以通过 Skill 菜单显式引用；
- Main Agent 可以根据自然语言自动路由；
- 启动前必须补齐并确认必要输入；
- 第一版三个 Skill 均使用 `fixed`：启动时封存输入。`append_until_sealed` 只作为未来可选协议值保留，不用于当前视频链；
- 正式 Run 必须属于 Project、同一 Project 写互斥属于 Runtime 全局不变量，不在每个 Capability 重复声明。

插入 Skill 引用不等于启动 Run。

## 8. Routing Definition

建议结构：

```json
{
  "explicit_aliases": [
    "小说转剧本",
    "novel_to_script"
  ],
  "intent_examples": [
    "把这本小说改成短剧剧本",
    "根据上传的小说生成分集剧本"
  ],
  "ambiguity_policy": "ask_user"
}
```

### 8.1 路由优先级

```text
用户明确引用 Skill
-> 检查该 Skill 是否可用
-> 检查输入是否符合
-> 缺信息则追问
-> 信息完整后请求启动确认
```

用户没有引用：

```text
Main Agent 判断意图
-> 0 个匹配：普通回复或说明缺少能力
-> 1 个高置信匹配：选择 Capability
-> 多个可能匹配：追问
-> 输入不足：追问
```

### 8.2 显式引用不是无条件服从

即使用户显式选择某个 Skill，Guard 仍要阻止：

- Capability 不可用；
- 文件类型不支持；
- 必要输入缺失；
- 当前 Project 有冲突 Run；
- 用户只插入 Skill 名称但没有发送；
- 用户发送后仍未表达执行目标；
- 需要扩写确认但尚未确认。

### 8.3 前端 Skill 引用

前端显示：

```text
@小说转剧本
```

请求中不能只依赖文本解析，应同时发送结构化引用：

```json
{
  "content": "@小说转剧本 把刚上传的原文改成 20 集",
  "capability_ref": {
    "capability_id": "novel_to_script",
    "source": "user_explicit"
  }
}
```

Main Agent 自动识别时：

```json
{
  "capability_ref": {
    "capability_id": "novel_to_script",
    "source": "agent_routed",
    "confidence": 0.94
  }
}
```

`confidence` 只帮助 Guard 判断是否追问，不是执行授权。

## 9. Capability Availability

Definition 存在不等于当前环境可执行。

可用性状态：

```text
available
degraded
unavailable
disabled
```

| 状态 | 含义 |
|---|---|
| `available` | 所需 Provider、Tool、Schema 和 Executor 全部可用 |
| `degraded` | 可以执行，但部分非关键增强不可用 |
| `unavailable` | 缺少关键 Provider、Tool、配置或 Schema |
| `disabled` | 被部署配置明确关闭 |

建议公开结构：

```json
{
  "capability_id": "video_reference_creation",
  "status": "unavailable",
  "reason_code": "VIDEO_PROVIDER_NOT_CONFIGURED",
  "user_message": "当前环境尚未配置视频理解服务。",
  "missing_requirements": [
    "multimodal_video_provider"
  ]
}
```

Agent 必须读取真实可用性，不得仅根据 Definition 声称具备能力。

Registry 为空或全部不可用时，Agent Core 仍正常工作。

## 10. Provider Requirement

```json
{
  "provider_id": "multimodal_video_provider",
  "required": true,
  "capabilities": [
    "video_understanding",
    "structured_output"
  ],
  "health_check": "startup_and_before_run"
}
```

第一版 Provider IDs：

- `control_model_provider`
- `content_model_provider`
- `multimodal_video_provider`
- `document_parser`
- `image_understanding_provider`
- `media_processor`
- `export_provider`

Provider Definition 与 Capability Definition 分离。更换豆包或其他模型时，不改变 `video_reference_creation` 的 Capability ID。

## 11. Step Definition

```go
type StepDefinition struct {
    ID                string
    Label             string
    Kind              StepKind
    ExecutorRef       string
    ConfigRef         *string
    InputRefs         []ArtifactRequirement
    OutputRefs        []ArtifactOutput
    PromptRef         string
    RuleRefs          []string
    ResponseAdapterRef *string
    RequiredContext   []ContextRequirement
    Approval          ApprovalPolicy
    Retry             RetryPolicy
    Batch             *BatchPolicy
    ContextPolicyRef  string
    Next              []StepTransition
    Visibility        StepVisibility
}
```

### 11.1 Step Kind

第一版：

| Kind | 用途 |
|---|---|
| `model` | 调用文本/多模态模型产生 Artifact |
| `tool` | 调用文件、媒体或其他确定性工具 |
| `system` | Runtime 确定性处理 |
| `aggregate` | 聚合多个子 Artifact |
| `batch` | 执行多个可恢复子任务 |
| `approval` | 独立业务确认，不产生模型内容 |
| `shared_workflow` | 复用内部已注册 Workflow Module |

### 11.2 Step ID

示例：

```text
ingest_source
build_story_bible
split_episodes
extract_video_scripts
analyze_reference_scripts
build_adaptation_brief
generate_script_units
```

规则：

- 在一个 Capability Version 内唯一；
- Run 保存的是 Definition 中的 Step ID；
- UI 文案不参与状态判断；
- 修改显示名不改变 Step ID；
- 删除或改义需要升级 Capability MAJOR 版本。

### 11.3 Input Requirement

```json
{
  "artifact_type": "story_bible",
  "required_status": "confirmed",
  "cardinality": "one",
  "version_policy": "approval_snapshot"
}
```

支持：

- `one`
- `zero_or_one`
- `many`
- `at_least_one`

默认必须读取审批时固定的精确版本，不能运行时重新找“最新版本”替代。

### 11.4 Output Definition

```json
{
  "artifact_type": "episode_cards",
  "cardinality": "one",
  "initial_status": "pending_approval",
  "schema_ref": "schemas/artifacts/episode_cards.v1.json"
}
```

模型输出只有通过以下校验后才能保存为成功 Artifact：

1. JSON 可解析；
2. Artifact Type 与当前 Step 一致；
3. Schema 通过；
4. 集数、集号、来源引用等确定性约束通过；
5. Provider 请求与当前 task cursor 匹配；
6. Run 未被取消或替换；
7. 输入版本仍符合 Step 快照。

## 12. Prompt 与 Rules

Step 可以引用：

```json
{
  "prompt_ref": "prompts/novel/build_story_bible.v1.md",
  "rule_refs": [
    "rules/shared/character_voice.v1.md",
    "rules/novel/source_fidelity.v1.md"
  ]
}
```

约束：

- Prompt 是当前步骤的可执行任务；
- Rule 是步骤按需加载的专业约束；
- Rule 不能参与 Capability 路由；
- Main Agent 不预加载全部 Rules；
- Worker 只加载 Step 明确引用的 Rules；
- 未引用 Rule 不进入模型上下文；
- Registry 启动校验所有路径存在、UTF-8 可读且非空；
- Manifest 中只保存受控相对路径；
- Rule 不能包含 API Key、Provider URL 或运行状态。

旧 Prompt 的响应包装与目标 Artifact Schema 不一致时，Step 必须显式声明 `response_adapter_ref`。Runtime 先执行受版本控制的 Adapter，再做目标 Schema 校验；没有声明 Adapter 时，响应必须直接符合目标 Schema，禁止依赖未登记的字段改写分支。

2026-09-10 G4.84实现核定：托管Skill无Adapter的直接产物若另声明`provider_result_schema_ref`，同一裸JSON对象必须同时满足provider与目标Artifact Schema。ContextPack用`allOf`表达联合约束，未声明或相同Schema保持原格式；Go提交也校验两个合同，包括仍保存分离Schema的旧冻结任务。联合JSON Schema不代表真实网关一定支持该结构化传输；SDK保留既有JSON兼容模式并仍进行完整输出校验，同一合同的修复和暂停恢复不重新启用已经放弃的结构化模式。Go行为测试未运行，模拟拒绝传输的SDK专项不是真实网关验收。

2026-09-10 G4.85实现核定：托管单任务的多个不同类型one产物使用`{"artifact_type_a": {...}, "artifact_type_b": {...}}`对象，每个声明键必需且不允许额外输出键；可选provider Schema约束整个对象。Go必须整组验证和原子保存，通过已有artifact_version_set确认绑定全部确切版本；不能仅确认第一份。重生成整组成功后才进入待确认，编辑任一份要重建集合审批并复验原冻结格式。成功回执的outputs列出原attempt产生的全部版本，后续人工修改不改写旧输出回执。当前仍保持逐集batch原有单many产物协议，不宣称任意批处理已实现；Go行为验收未运行。

2026-09-10 G4.86补接：ContextPack领取入口也允许上述托管多产物，不再误按固定剧本组合过滤。单产物/逐集/多产物的人工保存、返修提案和接受修改共用生成基线的冻结格式校验；沿版本祖先查最近模型生成合同，缺失或hash错误拒绝，不改用当前Skill包或更早生成合同。逐集编辑保留episode_no；多产物以只读兄弟产物重建完整输出对象，不能拆掉provider关联约束。返修事务产生`artifact_validation`（`response_schema`、`context_hash`及可选`output_key`/`output_bundle`），Sidecar传给SDK供生成前读取及最终补丁校验；中间补丁不必逐步满足关联约束，no_change不产生修改。普通agent_shell文档与平台固定适配器保持各自原有路径，不冒称本批覆盖所有产物业务语义。Go行为验收仍未运行。

## 13. Approval Policy

```json
{
  "type": "checkpoint",
  "scope": "artifact",
  "required": true,
  "invalidate_on_new_version": true,
  "allowed_actions": [
    "approve",
    "edit_artifact",
    "request_ai_revision",
    "regenerate_artifact"
  ]
}
```

Capability Step Manifest 可声明的 Approval 类型：

| 类型 | 用途 |
|---|---|
| `none` | 内部辅助步骤或纯聚合 |
| `checkpoint` | 单个主要 Artifact 确认 |
| `batch_checkpoint` | 批量子项完成后的统一确认 |
| `transition_checkpoint` | 缺集继续、扩写策略等流程转换确认 |

Approval Definition 只声明策略。Approval 实例由 Runtime 创建并保存精确 Artifact Version Snapshot。

`final_selection`、Asset 删除和 Project 删除同样使用 Runtime Approval 机制，但它们属于 Project/Asset Command 合同，不是 Capability Step Manifest 类型。

源码 Manifest 允许省略可确定的展开字段，编译器使用以下固定默认值：

| `type` | 默认 `scope` | 默认 `allowed_actions` |
|---|---|---|
| `none` | `none` | `[]` |
| `checkpoint` | `artifact` | `approve, edit_artifact, request_ai_revision, regenerate_artifact` |
| `batch_checkpoint` | `batch` | `approve, edit_artifact, request_ai_revision, regenerate_artifact` |
| `transition_checkpoint` | `transition` | 必须由流程需要显式声明；未声明时仅 `approve` |

Pause、Resume、Retry 和 Cancel 属于 Run/Task Control，不得写入 Approval `allowed_actions`。它们由 Runtime 根据当前状态投影为独立 `available_actions`。

2026-09-10 G4.87审批返修接口：`GET /api/v1/approvals/{id}/revision-targets`向当前可编辑用户返回审批快照和可返修版本摘要，摘要为`artifact_id/artifact_version_id/artifact_type/scope_key/version/label`，不返回正文。版本集合按原绑定的完整有序列表校验hash；`subject_version`是集合版本，不是成员数量。固定剧本组合中的handoff仍由正文修改派生，不作为独立目标；托管Skill自定义输出不套用固定剧本过滤。

原`POST /api/v1/approvals/{id}/regeneration-requests`在`action=request_ai_revision`时接受可选`target_artifact_version_id`，只能选择上述审批绑定的确切版本；其他action禁止指定单目标。省略字段保留旧请求序列化哈希及单产物/集号选择兼容。接受返修只创建持久RevisionRequest，返回202及`source_approval_request_id`，不同步调用模型、不批准审批、不自动继续Run。来源字段由不可变创建事件派生，无数据库schema变更。前端集合审批显式选择目标，未知回执保持原指令/版本/请求键。本批Go行为与真实联合未验收，后台Worker原提交者身份绑定仍为已登记P1缺口，不把接口接线当完整闭环通过。

2026-09-10 G4.88执行授权补接：上述G4.87身份缺口已有源码修复，Go行为仍待验。审批/对话创建、目标确认和显式执行在原事务中追加`revision_request.execution_authorized`，封存真实工作区/用户、请求/目标/基线、指令hash及授权版本；后台恢复该主体并在领取与提案/no_change提交时复查有效权限。旧请求缺失授权返回`REVISION_EXECUTION_OWNER_REQUIRED`，无效绑定返回`REVISION_EXECUTION_OWNER_INVALID`，HTTP均映射409，不猜为作品所有者或默认用户。无有效授权的排队请求记录失败，合法用户可用既有执行或目标确认入口显式重新授权；回执重试不替换新授权或重放执行。前端授权失败卡显示“确认并重新执行”，只读用户不可提交，服务端仍为权限真源。schema及SDK版本不变，不将源码接线当作真实后台验收通过。

其他默认值：

- `condition=null`；
- `visibility=user`；
- Step `retry` 继承顶层 `default_retry`；
- Step `config_ref` 继承顶层 `default_config_ref`；
- `response_adapter_ref=null`；
- `required_context=[]`，但 Compiler 仍根据 Input Refs 注入精确 Artifact Version 基础上下文。

显式字段始终覆盖默认值。编译结果必须完整展开并只读，Runtime 不在执行中再次猜默认值。

## 14. Batch Policy

```json
{
  "item_key": "episode_no",
  "execution": "sequential",
  "failure_policy": "preserve_success_retry_failed",
  "ordering": "natural_episode_order",
  "max_items_per_task": 5,
  "concurrency_policy": "novel_split_default",
  "preparation": {
    "id": "global_plan",
    "prompt_ref": "prompts/novel/episode_split_global.md",
    "schema_ref": "schemas/internal-task-checkpoints.json#/$defs/episodeSplitGlobalPlan"
  },
  "task_stage": {
    "id": "boundary_batch",
    "prompt_ref": "prompts/novel/episode_split_batch.md",
    "schema_ref": "schemas/internal-task-checkpoints.json#/$defs/episodeSplitBoundaryBatch"
  }
}
```

第一版规则：

- 子任务有独立状态和 cursor；
- 成功子任务不因其他子任务失败而回滚；
- 重试只执行失败或未完成子任务；
- 最终聚合按业务顺序，不按完成时间；
- 必需子项未全部成功时不能确认整个步骤；
- 单集允许编辑和重新生成；
- 用户只对整个批量步骤确认一次。

`preparation` 与 `task_stage` 是可选的内部 Task Stage 声明。它们各自绑定 Prompt 和输出 Schema，结果只保存为 Task Checkpoint，不注册为用户 Artifact。存在 `preparation` 时必须同时声明 `task_stage`；Runtime 必须先完成并校验 preparation，才可创建后续批次。

并发值是部署配置，Manifest 只声明串并行语义、每个 Task 最大子项数和策略名。Runtime 还要受全局并发控制。

## 15. Retry Policy

```json
{
  "automatic_attempts": 1,
  "user_retry_allowed": true,
  "resume_from_cursor": true,
  "retryable_errors": [
    "MODEL_TIMEOUT",
    "MODEL_RATE_LIMIT",
    "PROVIDER_TEMPORARY_FAILURE",
    "OUTPUT_REPAIR_FAILED"
  ]
}
```

区分：

- 自动有限修复：同一次任务内修复格式；
- 重试：失败后从原 cursor 再执行；
- 重新生成：用户主动要求产生新版本；
- 修改：基于现有 Artifact 创建修订版本；
- 恢复：暂停后继续同一 Run。

这些操作不能共用一个模糊的 `retry` 动作。

## 16. Command Definition

通用命令：

```text
inspect
start
pause
resume
cancel
retry_failed
approve
edit_artifact
request_ai_revision
regenerate_artifact
inspect_impact
```

`select_final` 属于 Project/Candidate Runtime 命令，不属于某个 Capability Definition。任何 Capability 生成的 Candidate 都通过统一 Final Selection API 处理。

Capability 可限制命令适用步骤和状态：

```json
{
  "id": "regenerate_artifact",
  "allowed_run_statuses": [
    "waiting_approval",
    "paused",
    "completed"
  ],
  "allowed_artifact_types": [
    "story_bible",
    "episode_cards",
    "script_unit"
  ],
  "requires_confirmation": true
}
```

Registry 声明“允许什么”，Runtime 根据当前真实状态决定“现在能不能执行”。

## 17. Step Transition

```json
{
  "when": "approved",
  "to": "split_episodes"
}
```

原合同列出的条件（不等于当前已全部实现和验收）：

- `completed`
- `approved`
- `user_continue`
- `user_stop`
- `incomplete_material_confirmed`
- `all_batch_items_succeeded`
- `failure`
- `volume_fit_sufficient`
- `expansion_strategy_confirmed`
- `adaptation_selection_and_creation_config_confirmed`

条件必须来自 Runtime 事实或结构化用户动作，不从模型回复文本中猜测。

2026-09-10 G4.77实现核定：成功推进与审批路径已按Runtime提交事实匹配`when`，不再直接取`next[0]`。当前接线覆盖`completed`、`approved`、`all_batch_items_succeeded`、`volume_fit_sufficient`、`expansion_strategy_confirmed`和`adaptation_selection_and_creation_config_confirmed`；其中批次成功要求当前步骤存在任务且全部为`succeeded`，不能把空批次、部分成功、失败或取消当作全部成功。多个已满足条件指向同一目标可以合并；指向不同目标或没有匹配条件时，返回明确冲突并回滚当前推进事务，不按清单顺序猜测。普通审批、单选、改编选择、扩写确认、重生成审批回接及质量审核均已核对调用入口。上述是生产源码接线，新增Go行为测试未运行，编译/静态检查不代替真实验收。

2026-09-10 G4.78补接`incomplete_material_confirmed`：确切封存输入存在缺集且continuation policy由用户确认，或当前批次部分成功决策与确切获批版本集合一致时，成功/审批推进可使用该事实；参考剧本聚合只使用实际来源步骤和当前选用版本，不继承同Run其他步骤的部分决策。失败项已经补齐时旧决策不再产生该事实，聚合输入也必须包含对应当前输出。编辑待确认的部分结果生成替代审批时，重新绑定决策和提示，要求用户批准新版本。新增Go用例未执行，以上为生产源码接线，不是行为验收通过。

2026-09-10 G4.79补接`user_continue`：完成事实没有匹配自动后继、但清单存在唯一用户继续目标时，复用现有审批表/决策快照/版本绑定创建`scope=workflow_transition`、`subject_kind=transition`的独立确认。该请求不是已经发生的用户继续事实；只有用户批准这个确切请求后才产生`user_continue`并推进。目标、来源步骤、封存输入ID和已确认版本引用由服务端固定，客户端不能提交目标或扩写策略。普通Resume不能跳过待确认；取消仍彻底结束，不执行后继。普通审批回接、Runtime步骤和质量审核接入该机制；质量审核的既有单一聚合后继约束保留。前端通过服务端registry投影到已有action_list，而不是扩写表单。Go行为用例未执行，源码接线不等于真实验收。

2026-09-10 G4.80补接`failure`：状态化步骤真实最终失败后，重试已耗尽/不可重试且整批已结束，才选择唯一failure后继。原步骤/任务/尝试保持失败，新步骤独立pending，Run paused，用户通过既有继续入口启动。后继输入来自失败步骤的确切输入依赖链；冻结失败项、已完成工具参数/结果摘要及旧checkpoint引用，新步骤不恢复旧RunState。未知写入、未结束交互进程、保护失败、独立重生成计划、输入/写锁变化及歧义不跳转；非法分支不阻止原失败报告落库，真实存储故障则整体回滚。首次继续重验确切决策和当前事实。相关Go行为用例尚未执行，不能称真实联合验收通过。

2026-09-10 G4.81注册前校验：Go编译器及Node合同校验拒绝原十种名称之外的条件，不修正大小写或空白；同一条件指向多个目标及failure自环拒绝，同目标重复条件和字符串completed简写保留。同一步骤的重试使用既有retry policy。manifest校验schema列出全部已知条件，但`user_stop`为保留且尚不可执行，当前注册明确拒绝，不因名称在枚举内就视为可用。Go transition对象拒绝额外字段，保留已编译快照的When/To兼容读取。最后Node合同回归40项通过；新增Go源码测试未执行，不代替真实Skill安装或Runtime推进验收。

仍未接通的原合同事件为`user_stop`。G4.79已异步询问停止是彻底取消还是允许仅整理已有结果的收尾分支，未收到答复前保持原CancelRun语义，不认为问题已授权；本次拒绝不可用清单不等于删除这项原需求。本节列出的原范围和真实验收欠账保留，不能宣称任意条件Workflow已可插拔运行。

2026-09-10 G4.82调度核定：Resume按当前事务内Run和确切后继状态处理连续Runtime步骤，不能因下一步骤kind可由Worker执行而把runtime执行器入队。待审批和真实终点停止自动推进；已由推进器启动的质量审核保留原任务。单次命令遇到已执行过的同名Runtime步骤时在新的pending实例让出，Run保持paused，后续继续是独立命令。所有Runtime执行器保留实际后继/完成结果，run.completed仅由真实终点状态提交产生，不以聚合完成代替整个Run完成。上述Go行为用例尚未运行。动态工作流首步batch仍被启动门禁拒绝，其后续批量规划/领取/提交闭环继续核查，不能据此宣称任意清单已可运行。

第一版流程应保持可验证的有限状态图。模型不能自由返回任意下一 Step ID。

### 托管 Skill 的逐集批量协议

2026-09-10 G4.83实现范围：上传包原先不允许任何batch；现可声明model/batch加`worker.structured_content`，沿用`episode_no`、`natural_episode_order`、串行/并行、`max_items_per_task=1`及`preserve_success_retry_failed`。输出必须为单个many基数的pending_approval产物，审批为必需batch_checkpoint（scope可省略并使用原默认batch）。包不引用平台内部阶段或响应适配器；普通SKILL.md仍可内联调用，不因这项支持被强制变成工作流。

包内直接输出Schema经ContextPack交给SDK，逐项返回自身payload而非旧平台信封。每项`episode_no`必须与权威Task的`episode:N`相符；成功产物与资产/配置/决策依赖同事务保存，全部结算后绑定确切版本集合审批。暂停/恢复、串行领取、并行结算和失败重试沿用Runtime，不新造模型循环。服务端目标集数上限为既有MaxAssetSetEpisodeNo=10000。

该范围不是任意批处理图、任意item_key或多项合并器支持。示例包仅在`fixtures/skills/batched/episode-review-workflow`，未安装到用户目录。源码与分层SDK/UI测试证据见Goal进度G4.83；Go行为与真实用户联合未验收，不据此签署全量能力达标。

## 18. Shared Workflow Module

为避免三个 Skills 复制相同后续步骤，引入内部 Workflow Module：

```text
script_development_from_story_seed
```

建议包含：

```text
story_seed
-> series_blueprint
-> episode_cards
-> script_context
-> script_unit[]
-> scripts
```

另一个内部模块：

```text
script_generation_from_episode_cards
```

包含：

```text
episode_cards
-> script_context
-> script_unit[]
-> scripts
```

Workflow Module：

- 不是用户可选 Skill；
- 不出现在 Skill 菜单；
- 没有独立主对话；
- 不创建第二个 Project；
- 默认不创建第二个顶层 Run；
- 由父 Capability Definition 引用；
- 复用 Step、Context、Schema 和 Executor；
- 事件仍归属父 Run；
- Artifact 保留父 `capability_id`，同时可记录 `workflow_module_id`。

## 19. 三个第一版 Capability Definitions

以下步骤列表必须与 `../capabilities/v1/*.json` 完全一致。确认动作由各 Step 的 `approval` 定义，不另造 `confirm_*` 虚拟 Step。

### 19.1 小说转剧本

```json
{
  "id": "novel_to_script",
  "version": "1.2.0",
  "label": "小说转剧本",
  "kind": "domain_workflow",
  "accepted_asset_kinds": ["text", "document"],
  "required_providers": [
    "content_model_provider",
    "document_parser"
  ],
  "steps": [
    "ingest_source",
    "build_story_bible",
    "review_volume_fit",
    "split_episodes",
    "build_episode_cards",
    "generate_script_units",
    "aggregate_scripts"
  ]
}
```

审批：

- 生成配置：启动确认；
- `story_bible`：checkpoint；
- `episode_split`：checkpoint；
- `episode_cards`：checkpoint；
- `script_unit[]`：batch checkpoint；
- `scripts`：聚合视图，不单独编辑。

### 19.2 非小说文本转剧本

```json
{
  "id": "non_novel_to_script",
  "version": "1.0.0",
  "label": "非小说文本转剧本",
  "kind": "domain_workflow",
  "accepted_asset_kinds": ["text", "document"],
  "required_providers": [
    "content_model_provider",
    "document_parser"
  ],
  "steps": [
    "ingest_source",
    "build_material_bank",
    "review_volume_fit",
    "build_story_seed",
    "build_series_blueprint",
    "build_episode_cards",
    "generate_script_units",
    "aggregate_scripts"
  ]
}
```

审批：

- 生成配置：启动确认；
- `material_bank`：checkpoint；
- `story_seed`：checkpoint；
- `series_blueprint`：checkpoint；
- `episode_cards`：checkpoint；
- `script_unit[]`：batch checkpoint。

### 19.3 视频参考创作

```json
{
  "id": "video_reference_creation",
  "version": "1.0.0",
  "label": "视频参考创作",
  "kind": "domain_workflow",
  "accepted_asset_kinds": ["video"],
  "required_providers": [
    "content_model_provider",
    "multimodal_video_provider",
    "media_processor"
  ],
  "steps": [
    "ingest_source",
    "extract_video_scripts",
    "aggregate_reference_scripts",
    "analyze_reference_scripts",
    "propose_adaptation_options",
    "build_adaptation_brief",
    "build_story_seed",
    "build_series_blueprint",
    "build_episode_cards",
    "build_script_contexts",
    "generate_script_units",
    "review_script_set",
    "aggregate_scripts"
  ]
}
```

关键规则：

- 视频上传、跨批追加、排序和缺集检查属于通用 Asset Set 能力，在 Run 创建前完成；
- 未收到“上传完成”确认且 Asset Set 未 Seal，不允许启动本 Skill；
- `ingest_source` 固化已 Seal 的精确 Asset Set Version，Run 内不继续追加视频；
- `extract_video_scripts` 为该封存版本中的每个视频创建一个可重试 Task；
- 集号和展示顺序来自 Asset Metadata，不依赖模型输出顺序；
- 缺集继续必须在 Seal Asset Set 时形成结构化用户确认；
- `video_script_unit[]` 完成子项编辑后使用统一 `batch_checkpoint`；
- `reference_scripts` 是聚合视图；
- `script_analysis` 是视频 Skill 内步骤，不是第四个 Skill；
- `propose_adaptation_options` 的转换审批同时固化 Adaptation Decision Snapshot 和 Creation Config Snapshot；
- `adaptation_brief` 确认前不能进入新剧本开发；
- 后半段引用共享 Workflow Module；
- 整个 Run 的 Capability ID 始终不变。

## 20. Public Registry API

前端只需要公开投影，不读取 Prompt、Rules、Executor 和内部 Schema 路径。

### 20.1 列出 Capabilities

```http
GET /api/v1/capabilities
```

响应：

```json
{
  "data": {
    "items": [
      {
        "capability_id": "novel_to_script",
        "version": "1.0.0",
        "label": "小说转剧本",
        "description": "",
        "status": "available",
        "accepted_asset_kinds": ["text", "document"],
        "commands": ["start"],
        "ui_entry": {
          "icon_key": "book_open_text",
          "menu_order": 10
        }
      }
    ]
  }
}
```

### 20.2 获取公开 Manifest

```http
GET /api/v1/capabilities/{capability_id}
```

公开：

- ID、版本、名称和说明；
- 可用性；
- 输入类型；
- 用户可见步骤；
- 用户可用命令；
- UI 标签。

不公开：

- Provider 凭据；
- 内网 URL；
- System Prompt；
- 完整 Rules；
- Executor 实现路径；
- 服务端文件路径；
- Guard 内部策略。

### 20.3 消息路由结果

Project Message 响应建议增加：

```json
{
  "routing": {
    "capability_id": "novel_to_script",
    "source": "agent_routed",
    "status": "needs_clarification",
    "missing_inputs": [
      "target_episode_count",
      "episode_duration_minutes"
    ]
  }
}
```

## 21. Internal Registry Interface

建议：

```go
type CapabilityRegistry interface {
    List(ctx context.Context) []CapabilityDefinition
    Get(ctx context.Context, id, version string) (CapabilityDefinition, bool)
    Current(ctx context.Context, id string) (CapabilityDefinition, bool)
    Availability(ctx context.Context, id string) CapabilityAvailability
    Validate() error
}
```

Runner 只能通过 Registry 获取 Definition，不在 Runtime 内维护第二份流程常量。

## 22. 启动校验

服务启动时 Registry 必须校验：

1. Capability ID 唯一；
2. Version 合法；
3. Step ID 在 Capability 内唯一；
4. Entry Step 存在；
5. Transition 目标存在；
6. 不存在无出口的非终止步骤；
7. Artifact Type 已注册；
8. Input/Config/Output Schema 存在；
9. Prompt 和 Rule 路径存在、非空、UTF-8 可读；
10. Executor 已注册；
11. 必填 Provider Requirement 有健康检查；
12. Approval Policy 合法；
13. Batch Step 定义 item key 和恢复策略；
14. Completion 条件可以从 Runtime 状态确定；
15. Shared Workflow Module 的输入输出兼容；
16. Public UI Metadata 不包含秘密和内部路径。

关键 Definition 不合法时：

- 服务启动失败，或对应 Capability 标记 `unavailable`；
- 不允许悄悄回退到模型自由执行；
- 健康检查返回明确错误码。

## 23. Completion Definition

Capability 必须声明完成条件：

```json
{
  "terminal_step_ids": [
    "aggregate_scripts"
  ],
  "required_artifacts": [
    {
      "artifact_type": "script_unit",
      "required_status": "confirmed",
      "coverage": "target_episode_count"
    },
    {
      "artifact_type": "scripts",
      "required_status": "confirmed",
      "coverage": "one"
    }
  ],
  "requires_terminal_approval": false
}
```

`requires_terminal_approval=false` 表示 `aggregate_scripts` 后不再创建一个重复的 Run 级确认点；前序 `script_unit[]` 批次已经统一确认。Run 产出的 Script Candidate 是否被设为作品最终稿，使用 Project 级 `final_selection` Approval，不能混入 Capability Completion。

Run 完成必须由 Runtime 确定：

- 所有必需 Step 完成；
- 所有必需子任务成功；
- 所有必需 Artifact 版本通过确认；
- 不存在未解决的关键 Approval；
- 集数覆盖符合配置；
- 聚合视图已经基于正确版本刷新。

模型输出“已完成”不能完成 Run。

## 24. Event 要求

所有 Capability 使用同一套通用事件类型：

```text
agent.decision_created
run.started
step.started
task.started
artifact.version_created
task.completed
approval.requested
approval.resolved
step.completed
step.failed
run.paused
run.resumed
run.cancelled
run.completed
```

事件 payload 至少包含：

```json
{
  "capability_id": "",
  "capability_version": "",
  "step_id": "",
  "step_run_id": "",
  "artifact_refs": [],
  "task_cursor_ref": ""
}
```

事件文案由前端标签和服务端错误码共同渲染，不使用英文硬编码消息作为唯一用户提示。

## 25. 安全边界

- Registry Definition 只从受信任发布包加载；
- 不执行用户上传的 Prompt、Rule、脚本或 Manifest；
- 相对路径必须限制在允许目录；
- 禁止路径穿越；
- Capability Definition 不保存 Key；
- Provider 凭据只存在服务端安全配置；
- 浏览器只得到 Public Manifest；
- Capability 调用必须经过 Guard；
- 用户显式引用不能绕过权限和可用性；
- MCP 或外部 Tool 需要独立允许列表；
- 所有写操作保留 Run/Event/Version 记录。

## 26. 不采用的方案

### 26.1 每个 Skill 一个完整 Agent

不采用。会重复对话、上下文、状态、文件、审批和版本系统。

### 26.2 Main Agent 自由决定任意流程

不采用。Main Agent 只在 Registry 提供的候选中路由，Runtime 执行确定状态机。

### 26.3 用 SourceMode 代替 Capability

不采用。`video`、`novel` 是输入来源属性，不足以表达完整业务能力。

### 26.4 把 Rules 全部放入 Agent System Prompt

不采用。Rules 由具体 Step 按需加载，避免上下文污染。

### 26.5 第一版支持用户安装任意插件

不采用。先做好三个内置 Skills 和稳定协议。

### 26.6 视频 Skill 中途切成非小说 Skill

不采用。视频 Run 只复用非小说共享子图，不改变顶层 Capability 身份。

## 27. 测试要求

### 27.1 Registry 单元测试

- 三个 ID 唯一；
- 版本合法；
- 所有路径存在；
- 所有 Schema 可加载；
- 所有 Step 转换有效；
- 所有 Artifact Type 已注册；
- 所有 Executor 已注册；
- Shared Workflow 输入输出兼容；
- Public Manifest 不泄露内部字段。

### 27.2 零 Capability 测试

在 Registry 为空时验证：

- Agent 可以普通对话；
- Agent 可以追问；
- Agent 可以查看 Project 和 Asset；
- Agent 可以说明当前没有剧本生产能力；
- Agent 不创建 Domain Run；
- Agent 不创建未知 Artifact；
- 前端 Skill 菜单显示为空或明确不可用；
- 产品不会崩溃或把 Agent 隐藏。

### 27.3 路由测试

- 显式引用三个 Skills；
- 自然语言正确路由；
- 只上传不启动；
- 模糊来源追问；
- 小说开头作为扩写大纲时不误走小说链；
- Capability 不可用时说明缺失 Provider；
- 当前 Project 有活动写 Run 时阻止第二个 Run。

### 27.4 版本恢复测试

- Run 固定 Definition Version；
- 服务升级后旧 Run 使用旧 Definition 恢复；
- 删除仍被未完成 Run 使用的 Definition 时启动失败；
- PATCH 版本升级不改变状态语义；
- MAJOR 版本不静默迁移旧 Run。

### 27.5 视频复用测试

- 视频 Run 全程保持一个 Capability ID；
- Brief 确认后进入共享子图；
- 共享子图产生的 Artifact 归属父 Run；
- 非小说独立 Skill 与视频 Skill 的 Artifact 不串用；
- 视频停止在任一已确认检查点后可恢复。

## 28. 实施顺序

1. 建立 Registry 接口和静态内置 Definition；
2. 为现有小说/非小说流程补 `capability_id` 和版本；
3. 把 Artifact 顺序移入 Definition；
4. 把审批策略移入 Step Definition；
5. 把 Prompt/Rules 路径移入 Step Definition；
6. 把 Schema 引用移入 Artifact Registry；
7. 建 Public Registry API；
8. 前端 Skill 菜单读取 Public Manifest；
9. 用现有两条链做兼容回归；
10. 用最小测试 Capability 验证“不改 Runtime 核心即可注册”；
11. 再实现视频 Capability。

## 29. 完成门禁

本契约完成实现必须满足：

- Agent Core 不依赖任何 Domain Capability；
- Registry 为空时 Agent 仍可用；
- 三个用户 Skills 都能通过 Definition 表达；
- 新增测试 Capability 不修改 Runtime 流程分支；
- Run 固定 Capability ID 和 Version；
- Step、Artifact、Approval 和 Context 均可追踪到 Capability；
- Rules 只由 Step 按需加载；
- 视频后续复用共享 Workflow，不切换顶层 Skill；
- Registry 无效时不会回退成模型自由执行；
- 前端 Skill 菜单来自 Public Manifest；
- Provider 不可用时能力状态真实可见；
- 所有状态变化仍由 Guard + Runtime 控制。

## 30. 阶段 3 第 11 步前 P0 补充

内容事实、`script_unit/script_handoff` 分层和质量审核的权威合同见：

```text
content-semantics-and-quality-review-contract.md
```

Registry 在第 11 步 `11A` 增加：

```text
StepKind: review
StepDefinition.result_schema_ref
StepDefinition.gate_policy_ref
ApprovalType: conditional_review
ApprovalScope: quality_review
```

三个 Capability 都在整个剧本 Batch Approval 之后、`aggregate_scripts` 之前插入 `review_script_set`。它是共享内部 Step，不注册为第四个 Domain Capability，不进入 Skill 菜单。

当前 `novel_to_script@1.4.0`、`non_novel_to_script@1.3.0` 和 `video_reference_creation@1.2.0` 均包含该 Step。已存在 Run 继续固定其启动时版本，不静默改写执行图；历史版本装载与恢复在迁移兼容批次单独验收。

## 31. Prompt/Rules 本地化约束

第 11 步 `11B` 完成后：

- `prompt_ref` 只能引用当前项目的 `design/prompts/**`；
- `rule_refs` 只能引用当前项目的 `design/rules/**`；
- Batch 内部阶段的 `prompt_ref` 同样只能引用 `design/prompts/**`；
- 引用必须使用相对 Manifest 的项目内路径，不能引用旧项目目录、用户绝对路径或项目外文件；
- Registry 启动时校验路径范围、文件存在且非空，任一失败时 Capability 标记为不可用；
- Context Pack 按 Manifest 顺序加载 Rule，并固化每份 Prompt/Rule 的 `ref`、正文和 `content_hash`；
- 不允许在路径失效时回退到旧仓库、内置默认 Prompt 或模型自由执行。

当前本地资产使用版本化文件名。文件内容变化必须创建新版本路径或同步提升对应 Capability 版本，不能静默覆盖已启动 Run 的执行输入。

## 32. Batch 内部阶段结果模式

`batch.preparation` 和 `batch.task_stage` 使用 `result_mode` 明确内部结果边界：

```text
checkpoint | artifact
```

- `checkpoint` 只保存不可变 Task Result Checkpoint，不创建用户 Artifact 和 Approval；
- `artifact` 使用该阶段 Prompt/Schema 生成 Step 的正式输出，并继续遵守 Step 的 Artifact、Dependency 和 Approval 合同；
- `preparation` 不允许声明 `artifact`；
- 小说 `build_story_bible` 固定为 `source_analysis(checkpoint) -> story_bible_aggregate(artifact)`；
- Runtime 不能根据模型返回字段自行改变 `result_mode`。
