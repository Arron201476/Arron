# TypeScript 前端实现计划

本文档把现有产品合同、状态合同、API 合同、组件语义和 Lexical 编辑器规格，落成后续 React / TypeScript 前端开发顺序。它不是 UI 稿本身，也不是要求把 `frontend-ui-state-gallery.html` 逐像素复制成代码。

## 当前状态

已具备：
- Vite + React + TypeScript 前端工程。
- 基础 API client：`frontend/src/api.ts`。
- 基础类型：`frontend/src/types.ts`。
- 基础三栏工作台雏形：`frontend/src/App.tsx`。
- 少量基础 UI 组件：`Button`、`Badge`、`EmptyState`。
- 文件添加、Agent 输入、基础消息流、mock/模型消息返回。
- ScriptEditor 选型 gate：Lexical 通过，Tiptap 归档为对照。

尚未具备：
- TypeScript 类型未完整对齐 `shared-schema-contract.md` 和 `api-event-contract.md`。
- API client 仍使用早期 `/api/agent/messages`，未对齐正式项目消息、审批、ScriptEditor 接口。
- 前端状态仍混在页面组件里，缺少 project/run/artifact/message 分层 store。
- 左侧目录、内容区、右侧 Agent 还没有按正式组件边界拆分。
- 中间内容区还没有字段化 artifact renderer。
- 最终剧本还没有正式 Lexical `ScriptEditor`。
- 视觉仍是工程雏形，不是 UI 定稿。

## 2026-06-30 实施快照

已进入 TypeScript 前端第一版落地，不再停留在 Figma / HTML 静态稿阶段。

本轮已完成：
- `frontend/src/App.tsx` 的工作台信息架构已按“左侧工作区目录 / 中间内容与剧本 / 右侧 Agent 助手”重排。
- 左侧目录不再同时暴露小说链和非小说链全部步骤，而是根据 `source_mode` 展开对应链路；未识别前只展示输入材料、剧本、运行记录。
- 右侧 Agent 输入区支持 Enter 发送、Shift+Enter 换行、本地文件添加入口和中文状态文案。
- 过程步骤不再用“确认”误标生成状态；完成态以低权重步骤点和中文“已完成”展示。
- 剧本视图加入第一版正文工作区：分集目录、正文工具栏、纸张式剧本正文区域。
- 三栏布局已收紧为可收缩中栏，避免普通桌面宽度下剧本编辑区压到右侧 Agent。

本轮验证：
- `npm run build` 通过。
- 本地 dev server 已可在 `http://127.0.0.1:8832/` 访问。
- `npm run gate:editor` 当前仍受 Windows / Playwright `spawn EPERM` 限制，未能完成浏览器 gate；这属于本机浏览器子进程权限问题，不是 TypeScript 构建错误。

仍需继续：
- 将第一层组件继续细拆为正式 React 组件树和 view model selector。
- 将旧 `/api/agent/messages` 适配到正式项目消息、approval、artifact、event API。
- 将最终剧本正文从当前展示壳替换为正式 Lexical `ScriptEditor`。
- 补 Playwright 或等价浏览器视觉回归，覆盖 Figma 29 张状态 frame 中的主路径。

## 2026-06-30 组件拆分快照

已完成第一层 React 组件拆分，目标是先把“状态/API 容器”和“页面展示组件”分开，不在此阶段改 workflow 或后端接口。

本轮已完成：
- `frontend/src/App.tsx` 收敛为工作台容器，保留状态、API 调用、附件读取、run/artifact/message 合并逻辑。
- `frontend/src/components/workbench/ProjectSidebar.tsx` 承接左侧工作区目录、链路目录和状态展示。
- `frontend/src/components/agent/AgentPanel.tsx` 承接右侧 Agent 助手、消息列表、审批卡、步骤 chips、附件 chips 和输入框。
- `frontend/src/components/artifacts/ArtifactWorkspace.tsx` 承接中间内容区、输入材料编辑、过程产物字段化阅读、运行记录和剧本展示壳。
- `frontend/src/lib/workbenchLabels.ts` 集中管理 artifact 名称、导航生成、状态文案、事件文案和执行步骤映射。

拆分结果：
- `App.tsx` 当前约 340 行。
- `AgentPanel.tsx` 当前约 182 行。
- `ArtifactWorkspace.tsx` 当前约 267 行。
- `ProjectSidebar.tsx` 当前约 33 行。
- `workbenchLabels.ts` 当前约 185 行。

本轮验证：
- `npm run build` 通过。
- 本地 dev server 返回 200。

下一步：
- 继续补正式 API adapter：把旧 `/api/agent/messages` 逐步适配到项目消息、approval、artifact 和 event 合同。
- 继续让前端类型对齐 `shared-schema-contract.md` 和 `api-event-contract.md`。
- 在正式 API adapter 前保留当前 runtime 行为，不新增另一套私有 mock 协议。

## 2026-06-30 API 层拆分快照

已完成第一层 API 文件拆分，目标是先把 HTTP client、API 类型和事件工具分开，不在此阶段改变后端 endpoint 或业务语义。

本轮已完成：
- 新增 `frontend/src/api/client.ts`，承接 `health` 和 `sendAgentMessage` HTTP 调用。
- 新增 `frontend/src/api/types.ts`，集中放当前前端 API 类型。
- 新增 `frontend/src/api/events.ts`，承接 run event 合并工具。
- `frontend/src/api.ts` 保留为兼容转发入口，避免旧导入立即失效。
- `frontend/src/types.ts` 保留为兼容类型转发入口，后续组件逐步迁到 `api/types.ts`。
- `App.tsx` 已改为直接使用 `api/client`、`api/events` 和 `api/types`。

本轮验证：
- `npm run build` 通过。
- 本地 dev server 返回 200。

仍需继续：
- 当前 `sendAgentMessage` 仍指向早期 `/api/agent/messages`，下一步要接正式 `POST /api/projects/{project_id}/messages`。
- 当前类型已补第一批合同对象，下一步要用 fixture/API response 校验字段是否和 Go 后端一致。
- `api/errors.ts` 已建立第一版，后续需要接入 UI 错误卡和错误码文案。

## 2026-06-30 正式 API 类型快照

已把 API 合同里的核心对象先落成 TypeScript 类型和 client 方法，但尚未让 UI 强制切到正式项目消息入口。这样可以在后端正式接口完成前继续保持前端可跑。

本轮已完成：
- `frontend/src/api/types.ts` 新增 `Project`、`Message`、`RunStep`、`FileRef`、`ArtifactRef`、`SelectionContext`、`ScriptDocument`、`ScriptSuggestion`、`ScriptComment`、`ErrorResponse`。
- `frontend/src/api/types.ts` 新增合同枚举：`ProjectStatus`、`StepStatus`、`IntentType`、`ApprovalStatus`、`ApprovalAction`、`FileStatus`、`EventLevel`、`ScriptBlockType`、`ScriptSuggestionStatus`、`ScriptCommentStatus`。
- `frontend/src/api/types.ts` 新增请求/响应类型：`ProjectMessageRequest`、`ProjectMessageResponse`、`CreateProjectRequest`、`CreateProjectResponse`、`ApprovalResolveRequest`、`ApprovalResolveResponse`。
- `frontend/src/api/client.ts` 新增正式方法：`listProjects`、`createProject`、`getProject`、`sendProjectMessage`、`resolveApproval`。
- 新增 `frontend/src/api/errors.ts`，支持把正式 `ErrorResponse` 转成 `ApiError`。
- 旧 `sendAgentMessage` 暂时保留，用于当前本地 mock/runtime 通路。

本轮验证：
- `npm run build` 通过。
- 本地 dev server 返回 200。

仍需继续：
- 后端补齐正式 `/api/projects`、`/api/projects/{project_id}/messages`、`/api/approvals/{approval_request_id}/resolve` 后，前端再从旧 `sendAgentMessage` 切到 `sendProjectMessage`。
- 当前 `SourceMode` 仍临时兼容 `auto`，后续需要区分 API 内部 `unknown` 与前端输入提示 `auto`。
- `FileAttachment` 仍是旧 inline 上传兼容对象，正式文件上传后应改为 `FileRef` / `file_ids`。

## 实现原则

1. 先对齐数据和状态，再做视觉细节。
2. 左侧只做项目目录和状态导航，不放 AI 操作按钮。
3. 中间只承载当前 artifact、最终剧本编辑器和运行记录，不放生成/改写按钮。
4. 右侧 Agent 是唯一 AI 指令入口，承载聊天、审批、补充说明、局部修改、文件和引用。
5. 过程产物使用字段化阅读和轻编辑，最终剧本使用 Lexical 正文编辑器。
6. `frontend-ui-state-gallery.html` 作为当前 UI/UE 定稿候选和状态源；实现时要转译为 React 组件，而不是复制静态 HTML / CSS。
7. 所有状态文案、按钮文案和 badge 文案以 `frontend-component-language.md` 为准。
8. 前端不本地伪造 run 状态；状态变化来自 API response、SSE event 或明确的 optimistic rule。

## 目标目录结构

```text
frontend/src/
  api/
    client.ts
    types.ts
    events.ts
    errors.ts
  store/
    projectStore.ts
    selectors.ts
    reducers.ts
  components/
    workbench/
      WorkbenchShell.tsx
      Topbar.tsx
      ProjectSidebar.tsx
      ProjectNavItem.tsx
    artifacts/
      ArtifactWorkspace.tsx
      ArtifactHeader.tsx
      ArtifactFieldGroup.tsx
      ArtifactEmptyState.tsx
      RunEventView.tsx
      ScriptEditor/
        ScriptEditor.tsx
        ScriptEditorToolbar.tsx
        ScriptDocumentCanvas.tsx
        nodes/
        plugins/
        serialization.ts
    agent/
      AgentPanel.tsx
      MessageList.tsx
      MessageBubble.tsx
      TaskStepList.tsx
      ApprovalCard.tsx
      ErrorCard.tsx
      CompletionCard.tsx
      AgentComposer.tsx
      AttachmentChip.tsx
      ReferenceChip.tsx
    common/
      Button.tsx
      Badge.tsx
      StatusBadge.tsx
      IconButton.tsx
      ScrollArea.tsx
      Tooltip.tsx
      EmptyState.tsx
  lib/
    classNames.ts
    format.ts
    ids.ts
```

## 组件库策略

第一阶段采用：
- shadcn/ui：按钮、输入、滚动区、弹窗、tooltip、dropdown、tabs、separator、textarea、badge 等基础组件来源。
- lucide-react：图标来源。
- Lexical：最终剧本文档编辑器。

可选后续引入：
- AI Elements：如果右侧 Agent 消息、tool call、reasoning、streaming parts 变复杂，再评估引入。

不采用：
- 直接复制 HTML gallery 的 CSS。
- 在中间内容区放大量 AI 操作按钮。
- 为了好看而改变后端状态语义。

## 实现阶段

### Phase 1. API 类型和 client 对齐

目标：
- 把 `shared-schema-contract.md`、`api-event-contract.md`、`script-editor-lexical-spec.md` 映射成 TypeScript 类型。
- 将早期 `api.ts` 拆为 `api/client.ts`、`api/types.ts`、`api/events.ts`。

必须覆盖：
- `Project`
- `Run`
- `RunStep`
- `RunEvent`
- `Artifact`
- `ApprovalRequest`
- `FileAttachment`
- `SelectionContext`
- `ScriptDocument`
- `ScriptSuggestion`
- `ScriptComment`

接口目标：
- `POST /api/projects/{project_id}/messages`
- `GET /api/projects/{project_id}/artifacts`
- `GET /api/runs/{run_id}`
- `GET /api/runs/{run_id}/events`
- `POST /api/approvals/{approval_request_id}/resolve`
- `POST /api/projects/{project_id}/files`
- `POST /api/artifacts/{artifact_id}/script-document/save`
- `POST /api/script-suggestions/{suggestion_id}/resolve`
- `POST /api/artifacts/{artifact_id}/script-comments`
- `PATCH /api/script-comments/{comment_id}`

验收：
- 前端不再依赖旧的 `/api/agent/messages` 作为唯一正式入口。
- 所有 API response 都有 TypeScript 类型。
- 错误码能映射到人话提示，不把 raw stack trace 展示给用户。

### Phase 2. Store 和状态 reducer

目标：
- 从页面组件里抽出 project/run/artifact/message 状态。
- 与 `workflow-state-transition-contract.md` 和 `runtime-state-api-map.md` 对齐。

Store 最小结构：
```text
ProjectState
  project
  currentRun
  artifactsByID
  artifactOrder
  messages
  approvalsByID
  events
  selectedArtifactID
  selectedReference
  composerDraft
  uploadQueue
```

规则：
- 普通闲聊不创建 run。
- 上传文件不自动创建 run。
- waiting_approval 下的输入是补充说明，不创建新 run。
- paused 下的输入是 pending instruction，不自动继续。
- run 事件永远追加，不插入旧消息中间。

验收：
- “你好 / ? / 今天天气如何”只产生聊天回复，不展开流程。
- 明确生成请求才创建 run。
- 暂停、补充说明、继续的消息顺序稳定。

### Phase 3. WorkbenchShell 和左侧目录

目标：
- 左侧目录只显示当前项目视角，不同时展开小说链和非小说链。
- source_mode 未识别前，只显示通用条目。
- source_mode 确认后，再展开对应链路。

目录规则：
- 剧本固定在前。
- 输入材料固定展示。
- 过程产物按识别结果展开。
- 运行记录固定在最后。

状态文案：
- 未开始：待添加 / 待生成。
- 运行中：生成中。
- 需要用户动作：待确认。
- 已有结果但未走确认动作：已生成。
- 用户明确确认后：已确认。
- 被上游修改影响：已失效。
- 失败：失败。

验收：
- 用户选择小说时，不展示空的非小说素材库、故事种子、剧集蓝图。
- 用户选择非小说时，不展示空的故事圣经、原文拆集。
- 没有确认动作的产物不得显示“已确认”。

### Phase 4. AgentPanel

目标：
- 右侧成为唯一 AI 指令入口。
- 审批卡嵌入 Agent 消息流，不脱离对话。
- 附件和选区引用显示为 chip，不写进 textarea。

组件：
- `MessageList`
- `MessageBubble`
- `TaskStepList`
- `ApprovalCard`
- `ErrorCard`
- `CompletionCard`
- `AgentComposer`
- `AttachmentChip`
- `ReferenceChip`

交互规则：
- Enter 发送。
- Shift+Enter 换行。
- 添加文件后显示 `AttachmentChip`。
- 选中文本后显示 `ReferenceChip`。
- 确认/暂停/重试按钮调用后端接口，不只改本地状态。
- task step 的 done 状态不使用绿色确认勾。

验收：
- 文件名不出现在输入框文本中。
- 审批卡和触发它的 Agent 说明在同一个消息气泡内。
- 等待确认时，用户输入补充说明后不会自动继续生成。

### Phase 5. ArtifactWorkspace 和字段化产物

目标：
- 中间区域展示结构化产物，而不是裸 JSON。
- 同一 artifact 下的字段按语义分组。

Artifact renderer：
- `source_input`：文件、文本、来源、解析状态。
- `story_bible`：主题、人物、世界观、冲突、风险。
- `episode_split`：集数、原文范围、钩子、连续性。
- `material_bank`：素材分类、人物、事件、情绪、可用风险。
- `story_seed`：核心命题、人物关系、主冲突。
- `series_blueprint`：主线、阶段、爽点、反转。
- `episode_cards`：每集目标、开场、冲突、钩子、连续性。
- `scripts`：进入 Lexical `ScriptEditor`。

验收：
- 不再默认展示大段 JSON。
- 空状态说明当前缺什么，而不是空白框。
- 字段缺失时显示“未生成”，不显示 undefined/null。

### Phase 6. Lexical ScriptEditor

目标：
- 最终剧本使用正式 Lexical 编辑器。
- 支持用户像文档编辑器一样改剧本正文。
- 支持 AI 选区修改，但 AI 修改必须以 suggestion/patch 形式由用户确认。

第一版能力：
- 按集、场景、台词、动作、转场分块。
- 加粗、斜体、下划线、删除线、引用、标题层级。
- 撤销/重做。
- 查找。
- 批注。
- 保存状态：已保存 / 未保存 / 保存中 / 保存失败。
- 选区生成 `SelectionContext`。
- suggestion patch 接受 / 拒绝。

验收：
- `npm run gate:editor` 必须继续通过。
- 保存后恢复仍保留 line id、episode id、scene id、marks。
- AI patch 不直接覆盖用户正文。

### Phase 7. SSE / 运行记录

目标：
- 将后端 run event 映射到 UI 状态。
- 右侧只显示摘要，完整 payload 放在运行记录。

规则：
- `run_started`：进入运行中。
- `step_started`：当前步骤生成中。
- `artifact_created`：新增或更新 artifact。
- `approval_requested`：创建嵌入式 ApprovalCard。
- `run_paused`：进入暂停。
- `run_completed`：进入完成。
- `run_failed`：展示 ErrorCard。

验收：
- 事件顺序按后端 created_at/event sequence。
- 刷新页面后可恢复运行记录。
- 技术 payload 默认折叠。

### Phase 8. 视觉定稿和回归

目标：
- 在数据流、状态流、组件边界稳定后，进入 UI 高保真定稿。

需要补：
- shadcn theme tokens。
- 字体、字号、行高、间距、边框、hover/focus 状态。
- 轻量滚动条或隐藏策略。
- 宽屏、普通桌面、小屏响应式。
- 关键状态截图回归。

验收：
- Playwright 覆盖未开始、闲聊、上传、运行中、等待确认、暂停、失败、完成、剧本编辑、局部修改、运行记录。
- 不出现文字溢出、组件重叠、按钮文案断裂。
- UI 状态与 `frontend-ui-state-gallery.html` 覆盖的状态一致；视觉以该稿为基准，只允许在组件化落地时做必要适配。

## 与后端的依赖关系

前端 Phase 1 和 Phase 2 可以在 mock API 上先做，但正式进入 Phase 3 以后需要后端提供：
- 项目消息接口。
- run 查询接口。
- run event 接口或 SSE。
- approval resolve 接口。
- artifact 查询接口。
- 文件上传接口。
- ScriptEditor 保存、suggestion、comment 接口。

如果后端尚未完成，可以先做 mock adapter，但 mock adapter 必须使用正式 API 类型，不再新增一套前端私有协议。

## 测试计划

单元测试：
- 类型转换。
- reducer。
- artifact selector。
- status 文案映射。
- ScriptEditor serialization。

组件测试：
- AgentComposer enter/shift+enter。
- AttachmentChip / ReferenceChip。
- ApprovalCard resolve。
- ProjectSidebar source_mode 展开。
- ArtifactFieldGroup 缺字段显示。

浏览器测试：
- 闲聊不启动 run。
- 上传文件不启动 run。
- 明确生成后启动 run。
- waiting_approval 下补充说明不自动继续。
- 确认后继续下游生成。
- 剧本编辑保存。
- 选区局部修改生成 suggestion。

保留 gate：
- `npm run gate:editor`
- `npm run build`

## 前端开工门槛

正式重构前端前，需要确认：
- `api-event-contract.md` 是当前唯一 API 合同。
- `shared-schema-contract.md` 是当前唯一共享类型合同。
- `script-editor-lexical-spec.md` 是当前唯一最终剧本编辑器合同。
- `frontend-component-language.md` 是当前唯一状态和动作文案来源。
- `frontend-ui-state-gallery.html` 已作为当前 UI/UE 定稿候选；React 实现需保留其状态覆盖和信息层级。
- Go backend 若暂未完全对齐，前端只能用正式类型做 mock adapter。

## 2026-06-30 Project API 迁移快照

本次已把当前 React 工作台从旧的单一 `sendAgentMessage` 入口切到正式项目接口。

已完成：
- `App.tsx` 新增 project state。
- 首次发送消息或上传文件时自动创建项目。
- 文件选择后调用 `uploadProjectFile`，得到 `file_id`。
- 右侧附件只显示 chip，发送消息时只提交 `file_ids`。
- 普通消息调用 `sendProjectMessage(project_id, payload)`。
- 审批按钮优先调用 `resolveApproval(approval_request_id, action)`，不再只靠普通文本消息模拟确认。
- 附件移除时调用 `deleteProjectFile(file_id)`。

保留：
- 旧 `sendAgentMessage` client 方法暂时保留，用于兼容旧调试路径，但当前 App 主路径不再直接使用。

已验证：
- `npm run build` 通过；普通权限下仍可能遇到 Windows / Vite `spawn EPERM`，提升权限重跑通过。
- TypeScript 已通过正式 project/file/approval 类型检查。

当前阻塞：
- 后端生成模型调用未成功产出内容时，前端会收到可读错误，但无法进入完整 episode_cards checkpoint。
- 下一步应优先排查 backend worker 的模型调用和输出校验，而不是继续改 UI。
## 2026-06-30 前端联调状态快照

当前前端已切到正式项目 API 形态，可以用于验证真实链路，但还不是最终交互稿。

已完成：
- 首次发送/上传会创建 project。
- 上传文件进入 project file API，消息发送时提交 `file_ids`。
- 生成请求进入 project message API。
- 分集卡确认按钮进入 approval resolve API。
- Enter 发送、Shift+Enter 换行的基础交互已落地。

已验证：
- 后端无代理环境下，文件 + 文本生成请求可返回 `waiting_approval` 和 episode_cards。
- 确认后后端可生成 scripts 并返回 `completed`。
- `npm run build` 在提升权限下通过；普通 sandbox 仍可能触发 Windows Vite/Rolldown `spawn EPERM`。

未完成：
- 前端仍需要按 Figma 最终稿重构左中右布局、组件对齐、按钮状态、Agent 对话卡片嵌入逻辑。
- 结构化 artifact renderer 还需要替代临时 JSON/简化展示。
- Lexical 剧本编辑器、保存、选区局部修改、suggestion accept/reject 仍是后续开发项。
