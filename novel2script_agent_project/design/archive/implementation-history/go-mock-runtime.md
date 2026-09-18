# Go Mock Agent Runtime

本文档记录当前 mock 阶段的工程实现。它是接入 Eino 前的占位运行时，用来先验证产品闭环，不调用真实模型。

## 目标

先跑通：

```text
用户输入
-> 创建 run
-> 产生 run events
-> 生成中间 artifacts
-> 触发 approval_request
-> 用户继续
-> 继续生成 script_context / script_unit / scripts
-> run completed
```

## 当前目录

```text
backend/
  go.mod
  cmd/mockserver/main.go
  internal/agent/types.go
  internal/agent/mock/runtime.go
```

## 当前能力

```text
StartRun
-> 支持 source_mode: auto / novel / non_novel
-> auto 会落成识别后的 novel 或 non_novel
-> 生成 source_input
-> 小说链生成 story_bible / episode_split / episode_cards
-> 非小说链生成 material_bank / story_seed / series_blueprint / episode_cards
-> 在 episode_cards 后暂停等待 approval

ContinueRun
-> 解决 approval
-> 生成 script_context
-> 分批产生 script_batch_inserted events
-> 生成 script_unit
-> 生成 scripts 聚合产物
-> run completed
```

## HTTP API

```text
GET  /healthz
POST /api/runs
GET  /api/runs/{run_id}
POST /api/runs/{run_id}/continue
POST /api/runs/{run_id}/steps/{step_id}/rerun
GET  /api/runs/{run_id}/events
GET  /api/runs/{run_id}/events/stream
GET  /api/runs/{run_id}/artifacts
```

## 本机验证状态

当前项目内已安装便携版 Go，不改系统环境：

```text
.tools/go/bin/go.exe
go version go1.26.4 windows/amd64
```

已验证：

```text
go test ./...
healthz 通过
POST /api/runs 通过
source_mode: auto 可识别为 non_novel
run 会停在 waiting_approval
POST /api/runs/{run_id}/continue 通过
run completed
artifacts: 8
events: 16
```

运行命令：

```powershell
cd C:\Users\egois\Desktop\novel2script_agent_project\backend
$env:GOCACHE = (Resolve-Path ..).Path + '\.tools\go-cache'
$env:GOTMPDIR = (Resolve-Path ..).Path + '\.tools\go-tmp'
..\.tools\go\bin\go.exe run ./cmd/mockserver
```

## 后续替换为 Eino

mock runtime 的接口按未来 Eino runtime 形态设计。后续替换原则：

```text
保留 Go API
保留 run_state / artifact / run_event 结构
保留 approval pause / continue 机制
只替换 internal/agent/mock 为 internal/agent/eino
```
