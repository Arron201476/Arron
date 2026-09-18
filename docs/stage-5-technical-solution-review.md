# 内容生产 Agent 阶段 5 技术方案审查

状态：阶段 5 T0-T7 已完成，等待产品负责人确认进入阶段 6  
版本：v1.0  
日期：2026-08-05

## 1. 当前结论

当前技术方向成立，不需要推倒重来：

```text
Web Workbench
-> Agent API
-> General Content Agent Shell
-> Capability Registry / Guard
-> Business Runtime
-> Executor Queue
-> Eino Execution Graph
-> Provider / Tool Adapter
```

当前已经覆盖 Registry、Runtime、Artifact Version、Dependency、Approval、Event、Asset、SSE、通用 Worker、独立 Video Worker 和三份 Capability Manifest。小说与非小说已闭环；视频已验证真实单集、真实多集故障场景、逐集保存和失败恢复语义。豆包 504 归入阶段 7 Provider 接入风险，40 集整剧验收归入阶段 9/发布门禁。

正式结论：

1. 阶段 4 已由产品负责人正式通过，阶段 5 已进入进行中；
2. T0-T7 已完成，小说、非小说、视频、共享质量审核、通用生产底座和阶段 6/7 交接均有证据；
3. 视频已经具备真实媒体载荷、逐集任务、独立 Worker、聚合和后续共享链路；
4. 视频网关稳定性必须在阶段 7 关闭，真实 40 集内容、性能和恢复必须在阶段 9 通过；两者不再错误阻塞阶段 5；
5. Main Agent、Content Worker 与 Video Worker 均使用受控 Eino Graph，Business Runtime 仍是业务状态唯一真源。

## 2. 审查范围

本轮检查：

- `docs/` 中的架构、Runtime、Capability、Context、Asset、API、前端和迁移合同；
- `capabilities/v1/` 的三个 Capability Manifest；
- `schemas/v1/`、Prompt、Rules 和 Response Adapter 引用；
- 顶层 `backend/` 的 Server、Shell、Registry、Runtime、Worker 和 Provider；
- `novel2script_agent_project/` 中可复用的 Eino Main Agent、Guard 和内容 Worker 实现；
- 阶段 2 机器验收矩阵和顶层 Go 测试可执行性；
- Eino 官方仓库和官方示例中的 ADK、Composition、Graph、Workflow、Batch、Interrupt/Resume 模式。

本文件最初为只读审查；T0、T1 实施结果现已回写。旧项目 Runtime 始终没有接回新项目。

## 3. 代码与资产归属

### 3.1 唯一生产边界

新项目的生产边界固定为：

```text
内容生产Agent项目/
├─ backend/
├─ frontend/                  # 阶段 6 创建
├─ capabilities/
├─ schemas/
├─ design/prompts/
├─ design/rules/
├─ docs/
├─ acceptance/
└─ data/
```

`novel2script_agent_project/` 是迁移参考副本：

- 不参与新服务编译；
- 不参与新服务启动；
- 不作为 Prompt、Rule、Schema 的运行时路径；
- 不读写新项目数据库；
- 不与新项目长期双写；
- 只允许按明确工作包提取代码或测试，再改成新项目包名和合同。

顶层 Registry 已禁止 Manifest 引用 `novel2script_agent_project/`，该约束保留。

### 3.2 可复用内容

允许迁移：

- Eino Main Agent 的“模型判断 -> 确定性 Guard”结构；
- 上下文压缩和选区优先规则；
- 小说拆集、分集卡和剧本生成的 Eino Worker 图；
- Provider 错误分类、输出修复和 Trace 处理经验；
- 文本链 Prompt、Rules、测试 Fixture 和验收场景。

禁止直接复用：

- 以 `SourceMode` 为流程身份的旧 Runtime；
- 旧 Workspace/Runtime 双数据库写路径；
- 固定两条链的 Artifact 顺序和前端枚举；
- 旧项目运行数据、Key、临时文件和构建缓存。

## 4. 当前发现

### P0-1：阶段 5 正式门禁已建立

`project-lifecycle-stage-gates.md` 已补充阶段 5 交付物和门禁。阶段 4 已正式确认，当前主阶段已切换为阶段 5。本项关闭。

### P0-2：Agent Shell 与真实通用对话已完成

当前已经具备可替换 `AgentService`、结构化 `AgentDecision`、Message Context、Agent Message 与 `proposed_action` 持久化。OpenAI-compatible 控制模型通过 Eino Main Agent Graph 处理普通对话和语义路由；未配置模型时保留确定性追问、能力发现和显式/别名路由，不伪造通用模型回答。

T1 实施结果：

- `AgentService` 输入包含 Conversation、用户请求、最近消息和 Registry 生成的可用能力目录；
- 通过 Eino Main Agent Graph 产生结构化 `AgentDecision`；
- 显式 Skill 直接优先，自动路由必须命中 Registry、允许 `auto_route` 且置信度达到阈值；
- 模型不能产生 Runtime ID 或写动作，只有确定性 Guard 可以生成 `collect_run_configuration`；
- 经过 Runtime 引用校验后原子保存 Agent Message、Decision 和 Proposed Action；
- 仅返回 `proposed_action`，不直接创建 Run；
- 用户提交配置卡后，Runtime 将提案升级为受版本与 Hash 保护的 `start_run` 确认卡；确认后前端再调用独立 Start Run API。

### P0-3：Main Agent 与 Worker Eino Graph 已完成

顶层 `backend/go.mod` 已固定 Eino v0.9.12，Main Agent 使用：

```text
interpret_intent
-> apply_deterministic_guard
```

Worker 当前执行：

```text
build_request
-> invoke_model
-> parse_or_repair
-> validate_provider_result
-> Runtime Submit
-> Runtime Commit
```

Worker 通过 Eino Graph 执行单个 Task；非法 JSON、明确的 `result_schema_ref` 和内部 checkpoint 结构错误只允许一次受控修复。兼容旧 Response Adapter 的步骤在 Worker 校验包装和对象结构，Runtime 在 Adapter 后执行最终完整 Schema 与业务约束校验。

技术处理：

- Main Agent 使用 Eino Graph 或受控 ADK Agent；
- 模型生产步骤使用 Eino Graph：Context Validate -> Prompt Render -> ChatModel/Tool -> Parse/Repair -> Output Validate；
- Runtime 继续负责跨步骤状态、审批、批次、重试、版本和恢复；
- 不把整个业务 Run 镜像成第二套 Eino 状态机；
- 不用 Eino Checkpoint 替代 Runtime 数据库。

### P0-4：视频 Capability 当前不可启动（已关闭）

原审计发现视频 Manifest 把来源收集放进 Run，且缺少媒体处理、视频 Worker 与参考剧本聚合执行器。现已统一为“Run 外收集、Seal 后启动”：

- 通用 Asset Set 负责跨批上传、集号识别、人工排序、缺集确认和 Seal；
- `runtime.ingest_source` 固化精确 Asset Set Version；
- `workflow.video_script_extract` 由独立 Video Worker 执行；
- `runtime.aggregate_reference_scripts` 聚合已确认逐集剧本。

Provider Adapter 现通过受控 `video_url` Data URL 发送预处理后视频，不发送本地路径。

技术处理：

1. 视频来源收集留在通用上传层，Run 只接受已 Seal Asset Set Version；
2. Asset Set Version 作为视频批次顺序和完整度真源；
3. Media Processor 生成符合 Provider 限制的派生视频；
4. Multimodal Provider 接收受控的 `MediaPart`，禁止把本地路径直接发给模型；
5. 每个视频生成一个 Task 和 `video_script_unit`；
6. 启动 Run 前 Seal，并检查重复、缺集、失败和人工顺序；
7. 用户显式确认不完整继续后才聚合；
8. 聚合后依次生成 `reference_scripts`、`script_analysis`、改编方案和 `adaptation_brief`；
9. Brief 确认后复用非小说后续执行模块，但 Run 的 Capability ID 保持不变。

### P0-5：质量审核 Executor 发现缺口已关闭

三个 Manifest 引用的 `workflow.shared_script_quality_review` 已进入统一 Executor Registry，Worker 默认订阅列表由 Registry 生成。`workflow.video_script_extract` 只注册给独立 Video Worker；通用 Content Worker 显式拒绝订阅它。

技术处理：

- Executor Registry 在服务启动时汇总 Runtime Executor、Worker Workflow 和 Provider 能力；
- Capability 可用性必须同时校验所有必需 Executor；
- Worker 默认订阅列表从同一 Executor Registry 生成或做启动时双向一致性校验；
- 缺 Executor 时 Capability 标记为 `unavailable`，不能让 Run 进入永久排队。

### P1-1：Runtime 仍有 Capability ID 分支

当前在来源匹配、Script Context 映射和非小说提交逻辑中仍直接判断三个 Capability ID。这与“新增测试 Capability 不修改 Runtime 核心分支”的合同不一致。

技术处理：

- 来源类型约束进入 Manifest `entry_policy`；
- Script Context 的 `source_kind` 和 Adapter 进入 Step Definition；
- 特定提交后动作进入声明式 Hook/Transition 或具名 Executor；
- Runtime 只按通用 Step Kind、Executor Ref、Artifact Contract 和 Transition 执行。

允许 Runtime 保留通用 executor switch，但不允许在通用 executor 内按 Capability ID 改业务语义。

### P1-2：Provider 与工具角色已按边界实现

OpenAI-compatible 模型配置已按以下角色拆分，并为 Control/Content 保留旧环境变量兼容回退：

- `ControlModelProvider`
- `ContentModelProvider`
- `MultimodalVideoProvider`
- `ImageUnderstandingProvider`

以下非模型工具角色已由 T5/T6 实现：

- `DocumentParser`
- `MediaProcessor`
- `ExportProvider`

这些是接口角色，不要求七个独立服务。一个公司网关 Adapter 可以实现多个模型角色，但配置、限额、超时、载荷和可观测性必须分开。PDF Parser、FFmpeg/FFprobe 和正式模型网关仍由部署环境提供。

### P1-3：架构文档与机器合同统计漂移

旧架构基线记录为 28 个 Step、8 个 Response Adapter；当前机器校验结果为：

```text
Capabilities: 3
Steps: 34
Response adapters: 14
Acceptance cases: 96
```

文档统计已经同步修正。以后统计只由 validator 输出，不手工维护第二份数字。

### P1-4：Go 测试执行受 Windows Application Control 影响（已规避）

2026-08-03 T0 回归结果：

- `internal/runtime` 全包通过；
- `internal/httpapi` 全包通过，包含消息提案、确认防篡改/重放、Approval、Action Projection 和非小说重生成闭环；
- `internal/worker` E2E、`internal/shell`、`internal/capability`、`internal/modelprovider` 与 `cmd/worker` 均通过；
- Capability Node validator 通过：3 个 Manifest、34 个 Step、14 个 Response Adapter；
- `go test ./...` 仍可能被 Windows 临时拒绝启动某个测试 EXE；Runtime、Worker 和 HTTP API 已使用固定路径测试二进制完成全回归，其余包通过普通 `go test`。

阶段 8/9 正式测试仍应使用公司允许的签名/白名单目录或 CI Runner，避免依赖本机偶发策略；阶段 5 不再以未执行测试包作为通过依据。

## 5. 冻结的架构边界

### 5.1 单一调度权威

```text
Capability Manifest
  声明步骤、输入输出、审批、上下文和 Executor

Business Runtime
  编译后的确定性状态推进、持久化、互斥和恢复

Eino
  Main Agent 决策图和单个 Executor 内的模型/工具编排
```

跨审批的业务流程不同时由 Runtime 和 Eino 各维护一套 cursor。

### 5.2 Agent 决策合同

```json
{
  "reply": "",
  "intent": "chat|clarify|inspect|propose_capability|revise|control_run|unsupported",
  "confidence": 0.0,
  "capability_ref": null,
  "target_ref": null,
  "clarification": null,
  "proposed_action": null
}
```

Guard 必须校验：

- Project、Conversation、Asset、Run 和 Artifact 归属；
- Capability 可用性和版本；
- 当前 Run 状态允许的动作；
- 焦点目标和版本是否仍有效；
- 是否需要用户确认；
- 是否存在活动写 Run；
- 输入与 Capability 类型是否匹配；
- 模型是否试图创建未注册 Artifact 或跳过审批。

### 5.3 消息与 Run 分离

```text
POST message
-> 保存用户消息
-> AgentDecision
-> Guard
-> 保存 Agent 消息
-> 返回结构化配置卡/确认卡

用户确认
-> POST start_run / resolve_approval / revise_artifact
-> Runtime command
```

普通聊天响应不得隐式启动生成。

### 5.4 Executor 发现

Executor 分为：

- `runtime.*`：确定性本地执行；
- `worker.*`：通用模型内容执行；
- `workflow.*`：Eino 模型/工具图；
- `tool.*`：文档、图片、媒体和导出工具。

启动检查：

```text
Manifest references
-> Executor Registry
-> Provider availability
-> Prompt/Rules/Schema references
-> Capability availability projection
```

## 6. 三条主链

### 6.1 小说

```text
ingest_source
-> build_source_manifest
-> build_story_bible
-> review_volume_fit
-> split_episodes
-> build_episode_cards
-> build_script_contexts
-> generate_script_units
-> review_script_set
-> aggregate_scripts
```

旧项目的小说拆集 Eino 图按 Executor 迁移，不能把旧 Runtime 一并复制。

### 6.2 非小说

```text
ingest_source
-> build_source_manifest
-> build_material_bank
-> review_volume_fit
-> build_story_seed
-> build_series_blueprint
-> build_episode_cards
-> build_script_contexts
-> generate_script_units
-> review_script_set
-> aggregate_scripts
```

以固定测试材料从 0 到 1验收，覆盖体量不足后的扩写策略确认。

### 6.3 视频参考创作

```text
ingest_source
-> extract_video_scripts
-> aggregate_reference_scripts
-> analyze_reference_scripts
-> propose_adaptation_options
-> build_adaptation_brief
-> build_story_seed
-> build_series_blueprint
-> build_episode_cards
-> build_script_contexts
-> generate_script_units
-> review_script_set
-> aggregate_scripts
```

视频链不能通过一次性把压缩包直接交给模型实现。压缩包只可作为上传传输形式，服务端仍需展开、校验、排序并为每集创建独立 Task。

### 6.4 共享质量审核

三个主链在全部 `script_unit` 完成子项级编辑，并由用户对整个剧本步骤统一确认后，自动进入 `review_script_set`：

```text
confirmed script_unit[]
-> automatic quality review
-> no blocking issue: aggregate_scripts
-> blocking issue: quality review card
-> user chooses revise / accept_with_risk / stop
```

约束：

- `review_script_set` 是共享内部步骤，不显示为第四个 Skill；
- 不在每次生成后询问“是否审核”，避免把固定质量门禁变成可跳过选项；
- 审核输入必须绑定精确的已确认 `script_unit` Version Set；
- 审核期间上游版本变化时，旧审核结果失效；
- 无阻断问题时自动继续聚合，不增加重复确认；
- 有阻断问题时进入结构化处理卡，用户可以按问题定位修改后重新审核；
- `accept_with_risk` 必须保存风险项、操作者和版本快照；
- 审核不能直接改写剧本，只能提出问题和推荐处理路径。

## 7. Eino 采用方式

Eino 官方当前同时提供 ADK、Graph/Workflow、Batch、Interrupt/Resume 和 GraphTool。对本项目的采用规则：

1. Main Agent 第一版使用受控 Agent Graph，不直接开放无限 ReAct；
2. Domain Capability 作为可发现业务能力，但正式写操作必须转成 Runtime Command；
3. 内容生成、质量审核、视频解析等 Executor 使用 Eino Graph/Workflow；
4. 批量任务并发和租约由 Runtime Task Queue 控制，Eino Batch 只用于单个执行器内部可界定的小批处理；
5. Eino Interrupt 可用于执行器内部需要补充输入的局部场景，正式业务审批仍写 Runtime Approval；
6. Eino Callback 统一接入 Trace、Token、Latency 和 Error Code；
7. 暂不引入 DeepAgent、多 Agent Supervisor 或第二套 Agent 框架。

官方依据：

- https://github.com/cloudwego/eino
- https://github.com/cloudwego/eino-examples

## 8. Provider 与媒体载荷

统一 Provider 请求不能只使用字符串 Message。目标类型至少支持：

```go
type ContentPart struct {
    Kind       string
    Text       string
    AssetRef   string
    VariantRef string
    MIMEType   string
}
```

约束：

- API Key 只在服务端；
- Provider Adapter 只接受 Runtime 已授权的 Snapshot/Variant；
- Adapter 根据 Provider 限制选择上传文件、临时 URL、File ID 或内联网关引用；
- 原视频不被压缩结果覆盖；
- Provider 返回必须经过 Adapter、Schema Validator 和 Runtime Commit；
- Provider 限制均配置化，不把 10 MB 写死为产品真源；
- 真实网关最大时长和大小由接入测试固化为部署配置。
- 当前视频处理副本默认目标为 8 MiB、硬边界为 10 MiB，允许在 1-10 MiB 范围配置；
- 当前视频 Worker 部署默认并发为 1。真实样本在 2/3 并发下出现 HTTP 504，单并发仍有集级 504，因此提升并发必须重新通过网关负载验收；
- 批任务采用 `preserve_success_retry_failed`：兄弟任务全部收敛后才结束失败 Run，重试只重置失败项并保留成功 Artifact。

## 9. API 与前端交接

阶段 5 冻结以下前端依赖：

- Project Snapshot；
- Conversation Message；
- Agent Decision / Proposed Action；
- Capability Public Manifest 和 Availability；
- Run Snapshot；
- Step/Task 状态；
- Artifact/Version/Lineage；
- Approval；
- Impact Review / Regeneration Plan；
- Script Candidate / Final Selection；
- Quality Review；
- Asset / Asset Set / Upload Session；
- Project SSE 和 Run SSE。

前端不能：

- 根据文字回复猜当前步骤；
- 自行拼 Capability 步骤顺序；
- 自行判断审批是否可提交；
- 直接调用 Provider；
- 用本地临时状态替代 Artifact Version。

## 10. 正式实施顺序

### T0：阶段 5 合同收口

- 按 `stage-5-interaction-baseline-audit.md` 冻结 Composer、Approval 和 Public Action 映射；
- 拆分 Approval Action 与 Run/Task Control Action；
- 冻结 Message、AgentDecision、proposed_action 和确认卡绑定；
- 关闭本报告 P0；
- 冻结 Agent Decision、Executor Registry 和 Provider Part；
- 对齐 API、Manifest、Schema 和 Runtime 状态；
- 建立完整 Go 测试执行环境。

### T1：通用 Agent Shell 最小闭环

- Eino Main Agent Graph；
- 通用聊天、追问、能力发现和缺口说明；
- 显式 Skill 引用和自动路由；
- Guard；
- Message 与 Run 分离；
- 零 Capability 验收。

状态：已完成。验收记录见 `stage-5-t1-main-agent-acceptance.md`。

### T2：Executor Eino 基础

- 将 Eino 扩展到 Executor/Worker Graph；
- Executor Registry；
- Worker Graph；
- Callback/Trace；
- Schema Validate 和 Repair；
- Provider Role 配置。

状态：已完成。验收记录见 `stage-5-t2-executor-eino-acceptance.md`。

### T3：小说链迁移

- 迁移旧 Eino 拆集和内容图；
- 对齐新 Artifact/Approval；
- 使用同一本固定小说回归；
- 验证编辑、重新生成和下游影响。

状态：已完成。旧项目专用 Eino 外壳未复制；其输入校验、批次约束和输出校验已分别由 Manifest、Business Runtime 与通用 Eino Worker Graph 承担。验收记录见 `stage-5-t3-novel-migration-acceptance.md`。

### T4：非小说链迁移

- 完成 material bank 到 scripts；
- 用自建固定样本从 0 到 1；
- 覆盖体量不足和扩写策略；
- 复用共享剧本生产和质量审核。

状态：已完成。验收记录见 `stage-5-t4-non-novel-quality-acceptance.md`。

### T5：通用生产能力

- 文件解析；
- 图片通用输入；
- Asset Set；
- DOCX 导出；
- Candidate/Final；
- 删除、保留、备份和迁移。

状态：已完成。验收记录见 `stage-5-t5-general-production-acceptance.md`。

### T6：视频参考创作

- Media Processor 和 Multimodal Provider；
- collecting/sealed Input Snapshot；
- 逐集 Task、排序、缺集和失败恢复；
- 剧本提取、整剧分析、改编方案和 Brief；
- 非小说后续共享模块；
- 冻结几十集真实样本的阶段 9 验收方案。

状态：已完成。视频转剧本已收敛为唯一的“MediaKit Subtitle 全量对白字幕 + 豆包完整视频理解 + 确定性字幕覆盖校验”链路，不提供备用处理路径或静默降级。真实单集和多集故障恢复已有证据；Provider 504 转阶段 7，40 集真实验收转阶段 9。记录见 `stage-5-t6-video-reference-acceptance.md`。

### T7：阶段 6/7 交接

- 阶段 6 按已确认 UI/UE 和本合同实现前端；
- 阶段 7 按 T1-T6 完成后端；
- 两边以 API/Schema/SSE 合同并行，不互相猜字段。

状态：已完成。正式工作包、风险归属和完成证据见 `stage-5-t7-stage-6-7-handoff.md`。

## 11. 阶段 5 验证清单

- 合同 validator 通过；
- 96 条设计矩阵保持通过；
- 所有 Manifest Executor 可发现；
- 所有 Capability 的 Provider Requirement 可解析；
- 零 Capability Agent 测试通过；
- 普通聊天不创建 Run；
- 明确确认才能启动 Run；
- 新增测试 Capability 不修改 Runtime 核心流程；
- 小说、非小说 Manifest 全路径可达并有执行实现；
- 视频 Capability 在缺 Provider/Executor 时显示不可用，不进入假运行；
- Runtime/Worker 全量 Go 测试可执行；
- API Contract Test、SSE 重连测试和 SQLite 重启恢复测试通过；
- 迁移 Dry Run、备份和回滚方案可执行；
- P0 问题全部关闭后再申请阶段 5 正式确认。

## 12. 本轮验证记录

```text
Capability contract validation: PASS
Capabilities: 3
Steps: 34
Response adapters: 14

Stage 2 acceptance matrix validation: PASS
Cases: 96
P0: 88
P1: 8
```

Go 测试：

```text
PASS: cmd/worker
PASS BUILD: cmd/server
PASS BUILD: cmd/video-worker
PASS: internal/capability
PASS: internal/executor
PASS: internal/httpapi
PASS: internal/mediaprocessor
PASS: internal/modelprovider
PASS: internal/runtime (fixed-path full regression)
PASS: internal/shell
PASS: internal/worker (fixed-path full regression)
PASS: go vet ./...
```

Windows Application Control 偶发阻止 Go 临时目录中的测试 EXE；Runtime 与 Worker 已改用固定路径测试二进制完成全回归。真实视频测试仍受正式媒体工具和网关配置约束。

2026-08-05 最终增量验证：

```text
PASS: Stage 2 acceptance matrix validator (96 cases)
PASS: Capability contract validator (3 manifests / 34 steps / 14 adapters)
PASS: internal/runtime full regression
PASS: internal/worker fixed-path full regression
PASS: internal/mediaprocessor
PASS: internal/capability fixed-path regression
PASS: internal/shell
PASS: internal/modelprovider
PASS: cmd/video-worker
ENV BLOCKED: internal/httpapi current fixed-path rerun
```

`internal/httpapi` 已有 2026-08-03 全量通过记录；2026-08-05 未修改 HTTP 包，本轮重新编译的测试 EXE 被 Windows Application Control 阻止启动。该环境限制如实保留，不将未启动描述为本轮通过。
