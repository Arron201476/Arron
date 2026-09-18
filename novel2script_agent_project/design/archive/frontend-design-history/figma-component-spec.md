# Figma 组件化设计蓝图

> 2026-06-29 当前有效结论：Figma 交付范围已更新为 `02-05` 组件页 + `06 State Frames` 的 29 张中文状态 frame。旧文档中的历史状态页数量属于历史阶段描述；当前以 `figma-delivery-qa.md`、`frame-state-list.md` 和 `frame-state-checklist.md` 为准。

本文档用于把 `frontend-ui-state-gallery.html` 从“静态状态稿”转成可在 Figma 中搭建的组件库和页面 frame。它不是前端代码，也不是要求直接复制 HTML / CSS；它定义 Figma 里应该拆哪些组件、变量、变体和状态页。

当前 Figma 文件已创建，后续组件化以该文件为准：

- 文件名：Novel2Script Agent UI Component Library
- 文件链接：https://www.figma.com/design/BRjZObtDaeZH6mTHTMeZls
- 当前进度：已创建页面骨架、Foundations token、Cover、29 张中文 State Frames、Prototype Flow、State Machine 和 Archive；基础组件、Workbench 组件、Agent 组件、Script Editor 组件已经按源组件页整理，`06 State Frames` 已完成逐帧截图复核。
- 当前限制：本轮 Figma 可以作为正式前端开发的 UI/组件来源；进入开发时仍要用 React/TypeScript 按组件边界实现，并在开发后补 Playwright 视觉回归和组件单测。不要直接复制旧 HTML/CSS 或 Figma 坐标。

## 当前 Figma 搭建状态

截至本轮设计整理，Figma 文件中已有以下页面：

| 页面 | 状态 | 用途 |
|---|---|---|
| 00 Cover | 已建立 | 文件说明、使用边界、设计状态 |
| 01 Foundations | 已建立 | 颜色、排版、间距、圆角、阴影、布局 token |
| 02 Components | 已组件化 | 已有 `Button`、`IconButton`、`Chip`，按钮和图标按钮已按水平/垂直居中规则截图复核 |
| 03 Workbench Components | 已组件化 | 已有 `AppHeader`、`ProjectNavItem`、`WorkspaceSidebar`、`EpisodeCard`，旧 review frame 已移入 Archive |
| 04 Agent Components | 已组件化 | 已有 `MessageBubble`、`TaskStepList`、`ApprovalCard`、`SuggestionPatchCard`、`CompletionCard`、`ErrorCard`、`AgentComposer`、`AgentPanel` |
| 05 Script Editor Components | 已组件化 | 已有 `EpisodeDirectory`、`ScriptEditorToolbar`、`ScriptDocumentCanvas`，目录与编辑器工具栏已拆分 |
| 06 State Frames | 已重建 | 29 张中文状态 frame 已逐帧截图复核对齐、覆盖、贴边和状态逻辑 |
| 07 Prototype Flow | 已建立 | 从输入到生成、确认、编辑、局部修改、异常恢复的主流程 |
| 08 State Machine | 已建立 | run_state、step_status、approval 与前端显示规则 |
| 99 Archive | 已建立 | 被替换的旧稿、参考截图、废弃组件和实验记录 |

当前 Figma 已从“静态状态稿”推进到“可开发组件稿”：`02-05` 组件页已重排为源组件页，`06 State Frames` 已按“先组件、后 frame”的原则重建，状态页中的 Topbar、Sidebar、EpisodeCard、Agent 消息、任务步骤、确认卡、建议修改卡、完成卡、错误卡、Composer、剧本目录、工具栏和正文画布均来自源组件实例。

本轮重点修正包括：组件页混放旧稿、状态页手搓散件、按钮/图标未居中、AgentComposer 贴边或裁切、确认卡没有补充说明入口、编辑器工具栏与正文画布不对齐、正文画布侵入 Agent 区域、完成态仍显示等待确认、错误态使用散文本等问题。所有改动均按“截图发现问题 -> 修改源组件或实例 -> 复截确认”的方式处理。

进入正式前端实现时，应以 Figma 组件结构、本文档、`frontend-figma-react-mapping.md` 和 `frontend-component-spec.md` 为准。Figma 仍不是代码实现，React 侧需要按正式数据结构、可访问性、响应式和测试要求重新实现。

说明：`Chip` 是跨基础组件和 Agent 输入区共用的组件。正式创建时只保留一个源头组件，不要在 `02 Components` 和 `04 Agent Components` 中重复创建同名组件。

## 目标

1. 让设计稿从一张大图变成可复用组件。
2. 让前端能按组件边界实现，而不是照静态 HTML 猜布局。
3. 让用户能逐个验收组件、状态页和交互卡片。
4. 让后续 React 状态页、Playwright 视觉回归和真实前端实现有同一个设计来源。

## Figma 文件结构

```text
Novel2Script Agent UI
  00 Cover / 使用说明
  01 Foundations / Tokens
  02 Components / 基础组件
  03 Workbench Components / 工作台组件
  04 Agent Components / Agent 组件
  05 Script Editor Components / 剧本编辑器组件
  06 State Frames / 29 张中文状态 frame
  07 Prototype Flow / 关键交互流
  99 Archive / 历史参考
```

### 00 Cover / 使用说明

内容：

- 当前版本号。
- 设计状态：UI/UE 定稿候选。
- 约束：右侧 Agent 是唯一 AI 指令入口。
- 约束：中间区不放生成、改写、续写、版本对比等 AI 操作按钮。
- 约束：剧本编辑器不显示代码式行号。
- 链接到 29 张中文状态 frame。

### 01 Foundations / Tokens

应创建变量或样式：

| Token 组 | Figma 表达 | 用途 |
|---|---|---|
| color | Variables / Paint styles | canvas、surface、text、muted、primary、blue、warning、danger |
| typography | Text styles | topbar、nav、body、script body、field label、caption |
| spacing | Variables | 4 / 8 / 10 / 12 / 16 / 20 / 24 / 32 |
| radius | Variables | 6 / 8 / 10 / 12 / 16 / full |
| shadow | Effect styles | card、screen、popover |
| layout | Variables / notes | topbar 56、sidebar 272、agent 404、screen min 1180 |

颜色规则：

- 主色使用青绿，不要让整页变成单色主题。
- 蓝色只用于运行中、引用、选区。
- 琥珀色只用于审批和待确认。
- 红色只用于失败、危险操作和删除内容。
- 成功状态不使用大面积绿色勾；内部 done 用低权重灰点和文字。

## 组件分层

```text
Foundations
  Button
  IconButton
  StatusBadge
  Chip
  Card
  Tabs
  Toolbar
  ScrollArea
  EmptyState

Workbench
  WorkbenchShell
  Topbar
  ProjectSidebar
  ProjectNavItem
  ProjectStatusSummary
  ArtifactWorkspace
  ArtifactHeader
  ArtifactFieldGroup
  EpisodeOutlineRail
  RunEventView

Agent
  AgentPanel
  MessageBubble
  TaskStepList
  ApprovalCard
  SuggestionPatchCard
  ErrorCard
  CompletionCard
  AgentComposer
  AttachmentChip
  ReferenceChip

Script Editor
  ScriptEditorShell
  ScriptEditorToolbar
  ScriptDocumentCanvas
  EpisodeSection
  SceneBlock
  ScriptParagraph
  DialogueBlock
  TransitionBlock
  SelectionHighlight
  CommentMarker
```

## 基础组件

### Button

用途：所有可点击动作。

Variants：

| 属性 | 选项 |
|---|---|
| variant | primary / secondary / ghost / danger |
| size | sm / md / icon |
| state | default / hover / pressed / disabled / loading |

规则：

- 主按钮文案不换行。
- 按钮文案默认水平、垂直居中；只有菜单项、列表项、表格行、树节点等明确左对齐控件例外。
- Figma 源组件必须用 Auto Layout 或等价约束保证居中：固定高度、明确 padding、`primaryAxisAlignItems=center`、`counterAxisAlignItems=center`；不允许用单独文本节点手动调 y 坐标模拟居中。
- 同一区域只允许一个 primary。
- icon-only 必须有 tooltip / aria-label。
- 不写“发送给 Agent”，只写“发送”。

### IconButton

用途：附件、关闭、更多、折叠等只有图标的轻量操作。

规则：

- 源组件必须使用图标节点、矢量节点或几何线段，不用 `+`、`×`、`…` 这类文本符号模拟图标。
- 命中区固定，默认 32-36px，图形中心与命中区中心重合。
- 图标按钮必须有 tooltip / aria-label。
- 不能靠手动拖文本坐标实现视觉居中；截图中只要出现图标偏上、偏下、贴边或被裁切，源组件不算通过。

### StatusBadge

用途：artifact、run、project 的状态展示。

Variants：

| status | 显示 |
|---|---|
| pending_add | 待添加 |
| pending_generate | 待生成 |
| running | 生成中 |
| pending_approval | 待确认 |
| generated | 已生成 |
| confirmed | 已确认 |
| invalidated | 已失效 |
| failed | 失败 |
| saved | 已保存 |
| unsaved | 未保存 |

规则：

- `已确认` 只能在用户真的确认后出现。
- 颜色不能作为唯一信息，必须有文字。
- failed 使用红色；pending_approval 使用琥珀色；running 使用蓝色；generated / confirmed 使用低权重青绿。
- 源组件必须固定高度、明确 padding，并保证文案水平、垂直居中。
- 不允许把 status 文本作为单独散节点手动摆放；状态页只能复用已自测通过的 StatusBadge。

### Chip

用途：附件、引用、meta 信息。

Variants：

| type | 用途 |
|---|---|
| attachment | 本地文件 |
| reference | 选区引用 |
| meta | artifact 元信息 |
| removable | 可移除 |

内容：

- 文件名。
- 文件大小。
- 文件类型或引用来源。
- 可选 remove control。

规则：

- 文件名不进入 textarea。
- 长文件名截断，但 hover / tooltip 可查看完整名。

## 工作台组件

### WorkbenchShell

Figma 结构：

```text
WorkbenchShell
  Topbar
  MainGrid
    ProjectSidebar
    ArtifactWorkspace
    AgentPanel
```

布局：

| 区域 | 宽高 |
|---|---|
| Topbar | height 56 |
| ProjectSidebar | width 272 |
| AgentPanel | width 404 |
| ArtifactWorkspace | fill |

规则：

- 三栏布局稳定。
- 中间区域是主要阅读/编辑区域。
- 右侧 Agent 固定为 AI 入口。
- 左侧不放生成按钮。

### Topbar

内容：

- 产品标识。
- 项目名。
- source mode pill。
- 自动保存状态。
- 项目设置。
- 导出。

不显示：

- prompt 调试入口。
- API key。
- 模型 endpoint。
- 生成按钮。

### ProjectSidebar

内部组件：

```text
ProjectSidebar
  ProjectCard
  ProjectNavGroup
  ProjectNavItem
  ProgressSummary
  CollapseButton
```

规则：

- 未识别 source mode 前只显示通用条目。
- 小说链只显示：输入材料、故事圣经、原文拆集、分集卡、剧本、运行记录。
- 非小说链只显示：输入材料、素材库、故事种子、剧集蓝图、分集卡、剧本、运行记录。
- 运行记录永远在最后。
- 点击 nav item 只切换内容，不触发生成。

### ProjectNavItem

Variants：

| 属性 | 选项 |
|---|---|
| active | true / false |
| status | pending_add / pending_generate / running / pending_approval / generated / confirmed / invalidated / failed |
| density | default / compact |

内容：

- optional icon slot。
- label。
- optional description。
- status text。

规则：

- icon 后续用 lucide 或 iconfont 统一线宽；在图标映射未定前，宁可留空，不允许用正方形占位块代替。
- 不能用绿色勾表示 done。
- active 状态只能通过背景、边框、文字色和已定稿 icon 表达；未配置 icon 时不强行补占位形状，不能出现穿过 label 或 status 的横线。
- NavItem 内部不放装饰性分割线；列表分隔由父级容器统一处理。

### ArtifactWorkspace

Figma 结构：

```text
ArtifactWorkspace
  ArtifactHeader
  ArtifactTabs
  OptionalToolbar
  ContentRegion
  ArtifactFooter
```

Variants：

| mode | 内容 |
|---|---|
| empty | 空项目 / 无活动产物 |
| field_artifact | 故事圣经、素材库、故事种子、剧集蓝图、分集卡 |
| script_editor | 最终剧本 |
| run_events | 运行记录 |
| error | 可恢复错误 |

规则：

- 中间不放 AI 指令按钮。
- 结构化产物不用裸 JSON。
- 最终剧本必须进入 ScriptEditorShell。

### ArtifactFieldGroup

用途：过程产物字段化展示。

结构：

```text
ArtifactFieldGroup
  FieldLabel
  FieldValue
  FieldMeta
```

Variants：

| type | 用途 |
|---|---|
| text | 普通文本 |
| list | 数组内容 |
| warning | 风险提示 |
| source_ref | 来源引用 |
| editable | 轻编辑字段 |

规则：

- 同一个条目下字段必须格式化展示。
- 缺失字段显示“未生成”，不显示 null / undefined。
- 长文本保留换行。

### EpisodeOutlineRail

用途：最终剧本视图的分集定位。分集卡阶段默认使用一集一张的 `EpisodeCard` 列表；只有进入重编辑或长列表定位场景时，才使用和剧本一致的分集目录。

Variants：

| 属性 | 选项 |
|---|---|
| active | true / false |
| status | 待生成 / 生成中 / 待确认 / 已生成 / 已确认 / 失败 |

内容：

- 集数。
- 标题或一句话摘要。
- 状态。

规则：

- 放在中间内容区左侧。
- 不放在右侧 Agent。
- 不承担流程推进，只负责定位。
- 分集卡阶段如果不放目录，必须在主区按集展示 `EpisodeCard`，每张卡包含集数、标题、核心冲突、尾钩和可编辑字段提示。

## Agent 组件

### AgentPanel

结构：

```text
AgentPanel
  AgentHeader
  MessageList
  AgentComposer
```

规则：

- 是唯一 AI 指令入口。
- 审批、补充说明、暂停、继续、局部修改都从这里发生。
- 内部 run event 不以满屏卡片展示，只作为低权重任务步骤或运行记录入口。

### MessageBubble

Variants：

| role | 类型 |
|---|---|
| user | 用户消息 |
| assistant | Agent 文本 |
| assistant_with_tasks | Agent 文本 + TaskStepList |
| assistant_with_approval | Agent 文本 + ApprovalCard |
| assistant_with_patch | Agent 文本 + SuggestionPatchCard |
| assistant_error | Agent 错误说明 |
| assistant_completion | 完成摘要 |

规则：

- ApprovalCard 和 SuggestionPatchCard 必须嵌在 Agent 消息内。
- 用户文件、引用作为 chip 显示。
- 暂停后补充说明作为新用户消息，不插入旧消息中间。

### TaskStepList

用途：展示 Agent 正在做什么。

Step 状态：

| status | 视觉 |
|---|---|
| pending | 空心点 + 灰文本 |
| running | 蓝色 loading 点 + 文本 |
| done | 低权重灰点 +“已完成”文本 |
| failed | 红点 + 失败文本 |

规则：

- 不用绿色确认勾。
- 不展示 raw event payload。
- 只展示用户可理解的步骤名。

### ApprovalCard

用途：等待用户确认后继续。

内容：

- 标题。
- 为什么需要确认。
- 会影响哪些下游产物。
- 主按钮：确认并继续 / 确认并开始生成。
- 次按钮：暂停处理 / 稍后处理。
- 可选风险提示。

Variants：

| approval_type | 场景 |
|---|---|
| episode_cards_to_scripts | 分集卡确认后生成剧本 |
| high_impact_revision | 高影响修改 |
| regenerate_downstream | 重生成下游 |

规则：

- 不能脱离消息流。
- 用户补充说明时不自动继续，除非明确说继续。

### SuggestionPatchCard

用途：局部修改确认。

内容：

- 修改范围。
- 原句。
- 建议句。
- 接受修改。
- 拒绝修改。

规则：

- 不使用代码 diff 行号。
- 删除内容用浅红底。
- 新增内容用浅绿或 primary-soft 底。
- 接受后才写入正文。

### AgentComposer

结构：

```text
AgentComposer
  AttachmentChipList
  ReferenceChipList
  Textarea
  ComposerActions
```

交互：

- Enter 发送。
- Shift+Enter 换行。
- 附件按钮打开本地文件选择。
- 设置按钮打开轻量配置。

规则：

- 附件不写进 textarea。
- 引用不写进 textarea。
- 发送按钮文案是“发送”，不是“发送给 Agent”。
- 底部左侧只保留 `+` 图标按钮作为附件 / 引用入口；已添加文件和引用上下文通过 chip 展示，不再单独放“引”按钮，也不使用“添加文件 / 引用选区”长文本占位。
- 发送按钮必须在容器内保留至少 16px 右侧和底部安全边距，按钮文字默认居中。
- AgentComposer 必须拆成 chip row、textarea row、action row 三层约束；`+` 和“发送”在 action row 内上下左右居中，不能贴边、裁切或覆盖 textarea。

## 剧本编辑器组件

### ScriptEditorShell

结构：

```text
ScriptEditorShell
  ScriptEditorToolbar
  EpisodeOutlineRail
  ScriptDocumentCanvas
  ScriptEditorFooter
```

规则：

- 只用于最终剧本，不用于普通过程产物。
- 支持 Word 类基础编辑。
- `ScriptEditorToolbar` 只属于正文编辑器，必须与 `ScriptDocumentCanvas` 的 x 和 width 对齐；`EpisodeOutlineRail` 是独立定位模块，不进入 toolbar 宽度计算。
- 不显示代码式行号。
- 不使用代码 gutter。
- 数据层保留 episode_id / scene_id / line_id，但 UI 不直接展示这些技术字段。

### ScriptEditorToolbar

基础按钮：

- 撤销。
- 重做。
- 文本类型。
- 字体。
- 字号。
- 加粗。
- 斜体。
- 下划线。
- 删除线。
- 批注。
- 查找。

规则：

- 可使用 lucide 图标，但必须有 tooltip。
- 文案和图标不能挤压换行。
- AI 操作按钮不放这里。

### ScriptDocumentCanvas

用途：剧本文档纸面。

内部节点：

```text
EpisodeSection
  SceneBlock
    SceneHeading
    ActionParagraph
    DialogueBlock
    TransitionBlock
```

规则：

- 正文行宽可读。
- 对白居中或剧本格式化展示。
- 场景标题有稳定背景和层级。
- 视觉上接近 Word 式文档纸面，不使用代码块、代码 gutter、连续行号，也不把每个场景包成厚重卡片。
- 场景、动作、对白是文档内的结构化段落；底层保留 episode / scene / line id，但不在正文视觉里暴露技术字段。
- 选中内容显示正文内 selection highlight，highlight 必须贴合文本所在行，不遮挡文字，不做成独立按钮。
- 批注显示 CommentMarker 或边栏旁注，不压住正文。

## 状态页 Frame

Figma 中应使用组件实例拼出 11 个 frame：

| Frame | 必须覆盖 |
|---|---|
| 01 Empty Project | 空项目、等待输入、Agent 欢迎 |
| 02 File Added | 附件 chip、文件预览、未启动 run |
| 03 Casual Chat | 普通聊天不创建流程 |
| 04 Run Starting | 路径已选择但不展示内部意图识别；输入已保存，第一个可见产物生成中 |
| 05A Novel Running | 小说链目录、故事圣经/拆集、任务步骤 |
| 05B Non Novel Running | 非小说链目录、素材库/蓝图、任务步骤 |
| 06 Waiting Approval | 分集卡、分集目录、Agent 内嵌 ApprovalCard |
| 07 Script Editing | Word 类剧本编辑器、分集目录、工具栏 |
| 08 Local Revision | 选区高亮、ReferenceChip、SuggestionPatchCard |
| 09 Complete | 剧本完成摘要、质量建议、继续编辑 |
| 10 Error Retry | 最后可用产物、错误卡、重试路径 |

每个 frame 规则：

- 完整三栏，不做局部截图。
- 组件实例来源一致。
- 文案来自 `frontend-component-language.md`。
- 状态与 `frontend-ui-state-spec.md` 一致。
- 不出现 lorem ipsum、裸 JSON 或技术调试文案。

## Figma 搭建顺序

1. 创建 Foundations / Tokens。
2. 创建基础组件：Button、StatusBadge、Chip、Card、Tabs、Toolbar、ScrollArea。
3. 创建 Workbench 组件：Topbar、Sidebar、ArtifactWorkspace、EpisodeOutlineRail。
4. 创建 Agent 组件：MessageBubble、TaskStepList、ApprovalCard、SuggestionPatchCard、AgentComposer、AgentPanel。
5. 创建 Script Editor 组件：Toolbar、DocumentCanvas、SceneBlock、DialogueBlock、SelectionHighlight。
6. 重建 29 张中文状态 frame。
7. 做组件级检查：文本是否溢出、按钮是否换行、scroll 区是否合理。
8. 做状态页检查：29 张状态 frame 是否完整、是否与 product / API / agent 合同冲突。

## Figma 验收清单

- 每个页面 frame 都由组件实例构成，不是全部散节点。
- 基础组件至少覆盖 default / hover / pressed / disabled / loading。
- 状态 badge 覆盖所有 artifact 状态。
- Agent 确认卡和局部修改卡是独立组件。
- 附件和引用 chip 是独立组件。
- 剧本编辑器 toolbar、场景块、对白块、选区高亮是独立组件。
- Workbench、Agent、Script Editor 源头组件必须先通过截图自测，再回填 State Frames。
- 所有分割线统一使用中性色 divider，不用暖色或状态色做普通内容分割。
- 所有按钮文案默认水平、垂直居中；图标按钮必须有明确含义和 tooltip / aria-label。
- 所有按钮、图标按钮、chip、toolbar button 和 composer action 必须由约束布局保证居中，不允许靠视觉手摆坐标。截图中一旦出现图标偏上/偏下、文字贴边、控件裁切，源组件不算通过。
- AgentComposer 中附件和引用显示为 chip，不写入 textarea；底部动作区只保留 `+` 图标按钮和发送按钮。
- ApprovalCard 和 SuggestionPatchCard 必须嵌入 Agent 对话流，不单独浮在右侧面板外。
- 分集卡阶段主区必须能一集一集读懂，不能只显示“故事圣经 + 分集卡”两个抽象卡片。
- 剧本正文必须是 Word-like 文档面，不能回退成多层场景卡片或代码编辑器视觉。
- 小说链和非小说链的左侧目录不会同时展开。
- 完成态和错误态都保留继续编辑或恢复路径。
- 文本不溢出按钮、chip、card。
- 没有 emoji 结构图标。
- 没有代码式行号或代码 gutter。

## 与前端实现的关系

Figma 组件名应映射到 React 组件名：

| Figma 组件 | React 组件 |
|---|---|
| WorkbenchShell | `WorkbenchShell` |
| Topbar | `Topbar` |
| ProjectSidebar | `ProjectSidebar` |
| ProjectNavItem | `ProjectNavItem` |
| ArtifactWorkspace | `ArtifactWorkspace` |
| ArtifactFieldGroup | `ArtifactFieldGroup` |
| EpisodeOutlineRail | `EpisodeOutlineRail` |
| AgentPanel | `AgentPanel` |
| MessageBubble | `MessageBubble` |
| TaskStepList | `TaskStepList` |
| ApprovalCard | `ApprovalCard` |
| SuggestionPatchCard | `SuggestionPatchCard` |
| AgentComposer | `AgentComposer` |
| AttachmentChip | `AttachmentChip` |
| ReferenceChip | `ReferenceChip` |
| ScriptEditorShell | `ScriptEditor` |
| ScriptEditorToolbar | `ScriptEditorToolbar` |
| ScriptDocumentCanvas | `ScriptDocumentCanvas` |
| SceneBlock | `SceneBlock` |
| DialogueBlock | `DialogueBlock` |

前端实现时应以 Figma 组件结构和本文档为准，不直接复制 `frontend-ui-state-gallery.html` 的 CSS。Figma 与 React 的落地映射见 `frontend-figma-react-mapping.md`；该文档同时记录了当前 Figma 仍是 review frame 而非完整 component set 的限制。
