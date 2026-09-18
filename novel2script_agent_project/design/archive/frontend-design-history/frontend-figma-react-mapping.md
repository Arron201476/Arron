# Frontend Figma React Mapping

> 2026-06-29 当前有效结论：Figma `06 State Frames` 已更新为 29 张中文状态 frame，包括入口线、小说线、非小说线、闲聊线和通用线。旧文档中的历史状态页数量属于历史阶段描述；当前以 `figma-delivery-qa.md` 和 `frame-state-list.md` 为准。

本文档把 Figma 设计稿、React 组件、API 数据字段和当前前端旧实现之间的关系固定下来。它用于进入正式前端开发前的拆组件和验收，不是视觉稿，也不是 API 详细定义。

## 当前结论

1. Figma 文件 `BRjZObtDaeZH6mTHTMeZls` 已经有页面、变量和 29 张中文状态 frame；基础组件、Workbench 组件、Agent 组件、Script Editor 组件已整理为源组件页，状态页已逐帧截图复核。
2. 前端开发可以先按本文档拆 React 组件；不能把 Figma 里的散节点坐标当成代码布局来源。
3. 当前 `frontend/src/App.tsx` 属于早期 mock 壳，不是正式 UI 实现来源。正式开发时应按本文档、`frontend-component-spec.md`、`figma-component-spec.md` 和 `shared-schema-contract.md` 重构。
4. 当前 Figma 可以作为正式前端开发的 UI/组件来源。开发前仍需要做代码侧实现计划确认：组件树、API 字段、状态映射、Playwright 视觉回归和组件单测。

## Source Of Truth

| 内容 | 主要来源 | 说明 |
|---|---|---|
| 视觉方向 | `frontend-ui-state-gallery.html` + Figma `06 State Frames` | 用于理解页面状态，不直接复制 CSS |
| 组件规则 | `frontend-component-spec.md` + `figma-component-spec.md` | React 拆组件以这两份为准 |
| 文案规则 | `frontend-component-language.md` | 状态文案、按钮文案、Agent 消息语气 |
| 数据字段 | `shared-schema-contract.md` + `api-event-contract.md` | API JSON 字段和状态枚举 |
| 剧本编辑器 | `script-editor-lexical-spec.md` | 正式 ScriptEditor 采用 Lexical |
| Figma 文件 | `https://www.figma.com/design/BRjZObtDaeZH6mTHTMeZls` | 当前是组件化设计稿来源，`02-05` 源组件页和 `06` 状态页已完成本轮截图复核 |

## Figma Page Mapping

| Figma 页面 | 当前状态 | 前端用途 |
|---|---|---|
| `00 Cover` | 已建立 | 设计说明，不进 React |
| `01 Foundations` | 已建立 | token 来源，转为 CSS variables / Tailwind tokens |
| `02 Components` | 已组件化 | Button、IconButton、Chip 等基础源组件 |
| `03 Workbench Components` | 已组件化 | AppHeader、ProjectNavItem、WorkspaceSidebar、EpisodeCard |
| `04 Agent Components` | 已组件化 | MessageBubble、TaskStepList、ApprovalCard、SuggestionPatchCard、CompletionCard、ErrorCard、AgentComposer、AgentPanel |
| `05 Script Editor Components` | 已组件化 | EpisodeDirectory、ScriptEditorToolbar、ScriptDocumentCanvas |
| `06 State Frames` | 29 张中文状态 frame，已完成截图与结构 QA | 页面状态验收来源；前端实现按组件边界还原，不复制坐标 |
| `07 Prototype Flow` | 已建立 | 主流程理解来源 |
| `08 State Machine` | 已建立 | 状态转换参考 |
| `99 Archive` | 已建立 | 历史稿，不进实现 |

## React Module Layout

```text
frontend/src/
  components/
    common/
      Button.tsx
      IconButton.tsx
      StatusBadge.tsx
      Chip.tsx
      Card.tsx
      Tabs.tsx
      ToolbarButton.tsx
      ScrollArea.tsx
      EmptyState.tsx
    workbench/
      WorkbenchShell.tsx
      Topbar.tsx
      ProjectSidebar.tsx
      ProjectNavItem.tsx
      ProjectStatusSummary.tsx
      ArtifactWorkspace.tsx
      ArtifactHeader.tsx
      ArtifactTabs.tsx
      ArtifactFooter.tsx
      ArtifactFieldGroup.tsx
      EpisodeCard.tsx
      EpisodeOutlineRail.tsx
      RunEventView.tsx
    agent/
      AgentPanel.tsx
      MessageList.tsx
      MessageBubble.tsx
      TaskStepList.tsx
      ApprovalCard.tsx
      SuggestionPatchCard.tsx
      ErrorCard.tsx
      CompletionCard.tsx
      AgentComposer.tsx
      AttachmentChip.tsx
      ReferenceChip.tsx
    script-editor/
      ScriptEditor.tsx
      ScriptEditorToolbar.tsx
      ScriptDocumentCanvas.tsx
      EpisodeSection.tsx
      SceneBlock.tsx
      ScriptLineBlock.tsx
      SelectionHighlight.tsx
      CommentMarker.tsx
  api/
    client.ts
    types.ts
  state/
    projectSelectors.ts
    artifactSelectors.ts
    viewSelectors.ts
```

## Component Mapping

### Common

| React 组件 | Figma 来源 | 数据输入 | 不允许 |
|---|---|---|---|
| `Button` | `02 Components` Button examples | `variant`, `size`, `disabled`, `loading`, `children` | 文案不居中；写“发送给 Agent” |
| `IconButton` | `02 Components` IconButton examples | `icon`, `ariaLabel`, `tooltip`, `disabled` | 用 `+ / × / …` 文本模拟图标 |
| `StatusBadge` | `02 Components` StatusBadge examples | `status`, `label` | 未确认时显示“已确认” |
| `Chip` | `04 Agent Components` chip examples | `type`, `label`, `meta`, `onRemove` | 把文件名或引用写进 textarea |
| `Card` | 多页面 card examples | `children`, `tone` | 卡片套卡片，或把页面 section 做成大卡片 |
| `Tabs` | center header examples | `items`, `activeKey` | 点击触发生成流程 |
| `ToolbarButton` | Script Editor toolbar | `icon`, `label`, `active` | AI 生成/改写按钮 |

### Workbench

| React 组件 | Figma 来源 | 数据输入 | 规则 |
|---|---|---|---|
| `WorkbenchShell` | `06 State Frames` top-level shell | `project`, `run`, `activeView` | 固定三栏：左侧项目，中间内容，右侧 Agent |
| `Topbar` | `Topbar` | `project.title`, `project.source_mode`, save status | 不显示 key、endpoint、prompt 调试 |
| `ProjectSidebar` | `ProjectSidebar / no placeholder icons` | `project`, `run`, `active_artifacts` | 只展开当前链路，不同时展示小说和非小说全链路 |
| `ProjectNavItem` | sidebar nav row | `label`, `status`, `active`, `count` | 不用正方形占位 icon；未定 icon 时留空 |
| `ProjectStatusSummary` | progress summary | `project.status`, progress estimate | 不把内部 step 当用户确认 |
| `ArtifactWorkspace` | center workspace | `activeArtifact`, `viewMode` | 中间不放 AI 操作按钮 |
| `ArtifactHeader` | center title/header | artifact envelope | 显示版本、状态、更新时间 |
| `ArtifactFieldGroup` | field cards | artifact payload fields | 不显示裸 JSON / null / undefined |
| `EpisodeCard` | 分集卡状态页 | `EpisodeCardPayload` | 一集一张卡，字段化展示 |
| `EpisodeOutlineRail` | 剧本/分集定位 | episodes, active episode | 只定位，不推进流程 |
| `RunEventView` | 运行记录 | `RunEvent[]` | 默认不展示 raw payload / prompt / API key |

### Agent

| React 组件 | Figma 来源 | 数据输入 | 规则 |
|---|---|---|---|
| `AgentPanel` | right panel | messages, run status, approval | 唯一 AI 指令入口 |
| `MessageList` | message area | `Message[]` | 新消息只追加到底部，不插入旧消息中间 |
| `MessageBubble` | user / agent bubble | role, content, parts | 审批卡和修改建议卡嵌在 agent 消息内 |
| `TaskStepList` | task rows | `StepViewModel[]` | `done` 展示为低权重“已完成”，不用绿色确认勾 |
| `ApprovalCard` | approval card | `ApprovalRequest` | 主按钮只有一个，确认走 approval resolve API |
| `SuggestionPatchCard` | patch card | `ScriptSuggestion` | 接受后才写入正文 |
| `ErrorCard` | failure card | recoverable error | 不展示堆栈或模型原始错误给普通用户 |
| `CompletionCard` | complete summary | completed run summary | 不做营销式成功页 |
| `AgentComposer` | composer | draft text, attachments, selection reference | 左侧只留 `+` 图标入口，发送按钮写“发送” |
| `AttachmentChip` | file chip | `FileRef` / local pending file | 文件名不进入 textarea |
| `ReferenceChip` | selection chip | `SelectionContext` | 选区引用不进入 textarea |

### Script Editor

| React 组件 | Figma 来源 | 数据输入 | 规则 |
|---|---|---|---|
| `ScriptEditor` | `05 Script Editor Components` + states 07/08/09 | `ScriptDocument`, `editor_state` | Lexical 实现，不用 textarea / CodeMirror |
| `ScriptEditorToolbar` | toolbar | editor command state | 与文档纸面宽度对齐，不包含 EpisodeOutlineRail |
| `ScriptDocumentCanvas` | document paper | episodes, scenes, lines | Word-like 文档面，不显示代码行号 |
| `EpisodeSection` | episode grouping | `ScriptEpisode` | 保留 stable ids，不用数组下标定位 |
| `SceneBlock` | scene block | `ScriptScene` | 场景标题、动作、对白层级清楚 |
| `ScriptLineBlock` | line block | `ScriptLine` | block type 决定显示，不暴露 line_id |
| `SelectionHighlight` | text selection | `SelectionContext` | 不遮挡文字，不做成按钮 |
| `CommentMarker` | comment marker | `ScriptComment` | 只标记选区，不压住正文 |

## View Model Contracts

前端组件不要直接消费原始 API payload。建议建立 view model selector。

### ProjectNavItemView

```ts
interface ProjectNavItemView {
  key: string;
  label: string;
  status: "pending_add" | "pending_generate" | "running" | "pending_approval" | "generated" | "confirmed" | "invalidated" | "failed";
  active: boolean;
  count?: number;
  artifact_type?: ArtifactType;
}
```

规则：
- `confirmed` 只能来自用户确认动作后的 artifact / approval 状态。
- `generated` 表示系统已生成但不等于用户确认。
- `running` 由 run/step 状态推导，不由 artifact title 推导。

### StepViewModel

```ts
interface StepViewModel {
  step_id: string;
  label: string;
  status: "pending" | "running" | "completed" | "failed" | "skipped";
  started_at?: string;
  completed_at?: string;
}
```

显示规则：
- `completed` 显示为“已完成”，低权重灰点。
- `running` 显示 spinner + 当前动作。
- 不使用绿色确认勾。

### ArtifactViewModel

```ts
interface ArtifactViewModel {
  artifact_id: string;
  artifact_type: ArtifactType;
  title: string;
  version: number;
  status: ArtifactStatus;
  source_mode: SourceMode;
  updated_at: string;
  sections: ArtifactSectionView[];
}
```

过程产物统一转成 `sections` 后展示，避免每个组件自己解析 payload。

### EpisodeCardView

```ts
interface EpisodeCardView {
  episode_id: string;
  episode_no: number;
  title: string;
  opening_pressure: string;
  main_conflict: string;
  character_state?: string;
  hook: string;
  risk_notes: string[];
  source_refs: ArtifactRef[];
}
```

规则：
- 分集卡阶段默认显示 EpisodeCard 列表。
- 如果集数多，可增加 EpisodeOutlineRail 做定位，但不能只显示目录不显示卡片。

### AgentComposerView

```ts
interface AgentComposerView {
  draft: string;
  attachments: AttachmentChipView[];
  references: ReferenceChipView[];
  disabled: boolean;
  mode: "chat" | "new_run" | "approval_instruction" | "paused_instruction" | "selection_revision";
}
```

规则：
- `waiting_approval` 时输入解释为补充说明，不自动继续。
- `paused` 时输入写入 pending instruction，不自动继续。
- 有 selection context 时显示 ReferenceChip。

## State Frame To React Route Mapping

| Figma state | React route/state | active view | 必需数据 |
|---|---|---|---|
| `01 Empty Project` | project idle | `source_input` empty | project shell, no run |
| `02 File Added` | files attached, no run | `source_input` preview | attachments / file parse result |
| `03 Casual Chat` | chat only | empty artifact | messages only, no run |
| `04 Run Starting` | run created | first visible artifact | run, first step, saved source input |
| `05A Novel Running` | novel run running | `episode_split` or `episode_cards` | novel nav items, step list |
| `05B Non Novel Running` | non-novel run running | `series_blueprint` or `episode_cards` | non-novel nav items, step list |
| `06 Waiting Approval` | `waiting_approval` | `episode_cards` | approval request, episode cards |
| `07 Script Editing` | scripts generated | `scripts` editor | ScriptDocument, save status |
| `08 Local Revision` | selection suggestion | `scripts` editor | SelectionContext, ScriptSuggestion |
| `09 Complete` | run completed | `scripts` editor / quality summary | completed run, scripts artifact |
| `10 Error Retry` | run failed | last usable artifact | error info, retry action, last artifact |

## Current Frontend Delta

当前 `frontend/src/App.tsx` 不是正式实现，进入开发时需要重构或替换：

| 当前代码现象 | 与合同冲突 | 处理方式 |
|---|---|---|
| `StepList` 使用 `CheckCircle2` 和 `done` 文本 | 内部 completed step 不能像用户确认 | 改为 `TaskStepList` view model，显示“已完成” |
| send button title 是“发送给 Agent” | 按钮文案合同要求只写“发送” | `aria-label` 可写“发送消息”，可见文案只写“发送” |
| 左侧 nav 仍有简化顺序 | 与 Figma/信息架构不完全一致 | 用 `ProjectNavItemView` 统一生成 |
| `ArtifactPayload` 中部分 artifact 仍手写解析 | 容易散落字段逻辑 | 先转 `ArtifactViewModel` 再展示 |
| `ScriptPanel` 是普通预览 | 正式剧本必须是 Lexical `ScriptEditor` | Phase 6 替换 |
| `SourceMode` 里有 `auto` | `shared-schema-contract.md` API 枚举不含 `auto` | client 内部可保留 `auto`，API 边界要转换为 `unknown` 或由后端识别 |

## Development Entry Criteria

正式开前端前至少完成：

1. Figma 状态帧已完成本轮 freeze：三栏结构、Topbar、AgentPanel、ProjectSidebar 和组件实例关系以 `06 State Frames` 为准。
2. React 组件目录按本文档建立，不继续在 `App.tsx` 堆页面。
3. API types 从 `shared-schema-contract.md` 生成或手写对齐，禁止临时字符串状态。
4. 所有 mock fixture 覆盖 10 个 state frame。
5. Playwright 视觉回归至少覆盖：空项目、上传文件、普通聊天、非小说运行、等待确认、剧本编辑、局部修改、错误恢复。

## Non Goals

- 不要求现在继续扩展更多 Figma 组件；本轮已覆盖正式前端开发所需的核心组件和状态 frame。
- 不要求现在实现 React 代码。
- 不要求第一版支持完整 Word 兼容。
- 不允许为了赶开发跳过 `ScriptEditor` 的 Lexical 数据模型。
