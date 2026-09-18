# Frontend UI/UE Implementation Handoff

## 当前要落地的方向

Novel2Script 前端不是普通文档站，也不是静态 demo。它是一个 AI 编剧工作台：左侧给用户理解项目进度，中间让用户检查和编辑内容，右侧让 Agent 承接所有生成、修改、确认和恢复。

## 实现优先级

1. 先修共享组件和文案来源：Button、IconButton、StatusBadge、NavItem、AgentMessage、ArtifactCard。
2. 再修三栏布局：左侧链路目录、中间 artifact/script/events、右侧 Agent。
3. 再修 artifact 阅读态：所有字段展示完整，字段名中文优先，英文 key 辅助。
4. 再修剧本编辑器：分集目录、工具栏、正文编辑、选区、保存。
5. 最后做视觉 polish：滚动条、空状态、错误态、长文本、响应式和 focus-visible。

## 开发时禁止

- 禁止用固定假数据冒充当前 artifact。
- 禁止在未确认上游 artifact 时显示下游生成中。
- 禁止把意图识别、prompt 拼装、上下文准备作为普通用户 tab。
- 禁止按钮只做左右居中但垂直不居中。
- 禁止图标按钮没有 `aria-label`。
- 禁止新增一个状态页却不覆盖小说和非小说两条主链路。

## 当前代码修正口径

- `frontend/src/lib/workbenchLabels.ts` 是前端状态、产物、事件文案的代码侧来源。
- `frontend/src/components/artifacts/ArtifactWorkspace.tsx` 负责中间产物阅读态、JSON 编辑和输入材料预览。
- `frontend/src/components/artifacts/ScriptWorkbench.tsx` 负责最终剧本编辑。
- `frontend/src/components/agent/AgentPanel.tsx` 负责聊天流、附件、配置卡、确认卡。
- `frontend/src/styles.css` 负责三栏布局、按钮居中、滚动条、焦点、响应式。

## 本轮视觉交互重构必须落地到代码

这轮不再只维护文档或 Figma frame，必须在真实前端里能肉眼看到变化。

已确定的落地方向：

1. 左侧工作区不再是纯文本目录，条目必须具备图标、状态 badge、hover、active 和可访问焦点。
2. 中间过程产物默认不是 JSON 调试页，必须是结构化阅读卡：中文字段名优先、英文 key 只作为辅助标记、空数组/空对象可读。
3. 过程产物的字段展示要按产品阅读顺序排列，未知字段也必须展示，不允许静默丢弃。
4. 剧本页保留分集目录 + 正文编辑器，工具栏只属于正文编辑器，不和目录混为一组。
5. 右侧 Agent 消息、配置卡、确认卡、建议修改卡必须嵌在同一聊天流，并且按钮、图标、输入框垂直水平居中。
6. 输入材料是只读预览，不是可随意编辑的源文本编辑器；修改输入材料通过 Agent 或重新发送材料。
7. 所有 icon-only 按钮必须有 `aria-label`，所有按钮必须使用统一 Button 样式。
8. CSS 改动必须服务于真实组件，不再新增只用于静态截图的样式。

## 验收口径

开发者每次改 UI/UE 后必须至少跑：

```bash
npm run build
python tools/rebuild_design_preview.py
```

如果改了真实交互，还需要跑浏览器验证：上传附件、发送生成意图、确认配置卡、查看 artifact、切换剧本分集、选中文本并发起局部修改。
