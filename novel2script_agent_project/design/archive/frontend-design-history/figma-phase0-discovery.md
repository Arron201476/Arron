# Figma Phase 0 Discovery

> 2026-06-29 当前有效结论：Phase 0 / Phase 1 的早期发现已被本轮 Figma 交付覆盖。当前状态为：`02-05` 组件页已重建，`06 State Frames` 已扩展为 29 张中文状态 frame，并完成结构 QA 与逐帧截图。

状态：开发准入发现结果

本文档记录进入 Figma 组件化写入前的真实盘点结果。它用于避免把 review frame 误当成正式组件库，也用于决定下一步先做哪些组件。

## 结论

1. Figma 文件已经有页面结构、变量和 29 张中文状态 frame。
2. Phase 0 盘点时，Figma 本地正式组件数量为 0：没有 `COMPONENT`，也没有 `COMPONENT_SET`。
3. Phase 1 第一批已创建 `Button`、`IconButton`、`Chip` 三个正式 component set。
4. Phase 1 第二批已创建 Agent 源组件：`MessageBubble`、`TaskStepList`、`ApprovalCard`、`SuggestionPatchCard`、`AgentComposer`、`AgentPanel`。
5. Phase 1 第三批已创建 Workbench 和 Script Editor 源组件：`ProjectNavItem`、`EpisodeCard`、`ScriptEditorToolbar`、`ScriptDocumentCanvas`。
6. `03 Workbench Components` 和 `05 Script Editor Components` 已清理旧 review frame：旧稿移入 `99 Archive`，组件页只保留说明卡和源 component set。
7. `06 State Frames` 的 29 张中文状态 frame 已完成重建，并完成逐帧截图复核；不再把早期三栏 layout 草稿作为交付来源。
8. `02 Components` 和 `04 Agent Components` 的旧 review frame 已移入 `99 Archive`，组件页应只保留源 component set / component，不再混放旧稿。
9. 当前 React 前端仍是早期 mock 壳，核心界面堆在 `frontend/src/App.tsx`，正式的 Workbench、Agent、ScriptEditor 组件还没有落地。

## Figma 盘点

| 项目 | 当前结果 | 影响 |
|---|---|---|
| 页面结构 | 已有 `00 Cover` 到 `99 Archive` | 可以继续在现有文件内推进 |
| 变量 | 已有 4 组变量：Color Primitives、Semantic Color、Spacing + Radius、Layout | 可以作为组件样式基础，但还要做命名和绑定 QA |
| 正式组件 | 已有基础、Workbench、Agent、Script Editor 源组件，包括 `Button`、`IconButton`、`Chip`、`AppHeader`、`WorkspaceSidebar`、`EpisodeCard`、`MessageBubble`、`TaskStepList`、`ApprovalCard`、`SuggestionPatchCard`、`CompletionCard`、`ErrorCard`、`AgentComposer`、`AgentPanel`、`EpisodeDirectory`、`ScriptEditorToolbar`、`ScriptDocumentCanvas` | 可作为 React 组件拆分和状态页验收来源 |
| 状态页 | 11 个，已用源组件 instance 重建并逐帧截图复核 | 可以作为正式前端开发的状态参考 |
| 目标 shell | 统一三栏结构：左侧项目工作区、中间内容区、右侧 Agent 助手 | React 侧应按组件边界实现，而不是复制坐标 |

## 前端盘点

当前前端已有：

- `frontend/src/components/ui/Button.tsx`
- `frontend/src/components/ui/Badge.tsx`
- `frontend/src/components/ui/EmptyState.tsx`
- `frontend/src/components/TextEditor.tsx`
- `frontend/src/spikes/script-editor/*` 中的 Lexical / Tiptap spike

当前前端仍缺：

- `WorkbenchShell`
- `Topbar`
- `ProjectSidebar`
- `ProjectNavItem`
- `ArtifactWorkspace`
- `ArtifactFieldGroup`
- `EpisodeCard`
- `AgentPanel`
- 正式 `ScriptEditor`

## v1 组件范围

第一批不应该一次做完所有组件。先做能修正用户反复指出问题的核心组件：

1. `Button`
2. `IconButton`
3. `Chip`
4. `ProjectNavItem`
5. `AgentComposer`
6. `MessageBubble`
7. `TaskStepList`
8. `ApprovalCard`
9. `SuggestionPatchCard`
10. `ScriptEditorToolbar`
11. `ScriptDocumentCanvas`
12. `EpisodeCard`

## 写入顺序

1. 先建基础组件：`Button`、`IconButton`、`Chip`。
2. 再建 Agent 组件：`MessageBubble`、`TaskStepList`、`ApprovalCard`、`SuggestionPatchCard`、`AgentComposer`、`AgentPanel`。
3. 已建工作台组件：`ProjectNavItem`、`EpisodeCard`。
4. 已建剧本编辑器组件：`ScriptEditorToolbar`、`ScriptDocumentCanvas`。
5. 已重建 29 张中文状态 frame，并逐帧截图修正入口、小说线、非小说线、闲聊线和通用状态中的逻辑/对齐问题。
6. 下一步可进入正式前端开发前的实现计划确认：按 Figma 组件树映射 React 组件、固定 API 字段、制定 Playwright 视觉回归。

## 不做的事

- 不直接把所有旧状态页坐标硬改一遍。
- 不把 review frame 继续包装成“已完成组件”。
- 不把中间内容区加 AI 操作按钮。
- 不把意图识别等内部步骤暴露给用户。
- 不在这一步开始 React 前端开发。

## 下一步出口

Figma Phase 1 纠偏已完成。下一步进入正式前端开发准备：确认 React 组件树、接口字段和测试计划后，再开始 TypeScript 前端实现。
