# ScriptEditor Spike 阶段结果

本文档记录 Tiptap / Lexical 两个最小 demo 的当前结果。它不是最终选型结论，只说明第一轮工程可行性和已暴露风险。

## 当前状态

已完成：

- 安装 Tiptap / Lexical 相关依赖。
- 修正 `frontend/package.json` 中不可解析的 `@types/react` / `@types/react-dom` 版本范围。
- 新增 Tiptap 最小 spike 页面。
- 新增 Lexical 最小 spike 页面。
- 两个页面共用 `design/fixtures/script-editor-spike-fixture.json`。
- 前端 `npm run build` 通过。

访问方式：

```text
默认端口：
http://127.0.0.1:8832/?spike=tiptap
http://127.0.0.1:8832/?spike=lexical

如果 8832 被占用，Vite 会自动切到下一个端口。本次启动端口：
http://127.0.0.1:8833/?spike=tiptap
http://127.0.0.1:8833/?spike=lexical
```

## 已验证能力

### Tiptap demo

已覆盖：

- 从统一 fixture 渲染 episode / scene / line。
- 基础格式按钮：bold / italic / underline / strike。
- 选区后生成 selection ref。
- 批注计数模拟。
- sample patch 预览，不直接写成正式保存状态。
- `editor.getJSON()` 可输出 JSON 摘要。

当前实现方式：

- 用 HTML + `data-episode-id` / `data-scene-id` / `data-line-id` 保留定位。
- 选区定位通过 DOM closest 找到 line id。

风险：

- 目前尚未做真正自定义 node。
- 选区 offset 仍是近似计算，需要验证跨 mark、跨节点、跨行选择。
- patch 现在是字符串替换 demo，正式实现必须改成结构化 transaction / suggestion。

### Lexical demo

已覆盖：

- 从统一 fixture 渲染 episode / scene / line。
- 自定义 `ScriptLineNode`，保存 episode / scene / line / block type。
- 基础格式按钮：bold / italic / underline / strikethrough。
- 选区后生成 selection ref。
- 批注计数模拟。
- sample patch 预览。
- `editorState.toJSON()` 可输出 JSON 摘要。

当前实现方式：

- 用自定义 Lexical node 保存剧本行结构。
- 通过 Lexical selection 向上查找 `ScriptLineNode` 得到 line id。

风险：

- 自定义 node 代码量明显高于 Tiptap。
- patch 仍是 demo，需要做 suggestion 层，不能直接替换文本。
- build 后 bundle 明显变大，需要后续 code-splitting。

## 构建结果

命令：

```powershell
cd frontend
npm run build
```

结果：

- TypeScript 通过。
- Vite build 通过。
- 有 warning：Lexical 包内 PURE annotation 被 Rolldown 忽略，不阻断构建。
- 有 warning：chunk 超过 500 kB，需要后续动态加载 spike / editor chunk。

## 初步判断

当前不能直接定稿。

第一轮倾向：

- Tiptap 上手更快，快速做 Word 类编辑和 demo 更直接。
- Lexical 结构控制更底层，自定义 node 更明确，但实现成本更高。

下一轮必须验证：

- 跨 mark / 跨 block 的真实选区定位。
- 批注 comment thread 是否能稳定挂到 range。
- patch suggestion 是否能不覆盖原文。
- 长剧本性能。
- 从编辑器 JSON 恢复后，line id 和 marks 是否稳定。

## 下一步

1. 用浏览器人工验证两个页面的基础交互。
2. 扩展 fixture 到 20 集长剧本，测试输入、滚动、选区。
3. 做真实 patch suggestion，而不是字符串替换。
4. 做批注 thread 的最小实现。
5. 根据证据写最终选型结论。
