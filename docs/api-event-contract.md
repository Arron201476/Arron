# API and Event Contract

状态：Stage 2 设计基线  
版本：v0.1  
适用范围：内容生产 Agent 第一版网页端  
上游文档：

- `stage-1-product-definition.md`
- `stage-2-architecture-baseline.md`
- `capability-registry-contract.md`
- `runtime-domain-model.md`
- `artifact-dependency-contract.md`
- `capability-context-pack-contract.md`
- `asset-and-retention-contract.md`

## 1. 文档目标

本合同定义网页端、General Content Agent Shell、Capability Runner、Runtime 和 Asset Service 之间的公开通信协议。

它负责回答：

1. Query 和 Command 如何区分；
2. 用户消息、Skill 选择和正式 Run 如何进入系统；
3. 写请求如何防止重复执行和并发覆盖；
4. 长任务为什么使用 SSE，而不是让 HTTP 请求一直等待；
5. 前端刷新、断网、SSE 丢事件和服务重启后如何恢复；
6. Event 如何排序、去重、重放和关联 Project/Run/Task；
7. Artifact、Approval、Asset、Asset Set 和 Final Selection 使用哪些接口；
8. 三个 Skills 如何复用同一套 API；
9. 零 Domain Capability 时通用 Agent 如何继续对话和管理作品；
10. 旧 Novel2Script API 如何迁移而不破坏现有两条链路。

本文不定义：

- 页面布局；
- Prompt 内容；
- Provider 私有请求格式；
- SDK Agent 与 Worker 内部节点接口；
- 数据库完整 DDL；
- 用户登录和权限体系。

## 2. 核心结论

### 2.1 第一版协议栈

```text
REST JSON
-> Query、Command、快照、编辑和审批

SSE
-> Project/Run 的单向实时事件

SQLite Current State + Append-only Event
-> 服务端事实真源
```

第一版不引入：

- WebSocket；
- GraphQL；
- gRPC-Web；
- 纯 Event Sourcing；
- 前端本地状态作为业务真源；
- 模型直接调用业务数据库。

### 2.2 为什么使用 REST + SSE

该组合符合当前长任务 Agent 产品常见架构：

- 浏览器到服务端以命令和资源查询为主；
- 服务端向浏览器主要是单向进度更新；
- SSE 原生支持事件 ID、断线重连和 HTTP 基础设施；
- 业务状态仍可通过普通 GET 快照恢复；
- 第一版不需要 WebSocket 的双向长连接复杂度。

当未来出现实时多人协作、持续音视频流或高频双向控制时，再评估 WebSocket。

### 2.3 快照是事实，Event 是变化通知

```text
Current State Snapshot
= 当前事实

Event
= 某次状态变化的审计和实时通知
```

前端不能只靠 Event 重建全部当前状态。

Event 可以：

- 更新局部进度；
- 提示哪个资源发生变化；
- 触发精确 Query；
- 展示时间线；
- 支持审计和调试。

Event 不能：

- 替代 Artifact Payload；
- 替代当前 Approval；
- 替代当前 Run 状态；
- 被前端修改；
- 作为唯一恢复来源。

### 2.4 Project 级 SSE 是工作台主事件流

现有系统只有 Run 级 SSE，但上传、Asset 到期、消息、候选稿和最终稿选择可能不属于某个 Run。

目标协议使用：

```text
Project Event Stream
-> 工作台主流

Run Event Stream
-> 可选的 Run 详情过滤流和旧接口兼容
```

Project Event 使用单调递增 `project_event_seq`；Run Event 同时拥有可选 `run_event_seq`。

### 2.5 所有写操作都是 Command

Command：

- 改变服务端状态；
- 可能创建 Event；
- 需要幂等和并发保护；
- 返回 Command Result 或异步资源引用。

Query：

- 不改变业务状态；
- 可以安全重试；
- 返回当前快照或列表。

普通 GET 不能触发：

- 模型调用；
- 文件解析；
- Run 启动；
- 自动修复；
- Approval Resolution；
- Artifact 新版本。

### 2.6 一个 Run 一个 Capability

启动 Run 必须明确：

```text
capability_id
capability_version
initial_input_snapshot
entry_stage_config_snapshot
```

收集型或多阶段 Capability 可以在同一 Run 内追加不可变 Snapshot Version。每个 Step Run 必须在自己的 `input_version_snapshot` 中记录精确 Input、Config、Artifact 和 Selection Snapshot ID；API 不提供读取可变“当前快照”来替换历史输入的捷径。

API 不再使用 `source_mode` 代替 Capability ID。

视频参考创作内部复用非小说后续流程时，仍保持同一个：

```text
capability_id = video_reference_creation
```

### 2.7 上传和启动 Skill 分离

```text
上传 Asset
!= 启动 Run

发送普通消息
!= 自动启动 Run

用户确认生成配置
+ 输入满足 Capability Schema
+ Runtime Guard 通过
= 才能启动 Run
```

### 2.8 不声称 Exactly Once

网络系统无法保证浏览器请求绝对只到达一次。

本合同采用：

```text
At-least-once request delivery
+ Idempotent command processing
+ Optimistic concurrency
+ Append-only events
```

对用户表现为重复点击或网络重试不会创建重复 Run、重复版本或重复审批结果。

## 3. API 版本与基础约定

### 3.1 Base Path

目标公开 API：

```text
/api/v1
```

健康检查：

```text
/healthz
```

旧 `/api/...` 路由只作为迁移兼容层，不继续增加视频等新能力。

### 3.2 Content Type

JSON：

```text
Content-Type: application/json; charset=utf-8
Accept: application/json
```

SSE：

```text
Accept: text/event-stream
Content-Type: text/event-stream; charset=utf-8
```

文件上传使用流式 multipart 或 Upload Item 二进制接口，不能使用 Base64 JSON。

### 3.3 字段命名

统一：

```text
JSON field -> snake_case
ID -> opaque ASCII string
enum -> lower_snake_case
time -> RFC3339Nano UTC
```

前端 TypeScript API 类型保留 `snake_case`，不在 Client Layer 静默改名。

### 3.4 ID

示例前缀：

```text
prj_  project
con_  conversation
msg_  message
ast_  asset
aset_ asset_set
asv_  asset_set_version
run_  run
stp_  step_run
tsk_  task_item
atm_  attempt
art_  artifact
av_   artifact_version
apr_  approval_request
cmd_  command
evt_  event
```

客户端只能把 ID 当作不透明字符串，不能解析前缀做业务判断。

### 3.5 Null、空值和缺失字段

- 未发生的时间使用 `null`；
- 未知数字使用 `null`；
- 空数组返回 `[]`，不返回 `null`；
- 空对象返回 `{}`；
- 可选字段没有业务值时可以省略；
- ID 不允许空字符串；
- 用户合法删除文本后，字段值可以是空字符串；
- 不用 `0` 同时表示未知和真实零值。

### 3.6 Request ID

每个 HTTP 请求携带：

```text
X-Request-ID: <uuid>
```

服务端：

- 无值时生成；
- 在响应头原样返回；
- 写入日志和 Trace；
- 不把 Request ID 当作 Idempotency Key。

### 3.7 Client Instance

网页启动时生成：

```text
X-Client-Instance-ID: <uuid>
```

用途：

- 区分同一共享工作区的浏览器标签页；
- 审计命令来源；
- 帮助排查并发覆盖。

它不是身份认证，不授予权限。

### 3.8 Trace Context

服务端支持标准：

```text
traceparent
tracestate
```

没有接入完整 Trace 系统时也应保留字段透传能力。

## 4. 响应 Envelope

### 4.1 单资源成功响应

```json
{
  "data": {
    "project": {}
  },
  "meta": {
    "request_id": "",
    "server_time": "",
    "event_cursor": null
  }
}
```

### 4.2 列表响应

```json
{
  "data": {
    "items": []
  },
  "meta": {
    "request_id": "",
    "server_time": "",
    "next_cursor": null,
    "has_more": false
  }
}
```

### 4.3 Command 响应

同步完成：

```json
{
  "data": {
    "command": {
      "command_id": "cmd_...",
      "command_type": "rename_project",
      "status": "completed"
    },
    "result": {
      "project_ref": {
        "project_id": "prj_...",
        "version": 4
      }
    }
  },
  "meta": {
    "request_id": "",
    "event_cursor": {
      "project_event_seq": 42
    }
  }
}
```

异步接受：

```json
{
  "data": {
    "command": {
      "command_id": "cmd_...",
      "command_type": "start_run",
      "status": "accepted"
    },
    "result": {
      "run_id": "run_..."
    }
  },
  "meta": {
    "request_id": "",
    "event_cursor": {
      "project_event_seq": 43
    }
  }
}
```

### 4.4 HTTP Status

| 场景 | Status |
|---|---:|
| Query 成功 | `200` |
| 同步 Command 成功 | `200` |
| 创建资源成功 | `201` |
| 异步 Command 已接受 | `202` |
| 删除成功且无 Body | `204` |
| 请求格式错误 | `400` |
| 未认证 | `401` |
| 无权限 | `403` |
| 不存在 | `404` |
| 状态/版本冲突 | `409` |
| 已删除或过期 | `410` |
| Body 太大 | `413` |
| MIME 不支持 | `415` |
| 语义校验失败 | `422` |
| 限流 | `429` |
| 服务暂不可用 | `503` |
| 上游超时 | `504` |

第一版产品没有登录，但部署网关仍可能返回 `401/403`。

## 5. Error Contract

### 5.1 Schema

```json
{
  "error": {
    "code": "ARTIFACT_VERSION_CONFLICT",
    "message": "当前内容已有新版本，请重新检查后保存。",
    "category": "conflict",
    "retryable": false,
    "recoverable": true,
    "user_action_required": true,
    "details": {
      "artifact_id": "art_...",
      "expected_version": 3,
      "current_version": 4
    },
    "current_state_refs": [
      {
        "resource_type": "artifact_version",
        "resource_id": "av_..."
      }
    ],
    "available_actions": [
      {
        "action_id": "reload_current_version",
        "label": "加载最新版本",
        "danger": false
      }
    ],
    "request_id": ""
  }
}
```

### 5.2 字段语义

`retryable`：

> 客户端是否可以使用同一个 Idempotency Key 和同一请求内容自动重试。

`recoverable`：

> 用户或系统是否存在可行恢复路径。

`user_action_required`：

> 是否必须等待用户明确操作，不能后台自动继续。

三者不能混用。

### 5.3 Error Category

```text
validation
not_found
conflict
gone
rate_limit
provider
storage
persistence
internal
capability_unavailable
user_action_required
```

### 5.4 安全错误

不得返回：

- SQLite 原始错误；
- 数据库路径；
- 服务端绝对路径；
- Provider API Key；
- Authorization Header；
- 完整 Prompt；
- 文件内容；
- Go Stack；
- Provider 未清洗的内部响应。

### 5.5 `WORKSPACE_BUSY`

SQLite 短暂繁忙：

```json
{
  "code": "WORKSPACE_BUSY",
  "retryable": false,
  "recoverable": true,
  "user_action_required": false,
  "available_actions": [
    {
      "action_id": "refresh_snapshot",
      "label": "刷新状态"
    }
  ]
}
```

对写请求不能断言“本次一定未执行”，客户端先刷新快照或查询 Command，再决定是否重试同一 Idempotency Key。

## 6. Query Contract

### 6.1 Query 原则

- GET 不改变业务状态；
- 支持安全有限重试；
- 列表使用 Cursor Pagination；
- 稳定排序；
- 不默认返回完整 Artifact Payload；
- 不默认返回全部历史 Event；
- 不因为前端当前不展示而省略关键状态字段。

### 6.2 Cursor Pagination

请求：

```text
?limit=50&cursor=<opaque>
```

规则：

- 默认 `limit=50`；
- 最大 `limit=100`；
- Cursor 不透明；
- Cursor 固定排序键；
- 数据变化导致 Cursor 无效时返回 `CURSOR_INVALID`；
- 禁止使用数组 offset 作为长期游标。

### 6.3 稳定排序

沿用 Runtime Contract：

| 资源 | 排序 |
|---|---|
| Project | `updated_at DESC, project_id ASC` |
| Message | `created_at ASC, message_id ASC` |
| Event | `project_event_seq ASC` |
| Artifact Version | `version ASC` |
| Episode/Task | `item_order ASC, task_item_id ASC` |
| Candidate | `created_at DESC, candidate_id ASC` |

### 6.4 ETag

资源 Query 可以返回：

```text
ETag: "<resource-version-or-snapshot-hash>"
```

前端可以使用：

```text
If-None-Match
```

第一版写并发仍以显式 `expected_version/base_version_id` 为权威，不强制使用 `If-Match`。

## 7. Command Contract

### 7.1 Command 结构

Runtime 内部记录：

```json
{
  "command_id": "cmd_...",
  "project_id": "prj_...",
  "command_type": "start_run",
  "status": "accepted",
  "actor": {
    "kind": "shared_workspace_user",
    "ref": "client_instance_id"
  },
  "idempotency_key": "",
  "request_hash": "",
  "request_id": "",
  "causation_event_id": null,
  "result_ref": null,
  "failure": null,
  "created_at": "",
  "started_at": null,
  "completed_at": null
}
```

### 7.2 Command Status

```text
accepted
running
completed
failed
cancelled
unknown
```

`unknown` 只用于客户端无法确认结果，不是 Runtime 持久化终态。

### 7.3 Command Endpoint

```text
GET /api/v1/commands/{command_id}
```

用于：

- POST 响应丢失后查询；
- 长 Command 查看状态；
- `WORKSPACE_BUSY` 后确认是否执行；
- 调试幂等结果。

### 7.4 Command 结果大小

Command 响应只返回：

- Command；
- 关键资源引用；
- 新版本号；
- 当前 Event Cursor；
- 下一步可用动作。

不返回：

- 全部历史 Event；
- 全部 Artifact Payload；
- 全部消息；
- 整个 Project 工作台状态。

需要完整状态时调用快照 Query。

## 8. Idempotency

### 8.1 Header

需要幂等的 Command 必须携带：

```text
Idempotency-Key: <uuid>
```

### 8.2 强制操作

至少包括：

- 创建 Project；
- 创建 Message；
- 启动 Run；
- Resolve Approval；
- Pause/Resume/Cancel；
- Retry Step/Task；
- 创建 Artifact Version；
- AI Revision；
- 创建/Seal Asset Set；
- Upload Complete；
- 确认删除 Asset/Project；
- 选择或改选 Final Script；
- 创建 Export。

### 8.3 幂等键范围

```text
workspace_id
+ project_id or global_scope
+ command_type
+ idempotency_key
```

### 8.4 Request Hash

服务端对规范化请求计算 Hash：

- HTTP Method；
- Canonical Route；
- Project ID；
- Canonical JSON Body；
- 关键资源版本；
- Actor/Client Instance。

不包含：

- Request ID；
- 重试次数；
- 时间戳；
- Header 顺序。

### 8.5 重复请求

同 Key + 同 Request Hash：

- Command 已完成：返回已保存响应快照；
- Command 运行中：返回相同 Command ID 和当前状态；
- Command 失败：返回相同失败结果；
- 不创建第二个副作用。

同 Key + 不同 Request Hash：

```text
409 IDEMPOTENCY_KEY_REUSED
```

### 8.6 客户端重试

写请求出现：

- 网络中断；
- 浏览器超时；
- `503`；
- 结果未知；

客户端只能使用原 Idempotency Key 重试原请求。

不能生成新 Key自动重发。

### 8.7 保留期

第一版：

- Command 幂等记录至少保留到终态后 7 天；
- 长 Run 期间不得过期；
- Approval/Final Selection 即使幂等记录过期，状态 Guard 仍阻止重复解析；
- Upload Item 幂等记录至少保留到 Session 终态后 24 小时。

## 9. Optimistic Concurrency

### 9.1 原则

幂等防止同一请求重复执行。

Expected Version 防止不同请求覆盖彼此。

两者必须同时存在。

### 9.2 Project

重命名：

```json
{
  "title": "新作品名",
  "expected_version": 3
}
```

### 9.3 Artifact

```json
{
  "base_version_id": "av_...",
  "base_version": 3,
  "change_mode": "scoped",
  "change_set": {}
}
```

### 9.4 Asset Set

```json
{
  "expected_asset_set_version": 4,
  "changes": []
}
```

### 9.5 Approval

```json
{
  "action": "approve",
  "expected_approval_version": 1,
  "subject_snapshot_hash": ""
}
```

### 9.6 冲突

返回：

```text
409 <RESOURCE>_VERSION_CONFLICT
```

并携带：

- Current Version；
- Current Resource Ref；
- 可用动作；
- 是否可以重新应用 Change Set。

服务端不能静默 Last-write-wins。

## 10. Capability Discovery

### 10.1 列表

```text
GET /api/v1/capabilities
```

返回：

```json
{
  "data": {
    "items": [
      {
        "capability_id": "novel_to_script",
        "version": "1.0.0",
        "label": "小说转剧本",
        "status": "available",
        "accepted_asset_kinds": ["text", "document"],
        "commands": ["start"],
        "ui_entry": {
          "menu_order": 1
        }
      }
    ]
  }
}
```

### 10.2 零 Capability

返回：

```json
{
  "data": {
    "items": []
  }
}
```

此时：

- Project API 仍可用；
- Message API 仍可用；
- Asset API 仍可用；
- 通用图片理解 Tool 可按部署能力使用；
- Agent 能说明缺少领域能力；
- 不伪造 Run。

### 10.3 详情

```text
GET /api/v1/capabilities/{capability_id}
```

公开：

- Label；
- Description；
- Accepted Asset Kinds；
- Required Config Fields；
- 当前可用状态；
- 不可用原因；
- Version；
- 前端 UI Manifest 引用。

不公开完整内部 Prompt 和 Rules。

## 11. Project API

### 11.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| GET | `/api/v1/projects` | Query | 作品列表 |
| POST | `/api/v1/projects` | Command | 新建作品 |
| GET | `/api/v1/projects/{project_id}` | Query | 作品摘要 |
| PATCH | `/api/v1/projects/{project_id}` | Command | 重命名 |
| GET | `/api/v1/projects/{project_id}/snapshot` | Query | 工作台恢复快照 |
| POST | `/api/v1/projects/{project_id}/delete-previews` | Command | 生成删除影响预览 |
| POST | `/api/v1/projects/{project_id}/delete-confirmations` | Command | 确认删除 |

### 11.2 创建

```json
{
  "title": "未命名作品"
}
```

不再要求 `source_mode`。

Project 在创建时不绑定永久 Skill。

### 11.3 删除

不直接使用无 Body 的：

```text
DELETE /projects/{id}
```

目标协议采用 Preview + Confirmation，确保：

- 活动 Run 已处理；
- 影响已展示；
- Preview Hash 未过期；
- 用户明确二次确认；
- Command 可幂等。

## 12. Conversation and Message API

### 12.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| GET | `/api/v1/projects/{project_id}/conversations` | Query | 对话列表 |
| GET | `/api/v1/conversations/{conversation_id}/messages` | Query | 消息列表 |
| POST | `/api/v1/conversations/{conversation_id}/messages` | Command | 发送用户消息 |
| POST | `/api/v1/proposed-actions/{proposed_action_id}/configuration` | Command | 提交配置卡并生成受保护的启动确认 |

第一版每个 Project 只有一个主要 Conversation，但 API 不把两者合成一个 ID。

### 12.2 Message Request

```json
{
  "content": "根据这些视频开始创作",
  "display_content": "根据这些视频开始创作",
  "capability_ref": {
    "capability_id": "video_reference_creation",
	"version": "1.0.0",
    "selection_mode": "explicit"
  },
  "attachment_refs": [
    {
	  "asset_id": "ast_...",
	  "asset_snapshot_id": "ass_..."
    }
  ],
  "selection_snapshot": null,
  "client_context": {
    "current_view": "asset_list",
    "current_artifact_version_id": null,
    "current_asset_set_version_id": "asv_..."
  }
}
```

### 12.3 Capability Ref

```text
explicit
-> 用户通过 Composer 的“能力”入口选择 Skill

inferred
-> Agent 意图识别

none
-> 普通对话或意图未知
```

用户显式选择优先，但不绕过输入和配置校验。

### 12.4 Message Response

```json
{
  "data": {
    "user_message": {},
    "agent_message": {},
    "agent_decision": {
	  "agent_decision_id": "agd_...",
	  "project_id": "prj_...",
	  "conversation_id": "con_...",
	  "user_message_id": "msg_user_...",
	  "agent_message_id": "msg_agent_...",
	  "decision": {
		"reply": "配置已就绪，请确认开始。",
		"intent": "propose_capability",
		"confidence": 0.98,
		"capability_ref": {
		  "capability_id": "video_reference_creation",
		  "version": "1.0.0",
		  "selection_mode": "explicit"
		},
		"target_ref": null,
		"clarification": null,
		"proposed_action": null
	  },
	  "created_at": "..."
    },
	"proposed_action": {
	  "proposed_action_id": "pac_...",
	  "confirmation_message_id": "msg_agent_...",
	  "action_type": "start_run",
	  "version": 1,
	  "status": "pending",
	  "capability_ref": {
		"capability_id": "video_reference_creation",
		"version": "1.0.0"
	  },
	  "input": {},
	  "config": {},
	  "snapshot_hash": "64_hex_chars",
	  "requires_confirmation": true
	},
    "run_ref": null
  }
}
```

`proposed_action_id`、`version`、`confirmation_message_id` 和 `snapshot_hash` 由 Runtime 生成。Agent 和前端均不得自行生成或覆盖；`start_run` 动作中的 `project_id` 与 `user_request_message_id` 也由 Runtime 注入。

Main Agent 初次选中 Skill 时返回 `collect_run_configuration`。前端提交配置卡后，Runtime 在同一 `proposed_action_id` 上执行受版本保护的状态转换：

```text
collect_run_configuration version 1
-> POST /proposed-actions/{id}/configuration
-> start_run version 2 + new snapshot_hash + requires_confirmation=true
```

配置提交必须携带 `expected_version`、结构化 `input` 和 `config`。Runtime 校验 Capability 版本、可执行入口、Asset/Snapshot 归属和输入基本结构，并注入权威的 `project_id`、`user_request_message_id`；旧版本重复提交返回 `PROPOSED_ACTION_STALE`。

### 12.5 Message 与 Run

自然语言入口和结构化 Run API 最终调用同一个 Runtime `StartRunCommand`。

第一版只允许一条用户可见启动路径：

```text
Message Command
-> AgentDecision
-> Guard
-> collect_run_configuration / 配置卡
-> 用户提交配置
-> Runtime 生成 start_run / 确认卡
-> 用户点击确认卡主操作
-> POST /api/v1/projects/{project_id}/runs
-> Runtime StartRunCommand
```

Message Command 不得在回复过程中隐式创建 Run。未来即使增加其他入口，也必须调用同一个结构化 Start Run API 和同一套 Guard，不能形成第二套启动语义。

### 12.6 活动写 Run

当前 Project 存在任一活动写 Run 时：

```text
pending
running
waiting_approval
pausing
paused
failed
```

- Message Command 仍可持久化并进入只读路由；
- `inspect_or_chat` 可立即回答，但不得修改 Run、Artifact 或 Approval；
- `revision_request` 可完成定位并排队，写入动作等待安全检查点；
- `start_or_switch_skill` 返回当前主链冲突和可用的 Pause/Stop 动作，不启动第二条主链；
- Start Run、Revision Job 和 Regeneration Plan 的事务写锁仍是最终权威；
- 前端 Composer 保持可输入，并展示只读回答、排队修改或主链冲突的明确状态；
- 其他 Project 的 Message 不受影响。

这允许用户在过程中继续向同一个 Agent 提问或提出修改，同时保持“同一作品只有一个写入者”的边界。第一版不热替换 Worker 输入、不做 Token 流抢占；完整合同见 `agent-targeting-and-revision-design.md`。

### 12.7 零 Capability 对话

无 Capability 时普通 Message：

- 可以对话；
- 可以解释通用材料；
- 可以询问用户意图；
- 可以返回能力缺口；
- `run_ref=null`；
- 不创建伪 Artifact。

## 13. Asset API

完整语义以 `asset-and-retention-contract.md` 为准。

### 13.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| GET | `/api/v1/meta/upload-policy` | Query | 当前上传限制 |
| POST | `/api/v1/projects/{project_id}/upload-sessions` | Command | 创建上传批次 |
| PUT | `/api/v1/upload-items/{upload_item_id}/content` | Command | 流式上传内容 |
| POST | `/api/v1/upload-items/{upload_item_id}/complete` | Command | 完成校验与提交 |
| GET | `/api/v1/upload-sessions/{upload_session_id}` | Query | 上传状态 |
| GET | `/api/v1/projects/{project_id}/assets` | Query | 材料列表 |
| GET | `/api/v1/assets/{asset_id}` | Query | Asset 详情 |
| PATCH | `/api/v1/assets/{asset_id}` | Command | 修改显示名 |
| POST | `/api/v1/assets/{asset_id}/parse-retries` | Command | 重试通用解析 |
| POST | `/api/v1/assets/{asset_id}/delete-previews` | Command | 删除影响预览 |
| POST | `/api/v1/assets/{asset_id}/delete-confirmations` | Command | 确认删除 |
| GET | `/api/v1/assets/{asset_id}/content` | Query | 读取可用源文件 |

### 13.2 上传内容

`PUT content`：

- 不使用 JSON；
- 按 Asset Contract 流式写入；
- 受 Route 级大小限制；
- 请求中断可清理 staging；
- 不返回 Base64；
- 不自动创建 Run。

### 13.3 过期/删除

读取返回：

```text
410 ASSET_SOURCE_EXPIRED
410 ASSET_SOURCE_DELETED
```

响应仍可以包含已有正式产物引用。

### 13.4 阶段 3 第 10 步实现补充

- `GET /api/v1/assets/{asset_id}/content` 已实现受控流式读取，响应不暴露 `storage_ref`；
- `POST /api/v1/assets/{asset_id}/delete-previews` 返回活动 Run、正式 Artifact 影响、保留说明和 `snapshot_hash`；
- `POST /api/v1/assets/{asset_id}/delete-confirmations` 必须提交 `preview_hash` 与 `confirmed=true`，成功返回 `202` 和异步 Retention Job；
- Preview 后 Asset 状态、引用或 Run 状态变化时返回 `409 DELETE_PREVIEW_EXPIRED`；
- 阻塞状态的 Run 仍引用来源时返回 `409 ASSET_DELETE_BLOCKED_BY_RUN`；
- 已过期和已删除源文件读取分别返回 `410 ASSET_SOURCE_EXPIRED`、`410 ASSET_SOURCE_DELETED`；
- 删除确认只撤销来源读取并安排 Blob 删除，不删除已有 Artifact、Candidate、Final Selection 或 Dependency；
- 视频上传完成时已经原子创建 168 小时到期 Job，不依赖前端补发调度请求。

## 14. Asset Set API

### 14.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| POST | `/api/v1/projects/{project_id}/asset-sets` | Command | 创建输入集合 |
| GET | `/api/v1/asset-sets/{asset_set_id}` | Query | 当前集合 |
| GET | `/api/v1/asset-set-versions/{asset_set_version_id}` | Query | 不可变版本 |
| POST | `/api/v1/asset-sets/{asset_set_id}/versions` | Command | 添加、移除、集号、排序修改 |
| POST | `/api/v1/asset-sets/{asset_set_id}/checks` | Command | 完整度检查 |
| POST | `/api/v1/asset-sets/{asset_set_id}/seal` | Command | 明确上传完成 |
| POST | `/api/v1/asset-sets/{asset_set_id}/reopen` | Command | 下游执行前重新收集 |

### 14.2 修改请求

```json
{
  "expected_asset_set_version": 3,
  "changes": [
    {
      "operation": "set_episode_order",
      "asset_id": "ast_...",
      "episode_order": 5
    },
    {
      "operation": "set_episode_no",
      "asset_id": "ast_...",
      "episode_no": 6
    }
  ]
}
```

`episode_no` 在收集和提取阶段可以为 `null`，但进入整批确认和 `reference_scripts` 聚合前必须由用户确认唯一正整数值。修改集号或顺序都会创建新的 Asset Set Version。

### 14.3 Seal

```json
{
  "expected_asset_set_version": 4,
  "continuation_policy": null,
  "user_confirmed_upload_complete": true
}
```

系统不能根据等待时间自动调用 Seal。

## 15. Run API

### 15.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| POST | `/api/v1/projects/{project_id}/runs` | Command | 启动 Capability Run |
| GET | `/api/v1/runs/{run_id}` | Query | Run 摘要 |
| GET | `/api/v1/runs/{run_id}/snapshot` | Query | Run 恢复快照 |
| POST | `/api/v1/runs/{run_id}/pause` | Command | 请求暂停 |
| POST | `/api/v1/runs/{run_id}/resume` | Command | 恢复 |
| POST | `/api/v1/runs/{run_id}/cancel` | Command | 结束本次 Run，保留已有记录和产物 |
| GET | `/api/v1/runs/{run_id}/steps` | Query | Step 列表 |
| GET | `/api/v1/steps/{step_run_id}/tasks` | Query | Task 列表 |
| POST | `/api/v1/tasks/{task_item_id}/retries` | Command | 重试失败 Task |
| POST | `/api/v1/steps/{step_run_id}/retries` | Command | 重试失败 Step |

### 15.2 Start Run

```json
{
  "capability_id": "video_reference_creation",
  "capability_version": "1.2.0",
  "run_kind": "generation",
  "conversation_id": "con_...",
  "input": {
    "project_id": "prj_...",
    "source_type": "video_reference",
    "asset_set_id": "aset_...",
    "asset_set_version_id": "asv_...",
    "collection_state": "sealed",
    "user_request_message_id": "msg_user_...",
    "user_notes": []
  },
  "config": {
    "config_ref": "extraction",
    "payload": {
      "fidelity_level": "high",
      "timecode_precision": "second",
      "uncertain_content_policy": "mark"
    }
  },
  "confirmation": {
    "confirmed": true,
	"proposed_action_id": "pac_...",
	"action_version": 1,
	"confirmation_message_id": "msg_agent_...",
	"snapshot_hash": "64_hex_chars"
  }
}
```

Start Run 必须与待确认动作中的 Project、Conversation、Capability、Input 和 Config 完全一致。动作消费与 Run 创建在同一数据库事务完成；旧卡重放返回 `CONFIRMATION_STALE`，内容或配置变化返回 `CONFIRMATION_SNAPSHOT_MISMATCH`。

Runtime 创建：

- sealed Input Snapshot Version；
- extraction Config Snapshot；
- Run；
- 初始 Step；
- `run.started` Event；
- Idempotency Record。

在同一事务或 Outbox 边界完成。

### 15.3 视频 Asset Set 输入

视频允许在 Run 外跨批上传。通用上传 API 每次追加、移除或重排都生成新的 Asset Set Version；只有当前版本已经 Seal，才允许创建 `video_reference_creation` Run。

```text
create collecting Asset Set
-> append upload batches and create immutable Asset Set Versions
-> user reorders or resolves episode numbers
-> user explicitly confirms upload complete
-> Seal current Asset Set Version
-> Start Run with exact sealed Asset Set Version
-> Runtime creates one immutable Run Input Snapshot
-> create one extraction Task per included video
```

每个 Task 固定：

- Run Input Snapshot Version ID；
- Asset Set Version ID；
- Asset Set Member ID；
- Asset Snapshot ID；
- Provider Variant ID。

旧 Snapshot Version 和已完成 Task 不被改写。

### 15.4 Start Guard

必须检查：

- Capability 存在且可用；
- Capability Version 可执行；
- Asset 属于当前 Project；
- Asset/Asset Set 未删除过期；
- Input Schema 通过；
- 当前入口阶段的 Config Schema 通过；
- Project 没有其他活动写 Run；
- 用户确认信息存在；
- 当前工作区允许 Provider。

体量不足扩写策略、视频缺集继续和视频改编方法属于后续条件 Approval，不是所有 Run 启动前都具备的信息。只有条件实际出现时才阻止对应 Transition。

### 15.5 Pause

Pause 返回 `202` 表示：

- 暂停请求已记录；
- 没有活动 Attempt 时，Runtime 直接返回 `paused`；
- 有活动 Attempt 时，Runtime 将在安全边界停止，状态可以先进入 `pausing`；
- `pausing` 必须提供撤销暂停动作；活动 Attempt Lease 失效后必须自动收敛为 `paused`；
- 已发送 Provider 请求不强行中断；
- 已完成 Task 和 Artifact 保留。

### 15.6 Resume

Resume Guard：

- Run 为 `paused`；
- 没有 Pending Approval；
- Capability Version 仍可恢复；
- 输入来源仍满足执行要求；
- Cursor 有效；
- Cursor 未因暂停期间的新 Artifact Version 标记为 `input_changed`；
- 没有另一活动写 Run。

暂停期间允许保存新 Artifact Version，但只要影响当前或后续 Step 输入，服务端必须返回 Impact Preview，并从 `available_actions` 移除 `resume`，直到用户明确选择从受影响步骤重生成或放弃该修改。

### 15.7 Retry

Retry 只能针对：

- Runtime 标记失败的 Step/Task；
- 当前有效 Attempt；
- 当前 Snapshot；
- 当前允许重试的错误。

成功 Task 不重复执行。

### 15.8 Cancelled

Cancel 用于用户明确不再继续当前 Run：

- 停止创建后续 Step/Task；
- 已发送 Provider 请求可以等待返回，但结果不得自动推进下游；
- 已完成 Task、Artifact Version、Event 和 Attempt 保留；
- Run 进入 `cancelled` 终态；
- 不能 Resume；
- 不删除已有产物；
- 以后继续创作需要创建新 Run 或 Revision Run。

Cancel 是破坏流程连续性的操作，前端必须二次确认，并使用“结束本次运行”文案，不能让用户误以为同时删除了产物。

## 16. Artifact API

### 16.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| GET | `/api/v1/projects/{project_id}/artifacts` | Query | Artifact 列表 |
| GET | `/api/v1/artifacts/{artifact_id}` | Query | Artifact 摘要和 Current Pointer |
| GET | `/api/v1/artifacts/{artifact_id}/versions` | Query | 版本列表 |
| GET | `/api/v1/artifact-versions/{artifact_version_id}` | Query | 精确版本 Payload |
| POST | `/api/v1/artifacts/{artifact_id}/versions` | Command | 手动创建新版本 |
| POST | `/api/v1/runs/{run_id}/script-edit-completions` | Command | 完成一批剧本编辑并刷新对应交接 |
| POST | `/api/v1/artifacts/{artifact_id}/ai-revisions` | Command | 请求 AI 修改 |
| POST | `/api/v1/artifact-versions/{artifact_version_id}/impact-previews` | Command | 下游影响预览 |

### 16.2 手动保存

全量：

```json
{
  "base_version_id": "av_...",
  "base_version": 3,
  "change_mode": "full_payload",
  "new_payload": {}
}
```

局部：

```json
{
  "base_version_id": "av_...",
  "base_version": 3,
  "change_mode": "scoped",
  "change_set": {
    "changes": []
  }
}
```

两者只能选择一种。

剧本步骤的 `script_unit` 保存成功后，响应额外返回：

```json
{
  "handoff_refresh_required": true,
  "pending_refresh_scopes": ["episode:2", "episode:5"],
  "approval": {}
}
```

此时旧 Batch Approval 已过期，对应 `script_handoff` 为 `stale`，不得创建一个绑定旧交接的伪新 Approval。

用户完成本轮全部剧本编辑后调用：

```text
POST /api/v1/runs/{run_id}/script-edit-completions
Idempotency-Key: <uuid>
```

```json
{
  "expected_script_version_ids": ["av_episode_2_v2", "av_episode_5_v3"]
}
```

服务端要求该集合与当前全部待刷新剧本 Version 完全一致。成功返回 `202 Accepted`，只把受影响集 Task 切换为 `script_handoff_refresh`，Run 回到 `running`。刷新期间继续保存剧本返回 `RUN_STATE_CONFLICT`。全部刷新成功后才创建新的 `2N` Batch Approval。

### 16.3 空字符串

局部 Change Set 中：

```json
{
  "operation": "replace",
  "field_path": "characters[char_01].description",
  "new_value": ""
}
```

空字符串是合法删除结果，不能因 `len(value)==0` 拒绝。

### 16.4 AI Revision

```json
{
  "base_version_id": "av_...",
  "instruction": "把这句台词改得更克制",
  "selection_snapshot": {},
  "expected_selection_hash": ""
}
```

Completed Run 后：

- 创建 Revision Run；
- 不重新打开原 Run；
- 新版本形成新 Candidate；
- 不自动替换 Final Script。

### 16.5 Payload 大小

Artifact 列表和 Event 不携带完整 Payload。

编辑器按精确 Version Query 获取 Payload，必要时按 Episode Scope 分页或分块。

## 17. Approval API

### 17.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| GET | `/api/v1/projects/{project_id}/approvals?status=pending` | Query | 当前待确认 |
| GET | `/api/v1/approvals/{approval_request_id}` | Query | Approval 详情 |
| POST | `/api/v1/approvals/{approval_request_id}/resolutions` | Command | 解决确认点 |

### 17.2 Resolution

```json
{
  "action": "approve",
  "expected_approval_version": 1,
  "subject_snapshot_hash": "",
  "instruction": null,
  "client_context": {
    "current_artifact_version_id": "av_..."
  }
}
```

### 17.3 Action

Action 来自服务端 Approval Options，不由前端按 Skill 硬编码。

第一版通用动作：

```text
approve
pause
reject
revise_instruction
edit_artifact
request_ai_revision
regenerate_artifact
retry_failed
keep_downstream
regenerate_downstream
continue_incomplete
select_adaptation_strategy
confirm_delete
select_final
```

具体 Approval 只返回允许动作子集。

### 17.4 改编方法确认

`select_adaptation_strategy` 必须同时提交用户最终选择和新剧本创作配置：

```json
{
  "action": "select_adaptation_strategy",
  "expected_approval_version": 1,
  "subject_snapshot_hash": "",
  "resolution_payload": {
    "adaptation_options_artifact_version_id": "av_...",
    "selection": {
      "selected_option_ids": ["option_1"],
      "combined_methods": [],
      "custom_changes": [],
      "user_instruction_message_id": "msg_..."
    },
    "creation_config": {
      "target_episode_count": 24,
      "episode_duration_minutes": 2,
      "user_requirements": []
    }
  }
}
```

Runtime 必须在同一事务中创建 Adaptation Decision Snapshot、Creation Config Snapshot、Approval Resolution 和 Event。任一步失败都不推进 `build_adaptation_brief`。

### 17.5 Resolution Guard

检查：

- Approval 仍为 `pending`；
- Version 未变化；
- Subject Snapshot Hash 一致；
- Action 在 Options 中；
- Project/Run 状态允许；
- Idempotency Key 合法；
- 影响 Preview 未过期。

### 17.6 重复 Resolution

同 Key 返回第一次结果。

不同 Key 再解决已完成 Approval：

```text
409 APPROVAL_ALREADY_RESOLVED
```

返回已存在 Resolution。

## 18. Candidate and Final Script API

### 18.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| GET | `/api/v1/projects/{project_id}/script-candidates` | Query | 候选剧本 |
| GET | `/api/v1/script-candidates/{candidate_id}` | Query | 候选详情 |
| GET | `/api/v1/projects/{project_id}/final-script-selection` | Query | 当前最终稿 |
| POST | `/api/v1/projects/{project_id}/final-script-selection-previews` | Command | 选择影响预览 |
| POST | `/api/v1/projects/{project_id}/final-script-selections` | Command | 确认选择/改选 |

### 18.2 选择

Preview：

```json
{
  "candidate_id": "cand_...",
  "expected_current_selection_id": null
}
```

Confirm：

```json
{
  "candidate_id": "cand_...",
  "preview_hash": "",
  "expected_current_selection_id": null,
  "confirmed": true
}
```

### 18.3 规则

- 一个 Project 同时只有一个 Active Final Selection；
- 改选创建新 Selection；
- 旧 Selection 保留；
- 其他 Candidate 保留；
- 不修改 Candidate Artifact；
- 必须确认。

### 18.4 阶段 3 第 9 步实现补充

- 上述五条路由已接入 Runtime；
- Candidate 列表为空时返回 `items: []`，尚未选择最终稿时当前 Final Query 返回 `data: null`；
- `scripts` Version 为 `confirmed` 或 `superseded` 的 Candidate 可选择，`pending_approval`、`stale`、`invalidated` 被 Guard 拒绝；
- Preview 创建 Project 级 `final_selection` Approval，新的 Preview 会使旧 Pending Preview 过期；
- Confirm 必须提交 `confirmed=true`、相同 `preview_hash` 和相同 `expected_current_selection_id`；
- `final_selection` Approval 不能通过通用 `/approvals/{id}/resolutions` 解决，只能由 Final Selection Confirm 命令原子处理；
- 当前稿变化返回 `409 FINAL_SELECTION_CONFLICT`，过期 Preview 返回 `409 FINAL_SELECTION_PREVIEW_EXPIRED`；
- Candidate 创建、首次选择、改选和历史保留均由数据库事务与部分唯一索引保护。

## 19. Export API

### 19.1 路由

| Method | Path | 类型 | 用途 |
|---|---|---|---|
| POST | `/api/v1/script-candidates/{candidate_id}/exports` | Command | 创建导出 |
| GET | `/api/v1/exports/{export_id}` | Query | 导出状态 |
| GET | `/api/v1/exports/{export_id}/content` | Query | 下载 |

### 19.2 Request

```json
{
  "format": "txt | docx",
  "artifact_version_id": "av_..."
}
```

### 19.3 Guard

- 使用精确 Artifact Version；
- 目标集数完整；
- 没有未保存修改；
- 没有生成中 Task；
- 格式在白名单；
- 临时导出文件按 Retention Contract 清理。

## 20. Snapshot Contract

### 20.1 为什么需要 Snapshot

以下情况必须依赖 Snapshot：

- 首次打开 Project；
- 浏览器刷新；
- 切换 Project；
- SSE 断线；
- Event Gap；
- 服务重启；
- Command 结果未知；
- 前端缓存与服务端版本冲突。

### 20.2 Project Workbench Snapshot

```json
{
  "project": {
    "project_id": "prj_...",
    "title": "",
    "version": 5,
    "status": "waiting_approval",
    "active_run_id": "run_...",
    "current_final_selection_id": null
  },
  "primary_conversation": {
    "conversation_id": "con_...",
    "latest_message_id": "msg_..."
  },
  "asset_summary": [],
  "active_run": {},
  "current_approval": {},
  "artifact_summary": [],
  "candidate_summary": [],
  "final_selection": null,
  "available_capabilities": [],
  "available_actions": [],
  "event_cursor": {
    "project_event_seq": 842
  },
  "snapshot_version": 17,
  "snapshot_hash": "",
  "generated_at": ""
}
```

### 20.3 Snapshot 不包含

- 全部历史消息；
- 全部 Event；
- 全部 Artifact Payload；
- 文件内容；
- 视频字节；
- 完整 Prompt/Response；
- 其他 Project 数据。

### 20.4 Run Snapshot

```json
{
  "run": {},
  "steps": [],
  "task_summary": [],
  "current_approval": null,
  "artifact_summary": [],
  "failure": null,
  "available_actions": [],
  "event_cursor": {
    "project_event_seq": 842,
    "run_event_seq": 51
  },
  "snapshot_hash": ""
}
```

### 20.5 原子 Cursor

Snapshot 状态和 `event_cursor` 必须从一致读取边界产生。

正确启动流程：

```text
GET Snapshot at seq N
-> render
-> connect SSE after N
-> receive N+1...
```

不能：

```text
先读状态
-> 很久以后再单独查当前 Event ID
```

否则中间事件可能丢失。

## 21. Event Model

### 21.1 Canonical Envelope

```json
{
  "event_id": "evt_...",
  "event_type": "artifact.version_created",
  "schema_version": 1,
  "project_id": "prj_...",
  "conversation_id": null,
  "run_id": "run_...",
  "step_run_id": "stp_...",
  "task_item_id": null,
  "project_event_seq": 842,
  "run_event_seq": 51,
  "actor": {
    "kind": "runtime",
    "ref": "capability_runner"
  },
  "subject": {
    "resource_type": "artifact_version",
    "resource_id": "av_..."
  },
  "payload": {},
  "command_id": "cmd_...",
  "causation_event_id": null,
  "correlation_id": "run_...",
  "trace_id": "",
  "occurred_at": ""
}
```

### 21.2 Actor

```text
shared_workspace_user
agent
runtime
worker
provider
system
```

### 21.3 Event 顺序

每个 Project：

```text
project_event_seq
```

单调递增、事务分配、不可重复。

属于 Run 的 Event 同时拥有：

```text
run_event_seq
```

不属于 Run 时 `run_event_seq=null`。

### 21.4 时间不是顺序

Event 排序不能使用：

- `occurred_at`；
- Event ID 字典序；
- 前端接收时间。

只使用：

- Project Stream：`project_event_seq`；
- Run Stream：`run_event_seq`。

### 21.5 Append-only

Event：

- 只追加；
- 不更新；
- 不删除单条；
- 与状态变化通过同一事务和 Outbox 提交；
- 不是当前状态表；
- Project 删除时按最小审计策略处理。

### 21.6 Payload 原则

Payload 只包含：

- 变化摘要；
- 资源引用；
- 进度；
- 安全错误摘要；
- 前端下一步需要的轻量信息。

Payload 不包含：

- 完整 Artifact；
- 完整剧本；
- 文件内容；
- Base64；
- Provider Key；
- 完整 Prompt/Response。

## 22. Event Names

### 22.1 命名

```text
<domain>.<past_tense_or_state_change>
```

统一小写点分命名。

### 22.2 Project

```text
project.created
project.renamed
project.status_changed
project.delete_requested
project.deleted
```

### 22.3 Conversation

```text
message.created
agent.decision_created
agent.capability_gap_detected
```

### 22.4 Asset

沿用 Asset Contract：

```text
asset.upload_started
asset.upload_completed
asset.upload_failed
asset.parse_started
asset.parse_completed
asset.parse_failed
asset.variant_created
asset.variant_deleted
asset.delete_requested
asset.delete_confirmed
asset.deleted
asset.expiration_scheduled
asset.expired
asset.retention_failed
```

### 22.5 Asset Set

```text
asset_set.created
asset_set.member_added
asset_set.member_removed
asset_set.order_changed
asset_set.completeness_checked
asset_set.sealed
asset_set.reopened
asset_set.superseded
```

### 22.6 Run

```text
run.created
run.started
run.pause_requested
run.paused
run.resumed
run.waiting_approval
run.completed
run.failed
run.cancel_requested
run.cancelled
```

### 22.7 Step and Task

```text
step.started
step.progressed
step.waiting_approval
step.completed
step.failed
step.skipped

task.queued
task.started
task.progressed
task.completed
task.failed
task.retry_scheduled
task.retried
```

### 22.8 Artifact

```text
artifact.created
artifact.version_created
artifact.status_changed
artifact.confirmed
artifact.superseded
artifact.invalidated
artifact.impact_preview_created
script_edit.awaiting_handoff_refresh
script_edit.completed
script_handoff.stale
script_handoff.refresh_requested
script_handoff.refresh_completed
```

### 22.9 Approval

```text
approval.requested
approval.resolved
approval.expired
approval.cancelled
```

### 22.10 Candidate and Final

```text
script_candidate.created
script_candidate.revised
final_selection.requested
final_selection.changed
```

### 22.11 Export

```text
export.requested
export.completed
export.failed
export.expired
```

### 22.12 System

```text
system.recovery_started
system.recovery_completed
stream.reset_required
```

`stream.reset_required` 是传输控制事件，不写入业务 Event 表。

## 23. 关键 Event Payload

### 23.1 `run.started`

```json
{
  "capability_id": "video_reference_creation",
  "capability_version": "1.0.0",
  "run_kind": "generation",
  "current_step_run_id": "stp_..."
}
```

### 23.2 `task.progressed`

```json
{
  "item_key": "episode:8",
  "item_order": 8,
  "completed_units": 1,
  "total_units": 1,
  "progress_percent": 100,
  "message": "第 8 集解析完成"
}
```

进度不能改变 Step/Task 权威状态。

### 23.3 `artifact.version_created`

```json
{
  "artifact_id": "art_...",
  "artifact_version_id": "av_...",
  "artifact_type": "script_unit",
  "version": 4,
  "status": "pending_approval",
  "scope_key": "episode:8",
  "base_version_id": "av_old",
  "creation_reason": "ai_revision"
}
```

不携带 Payload。

### 23.4 `approval.requested`

```json
{
  "approval_request_id": "apr_...",
  "scope": "artifact",
  "subject_refs": [
    {
      "resource_type": "artifact_version",
      "resource_id": "av_..."
    }
  ],
  "options": [
    {
      "action_id": "approve",
      "label": "确认"
    }
  ]
}
```

整步剧本确认使用：

```json
{
  "approval_request_id": "apr_...",
  "scope": "batch",
  "subject_kind": "artifact_version_set",
  "subject_ref_id": "stp_...",
  "subject_snapshot_hash": "...",
  "subject_refs": [
    {
      "resource_type": "artifact_version",
      "resource_id": "av_episode_1",
      "scope_key": "episode:1",
      "item_order": 1
    }
  ]
}
```

单集成功提交时，提交接口可先返回
`commit_status=task_artifact_saved`；最后一个必需 Task 成功后返回
`commit_status=waiting_approval` 和唯一的集合 Approval。编辑任一单集后，
旧 Approval 立即过期。普通批量产物可直接创建新的精确版本集合快照；
`script_unit` 必须先刷新对应 `script_handoff`，全部版本重新对齐后才能创建新快照。

### 23.5 `asset.expired`

```json
{
  "asset_id": "ast_...",
  "source_available": false,
  "formal_artifacts_preserved": true,
  "expired_at": ""
}
```

### 23.6 `run.failed`

```json
{
  "failure_code": "PROVIDER_TIMEOUT",
  "retryable": true,
  "failed_step_run_id": "stp_...",
  "failed_task_item_id": "tsk_...",
  "completed_work_preserved": true,
  "available_actions": [
    {
      "action_id": "retry_failed_task",
      "label": "重试"
    }
  ]
}
```

## 24. SSE Contract

### 24.1 Project Stream

```text
GET /api/v1/projects/{project_id}/events/stream
Accept: text/event-stream
Last-Event-ID: <opaque-cursor>
```

也支持首次显式：

```text
?after_seq=842
```

Header 优先于 Query。

### 24.2 Run Stream

```text
GET /api/v1/runs/{run_id}/events/stream
```

使用 `run_event_seq`，主要用于 Run 详情页和旧前端迁移。

### 24.3 SSE Frame

```text
id: 843
event: artifact.version_created
data: {"event_id":"evt_...","project_event_seq":843,...}

```

Project Stream 的 `id` 使用 Project Cursor；Run Stream 使用 Run Cursor。

客户端把 ID 当作不透明 Cursor，不拼接或计算。

阶段 3 第 7 步实现补充：

- 两条 Stream 都从已提交的 Event Table 按 Seq 有限批次轮询；
- `Last-Event-ID` 优先于 `after_seq`；
- 无 Cursor 时从连接建立时的当前 Seq 之后开始，不重放全部历史；
- Cursor Ahead 返回 `stream.reset_required` 并关闭连接；
- SSE 连接关闭、写失败或慢客户端退出不修改 Run、Task 或 Attempt 状态；
- 每条业务 Event 同事务写入 `event_outbox`，Event Table 仍是重连重放的权威来源。

### 24.4 Keepalive

每 15 秒：

```text
: keepalive

```

Keepalive：

- 不递增 Event Seq；
- 不写数据库；
- 不触发 UI 状态变化。

### 24.5 Retry Hint

连接时可以发送：

```text
retry: 3000

```

前端仍实现有上限指数退避。

### 24.6 Replay

连接携带有效 Cursor：

- 重放 `seq > cursor` 的 Event；
- 然后持续推送新 Event；
- 不重放已经确认的前序 Event。

### 24.7 无 Cursor

Workbench 标准流程必须先拿 Snapshot。

如果直接无 Cursor 连接：

- 只从连接时当前 Cursor 之后推送；或
- 服务端按明确配置返回有限最近事件。

不能默认重放 Project 全部历史。

### 24.8 Cursor Ahead

客户端 Cursor 大于服务端当前 Seq：

```text
stream.reset_required
```

原因可能是：

- 数据恢复；
- 客户端串了 Project；
- 本地缓存损坏。

前端必须丢弃该 Project 的事件缓存并重新获取 Snapshot。

### 24.9 Cursor Too Old

如果未来 Event 有保留窗口，Cursor 早于最小可重放 Seq：

- 发送 `stream.reset_required`；
- 包含当前最小/最大 Cursor；
- 关闭流；
- 前端重新获取 Snapshot。

第一版 Event 随 Project 保留，通常不会发生。

### 24.10 Gap

前端已处理 842，下一条收到 844：

- 不直接应用 844；
- 标记 Event Gap；
- 关闭当前流；
- 获取 Project Snapshot；
- 使用 Snapshot Cursor 重连。

### 24.11 重复

重复 Event：

- 按 `event_id` 去重；
- 按 Seq 检查；
- 不重复弹提示；
- 不重复发 Query；
- 不重复触发 Command。

### 24.12 SSE 鉴权

第一版没有用户账户，但仍检查：

- Project 存在；
- Project 未删除；
- 请求来自允许的内网部署边界；
- Run 属于 Project；
- 不允许跨 Project 订阅。

## 25. Frontend Recovery

### 25.1 首次打开

```text
GET Project Snapshot
-> 保存 Cursor N
-> 渲染
-> 连接 Project SSE after N
-> 顺序处理 N+1...
```

### 25.2 SSE 断开

```text
onerror
-> 保留当前 UI
-> 标记实时连接中断
-> 使用最后成功 Cursor 重连
-> 同时按退避周期 Query Snapshot
```

不能把 SSE 断开等同于 Run 失败。

### 25.3 Poll Fallback

建议：

```text
2s
5s
10s
20s
30s cap
```

恢复 SSE 后停止高频 Poll。

### 25.4 Snapshot 合并

Snapshot 到达后：

- 用服务端完整资源状态替换对应本地缓存；
- 不保留版本更旧的本地对象；
- 未保存编辑草稿单独保存并提示冲突；
- 从 Snapshot Cursor 重建 SSE；
- 不把本地 Event 反向写回服务端。

### 25.5 Command 超时

POST 超时：

```text
GET /commands/{command_id}
```

若客户端尚未取得 Command ID：

- 使用同一 Idempotency Key 重试原请求；
- 服务端返回同一结果；
- 不创建新 Key。

### 25.6 页面刷新

刷新后：

- 不依赖内存中的 Run；
- 获取 Project Snapshot；
- Pending Approval 恢复；
- Active Run 恢复；
- 失败 Task 和可用动作恢复；
- 输入框锁定状态从服务端恢复。

## 26. 前端状态应用规则

### 26.1 Event Reducer

Event Reducer 只能：

- 更新轻量状态；
- 标记资源 dirty；
- 触发 Query；
- 更新进度；
- 更新 Cursor。

不得：

- 生成业务 Artifact；
- 猜测 Approval 已解决；
- 自行修改 Run 状态；
- 根据中文 Message 判断 Event Type；
- 以本地时间排序。

### 26.2 Unknown Event

前端收到未知 `event_type`：

- 保存 Cursor；
- 忽略未知 Payload；
- 不断开连接；
- 必要时刷新 Snapshot；
- 记录安全 Telemetry。

这样允许后端增加兼容 Event。

### 26.3 Schema Version

同一 Event Type：

- 新增可选字段可以保留 Schema Version；
- 删除、改名或改变字段语义必须升级；
- 前端不支持更高重大版本时刷新 Snapshot并提示升级；
- 不对 Payload 做宽松猜测。

## 27. 三个 Skills 的 API 映射

### 27.1 小说转剧本

```text
upload/paste novel Asset
-> POST Message with explicit/inferred capability
-> confirm generation config
-> POST Start Run novel_to_script
-> Project SSE
-> query story_bible Artifact Version
-> resolve Approval
-> query episode_split
-> resolve Approval
-> query episode_cards
-> resolve Approval
-> batch script_unit Tasks
-> resolve script step Approval
-> runtime.aggregate_scripts creates confirmed scripts refs
-> Candidate
-> Final Selection
```

### 27.2 非小说文本转剧本

```text
upload/paste material Assets
-> select Asset Set
-> POST Start Run non_novel_to_script
-> material_bank
-> Approval
-> story_seed
-> Approval
-> series_blueprint
-> Approval
-> episode_cards
-> Approval
-> script_unit Tasks
-> Approval
-> Candidate
```

### 27.3 视频参考创作

```text
create video Asset Set
-> upload batches
-> Asset Events
-> add members/version
-> explicit Seal
-> completeness check
-> user confirms video_reference_creation and extraction config
-> start Run with exact sealed Asset Set Version
-> video_script_unit Tasks
-> unified Approval
-> reference_scripts aggregate
-> script_analysis
-> Approval
-> adaptation options
-> adaptation_brief
-> Approval
-> continue same video_reference_creation Run
-> shared non-novel internal workflow
-> Candidate
```

整个过程：

```text
run.capability_id = video_reference_creation
```

不切换为第二个用户 Skill。

### 27.4 其他未来 Skills

新增 Capability 时：

- 注册 Capability；
- 声明 Input/Config/Step/Artifact Schema；
- 复用 Project、Message、Asset、Run、Artifact、Approval、Event API；
- 通过 UI Registry 扩展领域视图；
- 不增加新的 Agent Host；
- 不复制一套 SSE 和状态机。

## 28. 通用图片输入

图片不自动启动 Domain Run。

用户：

```text
上传图片
-> Message 表达意图
-> Agent 使用 Generic Image Tool
-> 返回普通 Agent Message
```

如果图片作为某个 Capability 的参考输入：

- Message/Start Run 明确 Asset ID；
- Capability Input Schema 必须接受；
- Run Input Snapshot 固定；
- 不因上传本身自动进入剧本链路。

## 29. Available Actions

### 29.1 服务端权威

快照、Approval、Error 和部分 Event 可以返回：

```json
{
  "available_actions": [
    {
      "action_id": "resume_run",
      "label": "继续",
      "danger": false,
      "enabled": true,
      "disabled_reason": null,
      "command": {
        "method": "POST",
        "resource": "run_resume"
      }
    }
  ]
}
```

前端根据 Action ID 渲染按钮，但不能直接执行服务端返回的任意 URL。

### 29.2 为什么不返回任意 URL

避免：

- 服务端 Payload 变成前端 SSRF/开放跳转入口；
- 前端绕过类型化 Client；
- API 路径变更无法编译检查；
- Capability 注入任意请求。

前端维护：

```text
action_id -> typed client command
```

服务端维护动作是否允许。

### 29.3 Composer Action 投影

第一版受控 Action ID 与最终 Composer 状态固定映射：

| Runtime/UI 状态 | Action ID | 前端角色 | Command |
|---|---|---|---|
| Empty | `send_message` disabled | Send Disabled | 无 |
| Active / Context | `send_message` | Send | `POST message` |
| Running | `pause_run` | Pause | `POST run pause` |
| Paused | `resume_run` | Resume | `POST run resume` |
| Error，可重试 | `retry_failed_task` 或 `retry_failed_step` | Retry | 对应 retry command |
| 任意可结束状态 | `cancel_run` | More Menu 内“结束本次运行” | `POST run cancel` |

服务端不返回某 Action ID 时，前端不得仅根据状态名显示它。`cancel_run` 不进入 Composer 主动作位；Approval 的 `approve`、`request_ai_revision` 等动作只进入 Agent Timeline ApprovalCard。

## 30. Rate Limit and Backpressure

### 30.1 HTTP

至少按：

- 工作区；
- Project；
- Route；
- Provider；
- Client Instance；

实施限流。

### 30.2 429

```json
{
  "error": {
    "code": "RATE_LIMITED",
    "retryable": true,
    "details": {
      "retry_after_seconds": 5
    }
  }
}
```

响应头：

```text
Retry-After: 5
```

### 30.3 SSE 慢客户端

SSE 不能无限缓存：

- 每连接设置发送队列上限；
- 慢客户端超限时断开；
- 客户端用 Cursor 重连；
- Event 已持久化，不因连接断开丢失；
- 高频进度可以合并，但终态 Event 不能丢。

### 30.4 Progress Coalescing

`task.progressed` 可以按固定最小间隔合并，例如 500ms-1s。

必须保留：

- Task Started；
- Task Completed；
- Task Failed；
- Step/Run 终态。

## 31. Security Boundary

### 31.1 Same Origin

正式内网页面和 API 应同源部署。

本地开发 CORS：

- 只允许配置白名单 Origin；
- 不默认 `*`；
- 不允许任意 Credential Origin；
- Production 关闭不需要的 CORS。

### 31.2 Body Limit

按 Route 设置：

- JSON Message；
- Artifact Change Set；
- Upload Content；
- SSE 无 Request Body。

不能用一个全局 16 MB 限制同时承担 JSON 和视频上传。

### 31.3 Input Validation

所有 Command：

- 严格 JSON Schema；
- 拒绝未知关键字段；
- 校验 ID 归属；
- 校验状态；
- 校验版本；
- 校验 Capability 可用性；
- 校验 Payload 大小；
- 校验枚举；
- 不信任前端 Action Label。

### 31.4 Shared Workspace

第一版没有登录：

- 所有内部用户可见全部 Project；
- UI 明确共享空间；
- 部署网关负责内网访问；
- API 仍执行 Project 资源归属校验；
- Event Actor 使用 Client Instance，不伪装真实用户身份。

### 31.5 Credential

Provider Credential：

- 只在服务端；
- 不进入 API；
- 不进入 Event；
- 不进入前端环境变量；
- 不进入 Artifact；
- 不进入日志。

## 32. Event Transaction and Outbox

### 32.1 同一业务事务

例如 Resolve Approval：

```text
validate command
-> write resolution
-> update artifact/step/run state
-> create next task
-> allocate event seq
-> write event
-> write outbox
-> save idempotency result
-> commit
```

### 32.2 SSE 发布

SSE Publisher 从：

- Event Table；
- Outbox；
- 或数据库变更通知轮询；

读取已提交 Event。

不能先向 SSE 发 Event，再提交数据库。

### 32.3 服务重启

重启后：

- Current State 从 SQLite 恢复；
- 未发布 Outbox 继续发布；
- Event Seq 不回退；
- 成功 Task 不重复；
- Pending Command 可恢复或标记失败；
- 客户端 Snapshot + Cursor 恢复。

### 32.4 Event ID 与 Seq

- Event ID 全局唯一；
- Project Seq 在 Project 内唯一；
- Run Seq 在 Run 内唯一；
- Seq 分配与 Event Insert 同事务；
- 删除 Project 不复用 Seq。

## 33. 数据库约束

至少建立：

```text
commands
idempotency_records
events
outbox
```

约束：

- `commands(command_id)` 唯一；
- `idempotency_records(scope, command_type, idempotency_key)` 唯一；
- `events(event_id)` 唯一；
- `events(project_id, project_event_seq)` 唯一；
- `events(run_id, run_event_seq)` 对非空值唯一；
- `outbox(event_id)` 唯一。

索引：

- Project Event Cursor；
- Run Event Cursor；
- Command Status；
- Idempotency Expiration；
- Outbox Publish Status；
- Event Subject；
- Event Occurred At 仅用于审计查询，不用于顺序。

## 34. Legacy API Mapping

### 34.1 现有可复用行为

保留：

- Project 列表、创建、打开；
- Project Message 单一自然语言入口；
- Run Snapshot；
- Pause/Resume；
- Failed Step Retry；
- Artifact `base_version` 冲突保护；
- Approval Resolution；
- SSE `Last-Event-ID`；
- Snapshot 兜底；
- GET 有限重试；
- 结构化错误；
- SQLite 持久化。

### 34.2 必须替换

| 旧实现 | 目标 |
|---|---|
| `source_mode` | `capability_id + input_snapshot` |
| `File` | `Asset + Blob + Parse Result` |
| JSON Base64 上传 | 流式 Upload Session |
| Project 固定 SourceMode | Project 可运行多个 Capability Run |
| Command 返回全部 Events/Artifacts | 返回资源引用 + Cursor |
| 仅 Run SSE | Project SSE 为主 |
| Event 按时间排序 | 单调 Seq |
| 未知 Last-Event-ID 重放全部 | Reset + Snapshot |
| `PATCH artifact` 修改概念 | 创建不可变 Artifact Version |
| 前端固定 Event 列表 | Unknown Event 向前兼容 |
| 前端固定 SourceMode 类型 | Capability Registry/UI Manifest |

### 34.3 路由映射

| 旧路由 | 目标路由 |
|---|---|
| `GET /api/projects` | `GET /api/v1/projects` |
| `POST /api/projects` | `POST /api/v1/projects` |
| `POST /api/projects/{id}/messages` | `POST /api/v1/conversations/{id}/messages` |
| `GET /api/projects/{id}/files` | `GET /api/v1/projects/{id}/assets` |
| `POST /api/projects/{id}/files` | Upload Session API |
| `DELETE /api/files/{id}` | Delete Preview + Confirmation |
| `PATCH /api/artifacts/{id}` | `POST /api/v1/artifacts/{id}/versions` |
| `POST /api/approvals/{id}/resolve` | `POST /api/v1/approvals/{id}/resolutions` |
| `GET /api/runs/{id}` | `GET /api/v1/runs/{id}/snapshot` |
| `GET /api/runs/{id}/events/stream` | Project Stream + Run Compatibility Stream |

### 34.4 Event 映射

| 旧 Event | 目标 Event |
|---|---|
| `run_started` | `run.started` |
| `step_started` | `step.started` |
| `artifact_created` | `artifact.version_created` |
| `artifact_updated` | `artifact.version_created` |
| `script_batch_inserted` | `task.completed` + `artifact.version_created` |
| `approval_requested` | `approval.requested` |
| `approval_resolved` | `approval.resolved` |
| `progress_updated` | `task.progressed` 或 `step.progressed` |
| `step_completed` | `step.completed` |
| `run_completed` | `run.completed` |
| `step_failed` | `step.failed` / `task.failed` |
| `run_paused` | `run.paused` |
| `run_resumed` | `run.resumed` |

### 34.5 兼容层

迁移期：

- 旧 `/api` 继续服务现有前端；
- 新功能只接 `/api/v1`；
- Compatibility Adapter 将新 Runtime DTO 映射为旧 DTO；
- 不让旧 `source_mode` 回写新 Runtime 真源；
- 旧 Event 可以从新 Event 投影；
- 新前端完成迁移后删除兼容层；
- 删除前保留回归测试。

## 35. 错误码清单

### 35.1 通用

```text
INVALID_REQUEST
SCHEMA_VALIDATION_FAILED
RESOURCE_NOT_FOUND
RESOURCE_PROJECT_MISMATCH
RESOURCE_VERSION_CONFLICT
IDEMPOTENCY_KEY_REQUIRED
IDEMPOTENCY_KEY_REUSED
COMMAND_RESULT_UNKNOWN
WORKSPACE_BUSY
RATE_LIMITED
SERVICE_UNAVAILABLE
INTERNAL_ERROR
```

### 35.2 Agent/Capability

```text
CAPABILITY_NOT_FOUND
CAPABILITY_UNAVAILABLE
CAPABILITY_VERSION_UNAVAILABLE
CAPABILITY_INPUT_INVALID
CAPABILITY_CONFIG_INVALID
CAPABILITY_INTENT_AMBIGUOUS
REQUIRED_CONFIRMATION_MISSING
RUN_ACTIVE_MESSAGE_LOCKED  # 兼容旧客户端；新 Message 路由不再因活动 Run 一律返回此错误
PROJECT_WRITE_RUN_CONFLICT
```

### 35.3 Run

```text
RUN_NOT_FOUND
RUN_STATE_CONFLICT
RUN_NOT_PAUSABLE
RUN_NOT_RESUMABLE
RUN_NOT_CANCELLABLE
RUN_CURSOR_INVALID
STEP_NOT_RETRYABLE
TASK_NOT_RETRYABLE
PROVIDER_TIMEOUT
PROVIDER_RATE_LIMITED
PROVIDER_OUTPUT_INVALID
```

### 35.4 Artifact

```text
ARTIFACT_NOT_FOUND
ARTIFACT_VERSION_NOT_FOUND
ARTIFACT_VERSION_CONFLICT
ARTIFACT_PAYLOAD_INVALID
SELECTION_SNAPSHOT_STALE
IMPACT_PREVIEW_EXPIRED
DEPENDENCY_LINEAGE_CONFLICT
```

### 35.5 Approval

```text
APPROVAL_NOT_FOUND
APPROVAL_ALREADY_RESOLVED
APPROVAL_EXPIRED
APPROVAL_ACTION_NOT_ALLOWED
APPROVAL_SUBJECT_CHANGED
```

### 35.6 Asset

使用 Asset Contract 中：

```text
ASSET_TYPE_NOT_SUPPORTED
ASSET_MIME_MISMATCH
ASSET_FILE_TOO_LARGE
ASSET_VIDEO_TOO_LONG
ASSET_SOURCE_EXPIRED
ASSET_SOURCE_DELETED
ASSET_SET_NOT_SEALED
ASSET_SET_INCOMPLETE
ASSET_SET_VERSION_CONFLICT
DELETE_PREVIEW_EXPIRED
```

### 35.7 Stream

```text
EVENT_CURSOR_INVALID
EVENT_CURSOR_AHEAD
EVENT_CURSOR_TOO_OLD
EVENT_STREAM_PROJECT_MISMATCH
EVENT_SCHEMA_UNSUPPORTED
```

## 36. API Tests

### 36.1 Contract

- 所有 Response 符合 Schema；
- JSON 字段统一 `snake_case`；
- 时间统一 UTC RFC3339Nano；
- ID 不为空；
- 空数组不返回 null；
- Error 包含 Request ID；
- 未知关键 Command 字段被拒绝；
- API 不出现 `source_mode` 作为 Capability 身份。

### 36.2 Query

- GET 不产生 Event；
- GET 不启动 Run；
- GET 不调用 Provider；
- Cursor Pagination 稳定；
- 相同 Snapshot ETag 可 304；
- Project A 查询不到 Project B 资源；
- 列表不返回完整 Artifact Payload。

### 36.3 Idempotency

- 重复 Start Run 只创建一个 Run；
- 重复 Approval Resolution 只产生一个 Resolution；
- 重复 Artifact Save 只产生一个 Version；
- 同 Key 不同 Body 返回 409；
- 网络超时后同 Key 返回原结果；
- 幂等记录不会在长 Run 运行中到期。

### 36.4 Concurrency

- 两个标签页基于同一 Artifact Version 保存时，一个成功、一个冲突；
- Asset Set 旧版本修改被拒绝；
- 同一 Project 第二个写 Run 被拒绝；
- 不同 Project 可并行；
- Approval Subject 变化使旧 Resolution 请求失败。

### 36.5 Message

- 普通聊天不创建 Run；
- 零 Capability 时可聊天；
- 显式 Skill Ref 被保留；
- 输入不完整时返回 Missing Fields；
- 配置未确认不启动 Run；
- 活动写 Run 时普通聊天和只读查询仍可发送；
- 会产生冲突写入的动作由 Runtime/Guard 单独拒绝；
- `Active Run` 与客户端正在查看的 `Viewed Run` 分离；
- 其他 Project 消息不受影响。

### 36.6 Asset

- 上传不使用 Base64 JSON；
- 上传成功不启动 Run；
- Stream 中断不产生可用半文件；
- 删除先 Preview 后确认；
- 过期返回 410；
- Asset Event 出现在 Project Stream。

### 36.7 Artifact

- Full Payload 和 Scoped Change 互斥；
- 空字符串修改合法；
- 旧版本保留；
- Event 不携带完整 Payload；
- Completed Run 后 AI 修改创建 Revision Run；
- Impact Preview 绑定精确版本。

### 36.8 Approval

- Action 必须来自 Options；
- 重复 Resolve 幂等；
- 不同 Key 重复 Resolve 返回已解决冲突；
- Subject Hash 变化使 Approval 过期；
- 主要业务产物保持逐步确认。

## 37. SSE Tests

### 37.1 Bootstrap

- Snapshot 返回原子 Cursor；
- 连接 after Cursor 不漏 Event；
- Snapshot 与 SSE 并发时不会重复副作用；
- 无 Cursor 不重放无限历史。

### 37.2 顺序

- Project Seq 单调递增；
- Run Seq 单调递增；
- 相同时间戳不影响顺序；
- Event ID 字典序不影响顺序；
- 多 Task 并发完成仍有确定顺序。

### 37.3 Reconnect

- 有效 Last-Event-ID 只重放后续；
- 重复 Event 被去重；
- Cursor Ahead 触发 Reset；
- Cursor Too Old 触发 Reset；
- Gap 触发 Snapshot；
- SSE 断开不标记 Run Failed；
- Keepalive 不改变 Cursor。

### 37.4 Slow Client

- 发送队列有上限；
- 慢客户端断开不影响 Worker；
- 重连可恢复；
- 终态 Event 不因 Progress 合并丢失。

### 37.5 Service Restart

- Event Seq 不回退；
- Outbox 未发布 Event 继续发布；
- Pending Approval 恢复；
- Active Run Snapshot 恢复；
- 成功 Task 不重复；
- 前端通过 Snapshot + Cursor 恢复。

## 38. 三条链回归测试

### 38.1 小说

- 现有小说样本从上传到 Candidate 完整运行；
- 每个确认点可刷新恢复；
- Episode Batch Event 顺序正确；
- 单集编辑使用版本冲突保护；
- 原有 Prompt/Rules 行为不因 API 迁移改变。

### 38.2 非小说

- 自建非小说样本从 0 到完整剧本；
- Material Bank 到 Script Unit 每步确认；
- 运行中不允许普通聊天；
- Pause/Resume 和失败 Task Retry 正常；
- Scripts 聚合不成为编辑真源。

### 38.3 视频

- 多批上传进入同一 Asset Set；
- 单集 Task 状态进入 Project Stream；
- Seal 前不进入整剧分析；
- 缺集确认通过 Approval/Command 留痕；
- 多视频部分失败只重试失败项；
- 7 天过期 Event 不删除正式 Artifact；
- 同一 Run 保持 `video_reference_creation`；
- Brief 确认后复用共享后续流程。

## 39. 可观测性

### 39.1 HTTP

```text
http_request_total
http_request_duration_seconds
http_response_status_total
api_command_total
api_command_failed_total
api_idempotency_hit_total
api_version_conflict_total
```

### 39.2 SSE

```text
sse_connection_active
sse_connection_total
sse_reconnect_total
sse_event_sent_total
sse_event_replayed_total
sse_reset_required_total
sse_slow_client_disconnect_total
sse_delivery_lag_seconds
```

### 39.3 Runtime

```text
command_queue_depth
outbox_pending_total
outbox_publish_lag_seconds
event_seq_gap_total
snapshot_duration_seconds
snapshot_payload_bytes
```

### 39.4 标签安全

指标和日志标签不能包含：

- Project Title；
- 文件名；
- 用户内容；
- 剧本内容；
- Prompt；
- Credential。

只使用稳定低基数类型和状态。

## 40. 第一版不做

- WebSocket 双向协议；
- GraphQL；
- 前端直接调用 SDK Sidecar 或 Worker；
- 前端直接调用 Provider；
- 浏览器持有 API Key；
- 纯 Event Sourcing；
- 全部状态只靠 SSE；
- SSE Event 携带完整 Artifact；
- 每个 Capability 自建 API 前缀和事件流；
- 用户上传自定义 API Handler；
- 多人协同编辑；
- 复杂 RBAC；
- 移动端专用 API；
- 自动取消正在执行的 Provider 请求；
- Cancel 时删除已完成结果；
- 运行中并发写入同一 Project，或热替换 Worker 当前输入；
- 用未知 Last-Event-ID 重放全部历史；
- 将 Request ID 当作 Idempotency Key；
- 将 Event 时间戳当作顺序。

## 41. 实施顺序

### 41.1 P0

1. `/api/v1` 基础 Envelope；
2. 统一 Error Contract；
3. X-Request-ID；
4. Idempotency Record；
5. Command Record；
6. Project/Run Snapshot；
7. Project Event Seq；
8. Project SSE；
9. Snapshot + SSE 前端恢复；
10. `source_mode` 到 `capability_id` 的 Runtime DTO。

### 41.2 P1

1. Capability Discovery；
2. Conversation/Message v1；
3. Asset Upload Session；
4. Asset Set；
5. Artifact Version API；
6. Approval Resolution；
7. Candidate/Final Selection；
8. Export；
9. Available Actions；
10. 三条 Skills 映射。

### 41.3 P2

1. Compatibility Adapter；
2. Outbox Publisher；
3. SSE Slow Client Backpressure；
4. Event Schema Version；
5. ETag；
6. 完整 Telemetry；
7. 旧 `/api` 删除计划。

P0/P1/P2 是本协议内部实施优先级，不改变项目总阶段编号。

## 42. 完成门禁

进入 Frontend Capability UI Contract 前，本合同必须满足：

- [x] Query 与 Command 已分离；
- [x] API 版本、Envelope 和 Error 已定义；
- [x] Idempotency 与 Expected Version 已定义；
- [x] Message 与 Start Run 关系已定义；
- [x] 零 Capability 对话已定义；
- [x] Project/Run Snapshot 已定义；
- [x] Project/Run Event Seq 已定义；
- [x] SSE Bootstrap、Replay、Gap 和 Reset 已定义；
- [x] Asset、Asset Set、Artifact、Approval、Candidate、Final 和 Export API 已定义；
- [x] 三个 Skills 已映射；
- [x] 旧 API 迁移策略已定义；
- [x] 安全、限流、测试和可观测性已定义；
- [ ] 通过现有小说链和非小说链兼容实现验证；
- [ ] 通过真实几十集视频的 SSE、并发和恢复压力测试。

后两项属于实现和验收门禁，不阻止继续完成阶段 2 的前端协议设计。

## 43. QualityReview API/Event 补充

权威合同：

```text
content-semantics-and-quality-review-contract.md
```

11A 新增：

```text
GET  /api/v1/runs/{run_id}/quality-reviews/current
GET  /api/v1/quality-reviews/{quality_review_id}
POST /api/v1/quality-reviews/{quality_review_id}/actions
```

Action 必须携带 `idempotency_key`、`expected_review_status`、`input_snapshot_hash`、`action` 和 `actor_ref`。相同 Key 返回原响应；Review 状态或输入哈希变化返回冲突，不执行旧操作。

事件：

```text
quality_review.started
quality_review.batch_completed
quality_review.passed
quality_review.action_required
quality_review.override_confirmed
quality_review.superseded
quality_review.failed
```

Project/Run Snapshot 增加 `current_quality_review` 投影。Event 只用于时间线和 SSE，不替代 QualityReview 当前状态。

## 事件流运行时补充（2026-09-10，G4.89）

- Project 流保留原命名业务/SDK事件及持久游标。在一批确实发送的事件中存在非`agent.output.delta`事件时，追加一次`stream.snapshot_invalidated`通知：`{"stream_scope":"project","project_id":"...","project_event_seq":123}`。这不是新业务事件，不写事件表、不携带正文、不另设SSE id；Run过滤流和纯文本delta批次不发此通知。客户端校验作品/范围及有效序号后合并刷新当前工作台快照，无需为每个新业务事件维护前端白名单。
- Project/Run游标和重放在只读事务内重新核对实际作品工作区及用户当前成员资格。长连接在开始、每批重放及保活时重新认证原登录主体与认证方式，Cookie退出/过期不静默降级为另一种隐式登录，绑定SDK执行失效也终止。会话或读取权限失效后发`stream.access_revoked`（仅含`stream_scope`）并结束连接；不会把数据库错误统一当作权限失效。
- 浏览器收到有效Project撤权控制后关闭连接、移除作品操作并废弃已在途快照响应。流暂时失败时用只读快照恢复：有主对话/后台/工作流/返修执行时每3秒检查，无已知活动时每15秒检查，避免离线空闲页面永远发现不了另一个入口新建的任务。已连接时不运行该兜底轮询，同一轮询不重叠，不触发模型或业务写命令。
- `stream.reset_required`控制帧增加空`id:`行，清空浏览器下次重连的Last-Event-ID，避免cursor_ahead反复带回旧游标；显式URL中的`after_seq`仍由客户端管理。空ID语义依据[HTML SSE标准](https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation)。当前工作台使用不带`after_seq`的原生EventSource。
- 明确要求快照重置后，前端丢弃此前在途读取，并允许新的权威快照替换较高的本地审批/提议/返修版本；普通刷新仍保留单调版本保护。未发送草稿保留，不能再从快照核实的本地消息标作结果待确认，不自动重发或重建工作流。重置后的首次读取失败时，保留重置标记直到成功读取。
- Go只完成源码与build/vet检查，浏览器EventSource真实重连和同版Go/UI联合未运行；这些边界不作为已验收通过。
