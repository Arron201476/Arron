# Novel2Script 前端组件规格

本文档把 UI 设计系统落到可实现组件。后续 React / TypeScript 前端应按这里拆组件，而不是直接照 HTML 复制样式。Figma 页面、React 组件、API 字段和当前前端旧实现之间的具体映射见 `frontend-figma-react-mapping.md`。

## 组件层级

```text
WorkbenchShell
  Topbar
  ProjectSidebar
  ArtifactWorkspace
  AgentPanel
```

## WorkbenchShell

职责：

- 管理三栏布局。
- 管理响应式折叠。
- 承接 project status、runtime status。

不负责：

- 判断是否启动 run。
- 修改 artifact。
- 拼装 prompt。

## Topbar

显示：

- 产品标识。
- 项目名。
- project status。
- runtime/provider 脱敏状态。

不显示：

- 生成按钮。
- prompt 调试入口。
- API key / provider endpoint。

## ProjectSidebar

组件：

```text
ProjectSidebar
ProjectNavItem
ProjectStatusSummary
```

NavItem 字段：

| 字段 | 说明 |
|---|---|
| label | 剧本、输入材料、故事圣经等 |
| description | 一句话说明或数量 |
| status | 待添加 / 待生成 / 生成中 / 待确认 / 已生成 / 已确认 / 已失效 / 失败 |
| active | 是否当前选中 |
| invalidated_reason | 已失效原因，可选 |

规则：

- 未识别 source_mode 前不展开小说 / 非小说两条完整链路。
- run 创建后按 `run.source_mode` 展开对应链路。
- 点击只切换中间内容，不触发生成。
- 运行记录永远在最后。

## ArtifactWorkspace

组件：

```text
ArtifactWorkspace
ArtifactHeader
ArtifactEmptyState
ArtifactSkeleton
ArtifactFieldGroup
ScriptEditor
RunEventView
```

职责：

- 展示当前 artifact。
- 支持字段化阅读。
- 支持选区引用。
- 展示保存、失效、失败状态。

不负责：

- 发起 AI 指令。
- 承载审批按钮。
- 展示完整 prompt。

### ArtifactHeader

显示：

- artifact 名称。
- 版本。
- 状态。
- 来源链路。
- 最近更新时间。

允许的轻操作：

- 复制引用。
- 展开字段。
- 查看版本。

不允许：

- 生成 / 重写 / 局部改写按钮。

### ArtifactFieldGroup

用于故事圣经、素材库、故事种子、剧集蓝图、分集卡。

字段结构：

```text
FieldGroup
  FieldLabel
  FieldValue
  FieldMeta
```

字段值展示规则：

- 数组用列表。
- 风险用轻量 warning。
- 来源追踪用小号文本或折叠区。
- 长文本保留换行。

### ScriptEditor

选型结论：正式 `ScriptEditor` 采用 Lexical。详细节点模型、选区、suggestion patch、批注、保存和验收规则见 `script-editor-lexical-spec.md`。

ScriptEditor 是最终剧本的正文编辑器，不是过程产物的字段卡片，也不是普通 textarea。

内部组件：

```text
ScriptEditor
  ScriptEditorToolbar
  EpisodeOutlineRail
  ScriptDocumentCanvas
  EpisodeSection
  SceneBlock
  ScriptLineBlock
  CommentThread
  SuggestionPatch
```

第一版要求：

- 按集分组。
- 每集按场景分组。
- 最终剧本和分集卡视图必须提供分集目录 / EpisodeOutlineRail，用于定位，不用于流程推进。
- 对白、动作、转场有不同视觉层级。
- 支持用户直接编辑正文。
- 支持常用文字格式：加粗、斜体、下划线、删除线、引用、标题层级。
- 支持批注 / 评论入口，用于用户自己标记修改点。
- 支持撤销 / 重做、保存状态、未保存提示。
- 支持选中文本后生成 ReferenceChip。
- 选区修改只通过右侧 Agent 发起。
- 选区引用必须携带 artifact id、episode id、scene id、line id、offset range。
- 模型改写返回 patch 后，先展示差异或候选，不直接覆盖用户正文。
- 正文不显示代码式行号；`line_id` 只用于数据定位，不直接展示给用户。
- 正文不使用代码编辑器 gutter、等宽代码块背景或代码高亮风格。

不要求第一版完整兼容 Word，但要按“文档编辑器 / 块编辑器”设计。过程产物可以用 `ArtifactFieldGroup` 增删改，最终剧本必须用 `ScriptEditor`。

### ScriptEditorToolbar

Toolbar 属于人工编辑工具栏，不是 AI 操作区。

第一版工具：

| 工具 | 说明 |
|---|---|
| undo / redo | 撤销 / 重做 |
| block type | 场景标题、动作、对白、转场、普通段落 |
| bold / italic / underline / strike | 常用文字格式 |
| comment | 对选区添加批注 |
| find | 查找正文 |
| save status | 已保存 / 未保存 / 保存中 |

规则：

- 工具栏可以在中间编辑器顶部 sticky。
- 图标优先，必要时加短文案；后续实现使用 lucide 图标。
- 不出现“生成剧本 / 改写 / 续写 / 版本对比”按钮。
- AI 修改入口仍然是右侧 Agent；工具栏只负责本地编辑和格式。

### EpisodeOutlineRail

用途：
- 在最终剧本和分集卡视图中快速定位集。
- 可选展示当前集下的场景折叠列表。

显示：
- 集数。
- 一句话标题或摘要。
- 状态。
- 当前选中态。

规则：
- 不放生成、改写、继续等 AI 操作按钮。
- 不显示不存在的小说 / 非小说链路。
- 点击只定位中间内容，不触发 run。
- 在窄屏下可折叠为下拉目录或抽屉。

实现建议：

- 编辑器数据层保留结构化块，而不是只保存 HTML 字符串。
- 富文本 marks 和剧本结构分开：加粗 / 下划线是 mark，场景 / 动作 / 对白是 block type。
- 初始导入模型生成剧本时，把 `scripts` artifact 转成编辑器 document tree。
- AI 修改只生成 patch / suggestion，由用户确认后写入编辑器状态。
- ScriptEditor 技术选型在 Tiptap 和 Lexical 之间做 spike 后定稿。
- 不直接默认原生 ProseMirror：能力强但底层，开发成本高。
- 不优先 Slate：除非团队已有经验，否则 beta 状态和维护节奏会增加风险。
- CodeMirror 更适合源码、Markdown、纯文本和原文输入，不作为最终剧本富文本编辑器的默认方向。

### ScriptEditor Spike

Spike 必须用同一份剧本样例验证 Tiptap 和 Lexical。

验证项：

| 项 | 要求 |
|---|---|
| 基础格式 | 加粗、斜体、下划线、删除线、标题、引用 |
| 剧本结构 | episode、scene、action、dialogue、transition |
| 选区定位 | 输出 artifact id、episode id、scene id、line id、offset range |
| AI patch | 只展示 suggestion，用户确认后写入 |
| 批注 | 选区可挂 comment thread |
| 序列化 | JSON 可保存、恢复、对比 |
| 粘贴 | Word / 网页粘贴结果可控 |
| 性能 | 多集长剧本下输入、滚动、选区不卡顿 |

定稿标准：

- Tiptap 如果更快满足结构化剧本和 patch 预览，优先 Tiptap。
- Lexical 如果在性能、状态控制、自定义节点上明显更稳，优先 Lexical。
- 不因名气或默认印象选型，以 spike 结果为准。

## AgentPanel

组件：

```text
AgentPanel
MessageList
MessageBubble
TaskStepList
ApprovalCard
ErrorCard
CompletionCard
SuggestionPatchCard
AgentComposer
AttachmentChip
ReferenceChip
```

职责：

- 用户输入主入口。
- 普通聊天。
- 生成/改编意图。
- 补充说明。
- 审批动作。
- 暂停、重试、取消。
- 局部修改请求。

## MessageBubble

类型：

| 类型 | 规则 |
|---|---|
| user | 右侧弱蓝背景，不插入附件文件名 |
| agent | 白底或浅灰底，承载说明、任务步骤和卡片 |
| system | 仅用于低权重系统状态，不混入用户对话 |

消息流规则：

- 新消息永远追加在底部，不插入旧消息中间。
- 审批卡嵌在 Agent 消息内。
- 运行事件只摘要展示，完整 payload 去运行记录。

## TaskStepList

用途：

- 表达 Agent 正在做什么。
- 给用户 loading 感。

显示：

| 状态 | 显示 |
|---|---|
| pending | 灰点 + 步骤名 |
| running | 小 spinner + 正在... |
| done | 灰点 + 已完成文本 |
| failed | 红色文本 + 重试入口 |

禁止：

- done 用绿色确认勾。
- 每一步做成按钮。
- 大量事件刷屏。

## ApprovalCard

ApprovalCard 是高权重组件。

字段：

| 字段 | 说明 |
|---|---|
| title | 需要确认什么 |
| reason | 为什么停下来 |
| impact | 继续后影响哪些产物 |
| primary_action | 确认继续 |
| secondary_actions | 补充要求、暂停、重生成这一步 |

规则：

- 必须嵌在 Agent 消息内。
- 主按钮只能一个。
- 补充要求不是按钮跳转，用户可直接在 composer 输入。
- 点击确认走 approval resolve API，不由前端本地改状态。

## AgentComposer

组成：

```text
AttachmentChipList
ReferenceChipList
Textarea
ComposerToolbar
SendButton
```

规则：

- Enter 发送。
- Shift+Enter 换行。
- 文件显示为 AttachmentChip，不进入 textarea。
- 引用显示为 ReferenceChip，不进入 textarea。
- waiting_approval 时输入为补充说明，不创建新 run。
- paused 时输入为 pending instruction，不自动继续。

## SuggestionPatchCard

用于局部修改后等待用户确认。

显示：
- 修改范围，例如第几集、第几场、角色对白。
- 删除内容和新增内容的可读对比。
- 接受修改。
- 拒绝修改。

规则：
- 必须嵌在 Agent 消息内。
- 不使用代码 diff 行号。
- 不把 patch 弹到中间正文顶部。
- 接受后写入 ScriptEditor 并保存新 artifact version。
- 拒绝后正文不变。

## Chip

### AttachmentChip

显示：

- 文件名。
- 文件类型。
- 文件大小。
- 解析状态。
- 移除按钮。

### ReferenceChip

显示：

- artifact 名称。
- episode / scene / selection。
- 版本。

## StatusBadge

状态文案必须来自 `frontend-component-language.md`。

实现建议：

```text
neutral: 待添加 / 待生成
active: 生成中
warning: 待确认 / 已失效
success-muted: 已生成
confirmed: 已确认
danger: 失败
```

## RunEventView

默认展示：

- 时间。
- 事件摘要。
- 状态。
- 关联 artifact。

折叠展示：

- payload。
- prompt 摘要。
- LLM 输入输出摘要。

禁止默认展示：

- 完整 prompt。
- 完整 raw LLM output。
- API key。
- 本地 storage path。

## React 映射建议

```text
components/workbench/WorkbenchShell.tsx
components/workbench/Topbar.tsx
components/workbench/ProjectSidebar.tsx
components/artifacts/ArtifactWorkspace.tsx
components/artifacts/ArtifactFieldGroup.tsx
components/artifacts/ScriptEditor.tsx
components/agent/AgentPanel.tsx
components/agent/MessageBubble.tsx
components/agent/TaskStepList.tsx
components/agent/ApprovalCard.tsx
components/agent/AgentComposer.tsx
components/common/StatusBadge.tsx
components/common/Chip.tsx
```

## 验收清单

- 左侧没有 AI 操作按钮。
- 中间没有生成 / 改写按钮。
- 右侧输入按钮文案是“发送”。
- 审批卡嵌在对话流里。
- 任务步骤不是按钮。
- done 不使用绿色确认勾。
- 文件和选区是 chip。
- Artifact 内容字段化展示。
- 状态文案与 `frontend-component-language.md` 一致。
- 小屏有目录抽屉和中间 / Agent 切换策略。
