# Novel2Script 前端组件库落地方案

本文档定义后续 TypeScript / React 前端如何把设计稿落成组件库。`frontend-ui-state-gallery.html` 是当前 UI/UE 定稿候选和状态源；`figma-component-spec.md` 定义这些状态在 Figma 中应如何拆成组件、变体和页面 frame。二者都不是最终组件库，也不应直接复制 HTML / CSS 到业务代码。

## 当前结论

前端组件库采用三层结构：

1. 基础 UI 组件层：按钮、状态、弹窗、输入、菜单、Tabs、Tooltip、ScrollArea。
2. 业务工作台组件层：三栏 Shell、目录、Artifact 展示、Agent 对话、审批卡、任务步骤。
3. 剧本正文编辑器层：Word 类基础编辑能力 + 剧本结构块。

目标不是做一个通用后台模板，而是做一个短剧写作 / 改编工作台。

## 依赖策略

当前 `frontend` 已有：

- React / TypeScript / Vite。
- `lucide-react`：可作为统一图标库。
- CodeMirror：适合输入原文、素材、Markdown / 纯文本编辑，不适合作为最终剧本富文本编辑器。

建议补充：

| 方向 | 推荐 | 原因 |
|---|---|---|
| 基础组件 | shadcn/ui 风格组件 + Radix 行为组件 | 组件质量高，可控，可按项目设计系统改样式 |
| 图标 | lucide-react | 已安装，线性图标适合工作台 |
| 剧本编辑器 | Tiptap 与 Lexical 双候选 | 两者都适合富文本和结构化编辑；需要 spike 后定稿 |
| 视觉回归 | Playwright | 用于状态稿和组件库回归 |

注意：

- shadcn/ui 不是传统 npm 组件包，而是把组件代码生成到项目里；后续要明确是否引入 Tailwind。
- 如果不引入 Tailwind，也可以按 shadcn 的组件结构和 Radix 行为思想，实现本地 CSS token 版本。
- Tiptap 基于 ProseMirror，适合快速做富文本、marks、comments、AI patch 等能力。
- Lexical 是 Meta 的现代编辑器框架，性能、可访问性、JSON 状态和自定义节点能力都强。
- 不直接默认原生 ProseMirror：能力强但底层，开发成本高。
- 不优先 Slate：除非团队很熟，否则它的 beta 状态和维护节奏会增加风险。
- CodeMirror 保留给原文 / 素材输入，不作为最终剧本富文本编辑器。

## 剧本编辑器 spike 决策

在进入真实 ScriptEditor 开发前，先用同一份剧本样例分别验证 Tiptap 和 Lexical。

必须验证：

- 基础格式：加粗、斜体、下划线、删除线、标题、引用。
- 剧本块：episode、scene、action、dialogue、transition。
- 自定义节点 / block type 的实现成本。
- 选区定位：能否稳定得到 episode id、scene id、line id、offset range。
- AI patch：能否把候选改写作为 suggestion 展示，用户确认后再落库。
- 批注：能否对选区挂 comment thread。
- 序列化：能否稳定转成 JSON，并从 JSON 恢复编辑器状态。
- 粘贴：从 Word / 网页粘贴时是否可控。
- 性能：长剧本、多集、多场景下输入和滚动是否稳定。

定稿标准：

- 如果 Tiptap 更快满足结构化剧本和 AI patch，则选 Tiptap。
- 如果 Lexical 在性能、状态控制和自定义节点上明显更稳，则选 Lexical。
- 不以“哪个更有名”作为选择依据，以 spike 结果为准。

## 组件目录建议

Figma 组件、React 组件和状态页的映射关系见 `figma-component-spec.md`。真实前端实现前，必须先确认 Figma 组件树和 React 组件树是否一致。

```text
frontend/src/components/
  ui/
    button.tsx
    badge.tsx
    tooltip.tsx
    dialog.tsx
    tabs.tsx
    scroll-area.tsx
    dropdown-menu.tsx
    textarea.tsx
    separator.tsx
  workbench/
    WorkbenchShell.tsx
    Topbar.tsx
    ProjectSidebar.tsx
    ProjectNavItem.tsx
  artifacts/
    ArtifactWorkspace.tsx
    ArtifactHeader.tsx
    ArtifactFieldGroup.tsx
    RunEventView.tsx
  agent/
    AgentPanel.tsx
    MessageBubble.tsx
    TaskStepList.tsx
    ApprovalCard.tsx
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
    SuggestionPatch.tsx
```

## 基础 UI 组件原则

- 所有图标使用 lucide-react，不使用 emoji 作为结构图标。
- 按钮文案短，不换行；图标按钮必须有 `aria-label` / tooltip。
- 状态颜色来自 `frontend-design-system.md`，不得在业务组件里随意写 raw color。
- Button、Badge、Chip、Card、Dialog、Tabs、Tooltip、ScrollArea 先做稳定基础组件，再做业务组件。
- 不做全局花哨动画；只保留 hover、pressed、focus、loading、disabled 等必要状态。

## 工作台组件原则

- `WorkbenchShell` 只负责布局，不负责业务判断。
- `ProjectSidebar` 只导航和展示 artifact 状态，不触发生成。
- `ArtifactWorkspace` 只展示 / 编辑当前内容，不发起 AI 指令。
- `AgentPanel` 是唯一 AI 指令入口。
- 审批卡必须嵌在 Agent 消息流内。
- 中间区域可以出现人工编辑工具栏，但不出现生成、改写、续写、版本对比等 AI 按钮。

## 剧本编辑器原则

最终剧本必须用 `ScriptEditor`，不能用普通 textarea，也不能用过程产物字段卡片替代。

第一版能力：

- Word 类基础编辑：加粗、斜体、下划线、删除线、批注、查找、撤销 / 重做、保存状态。
- 剧本结构块：episode、scene、action、dialogue、transition。
- 选区引用：artifact id、episode id、scene id、line id、offset range。
- AI patch：模型改写只生成候选 / 差异，用户确认后写入编辑器。

第一版不做：

- 页眉页脚。
- 多栏排版。
- 复杂表格。
- 艺术字。
- 完整 Word 文件排版兼容。

## Gallery 与组件库的关系

`frontend-ui-state-gallery.html` 用于确认视觉方向、信息层级和状态覆盖，是 React 状态页的设计来源，但不是可直接复制的实现源码。

真实组件库落地后，应新增一个 React 状态展示页，用真实组件渲染以下状态：

- 未开始。
- 普通聊天。
- 上传文件。
- 小说运行中。
- 非小说运行中。
- 等待确认。
- 暂停。
- 失败。
- 完成。
- 剧本编辑。
- 局部修改。
- 运行记录。

到那一步，HTML gallery 只保留为设计参考，不再作为前端验收主稿。

## 落地顺序

1. 整理 design tokens：颜色、字号、间距、圆角、阴影。
2. 建基础 UI 组件：Button、Badge、Chip、Tooltip、Dialog、Tabs、ScrollArea。
3. 建工作台 Shell：Topbar、Sidebar、Workspace、AgentPanel。
4. 建 Agent 对话组件：Message、TaskStepList、ApprovalCard、Composer。
5. 建 Artifact 展示组件：FieldGroup、EventView。
6. 接入 ScriptEditor 技术方案。
7. 用真实 React 组件重建状态 gallery。
8. 接入 mock runtime 数据。
9. 做 Playwright 视觉回归。

## 准入检查

进入真实前端实现前必须确认：

- 是否引入 Tailwind + shadcn 官方 CLI。
- 是否完成 Tiptap vs Lexical 的 ScriptEditor spike。
- 是否根据 spike 结果安装 Tiptap 或 Lexical。
- 是否保留 CodeMirror 仅用于原文 / 素材输入。
- 是否接受当前 `frontend-ui-state-gallery.html` 作为 UI/UE 定稿候选，并进入 React 组件状态页落地。
- 是否先做 React 组件状态页，再接真实后端。
