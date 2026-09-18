# 模型与 Eino 运行时策略

本文档定义 Novel2Script Agent 的模型分工、Go + Eino 运行时边界、上下文裁剪、失败兜底、mock runtime 到 Eino runtime 的替换策略。它是运行时开发合同，不是某个 Eino 版本的 SDK 调用手册。

## 技术路线

```text
Frontend: TypeScript
Backend: Go
Agent Runtime: Go + Eino
Model API: OpenAI-compatible chat completions / provider adapter
```

原则：

1. Go 后端是 project_state、run_state、artifact、run_event、approval 的唯一事实来源。
2. Eino 负责 Agent 运行图和模型 / 工具编排，不负责替代业务状态存储。
3. 模型 provider 可替换，不能把 provider 特性写死进前端。
4. mock runtime 和 Eino runtime 必须遵守同一套 API、schema、event、approval 协议。

## 模型分工

### 控制模型

职责：

- 意图识别。
- source_mode 判断。
- run 是否启动。
- waiting_approval / paused 状态下的用户输入处理。
- next_action 判断。
- 局部修改范围判断。
- 是否请求 approval。
- 失败时给出恢复建议。

不负责：

- 大段剧本正文生成。
- 故事圣经、素材库、分集卡等长内容生成。
- 直接写 artifact payload。

### 生成模型

职责：

- story_bible。
- episode_split。
- material_bank。
- story_seed。
- series_blueprint。
- episode_cards。
- script_unit / scripts。
- revise selection / revise artifact 的实际内容改写。

不负责：

- 是否创建 run。
- 是否覆盖已确认 artifact。
- 是否跳过 approval。
- 直接决定项目状态。

## 模型配置

后端读取环境变量：

```text
N2S_LLM_BASE_URL
N2S_LLM_API_KEY
N2S_CONTROL_MODEL
N2S_GENERATION_MODEL
```

前端只能看到脱敏状态：

```json
{
  "provider_status": "configured | mock | error",
  "control_model": "configured | missing",
  "generation_model": "configured | missing"
}
```

禁止前端接触：

- API key。
- provider token。
- 完整敏感 endpoint。
- provider 原始错误堆栈。

## Runtime 分层

```text
HTTP API Layer
-> Main Agent Service
-> Runtime Adapter
   -> Mock Runtime
   -> Eino Runtime
-> Model Client / Provider Adapter
-> Artifact Store / Run Store / Event Store
```

### HTTP API Layer

职责：

- 接收 message / approval / file / artifact API。
- 做 project_id / run_id / artifact_id 归属校验。
- 返回统一 ErrorResponse。

不做：

- 本地判断是否生成。
- 直接调用 prompt。

### Main Agent Service

职责：

- 调用主控制模型；可以调用显式配置的备用控制模型。
- 读取 project_state。
- 选择 next_action。
- 决定 approval_policy。
- 调用 Runtime Adapter。

### Runtime Adapter

职责：

- 提供稳定接口：

```text
StartRun(project_id, message)
ContinueRun(run_id, approval_response)
RerunStep(run_id, step_id)
ReviseArtifact(run_id, instruction)
ReviseSelection(run_id, selection_context, instruction)
StreamRunEvents(run_id)
```

当前 native runtime 提供这些稳定语义；完整 Eino runtime 接入后必须保持一致。

### Artifact / Run / Event Store

职责：

- 持久化 project_state。
- 持久化 run_state。
- 持久化 artifact version。
- 追加 run_event。
- 保存 approval_request。

Eino 节点不能绕过 store 直接改内存状态。

## Eino Runtime 图

建议运行图：

```text
LoadProjectState
-> ResolveNextAction
-> BuildPromptJob
-> LoadPromptAndSkills
-> AssembleInputPayload
-> CallModel
-> ValidateOutput
-> PersistArtifact
-> MergeContinuityState
-> UpdateRunState
-> EmitRunEvent
-> MaybeRequestApproval
```

### LoadProjectState

读取：

- project。
- active_run。
- active_artifacts。
- active_approval。
- recent_messages。

### ResolveNextAction

输入：

- Main Agent decision。
- run_state。
- artifact status。

输出：

- action_type。
- target prompt_id。
- required artifacts。
- approval policy。

### BuildPromptJob

遵守 `runner-prompt-assembly-contract.md`。

### CallModel

根据任务类型选择：

- control model。
- generation model。

### ValidateOutput

遵守：

- `shared-schema-contract.md`。
- `artifact-schemas.md`。
- prompt 输出 contract。

### MaybeRequestApproval

只由运行时 / Main Agent 根据规则创建 approval，不由模型自行决定。

### Episode Split Stage Graph

小说原文拆集使用独立编译图：

```text
validate_split_stage_input
-> invoke_split_stage_model
-> validate_split_stage_output
```

图分两种 stage：`global_plan` 生成全局集数骨架，`boundary_batch` 每批处理 5 集并只选择后端给出的候选边界。Go Runtime 负责确定性原文索引、互斥候选区间、输入版本锁、批次 checkpoint、失败续跑和最终全文覆盖校验。模型不得自行生成字符 offset，Eino 也不得直接写 Artifact 或 Run metadata。

## Mock -> Eino 替换策略

替换时保持不变：

- HTTP API。
- Project / Run / Artifact / Approval / Event schema。
- run status。
- approval pause / continue 机制。
- 前端渲染逻辑。
- 12 类验收用例。

替换内容：

```text
internal/agent/mock
-> internal/agent/eino
```

第一阶段可共存：

```text
N2S_RUNTIME=eino | native
```

前端不感知 runtime 类型，只显示脱敏 provider/runtime 状态。

## 上下文裁剪策略

### 控制模型上下文

控制模型只需要：

- 用户当前 message。
- 当前 project_state 摘要。
- active_run 摘要。
- active_approval 摘要。
- 当前 artifact 摘要。
- selection_context 摘要。
- 最近少量消息。

不传：

- 完整小说原文。
- 完整剧本。
- 完整文件正文。
- 大段 artifact payload。

### 生成模型上下文

生成模型按 step 装配：

- 当前 prompt 的必需 artifact。
- 当前 planning_scope / episode_id 需要的局部内容。
- context_pack。
- user_notes。
- selected skills。

不传：

- 无关历史消息。
- 所有 artifact 全量。
- 已 invalidated artifact。
- 与当前 source_mode 无关的链路产物。

## 上下文预算

第一版建议按字符 / token 预算粗控：

```text
control model input：短上下文，优先 < 8k tokens
generation model planning：中上下文，按 step 控制
script_generate：单集上下文，禁止整部剧本全量输入
```

超预算处理：

1. 压缩 recent_messages。
2. 用 artifact summary 替代完整 payload。
3. 只取当前 episode source_refs。
4. 对前文剧本使用 recent_script_summaries。
5. 必要时请求用户缩小范围。

## 模型调用记录

每次调用写事件：

```text
llm_input_prepared
llm_output_received
```

事件 payload 默认只记录摘要：

```json
{
  "model_role": "control | generation",
  "model_name": "configured",
  "prompt_id": "non_novel.step1_material_bank",
  "input_artifact_refs": [],
  "text_chars": 1200,
  "duration_ms": 0
}
```

默认不记录：

- 完整 prompt。
- 完整 source_text。
- 完整 LLM raw output。
- API key。

## 失败与兜底

### 控制模型失败

不允许本地关键词规则替代语义意图识别。只允许显式配置的备用控制模型。

行为：

- 主控制模型失败时尝试备用控制模型，并写入 warning。
- 两个控制模型都失败时返回安全 `reply`，说明控制模型不可用。
- 不执行 start、approve、revise、rerun 等动作。

不能：

- 伪装成模型真实判断。
- 在低置信度下启动高影响 workflow。

### 生成模型失败

不允许用本地假内容伪装成真实生成。

行为：

- step_failed。
- run.status = failed 或等待 retry。
- 不写 active artifact。
- 允许重试、暂停、补充说明。

### JSON / schema 失败

遵守 `runner-prompt-assembly-contract.md`：

- 可做一次 JSON repair。
- 关键字段缺失则失败。
- 不写 active artifact。

### Provider timeout / rate limit

行为：

- 返回 `MODEL_TIMEOUT` 或 `MODEL_CALL_FAILED`。
- 保留 run_state 和已生成 artifact。
- 前端显示错误卡。

## Streaming 策略

第一版前端事件流以 run_event 为主，不要求 LLM token 级 streaming。

推荐顺序：

1. run_event streaming。
2. artifact 分批落盘。
3. script scene / block 级插入事件。
4. 最后再考虑 token streaming。

原因：

- 剧本和分集卡需要结构化校验。
- token streaming 容易展示未校验内容。
- 用户更需要知道“哪一步完成 / 失败 / 等待确认”。

## 运行时配置

建议：

```text
N2S_RUNTIME=eino | native
N2S_LLM_BASE_URL=
N2S_LLM_API_KEY=
N2S_CONTROL_MODEL=
N2S_GENERATION_MODEL=
N2S_MODEL_TIMEOUT_SECONDS=60
N2S_DEBUG_LLM_IO=false
```

`N2S_DEBUG_LLM_IO=true` 仅允许本地开发使用，并遵守 `data-security-contract.md` 的日志限制。

## 观测指标

至少记录：

- run duration。
- step duration。
- model call duration。
- model role。
- prompt_id。
- success / failure。
- error code。
- artifact_type。
- retry count。

不记录：

- 完整原文。
- 完整剧本。
- API key。

## 必测场景

### MRT-001 控制模型失败保护

预期：

- 有备用控制模型时只使用该备用模型；没有或仍失败时停止语义执行。
- 返回 warning。
- 不启动 run，不修改 artifact，不推进 approval。

### MRT-002 生成模型失败不写 artifact

预期：

- step_failed。
- 不生成 active artifact。
- 前端显示错误卡。

### MRT-003 native / Eino API 行为一致

同一输入在 native 和 Eino runtime 下：

- run_event 类型一致。
- artifact_type 一致。
- approval 行为一致。

### MRT-004 script_generate 单集上下文

生成第 3 集时：

- 不传整部小说全文。
- 只传当前集 source_refs、episode_card、context_pack。

### MRT-005 debug 日志脱敏

开启 debug 后：

- 不出现 API key。
- 不进入普通 run_event。

## 禁止事项

- 前端直接调用模型。
- 前端保存 API key。
- Eino 节点绕过 run/artifact store。
- 控制模型直接写剧本正文。
- 生成模型决定是否跳过 approval。
- 模型失败时写入假 artifact。
- token streaming 未校验内容直接成为正式剧本。
- 把 provider 原始敏感报错展示给用户。
