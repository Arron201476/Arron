# Frontend Mock Shell

本文档记录当前最小前端壳。它用于产品检查，不是最终前端架构定稿。

## 当前目标

先让用户能用页面检查 Agent 流程：

```text
左侧：项目目录和 artifact 状态
中间：剧本 / 过程产物 / 运行事件展示
右侧：Agent 聊天主入口
-> 用户从右侧 Agent 输入小说 / 灵感 / 素材
-> Agent 启动 run
-> 分集卡后等待用户确认
-> 用户确认后继续生成 mock 剧本
```

## 当前目录

```text
frontend/
  index.html
  package.json
  src/app.ts
  src/styles.css
  scripts/build.mjs
  scripts/serve.mjs
  dist/app.js
```

## 当前能力

```text
右侧 Agent 输入 source text
选择 source_mode: auto / novel / non_novel
POST /api/runs
左侧目录显示 artifact 状态
中间展示选中的 artifact
右侧展示 run events timeline
展示 approval card
POST /api/runs/{run_id}/continue
展示 script_unit / scripts
```

## 本机验证状态

已验证：

```text
Backend: mock
右侧发送给 Agent
source_mode: auto -> non_novel
左侧高亮分集卡
Waiting approval
确认继续
run completed
事件：16 条
剧本页显示 scene heading + script blocks
左侧没有输入框
中间默认展示中文可读卡片，不把 JSON 当主界面
右侧默认显示 Agent 聊天和 Codex 风格执行 step 列表，不刷技术事件
技术事件放到“运行记录 / 事件”里
确认后剧本页显示中文 mock 剧本
闲聊输入只回复，不创建 run
明确生成 / 改编 / 剧本任务才进入 workflow
右侧执行体验参考 Codex：先显示“正在思考”，再显示 step pill：`理解请求 — done`、`识别意图 — done`、`规划分集卡 — done`，当前动作显示文字 loading。
如果后端连接失败，右侧会停止 loading 并给出失败说明，避免旧流程状态卡住。
非小说链说明为：素材库 -> 故事种子 -> 剧集蓝图 -> 分集卡 -> 剧本
小说链说明为：故事圣经 -> 原文拆集 -> 分集卡 -> 剧本
左侧状态只显示：待生成 / 生成中 / 已生成 / 待确认
系统内部 confirmed 不直接翻译成“已确认”
```

## 启动命令

先启动 Go mock 后端：

```powershell
cd C:\Users\egois\Desktop\novel2script_agent_project\backend
$env:GOCACHE = (Resolve-Path ..).Path + '\.tools\go-cache'
$env:GOTMPDIR = (Resolve-Path ..).Path + '\.tools\go-tmp'
..\.tools\go\bin\go.exe run ./cmd/mockserver
```

再启动前端：

```powershell
cd C:\Users\egois\Desktop\novel2script_agent_project\frontend
npm run build
npm run serve
```

打开：

```text
http://127.0.0.1:8832/
```

## 当前边界

- 这是无依赖的最小 TypeScript 壳，先不引入 React / Vite。
- `scripts/build.mjs` 当前只是把 `src/app.ts` 输出到 `dist/app.js`，用于无依赖浏览器验证。
- mock 剧本内容来自 Go mock runtime，不代表真实模型质量。
- 还没有持久化项目列表、登录、多 run 切换、选中文本局部修改、SSE 实时流。

## 下一步

```text
把当前壳升级为正式前端工程
-> 接入真实 run/project state API
-> 增加 selected_text / artifact_ref / episode / scene 上下文
-> 让局部修改从右侧 Agent 入口发起
-> 接 Eino runtime
```
