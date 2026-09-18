# Go 后端实现计划

状态：开发准入合同

本文档把后端从“早期 mock runtime”推进到“可替换 Eino runtime 的正式 Go 架构”。它不要求现在立刻写代码，但后续进入后端实现时应按这里拆模块、排顺序、写测试。

## 当前现状

已有目录：

```text
backend/
  cmd/mockserver/main.go
  internal/agent/types.go
  internal/agent/mock/runtime.go
  internal/llm/client.go
  internal/mainagent/agent.go
  internal/mainagent/types.go
  internal/worker/llmworker.go
```

已具备：

- `mainagent` 可做控制模型 / fallback 意图判断。
- `llmworker` 可调用生成模型产出内容。
- `mock/runtime.go` 可跑早期 run -> approval -> scripts 闭环。
- `cmd/mockserver/main.go` 有 HTTP server、SSE、run artifacts/events 查询。

主要不一致：

- 早期 API 仍有 `POST /api/runs`、`POST /api/runs/{run_id}/continue`。
- 新合同要求用户输入统一走 `POST /api/projects/{project_id}/messages`。
- 当前缺少 Project、Message、FileRef、ScriptSuggestion、ScriptComment 的正式 store。
- 当前状态更新散在 runtime 逻辑里，还没有独立 reducer。
- 当前 events 还包含旧的 `script_batch_inserted`，需要对齐 `shared-schema-contract.md`。
- 当前 `scripts` artifact 还没有正式 `script_document` / `editor_state` / `suggestions` / `comments`。

## 目标架构

```text
backend/
  cmd/server/
  internal/api/
    handlers/
    middleware/
    responses/
  internal/domain/
    model/
    reducer/
    validation/
  internal/store/
    memory/
    jsonfile/
  internal/runtime/
    service/
    mock/
    eino/
  internal/mainagent/
  internal/scripteditor/
  internal/llm/
  internal/worker/
```

第一版可以继续内存存储，但模块边界要按正式结构拆，避免 mock 和正式 runtime 两套字段。

## 模块职责

### 1. domain/model

职责：

- 定义 Project、Message、Run、RunStep、RunEvent、Artifact、ApprovalRequest、FileRef、ScriptSuggestion、ScriptComment。
- 定义所有枚举。
- 保持 API JSON 使用 snake_case。

不负责：

- HTTP handler。
- 模型调用。
- UI 文案。

### 2. domain/reducer

职责：

- 集中处理状态变化。
- 输入当前 state + command，输出新 state + events。
- 保证每次状态变化都有事件。

应支持的 command：

```text
CreateMessage
AttachFile
StartRun
StartStep
CompleteStep
FailStep
CreateArtifact
UpdateArtifact
InvalidateArtifact
RequestApproval
ResolveApproval
PauseRun
ResumeRun
CancelRun
CompleteRun
CreateScriptSuggestion
ResolveScriptSuggestion
CreateScriptComment
UpdateScriptComment
SaveScriptDocument
```

禁止：

- handler 直接改 `run.status`。
- runtime 直接绕过 reducer 写 artifact status。

### 3. store

职责：

- 保存 project、message、run、event、artifact、approval、file、suggestion、comment。
- 第一版用 memory store。
- 后续可换 jsonfile / db。

第一版接口：

```text
ProjectStore
MessageStore
RunStore
EventStore
ArtifactStore
ApprovalStore
FileStore
ScriptSuggestionStore
ScriptCommentStore
```

规则：

- store 只负责持久化，不做业务判断。
- version 冲突由 service/reducer 判断。
- EventStore 必须支持按 run_id 追加和按 after_event_id 查询。

### 4. runtime/service

职责：

- 执行 Main Agent 决策。
- 根据 action 调用 mock/eino worker。
- 组装 prompt payload。
- 调用 reducer 产生状态和 events。

第一版 service：

```text
MessageService
RunService
ApprovalService
ArtifactService
ScriptEditorService
FileService
EventStreamService
```

### 5. runtime/mock

职责：

- 在不接 Eino 时按合同跑通流程。
- 使用与正式 runtime 相同的 domain model、reducer、store。
- 不再拥有另一套独立 run/artifact/event 字段。

保留：

- 快速生成 source_input / material_bank / story_seed / series_blueprint / episode_cards / script_context / script_unit / scripts。
- checkpoint approval。
- SSE events。

替换：

- 旧 `StartRun` / `ContinueRun` 作为内部兼容层，不再是主 API。

### 6. runtime/eino

职责：

- 未来替换 mock worker。
- 只替换执行节点，不替换 API、store、reducer、events。

边界：

- Eino 负责 orchestration / node execution。
- Go service 仍负责项目状态、artifact version、approval、event 持久化。
- Eino 不直接写 HTTP response。

### 7. scripteditor

职责：

- 保存 `scripts` artifact 的 `script_document` 和 `editor_state`。
- 创建/接受/拒绝 `ScriptSuggestion`。
- 创建/更新 `ScriptComment`。
- 校验 `selection_hash`、`base_version`、`line_id`。

第一版 service：

```text
SaveScriptDocument
CreateSuggestionFromSelection
ResolveSuggestion
CreateComment
UpdateComment
MarkStaleComments
```

## API 实现顺序

### Phase 1：协议对齐壳

目标：先把 handler 路由对齐，不要求完整业务都实现。

实现：

- `GET /healthz`
- `GET /api/projects`
- `POST /api/projects`
- `GET /api/projects/{project_id}`
- `POST /api/projects/{project_id}/messages`
- `GET /api/projects/{project_id}/artifacts`
- `GET /api/runs/{run_id}`
- `GET /api/runs/{run_id}/events`
- `GET /api/runs/{run_id}/events/stream`

验收：

- 闲聊返回 `run: null`。
- 明确生成请求创建 run。
- 上传文件不创建 run。

### Phase 2：reducer 和 memory store

目标：状态更新集中化。

实现：

- domain model。
- reducer command。
- memory store。
- EventStore 追加查询。

验收：

- chat 不创建 run。
- generate 创建 run 并追加 `message_created`、`intent_detected`、`run_started`。
- 每次 artifact 创建都追加 `artifact_created`。

### Phase 3：主流程 mock runtime 对齐

目标：用新协议跑通小说链 / 非小说链到 episode_cards checkpoint。

实现：

- source_input。
- novel chain mock。
- non_novel chain mock。
- episode_cards approval。

验收：

- 非小说链到 `waiting_approval`。
- 小说链到 `waiting_approval`。
- 左侧目录可按 `project.active_artifacts` 渲染。
- approval card 可嵌入 Agent 消息流。

### Phase 4：approval / pause / resume / retry

目标：解决用户此前指出的暂停、补充说明、确认卡问题。

实现：

- `POST /api/approvals/{approval_request_id}/resolve`
- `POST /api/runs/{run_id}/pause`
- `POST /api/runs/{run_id}/resume`
- `POST /api/runs/{run_id}/cancel`
- `POST /api/runs/{run_id}/retry`
- `POST /api/runs/{run_id}/steps/{step_id}/retry`

验收：

- approval approve 不创建新 run。
- approval revise_instruction 不创建新 run。
- paused 状态补充说明不自动继续。
- failed retry 不清空已生成 artifact。

### Phase 5：scripts 聚合和 ScriptEditor API

目标：支持 Lexical 正文编辑器的数据闭环。

实现：

- `POST /api/artifacts/{artifact_id}/script-document/save`
- `POST /api/script-suggestions/{suggestion_id}/resolve`
- `POST /api/artifacts/{artifact_id}/script-comments`
- `PATCH /api/script-comments/{comment_id}`

验收：

- `scripts` artifact 有 `script_document`。
- 保存正文生成新 version。
- suggestion accept 生成新 `scripts` version。
- suggestion reject 不改正文。
- comment stale 不静默挪位置。

### Phase 6：文件上传和模型上下文

目标：文件只作为 FileRef，是否进入模型上下文由 Agent/run 决定。

实现：

- `POST /api/projects/{project_id}/files`
- `GET /api/projects/{project_id}/files`
- `GET /api/files/{file_id}`
- `DELETE /api/files/{file_id}`

验收：

- 文件显示为 attachment chip。
- 文件名不写入 textarea。
- message 通过 `file_ids` 引用文件。

### Phase 7：Eino runtime 接入

目标：只替换执行引擎，不改 API 和状态合同。

实现：

- `runtime/eino`。
- prompt runner。
- selected skills assembly。
- LLM output validation。
- retry / timeout / output invalid handling。

验收：

- mock runtime 与 Eino runtime 返回同一套 API 对象。
- Eino 失败不破坏已有 artifact。
- 模型输出 invalid 时进入 `MODEL_OUTPUT_INVALID`，可 retry。

## 测试计划

### 单元测试

- reducer 每个 command。
- artifact version superseded。
- dependency invalidation。
- approval resolve。
- selection hash conflict。
- suggestion accept/reject。

### API 测试

- chat message。
- generate message。
- approval approve/revise/pause。
- pause/resume/cancel。
- script document save。
- suggestion resolve。
- comment create/update。
- file upload。

### E2E 测试

- 非小说灵感 -> episode_cards -> approval -> scripts。
- 小说原文 -> story_bible -> episode_split -> episode_cards -> approval -> scripts。
- 闲聊不创建 run。
- waiting_approval 补充说明不新建 run。
- paused 补充说明不自动 resume。
- 选区改写生成 suggestion，不覆盖正文。

## 迁移策略

第一步不要删除旧 runtime，先做兼容：

```text
旧 /api/runs
-> 标记 deprecated
-> 内部调用新的 RunService
-> 前端逐步切到 /messages 和 /approvals
```

第二步让 mock runtime 使用 reducer/store：

```text
internal/agent/mock/runtime.go
-> 拆为 runtime/mock + runtime/service
```

第三步接 Eino：

```text
internal/runtime/eino
-> 实现同一 Worker 接口
-> 不改 handler 和 store
```

## 当前不能做的事

- 不直接把 Eino 写进 handler。
- 不让前端继续依赖 `/api/runs` 作为主入口。
- 不把 ScriptEditor 的 suggestion/comment 做成前端本地状态。
- 不为了 mock 简单而返回另一套字段。
- 不跳过 reducer 直接在 handler 里改 status。

## 正式进入后端编码前的准入

- [ ] `shared-schema-contract.md` 的对象映射到 Go struct。
- [ ] `api-event-contract.md` 的接口映射到 handler 列表。
- [ ] `runtime-state-api-map.md` 的状态变化映射到 reducer command。
- [ ] `script-editor-lexical-spec.md` 的 suggestion/comment/save 映射到 ScriptEditorService。
- [ ] mock runtime 和 Eino runtime 的共同 Worker 接口确定。
- [ ] 第一批 API 测试用例确定。

## 2026-06-30 接口壳实现快照

本次已在 Go mockserver 上补齐第一层正式项目接口，目标是让前端后续从旧的 `/api/agent/messages` 迁移到项目态 API，而不改变现有 Main Agent 和 mock runtime 的执行逻辑。

已实现：
- `GET /api/projects`
- `POST /api/projects`
- `GET /api/projects/{project_id}`
- `POST /api/projects/{project_id}/messages`
- `POST /api/approvals/{approval_request_id}/resolve`

保留兼容：
- `POST /api/agent/messages`
- `POST /api/runs`
- `GET /api/runs/{run_id}`
- `POST /api/runs/{run_id}/continue`
- `POST /api/runs/{run_id}/steps/{step_id}/rerun`
- `GET /api/runs/{run_id}/events`
- `GET /api/runs/{run_id}/events/stream`
- `GET /api/runs/{run_id}/artifacts`

当前实现边界：
- 项目、消息仍为内存态，服务重启后清空。
- 文件上传接口还未实现，正式 message 接口暂时只接收 `file_ids` 字段，不负责保存文件。
- approval resolve 已接入当前 runtime approval，但 revise/pause 的深层语义仍沿用现有 `ContinueRunContext` 兜底逻辑。
- ScriptEditor 保存、suggestion、comment 相关接口尚未实现。

已验证：
- 使用项目内 `.tools/go/bin/go.exe` 执行 `go test ./...` 通过。
- `GET /healthz` 返回 `{"ok":true,"runtime":"main_agent"}`。
- `POST /api/projects` 可创建项目。
- `POST /api/projects/{project_id}/messages` 对普通闲聊返回 user/agent message，且 `run` 为空。
- `GET /api/projects` 可返回刚创建的项目。
- `POST /api/approvals/missing/resolve` 返回 404。
- 旧 `POST /api/agent/messages` 对普通闲聊仍可用。

下一步后端应补：
- `POST /api/projects/{project_id}/files`
- `GET /api/projects/{project_id}/files`
- `GET /api/files/{file_id}`
- `DELETE /api/files/{file_id}`
- 将 frontend 从旧 `sendAgentMessage` 逐步切到 `sendProjectMessage`。

## 2026-06-30 文件接口与前端迁移快照

本次已补齐第一版文件 API，并让正式项目消息接口支持通过 `file_ids` 引用附件内容。

已实现：
- `POST /api/projects/{project_id}/files`
- `GET /api/projects/{project_id}/files`
- `GET /api/files/{file_id}`
- `DELETE /api/files/{file_id}`

行为规则：
- 上传文件只保存为 `FileRef`，不会自动创建 run。
- 发送消息时，前端只传 `file_ids`，不把文件正文写入 textarea。
- 如果用户只发送文件、正文为空，Main Agent 会看到附件内容预览，用于判断是否启动生成流程。
- 文件内容仍是内存态，服务重启后清空。

已验证：
- `go test ./...` 通过。
- `POST /api/projects/{project_id}/files` 可返回 `file_id`。
- `GET /api/projects/{project_id}/files` 可返回项目文件列表。
- `POST /api/projects/{project_id}/messages` 可接收空正文 + `file_ids`。
- `DELETE /api/files/{file_id}` 可删除文件。

当前阻塞：
- 文件内容已经能触发生成意图，但当前模型生成调用没有成功产出内容，后端会返回“内容生成模型没有成功产出结果”，不会伪造 run 或 artifact。
- 下一步需要检查模型调用错误细节、输出格式校验和 worker prompt 约束，才能进入完整生成链路验证。
## 2026-06-30 模型生成链路验证快照

本轮重点从 mock worker 过渡到真实模型调用链路验证，已经确认主流程不再停留在纯 mock。

已完成：
- 修复 content worker 的 prompt 乱码，规划阶段要求模型返回严格 JSON。
- `N2S_LLM_TIMEOUT_SECONDS` 支持配置，默认请求超时从 60 秒放宽到 180 秒；控制模型仍由 Main Agent 的 55 秒上下文限制保护。
- `approval resolve` 在模型失败时返回可展示的 Agent 消息和当前 run/artifact 状态，不再直接把前端打成裸 HTTP 500。
- 修复附件拼接模板乱码，上传文件会进入模型上下文的 `【附件：文件名】` 块。

已验证：
- 无代理环境下，上传文件 + 文本生成请求可进入 `waiting_approval`。
- 规划阶段生成 `source_input / material_bank / story_seed / series_blueprint / episode_cards`。
- 点击确认后可继续生成 `script_context / script_unit / scripts`，run 最终进入 `completed`。
- `go test ./...` 通过；Go telemetry 写入 warning 不影响测试结果。

运行注意：
- 当前系统环境里 `HTTP_PROXY / HTTPS_PROXY / ALL_PROXY` 指向 `127.0.0.1:9` 时，模型请求会失败：`proxyconnect tcp ... actively refused`。
- 启动后端前需要清掉这些代理变量，或把模型域名加入可用网络路径。
- 附件乱码修复需要重启后端后才会在正在运行的服务中生效。
