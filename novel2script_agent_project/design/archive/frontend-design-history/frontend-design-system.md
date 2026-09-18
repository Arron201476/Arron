# Novel2Script 前端设计系统

本文档定义 Novel2Script Agent 工作台的视觉与布局规则。它承接 `information-architecture-contract.md`、`frontend-component-language.md` 和 `frontend-ui-state-spec.md`，用于约束后续 TypeScript 前端实现。

## 设计定位

Novel2Script 是写作 / 改编工作台，不是营销页、后台管理页，也不是通用聊天机器人。

设计关键词：

- 内容优先。
- 安静、专业、可长时间阅读。
- 三栏稳定布局。
- Agent 动作集中在右侧。
- 中间区域像结构化编辑器，而不是调试 JSON 面板。

不采用：

- 大面积紫色 / 蓝紫渐变。
- 装饰性发光、气泡、玻璃拟态。
- 大 hero、营销式卡片堆叠。
- 把所有事件做成按钮卡。

## 视觉方向

第一版视觉方向采用“写作工作台 + 结构化文档编辑器”，而不是通用 SaaS 后台或聊天应用。

当前视觉方向选择见 `frontend-visual-direction-decision.md`。正式稿以视觉方向 1 的左侧、中间和右侧整体布局为基础，吸收方向 3 的右侧局部修改确认和附件 chip，并补齐分集目录。

必须体现：

- 中间区域有文档纸面感和字段化编辑层级，默认阅读的是内容，不是 JSON。
- UI 状态稿里的故事、对白、剧情文本都是占位示例；真实内容必须来自 artifact / 模型输出 / 用户编辑结果。
- 左侧目录像项目导航，能快速判断当前链路和产物状态，但不承担流程推进。
- 右侧 Agent 像工作流对话区，消息、任务步骤、审批卡在同一条对话流里。
- 内部步骤 done 用低权重灰点和文字，不用绿色确认勾。
- 等待确认、失败、完成、暂停使用不同卡片权重，不能都长成一样的白框。
- 滚动条、边框、阴影要轻，但要有层级；不能粗重到抢占剧本内容。

视觉约束：

- 可以使用非常轻的纸张线、网格线、左侧文档 spine 来强化写作场景。
- 可以使用青绿、蓝、琥珀三类功能色，但任一颜色不能统治整页。
- 不使用 emoji 作为图标；后续 React 实现使用 lucide 图标，并保持统一线宽。
- 中间区不出现“生成剧本 / 局部改写 / 版本对比”等 AI 操作按钮，这些需求全部从右侧 Agent 发起。

## 色彩 Token

第一版采用偏中性的写作工作台色彩，少量青绿色作为主操作色，琥珀色用于审批，红色用于失败。

```text
--color-canvas: #f6f7f8
--color-surface: #ffffff
--color-surface-subtle: #f9fafb
--color-surface-raised: #ffffff

--color-text: #172033
--color-text-strong: #0f172a
--color-text-muted: #667085
--color-text-faint: #98a2b3

--color-line: #d9e2ec
--color-line-soft: #e8eef5

--color-primary: #0f766e
--color-primary-strong: #115e59
--color-primary-soft: #e7f6f3

--color-accent: #2563eb
--color-accent-soft: #eaf2ff

--color-warning: #b45309
--color-warning-soft: #fff7ed

--color-danger: #b42318
--color-danger-soft: #fef3f2

--color-success: #047857
--color-success-soft: #ecfdf3
```

使用规则：

- Primary 只用于当前主操作、选中状态和少量高亮。
- Warning 只用于等待确认 / 审批。
- Danger 只用于失败、取消、删除、不可恢复动作。
- Success 不用于内部 step done；内部 step done 用低权重文本或灰点。
- 不允许用颜色作为唯一信息，必须配合文字。

## 字体与字号

字体：

```text
font-family: Inter, "Segoe UI", "Microsoft YaHei", Arial, sans-serif
```

如果后续不接网络字体，直接使用系统字体。

字号：

| Token | Size | 用途 |
|---|---:|---|
| text-xs | 12px | 辅助标签、时间、状态说明 |
| text-sm | 13px | 侧栏、Agent 辅助信息 |
| text-md | 14px | 默认 UI 文案 |
| text-body | 15px | 对话、正文预览 |
| text-lg | 18px | 面板标题 |
| text-xl | 22px | 状态稿标题 |

规则：

- 不使用 viewport width 缩放字体。
- letter-spacing 保持 0。
- 长文正文 line-height 为 1.72。
- UI 文案 line-height 为 1.45-1.6。

## 间距与密度

采用 4 / 8px 节奏：

```text
space-1: 4px
space-2: 8px
space-3: 12px
space-4: 16px
space-5: 20px
space-6: 24px
```

工作台密度：

- 左侧目录每行高度 44-52px。
- 右侧消息气泡上下间距 10-12px。
- 中间 artifact 字段块内边距 14-18px。
- 按钮最小点击高度 36px；主要按钮建议 38-40px。

## 圆角、边框、阴影

| Token | Value | 用途 |
|---|---:|---|
| radius-sm | 6px | chip、状态标签 |
| radius-md | 8px | 按钮、输入框、字段块 |
| radius-lg | 10px | 消息气泡、审批卡 |
| radius-panel | 12px | 状态稿外壳 |

规则：

- 页面区域不要做大圆角漂浮卡片。
- 卡片只用于消息、审批、错误、字段块和重复条目。
- 阴影只用于顶层状态稿、浮层或当前焦点，不用于每个普通 item。

## 三栏布局

桌面布局：

```text
Topbar: 52px
Left: 260px
Center: minmax(560px, 1fr)
Right: 360px
```

规则：

- Topbar 只展示项目名、状态、runtime 脱敏状态。
- Left 只展示项目目录和 artifact 状态。
- Center 只展示当前 artifact / 剧本编辑器 / 运行记录。
- Right 是 Agent 唯一主入口。

禁止：

- Center 顶部出现“生成剧本”“局部改写”“版本对比”这类 AI 操作按钮。
- Left 出现流程推进按钮。
- Right 的审批卡脱离消息流。

## 滚动策略

- 页面本身不横向滚动。
- 三栏内部各自滚动，但滚动条要轻量。
- Center 长内容滚动，Right 消息区滚动，composer 固定在底部。
- Left 目录固定高度滚动，运行记录永远在最后。

滚动条：

```text
width: 8px
thumb: #c7d2df
track: transparent
hover thumb: #98a2b3
```

## 状态视觉

| 状态 | 视觉规则 |
|---|---|
| 待添加 / 待生成 | 灰色文本，低权重 |
| 生成中 | 蓝色点或细进度，不用大 spinner 堆满 |
| 待确认 | 琥珀色边框 / 背景，仅审批卡高权重 |
| 已生成 | 中性或 primary-soft，不等于已确认 |
| 已确认 | 只在用户明确确认后出现 |
| 已失效 | 灰 + warning 提示，说明原因 |
| 失败 | 红色错误卡，保留恢复动作 |

## 中间编辑区

中间内容分为两类：过程产物和最终剧本。

过程产物必须字段化：

- 故事圣经：概要、角色、关系、世界规则、必须保留事实。
- 素材库：用户事实、模型推断、缺失信息、卖点。
- 剧集蓝图：阶段、高潮、人物弧、付费点。
- 分集卡：集数、开头压力、主冲突、人物变化、尾钩、风险。

最终剧本必须使用正文编辑器：

- 按集、场景、动作、对白、转场分层。
- 当前内容是最终剧本或分集卡时，中间区必须有分集目录 / Episode Outline，用于快速定位集和场景。
- 用户可以直接编辑正文。
- 支持选择文本后发起 Agent 局部修改。
- 支持常用排版标记：加粗、斜体、下划线、删除线、引用、批注 / 评论、标题层级。
- 支持撤销 / 重做、复制粘贴、查找、保存状态、未保存提示。
- 支持从结构化剧本块映射回 artifact：episode、scene、line、speaker、selection range。
- 第一版可以不追求完整 Word 兼容，但不能把最终剧本当作普通字段卡片或纯 textarea。

剧本编辑器排版：

- 中间区可以出现 Word 类格式工具栏；它只承载人工编辑能力，不承载生成、改写、续写、版本对比等 AI 指令。
- 工具栏默认收敛为一行：撤销、重做、段落类型、加粗、斜体、下划线、删除线、批注、查找、保存状态。
- 正文采用文档页布局，推荐正文宽度 720-840px，左右留白足够，长时间编辑不贴边。
- 场景标题、动作、对白、转场必须有不同的块样式；不能只靠用户手动加粗来区分剧本结构。
- 基础排版能力服务阅读和修改，不追求复杂 Word 排版，例如页眉页脚、复杂表格、多栏排版、艺术字等不进入第一版。
- 剧本正文禁止显示连续行号、代码编辑器 gutter、代码块式等宽正文和代码高亮式背景。
- `line_id`、`scene_id`、`episode_id` 只存在于数据层，不直接展示给用户。
- 如果需要定位，只显示“第 2 集 / 第 3 场”这类轻量标签，不显示每行编号。

禁止裸 JSON 作为默认展示。技术 JSON 只能在运行记录的折叠详情里出现。

## 响应式策略

第一版以前端桌面工作台为主，但必须定义降级：

```text
>= 1180px: 三栏并排
900-1179px: 左侧可折叠，中间 + Agent 并排
< 900px: 左侧抽屉，中间 / Agent 使用 tabs 切换
```

窄屏优先级：

1. 当前内容。
2. Agent 输入。
3. 左侧目录。
4. 运行记录。

## 可访问性

- 所有按钮需要可键盘聚焦。
- focus ring 不可移除。
- 图标按钮必须有 aria-label / tooltip。
- 颜色不作为唯一状态表达。
- 输入框支持 Enter 发送、Shift+Enter 换行。
- 审批卡主按钮和次按钮必须有清晰 tab 顺序。

## 设计验收

合格表现：

- 用户一眼知道当前项目在哪个状态。
- 用户能区分“已生成”和“已确认”。
- 用户知道 AI 指令只能从右侧 Agent 发起。
- 用户能在中间阅读结构化内容，不需要看 JSON。
- 等待确认时，审批卡在对话流里。
- 内部 step done 不像用户确认。

不合格表现：

- 大面积紫色或渐变像 AI 营销模板。
- 左侧同时展开小说 / 非小说两条完整链路。
- 中间堆操作按钮。
- 右侧消息区像技术日志流。
- 文件名进入 textarea。
- 滚动条粗重、嵌套滚动互相抢焦点。
