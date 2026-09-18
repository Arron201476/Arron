# ScriptEditor Selection Gate

日期：2026-06-26

## 结论

本轮 gate 建议：正式 `ScriptEditor` 底座选择 **Lexical**。

Tiptap 不删除出局，但当前 spike 不能作为正式剧本编辑器底座直接进入开发。原因不是 Tiptap 不能做，而是当前实现只用普通 HTML / paragraph 承载剧本行，结构锚点没有被编辑器 schema 稳定保留。

如果未来继续考虑 Tiptap，必须先补自定义 node/extension，把 episode / scene / line / block type 作为一等结构处理，再重新跑同一套 gate。

## 验证范围

同一份 fixture：

- 2 集。
- 4 场。
- 12 条剧本行。
- 包含动作、对白、转场、marks、批注、sample patch。

自动化脚本：

```powershell
cd frontend
npm run gate:editor
```

输出文件：

- `design/audits/script-editor-selection-gate/results.json`
- `design/audits/script-editor-selection-gate/tiptap-after-patch.png`
- `design/audits/script-editor-selection-gate/lexical-after-patch.png`

## Gate 项目

本轮检查了：

- 编辑器是否正常加载。
- fixture 的 12 条剧本行是否都有稳定 `line_id` 锚点。
- 目标行 `ln_001_001_002` 是否存在。
- 工具栏是否可操作。
- 侧边 JSON / 状态面板是否输出。
- 拖选目标行后是否能报告 selection ref。
- patch 按钮是否可点击。
- patch 后是否保留目标行锚点。
- patch 后是否把建议文本写入目标行。
- patch 是否有可回看的建议状态。
- save 按钮是否可点击。
- save 后是否保留行锚点。
- after-patch 截图是否保存。

## Tiptap 结果

总结果：未通过。

通过项：

- 页面和编辑器 shell 能加载。
- 工具栏可渲染，8 个按钮存在。
- JSON / 状态面板有输出。
- patch 按钮可点击。
- save 按钮可点击。
- after-patch 截图已保存。

失败项：

- 预期 12 条 line anchors，实际找到 0 条。
- 目标行 `ln_001_001_002` 在 patch 前不存在可读结构锚点。
- 拖选后侧边面板没有暴露目标行 selection ref。
- patch 后没有可定位的目标行。
- patch 后无法确认建议文本写入了目标行。
- patch 没有保留可机器识别的建议标记。
- save 后仍没有 line anchors。

判断：

Tiptap 当前实现更像“可编辑富文本页”，不是“可追踪剧本结构编辑器”。它适合快速做 Word-like 体验，但如果不做自定义 schema，Agent 的局部修改、选区引用、批注挂载、版本对比都会不稳定。

## Lexical 结果

总结果：通过。

通过项：

- 页面和编辑器 shell 能加载。
- 12 条 line anchors 全部保留。
- 目标行 `ln_001_001_002` 在 patch 前存在。
- 工具栏可渲染，7 个按钮存在。
- JSON / 状态面板有输出。
- 拖选后侧边面板能暴露目标行 selection ref。
- patch 按钮可点击。
- patch 后目标行锚点仍存在。
- patch 后建议文本写入目标行。
- patch 后有可见“建议”状态。
- save 按钮可点击。
- save 后 12 条 line anchors 仍保留。
- after-patch 截图已保存。

判断：

Lexical 当前实现更接近正式需求：剧本行可以作为自定义节点保存 episode / scene / line / block type，Agent 可以从选区追溯 artifact 来源，并把 patch 落到目标节点。

## 仍未完成的验证

本轮 gate 只能说明 Lexical 更适合作为底座，还不能说明它已经产品可用。下一轮需要补：

- 长剧本性能：20 集以上、数百场、数千行。
- 真实 suggestion model：不要直接替换文本，要支持建议、接受、拒绝、撤回。
- 批注 thread：批注需要挂到 range，并能随文本编辑保持或失效。
- 版本对比：原文、AI 建议、用户修改之间要能清楚比较。
- 导出：至少确认 HTML / JSON / DOCX 路线。
- 输入法：中文输入、换行、撤销、粘贴 Word 文本。
- 可访问性：键盘、focus、toolbar label、屏幕阅读器语义。

## 工程决策

后续正式前端实现建议：

- `ScriptEditor` 采用 Lexical。
- 剧本正文用自定义节点建模：episode heading、scene heading、script line、dialogue line、action line、transition line。
- 过程产物不要复用 `ScriptEditor`，用字段型 `ArtifactFieldEditor`。
- Tiptap spike 保留为对照，不再作为主路线推进。
