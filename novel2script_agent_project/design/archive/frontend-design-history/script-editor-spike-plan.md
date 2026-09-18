# ScriptEditor Spike 任务合同

本文档定义最终剧本正文编辑器的技术选型 spike。目标是在进入正式前端开发前，用同一份剧本 fixture 对比 Tiptap 和 Lexical，避免凭印象锁死技术路线。

## 背景

Novel2Script 的过程产物可以用结构化字段编辑，但最终剧本必须是正文编辑器。用户需要像 Word 一样进行基础编辑，同时系统还要保留剧本结构和 Agent 局部修改所需的选区定位。

已确定：

- CodeMirror 只用于原文、素材、Markdown 或纯文本输入。
- 最终剧本不使用普通 textarea。
- 最终剧本不使用过程产物字段卡片替代。
- UI 组件层可以使用 shadcn/ui 风格 + Radix + lucide-react。

未确定：

- ScriptEditor 选 Tiptap 还是 Lexical。

## Spike 范围

用同一份 fixture：`fixtures/script-editor-spike-fixture.json`。

分别实现两个最小 demo：

```text
frontend/src/spikes/script-editor-tiptap/
frontend/src/spikes/script-editor-lexical/
```

每个 demo 只验证编辑器能力，不接真实后端，不接模型，不重构正式工作台。

## 必测能力

### 1. 剧本结构块

必须支持：

- episode
- scene
- action
- dialogue
- transition

每个可编辑块必须保留：

- artifact_id
- episode_id
- scene_id
- line_id
- block_type
- speaker，可选

### 2. Word 类基础编辑

必须支持：

- 加粗。
- 斜体。
- 下划线。
- 删除线。
- 引用 / 标记。
- 撤销 / 重做。
- 查找。
- 保存状态提示。

第一版不测：

- 页眉页脚。
- 多栏排版。
- 复杂表格。
- 艺术字。
- 完整 Word 导入导出。

### 3. 选区定位

用户选中一段文本后，编辑器必须能输出：

```json
{
  "artifact_id": "scripts_demo",
  "version": 1,
  "episode_id": "ep_001",
  "scene_id": "sc_001_001",
  "line_id": "ln_001_001_002",
  "offset_range": [0, 6],
  "selected_text": "三年冷宫",
  "before_context": "...",
  "after_context": "..."
}
```

如果一个选区跨多个 line，必须能拆成多个 range 或明确禁止跨块选择。

### 4. AI Patch 预览

模型返回候选改写后：

- 只显示 suggestion / diff。
- 不直接覆盖正文。
- 用户确认后才写入编辑器状态。
- 取消后恢复原文。

### 5. 批注

用户可以对选区挂 comment thread。

批注必须保留：

- comment_id
- target line_id
- offset_range
- author
- text
- created_at
- resolved

### 6. JSON 序列化

必须能完成：

1. 从 fixture JSON 初始化编辑器。
2. 编辑后导出 JSON。
3. 从导出的 JSON 恢复编辑器。
4. 对比编辑前后差异。

不能只保存 HTML 字符串。

### 7. 粘贴行为

测试从以下来源粘贴：

- 普通纯文本。
- Word / WPS 风格富文本。
- 网页复制内容。

要求：

- 不破坏剧本结构块。
- 不带入不可控样式。
- 基础 marks 可保留，复杂样式应清洗。

### 8. 性能

至少测试：

- 2 集小 fixture。
- 扩展到 20 集、每集 8 场、每场 12 行的长剧本。

关注：

- 首次渲染。
- 输入延迟。
- 滚动。
- 选区创建。
- patch 预览。

## 对比评分

| 项 | 权重 | 说明 |
|---|---:|---|
| 结构化 block 建模 | 20 | 是否自然支持 episode / scene / line |
| 选区定位 | 20 | 是否稳定拿到 line id 和 offset range |
| AI patch / suggestion | 15 | 是否容易做候选、不覆盖正文 |
| 批注 | 10 | comment thread 实现成本 |
| JSON 序列化 | 10 | 保存、恢复、对比是否稳定 |
| 基础编辑能力 | 10 | Word 类基础功能完成度 |
| 性能 | 10 | 长剧本输入、滚动、选区 |
| 工程复杂度 | 5 | 代码量、类型复杂度、调试成本 |

总分不是唯一标准。若某候选在选区定位或 JSON 序列化上不稳定，即使总分高，也不能进入正式实现。

## 输出物

Spike 完成后必须产出：

```text
design/script-editor-spike-result.md
frontend/src/spikes/script-editor-tiptap/
frontend/src/spikes/script-editor-lexical/
```

`script-editor-spike-result.md` 必须包含：

- 选择结果。
- 关键证据。
- 放弃另一个方案的原因。
- 仍需补的风险。
- 正式 ScriptEditor 的组件拆分建议。

## 不做事项

Spike 阶段不做：

- 接真实模型。
- 接真实 Go 后端。
- 完整 UI 美化。
- 完整工作台重构。
- 大规模依赖重构。
- 导出 DOCX。

## 通过标准

只有满足以下条件，才能进入正式 ScriptEditor 实现：

- 同一份 fixture 在候选编辑器里可以完整导入、编辑、导出。
- 选区定位可以稳定生成 Agent 需要的引用对象。
- AI patch 不直接覆盖正文。
- 长剧本性能没有明显卡顿。
- 选择理由写入 `script-editor-spike-result.md`。
