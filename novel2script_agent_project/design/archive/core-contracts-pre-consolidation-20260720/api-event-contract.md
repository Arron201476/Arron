# API / 事件协议

状态：Current。本文档只描述当前 Go 服务公开且经过路由测试的接口。未列出的旧接口不属于产品合同。

## 基本原则

1. 后端是 project、message、file、run、artifact、approval 和 event 的唯一事实来源。
2. 用户自然语言统一进入作品消息接口，由 Main Agent 决定回复、生成、确认、暂停、恢复、修改、读取附件或失败重跑。
3. 上传只保存文件；是否读取文件由当前请求的 `file_ids` 或 Main Agent 的 `target_file_ids` 决定。
4. 启动生成前必须确认目标集数和单集时长。
5. 长任务异步执行，前端用 run 快照和 SSE 同步。
6. 所有修改使用稳定 ID 和版本号，不按标题猜目标。

## 当前公开接口

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/healthz` | 服务、runtime、SQLite 健康状态 |
| GET | `/api/projects` | 作品列表 |
| POST | `/api/projects` | 新建作品 |
| GET | `/api/projects/{project_id}` | 作品状态 |
| GET | `/api/projects/{project_id}/messages` | 持久化聊天记录 |
| POST | `/api/projects/{project_id}/messages` | Main Agent 唯一消息入口 |
| GET | `/api/projects/{project_id}/files` | 作品附件列表 |
| POST | `/api/projects/{project_id}/files` | 上传附件 |
| DELETE | `/api/files/{file_id}` | 删除附件 |
| PATCH | `/api/artifacts/{artifact_id}` | 按 `base_version` 手动保存产物新版本 |
| POST | `/api/approvals/{approval_request_id}/resolve` | 确认、暂停、保留下游或重生成下游 |
| GET | `/api/runs/{run_id}` | run、events、artifacts、approval 快照 |
| POST | `/api/runs/{run_id}/pause` | 暂停运行中的 run |
| POST | `/api/runs/{run_id}/resume` | 恢复用户主动暂停且无待确认点的 run |
| POST | `/api/runs/{run_id}/steps/{step_id}/rerun` | 只重跑当前失败 step/task |
| GET | `/api/runs/{run_id}/events` | 完整事件列表 |
| GET | `/api/runs/{run_id}/events/stream` | SSE 事件流 |
| GET | `/api/runs/{run_id}/artifacts` | run 的全部产物版本 |

以下旧路径明确不公开：`POST /api/agent/messages`、`POST /api/runs`、`POST /api/runs/{id}/continue`、`GET /api/files/{id}`。当前也没有 cancel、整 run retry、script suggestion、comment 或独立 script-document 接口。

## 作品消息

请求：

```json
{
  "content": "根据这个附件生成短剧",
  "display_content": "确认生成配置：2 集，每集 1.5 分钟",
  "source_mode_hint": "auto | novel | non_novel",
  "file_ids": ["file_xxx"],
  "generation_config": {
    "target_episode_count": 2,
    "episode_duration_minutes": 1.5,
    "target_script_chars": 500
  },
  "selection_context": null,
  "client_context": {
    "current_artifact_id": "artifact_xxx",
    "current_view": "story_bible"
  }
}
```

响应：

```json
{
  "user_message": {},
  "agent_message": {},
  "decision": {},
  "project": {},
  "run": null,
  "approval_request": null,
  "events": [],
  "artifacts": []
}
```

语义：

- 闲聊、追问、状态解释：`run = null`，不创建产物。
- 缺生成配置：返回决策和聊天消息，不创建 run；聊天记录持久化配置卡恢复信息和附件 ID。
- 明确且配置完整的生成：创建 run。
- 局部修改：在当前 run 中创建目标产物新版本；无关字段与下游现有内容保持不变。
- 读取附件：只读取本次明确选择的附件；多个历史附件不明确时先追问。

## Artifact 版本

```json
{
  "artifact_id": "artifact_xxx",
  "artifact_type": "story_bible",
  "project_id": "project_xxx",
  "run_id": "run_xxx",
  "version": 2,
  "status": "draft | pending_approval | confirmed | stale | superseded | invalidated",
  "source_mode": "novel | non_novel",
  "derived_from": [],
  "payload": {},
  "created_at": "",
  "updated_at": ""
}
```

手动保存必须传当前 `base_version`。版本冲突返回 HTTP 409 和最新 artifact；结构或集数合同不合法返回 `VALIDATION_FAILED`。旧版本保留并标记 `superseded`，新版本号递增。

上游修改后，已存在下游先标记 `stale` 并继续展示；用户选择“保留”后恢复状态，选择“重新生成”后才将受影响下游标记 `invalidated` 并覆盖生成。`run.invalidated_artifacts` 与 artifact 状态同步。

## Approval

所有主要用户可见产物都需要确认：

- 小说：`story_bible`、`episode_split`、`episode_cards`、每个 `script_unit`。
- 非小说：`material_bank`、`story_seed`、`series_blueprint`、`episode_cards`、每个 `script_unit`。
- `script_context` 是内部产物，不展示确认卡。
- `scripts` 是最终聚合产物，不单独确认。

`resolve` 支持当前前端使用的动作：`approve`、`pause`、`keep_downstream`、`regenerate_downstream`。

## Run 与失败恢复

Run 状态：`pending | running | waiting_approval | paused | completed | failed | cancelled`。

- 运行中暂停：保留已经完成的 task 和 artifact cursor。
- 恢复：只能恢复用户主动暂停且没有 pending approval 的 run。
- 失败重跑：接口只接受 runtime 记录的失败 `step_id`，从失败 task cursor 继续剩余任务；不能重跑成功或未失败步骤。

## 当前事件类型

```text
run_started
step_started
artifact_created
artifact_updated
script_batch_inserted
approval_requested
approval_resolved
step_completed
run_completed
step_failed
run_paused
run_resumed
```

事件按 `event_id` 去重并按 `created_at`、`event_id` 恢复顺序。SSE 使用事件 ID；客户端传入未知 `Last-Event-ID` 时服务端重放全部事件，由前端去重。

## 错误规则

- 400：请求、目标、schema 或失败 step 不合法。
- 404：对象不存在。
- 409：版本冲突、状态不允许或确认动作冲突。
- 503：模型、runtime 持久化或 SQLite 不可用。
- GET 可对网络错误和 5xx 做有限重试；404 等确定性错误不重试。
- workspace SQLite 短暂繁忙时返回结构化错误码 `WORKSPACE_BUSY`，不得把 `SQLITE_BUSY`、数据库路径或原始驱动错误展示给用户。
- `WORKSPACE_BUSY` 表示请求结果需要重新同步确认，前端应刷新当前 project/run/artifact；不能断言“本次没有执行”，也不能自动重复提交可能已经启动的修改。
