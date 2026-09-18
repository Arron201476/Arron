# ScriptEditor Lexical 规格

状态：开发准入规格

结论：正式剧本正文编辑器 `ScriptEditor` 采用 Lexical 作为底座。Tiptap spike 保留为对照，不进入主实现路线，除非未来为 Tiptap 补自定义 node/schema 后重新通过 gate。

## 目标

`ScriptEditor` 不是普通富文本编辑器，也不是 textarea。它负责最终剧本正文的重编辑场景，必须同时满足：

- 用户像 Word 一样编辑正文。
- Agent 能从用户选区稳定定位到 artifact、episode、scene、line、offset。
- AI 修改以 suggestion patch 进入，不直接覆盖用户正文。
- 批注、版本、保存、导出都能追溯到结构化剧本节点。
- 视觉表现采用 Word-like 文档纸面：无代码式行号、无代码 gutter、无裸 JSON、无厚重场景卡片堆叠。

## 非目标

- 不承载故事圣经、素材库、故事种子、剧集蓝图、分集卡等过程产物的字段编辑。
- 不在中间编辑器里放“生成 / 改写 / 续写”按钮，AI 操作入口仍在右侧 Agent。
- 不追求第一版完整复刻 Word，只保证剧本正文所需的基础编辑、批注、建议和结构追踪。
- 不把每个场景渲染成独立重卡片；场景、动作、对白应作为同一文档纸面内的结构段落。

## 组件边界

```text
ScriptEditor
  ScriptEditorToolbar
  ScriptDocumentCanvas
  EpisodeNodeView
  SceneNodeView
  ScriptLineNodeView
  SelectionReferenceBridge
  SuggestionLayer
  CommentLayer
  VersionStatusBar
```

`ScriptEditor` 输入 `scripts` artifact，输出用户编辑后的 `scripts` 新版本，或者向右侧 Agent 提供 `SelectionContext`。

## Lexical 节点模型

第一版至少定义这些自定义节点：

| 节点 | 用途 | 必需字段 |
|---|---|---|
| `ScriptDocumentNode` | 整个剧本文档根节点 | `artifact_id`, `version`, `source_mode` |
| `EpisodeHeadingNode` | 集标题 | `episode_id`, `episode_no`, `title` |
| `SceneHeadingNode` | 场标题 | `episode_id`, `scene_id`, `scene_no`, `title`, `location`, `time_of_day` |
| `ScriptLineNode` | 剧本正文行 | `episode_id`, `scene_id`, `line_id`, `block_type`, `speaker` |
| `SuggestionNode` | AI 建议片段 | `suggestion_id`, `status`, `base_version`, `target` |
| `CommentAnchorNode` | 批注锚点 | `comment_id`, `target`, `status` |

`block_type` 第一版枚举：

```text
action
dialogue
transition
scene_note
```

规则：

- episode / scene / line 必须使用稳定 id，不使用数组下标作为长期定位。
- 富文本 mark 与剧本结构分离；加粗、斜体、下划线、删除线是 text mark，不是 block type。
- 用户手动新增行时，前端可以创建临时 id，但保存时必须由后端确认正式 id。
- 从模型生成结果导入时，必须先把 `scripts` artifact 转为 Lexical document tree，再进入编辑器。

## SelectionContext

用户选中文本后，`SelectionReferenceBridge` 必须生成完整 selection snapshot：

```json
{
  "artifact_id": "artifact_xxx",
  "artifact_type": "scripts",
  "version": 1,
  "node_id": "line_xxx",
  "episode_id": 1,
  "scene_id": "scene_01",
  "line_id": "line_03",
  "start_scene_id": "scene_01",
  "end_scene_id": "scene_02",
  "start_line_id": "line_03",
  "end_line_id": "line_07",
  "line_ids": ["line_03", "line_04", "line_07"],
  "selection_scope": "range",
  "block_type": "dialogue",
  "selection_start": 12,
  "selection_end": 46,
  "selected_text": "",
  "before_context": "",
  "after_context": "",
  "selection_hash": "sha256_xxx"
}
```

规则：

- 发送给 Agent 时锁定 snapshot，后续用户继续编辑不能改变已发送请求的 selection。
- `selected_text` 必须来自当前 Lexical state，不从 DOM 文本临时拼接。
- 单行选区时，`selection_start` / `selection_end` 是目标 line 内 offset，不是整篇文档 offset。
- 多行选区时，`selection_start` 是首行起点，`selection_end` 是末行终点；`line_ids` 必须按文档顺序完整列出覆盖到的行。
- 同一集内允许跨行、跨场景连续选区；拖选方向不影响最终顺序，前端必须归一化为文档顺序。
- 不允许跨集选区。剧本编辑器按单集加载，从交互结构上阻止一次选中多个 episode。
- 多行局部修改必须保留原来的行数、`line_id`、`scene_id`、`block_type` 和 `speaker`。首尾行只替换框选片段，中间行替换整行选中内容。
- `selection_hash` 用于后端判断 selection 是否已经过期。

## Suggestion Patch

AI 局部修改不直接覆盖正文，先创建 suggestion：

```json
{
  "suggestion_id": "suggestion_xxx",
  "artifact_id": "artifact_xxx",
  "base_version": 1,
  "target": {
    "episode_id": 1,
    "scene_id": "scene_01",
    "line_id": "line_03",
    "selection_start": 12,
    "selection_end": 46,
    "selection_hash": "sha256_xxx"
  },
  "before": "",
  "after": "",
  "reason": "",
  "status": "suggested"
}
```

`status` 枚举：

```text
suggested
accepted
rejected
stale
superseded
```

规则：

- `suggested` 只展示差异，不写成正式正文。
- 用户点击接受后，才把 `after` 写入 Lexical state，并生成 artifact 新版本。
- 用户拒绝后，正文不变，suggestion 标记为 `rejected`。
- 如果 `base_version` 或 `selection_hash` 不匹配当前正文，suggestion 标记为 `stale`，不允许直接接受。
- 第一版允许整段替换目标 line 的选区，不做复杂文本 diff 合并。

## Comment Thread

批注也是结构化对象，不只是一段浮层文本：

```json
{
  "comment_id": "comment_xxx",
  "artifact_id": "artifact_xxx",
  "version": 1,
  "target": {
    "episode_id": 1,
    "scene_id": "scene_01",
    "line_id": "line_03",
    "selection_start": 12,
    "selection_end": 46,
    "selection_hash": "sha256_xxx"
  },
  "author": "user",
  "body": "",
  "status": "open",
  "created_at": "",
  "updated_at": ""
}
```

`status` 枚举：

```text
open
resolved
stale
deleted
```

规则：

- 批注入口可以在 toolbar，但批注 thread 的展开和回复在右侧或正文边栏呈现。
- 如果正文编辑导致 selection hash 失效，批注显示为 `stale`，不静默挪到错误文本上。
- 第一版可以不做多人协作，但数据结构保留 author 和时间。

## 保存与版本

保存分两类：

### 用户直接编辑保存

用于正文手动修改、格式调整、手动新增/删除行。

请求应包含：

```json
{
  "base_version": 1,
  "document": {},
  "change_summary": "",
  "client_generated_ids": []
}
```

后端返回新的 `scripts` artifact version，并返回 id 映射：

```json
{
  "artifact": {},
  "id_map": {
    "client_tmp_line_01": "line_123"
  },
  "events": []
}
```

### 接受 AI suggestion 保存

用于用户确认 AI 建议。

请求应包含：

```json
{
  "suggestion_id": "suggestion_xxx",
  "base_version": 1,
  "action": "accept"
}
```

规则：

- 前端不本地假定保存成功，必须以后端返回的新 artifact version 为准。
- 保存成功后旧版本标记为 `superseded`，新版本状态按上下文为 `draft` 或 `confirmed`。
- 如果后端返回 `CONFLICT`，前端展示冲突卡，不覆盖当前编辑器内容。

## Toolbar

第一版工具：

| 工具 | 规则 |
|---|---|
| undo / redo | 只影响本地编辑历史，不触发 Agent |
| block type | 切换 action / dialogue / transition / scene_note |
| bold / italic / underline / strike | text mark |
| comment | 对当前选区创建 comment thread |
| find | 搜索正文 |
| save | 保存当前正文版本 |
| save status | `saved` / `dirty` / `saving` / `conflict` |

禁止项：

- 不放“生成剧本”。
- 不放“AI 改写”。
- 不放“续写”。
- 不放“版本对比”主按钮；版本对比属于后续版本管理入口。
- 工具栏按钮文字或图标必须居中，不允许互相覆盖、挤压或换行。
- 工具栏只覆盖正文编辑器宽度，必须与正文纸面左边界对齐；分集目录、侧栏目录和过程产物目录都不是工具栏的一部分。

## 与 Agent 的关系

右侧 Agent 读取 `SelectionContext`，不是直接读取 DOM。

局部修改流程：

```text
用户选中文本
-> ScriptEditor 生成 SelectionContext
-> 用户在 Agent 输入修改要求
-> POST /api/projects/{project_id}/messages 携带 selection_context
-> Agent 返回 suggestion patch
-> ScriptEditor 展示 suggestion
-> 用户接受或拒绝
-> 后端保存新 artifact version
```

Agent 不直接调用编辑器命令；Agent 只返回结构化 suggestion，由前端和后端按版本规则落地。

## 导入导出

第一版导入：

- 从 `scripts` artifact payload 导入。
- 从 `script_unit` 聚合导入。
- 从 mock fixture 导入。

第一版导出：

- Lexical JSON：用于保存编辑状态。
- Plain text：用于模型上下文和调试。
- HTML：用于预览。

后续导出：

- DOCX。
- PDF。
- 平台投放格式。

## 第一版验收

必须通过：

- 20 集长剧本 fixture 能加载、滚动、编辑。
- 任意单行局部选区能生成精确 offset 的 `SelectionContext`。
- 同集跨行、跨场景和反向拖选能生成顺序稳定的 `SelectionContext`，并保留完整 `line_ids`。
- 选区发给 Agent 后，用户继续编辑不会改变已发送 snapshot。
- suggestion 能展示、接受、拒绝。
- 选区高亮在正文内贴合文本，不遮挡文字，不显示成按钮。
- 接受 suggestion 后生成新 artifact version。
- 批注能挂到选区，正文改变后能识别 stale。
- 保存冲突能被拦截，不覆盖用户正文。
- `npm run gate:editor` 保持 Lexical 通过。

暂不要求：

- 多人实时协作。
- 完整 Word 格式兼容。
- 复杂跨段 diff 合并。
- 移动端完整编辑体验。

## 实现注意

- 第一版可以先以 `ScriptLineNode` 为核心，不急着做完整页面分页。
- 大文档必须考虑虚拟滚动或分集懒加载，否则长剧本会卡。
- Lexical JSON 不能作为唯一业务 schema；业务保存仍以 `scripts` artifact 为准。
- 编辑器内部 state 和 API JSON 都要保留 `snake_case` 边界映射规则。
- 所有临时 UI 状态不能写入 artifact，除非它是 comment / suggestion / version 的业务状态。
