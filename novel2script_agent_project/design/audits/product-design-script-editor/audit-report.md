# Product Design Audit: Script Editor And UI State Gallery

审计日期：2026-06-26

## Audit Scope

本次只审计当前可见的前端设计状态，不审计真实模型输出质量。

证据来源为本轮新截图：

- `01-gallery-novel-flow.png`：UI 状态稿中的未开始 / 小说链路状态。
- `02-gallery-script-edit.png`：UI 状态稿中的小说运行与等待确认状态。
- `03-tiptap-spike.png`：Tiptap 剧本编辑器候选。
- `04-lexical-spike.png`：Lexical 剧本编辑器候选。

用户目标：前端进入正式开发前，需要确认三栏工作台、Agent 操作流、剧本正文编辑器、过程产物展示和状态反馈是否足够清楚，避免继续沿用过于原始的 mock 交互。

无障碍目标：关键操作可被键盘使用、状态变化可被理解、按钮/输入/附件/确认卡有清晰标签，长文本编辑不依赖纯视觉线索。

## Step List

1. `01-gallery-novel-flow.png`：未开始状态。健康度：中等，可表达大结构，但仍偏静态展示。
2. `02-gallery-script-edit.png`：小说链路运行中。健康度：中等偏低，流程关系可见，但画面密度和确认机制仍不够像真实产品。
3. `03-tiptap-spike.png`：Tiptap 编辑器。健康度：中等，适合快速落地 Word-like 编辑，但结构化剧本能力需要补强。
4. `04-lexical-spike.png`：Lexical 编辑器。健康度：中等偏高，适合结构化剧本节点，但实现成本更高。

## Strengths

- 三栏关系已经比早期 mock 清楚：左侧是项目目录，中间承载内容，右侧承载 Agent 对话和流程推进。
- 左侧目录已经开始按链路收敛，没有再把小说和非小说两条链的所有步骤同时暴露给用户。
- 确认动作已经从中间内容区移向 Agent 区域，方向正确，因为确认、补充说明、暂停、继续都属于 Agent 交互。
- 剧本正文已经单独作为重编辑区域处理，而不是把所有 artifact 都用同一种 JSON 卡片呈现。
- Tiptap 和 Lexical spike 都证明基础富文本能力可落地，包括加粗、斜体、下划线、删除线、批注入口、保存和 patch 预览。

## UX Risks

### 1. Gallery 仍像状态说明稿，不像最终操作界面

旧截图 `01` 和 `02` 适合定义信息架构，但不适合作为最终视觉定稿。当前 `frontend-ui-state-gallery.html` 已升级为 UI/UE 定稿候选；进入实现前仍需要用户验收，并在 React 组件状态页中复刻其信息层级和交互语义。

建议：gallery 作为状态契约和 UI/UE 定稿候选；正式实现时减少说明文字，强化真实可操作控件，并通过 React 组件状态页做视觉回归。

### 2. Agent 区域仍需要更像对话流

截图 `02` 里 Agent 有消息、任务步骤和输入框，但确认卡仍像一个流程面板。真实体验里，确认卡应嵌入对话流，和 Agent 的解释、用户补充说明、继续/暂停操作形成同一条时间线。

建议：Agent 右侧消息模型统一为：

- 用户消息。
- Agent 文本回复。
- Agent 正在执行状态。
- tool/task step chips。
- approval card。
- attachment pill。
- run summary card。

不要再把确认卡、日志和聊天拆成互不相干的模块。

### 3. 左侧目录的状态文案需要绑定真实动作

截图 `01` 里有 `待生成`、`待添加`、`未识别`，方向比“已确认”正确。但后续必须明确：只有用户点击过确认，或 Agent 明确完成过确认动作，才能显示“已确认”。自动生成出来的内容只能叫“已生成”“待确认”“生成中”“待生成”。

建议：左侧状态只显示 artifact 生命周期，不显示 Agent 过程细节。

### 4. 中间内容区需要分两种编辑强度

过程产物适合结构化字段编辑，例如故事圣经、人物、分集卡；最终剧本适合 Word-like 正文编辑。当前 gallery 已经分出方向，但还需要写成组件规则，避免所有内容都塞进同一个 editor。

建议：

- `ScriptEditor`：正文级编辑，支持格式、批注、局部修改、选择引用、保存版本。
- `ArtifactFieldEditor`：字段级编辑，支持增删字段、重排、局部重写、确认。
- `ArtifactViewer`：只读或弱编辑，用于运行过程回看。

### 5. Tiptap spike 更快，但剧本结构需要额外建模

截图 `03` 中 Tiptap 的正文体验更接近普通富文本编辑器，工具栏也更自然。风险是结构锚点依赖 `data-episode-id` / `data-scene-id` / `data-line-id`，如果没有严格节点规范，局部修改时容易只拿到文本片段，拿不到稳定的 episode / scene / line 来源。

建议：如果选 Tiptap，必须定义自定义节点或严格 schema，不要只用普通 paragraph 承载剧本行。

### 6. Lexical spike 更适合结构化，但视觉表现还没到产品级

截图 `04` 的每一行更像独立剧本节点，便于绑定 episode / scene / line / block type。风险是工具栏、选区、批注、撤销栈和 JSON 视图还偏工程验证，不是给最终用户看的。

建议：如果选 Lexical，先把底层节点模型打稳，再用 shadcn/ui 或同等组件重做工具栏、批注、版本、保存状态。

## Accessibility Risks

- 截图无法证明键盘焦点顺序、屏幕阅读器语义、快捷键冲突和输入法行为，需要后续浏览器测试。
- 部分工具栏按钮只有短标签或图标，正式实现必须提供 `aria-label`、tooltip 和可见 focus ring。
- 左侧状态 pill 不能只靠颜色区分，需要文字和语义状态同步。
- 运行中、完成、等待确认等状态不能只用视觉位置表达，需要在 Agent 消息流里有明确文本。
- 剧本正文编辑器要避免把结构信息只存在视觉排版中；场次、对白、转场、动作都应有可被程序识别的节点类型。

## Recommendations

1. 把 `frontend-ui-state-gallery.html` 定位为状态契约和 UI/UE 定稿候选；用户验收前不视为最终冻结稿。
2. 下一步先选编辑器底座：Lexical 更适合结构化剧本，Tiptap 更适合快速接近 Word-like 体验。
3. 在选型前补一轮真实交互 spike：选择一段对白，让 Agent 读取选区来源，生成 suggestion patch，再由用户接受/拒绝。
4. 把 Agent 右侧重构为统一消息流，确认卡、附件、任务步骤、运行总结都作为消息 part。
5. 建立正式组件库：按钮、输入、附件、任务步骤、确认卡、状态 pill、左侧目录项、文档页、剧本行节点。
6. 高保真 UI 定稿应基于真实 fixture 和长文本，不再使用过短占位文案。

## Evidence Limits And Verification Gaps

- 本次是截图审计和本地 spike 审计，不等于完整可用性测试。
- 没有覆盖移动端、窄屏、缩放 200%、键盘全流程、屏幕阅读器。
- 没有接真实模型输出，因此无法判断长剧本、多 episode、多 artifact 的性能和排版稳定性。
- 没有验证后端 run / artifact / approval 事件是否与 UI 状态逐项绑定。

## Next Design Gate

正式前端开发前，建议先完成一个“编辑器选型 gate”：

- 用同一份长剧本 fixture。
- 分别验证 Tiptap 和 Lexical 的节点结构、选区来源、patch 应用、批注、撤销、保存和导出。
- 输出结论：选择一个作为 `ScriptEditor` 底座，另一个归档。
