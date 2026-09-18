# 开工前合同一致性审计

本审计用于确认 Novel2Script Agent 在进入前后端实现前，核心设计合同之间没有互相冲突的接口、状态、数据对象和职责边界。

审计范围：
- `shared-schema-contract.md`
- `api-event-contract.md`
- `runtime-state-api-map.md`
- `backend-implementation-plan.md`
- `frontend-implementation-plan.md`
- `workflow-state-transition-contract.md`
- `script-editor-lexical-spec.md`
- `local-file-attachments.md`
- `acceptance-test-contract.md`
- `main-agent-model-runtime.md`

## 结论

当前设计合同已达到“可进入前后端 Phase 1”的一致性要求。

可以开始：
- Go 后端协议对齐壳。
- TypeScript API client/types 对齐。
- mock adapter 按正式 API 类型运行。

不能直接跳到：
- UI 视觉终稿。
- Eino runtime 正式接入。
- 复杂局部改写真实生成。
- 持久化数据库设计。

这些仍需要在 Phase 1 / Phase 2 后继续验证。

## 已修正的不一致

### 1. 消息入口统一

问题：
- 部分文档使用 `POST /messages`。
- 部分历史记录使用 `/api/agent/messages`。
- 正式 API 合同使用 `POST /api/projects/{project_id}/messages`。

修正：
- `runtime-state-api-map.md` 已统一为 `POST /api/projects/{project_id}/messages`。
- `script-editor-lexical-spec.md` 的选区修改流程已统一为 `POST /api/projects/{project_id}/messages`。
- `acceptance-test-contract.md` 的闲聊用例已统一为 `POST /api/projects/{project_id}/messages`。
- `main-agent-model-runtime.md` 明确 `/api/agent/messages` 只允许作为过渡兼容层，不作为新实现目标。

正式结论：
- 新前端只能把 Agent 输入发到 `POST /api/projects/{project_id}/messages`。
- 后端如保留 `/api/agent/messages`，只能作为兼容 adapter。

### 2. 文件上传入口统一

问题：
- 部分文档写 `POST /files`。
- 早期文件说明把附件直接塞进 `/api/agent/messages.attachments`。
- 正式 API 合同使用 `POST /api/projects/{project_id}/files`。

修正：
- `runtime-state-api-map.md` 已统一为 `POST /api/projects/{project_id}/files`。
- `frontend-implementation-plan.md` 已统一为 `POST /api/projects/{project_id}/files`。
- `local-file-attachments.md` 已改为正式 `FileRef` 流程。

正式结论：
- 上传文件只创建 `FileRef`。
- 上传文件不启动 run。
- 上传文件不自动进入模型上下文。
- 用户消息通过 `file_ids` 引用文件，由 Main Agent 决定是否消费。

### 3. Approval resolve 入口统一

问题：
- 前端计划曾写 `POST /api/runs/{run_id}/approvals/{approval_id}/resolve`。
- runtime map 曾写 `POST /approvals/{id}/resolve`。
- 正式 API 合同使用 `POST /api/approvals/{approval_request_id}/resolve`。

修正：
- `frontend-implementation-plan.md` 已统一为 `POST /api/approvals/{approval_request_id}/resolve`。
- `runtime-state-api-map.md` 已统一为 `POST /api/approvals/{approval_request_id}/resolve`。
- `acceptance-test-contract.md` 已统一为 `POST /api/approvals/{approval_request_id}/resolve`。

正式结论：
- approval 是独立资源，不挂在 run 路径下。
- approve / revise_instruction / pause / reject 都走同一个 resolve 接口。
- approval resolve 不创建新 run。

### 4. ScriptEditor 接口统一

问题：
- ScriptEditor 相关接口在不同文档里有省略 `/api` 的写法。

修正：
- `api-event-contract.md`、`runtime-state-api-map.md`、`frontend-implementation-plan.md` 已统一为：
  - `POST /api/artifacts/{artifact_id}/script-document/save`
  - `POST /api/script-suggestions/{suggestion_id}/resolve`
  - `POST /api/artifacts/{artifact_id}/script-comments`
  - `PATCH /api/script-comments/{comment_id}`

正式结论：
- 最终剧本正文保存、AI suggestion 接受/拒绝、批注创建/更新都有独立接口。
- AI 局部修改不直接覆盖正文，必须以 suggestion/patch 进入用户确认。

### 5. 后端 Phase 1 接口清单补齐

问题：
- `api-event-contract.md` 有 `GET /api/projects`。
- `backend-implementation-plan.md` Phase 1 原清单缺少该接口。

修正：
- `backend-implementation-plan.md` Phase 1 已补 `GET /api/projects`。

正式结论：
- 后端协议壳第一阶段要覆盖项目列表、项目创建、项目详情、消息、artifact、run、event。

## 当前统一后的事实来源

| 领域 | 唯一事实来源 |
|---|---|
| 枚举、对象、ScriptEditor 数据对象 | `shared-schema-contract.md` |
| HTTP API、响应结构、错误码 | `api-event-contract.md` |
| 用户动作到 API / 状态 / 事件映射 | `runtime-state-api-map.md` |
| run / project / artifact / approval 状态流转 | `workflow-state-transition-contract.md` |
| 最终剧本文档编辑器 | `script-editor-lexical-spec.md` |
| Go 后端实现顺序 | `backend-implementation-plan.md` |
| TypeScript 前端实现顺序 | `frontend-implementation-plan.md` |
| 验收用例 | `acceptance-test-contract.md` |

## 仍需编码阶段验证

这些不是设计冲突，但不能只靠文档证明：

1. Go struct 与 TypeScript type 是否逐字段一致。
2. API response 是否能被前端 client 直接消费。
3. reducer 是否严格按 `runtime-state-api-map.md` 更新状态。
4. approval resolve 是否真的不创建新 run。
5. waiting_approval 下补充说明是否真的不创建新 run。
6. 文件上传是否只产生 `FileRef`，不直接进入模型上下文。
7. ScriptEditor 保存后是否保留 line_id、scene_id、episode_id、marks。
8. suggestion patch 是否不会直接覆盖用户正文。
9. SSE event 顺序是否稳定。
10. 刷新页面后 run / artifact / approval / event 是否能恢复。

## 开工判断

前后端可以进入 Phase 1，但必须按以下顺序：

1. 后端先做正式 API 壳和共享类型。
2. 前端同步做 API client/types，不再扩展旧 `api.ts` 私有协议。
3. mock runtime 使用正式对象返回数据。
4. 通过 API 测试和前端 contract fixture 后，再做三栏组件重构。
5. UI 高保真和视觉定稿放在数据流稳定之后。

如果实现过程中发现合同缺字段，先回到本套设计文档更新合同，再改代码。
