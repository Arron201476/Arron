# Agent 上下文与前端交互最终审计

日期：2026-08-11

## 审计范围

- Agent Shell 当前作品、Run、Skill、Artifact、Artifact Version、Scope 定位。
- 只读过程产物和单集剧本的文本选区引用。
- 选区发送、服务端校验、消息持久化、历史恢复。
- 小说、非小说、视频三条链路的目录与产物展示。
- 单集剧本目录折叠、剧本编辑、粘性保存栏、历史版本入口。
- 作品、候选稿、导出、生成记录、Skill 菜单的关闭行为。
- 1920x1080 和 1280x800 布局。

## 已修复问题

1. 前端原先未提交 Selection Snapshot，只发送空占位。
2. Runtime 原先未验证选区内 Artifact ID、Artifact Type、Scope 和当前版本一致性。
3. 消息列表原先不恢复 Message Context，刷新后引用丢失。
4. Main Agent 控制 Prompt 原先没有声明选区优先级和指代规则。
5. 多个锚定菜单缺少 Esc 或点击外部关闭。
6. 剧本只读正文缺少稳定的字段、集、场景、台词行定位属性。

## 自动化结果

- 前端 Vitest：6 个文件，29 项通过。
- 前端生产构建：通过。
- 后端 Go：`go test -vet=off -p=1 ./...` 全部包通过。
- 三链路浏览器回归：小说 12 集、非小说完成态、视频目录与合集、剧本编辑 7 个控件、滚动后保存按钮可达，全部通过。
- 定位交互浏览器回归：选区提交、当前 Artifact 上下文提交、64 位快照哈希、历史引用显示、菜单关闭、1280 无横向溢出，全部通过。

## 证据

- `01-selection-reference.png`：正文选区与 Composer 引用卡。
- `02-history-reference.png`：发送后用户消息保留引用信息。
- `03-context-1280.png`：1280x800 工作台布局。
- `report.json`：专项行为断言结果。
- `../2026-08-11-full-frontend/after/report.json`：三条链路行为断言结果。

## 边界

本轮完成的是 Agent Shell 通用定位与上下文基础，以及前端交互一致性。选区被准确传给 Main Agent 并持久化；是否执行具体产物改写，仍由后续 Agent Action/审批合同决定，不在 Skill 内硬编码。
