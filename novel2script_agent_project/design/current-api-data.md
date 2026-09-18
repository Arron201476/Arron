# 当前 API 与数据规范

更新时间：2026-07-20

## 正式 HTTP API

```text
GET    /healthz
GET    /api/projects
POST   /api/projects
GET    /api/projects/{project_id}
GET    /api/projects/{project_id}/messages
POST   /api/projects/{project_id}/messages
GET    /api/projects/{project_id}/files
POST   /api/projects/{project_id}/files
DELETE /api/files/{file_id}
PATCH  /api/artifacts/{artifact_id}
POST   /api/approvals/{approval_request_id}/resolve
GET    /api/runs/{run_id}
POST   /api/runs/{run_id}/pause
POST   /api/runs/{run_id}/resume
POST   /api/runs/{run_id}/steps/{step_id}/rerun
GET    /api/runs/{run_id}/events
GET    /api/runs/{run_id}/events/stream
GET    /api/runs/{run_id}/artifacts
POST   /api/system/shutdown
```

旧的 `/api/agent/messages`、直接创建 Run 和绕过作品消息入口的接口不是正式 API，并由路由测试明确拒绝。

事件流使用 SSE；前端同时保留快照刷新兜底。请求体统一限制为 16 MB。

## 核心状态

Run：`pending`、`running`、`waiting_approval`、`paused`、`completed`、`failed`、`cancelled`。

Step：`pending`、`running`、`waiting_approval`、`completed`、`failed`、`skipped`。

Artifact：`draft`、`pending_approval`、`confirmed`、`stale`、`superseded`、`invalidated`。

事件：Run 启动、步骤启动、Artifact 创建/更新、剧本批次写入、审批请求/解决、进度更新、步骤完成/失败、Run 暂停/恢复/完成。

## Artifact 合同

所有 Artifact 必须包含 `artifact_id`、`artifact_type`、`project_id`、`run_id`、`version`、`status`、`source_mode`、`derived_from`、`payload` 和时间戳。

`payload` 随类型变化；字段不得因为前端不认识而丢失。前端使用中文映射展示已知字段，对未知字段使用稳定的可读回退标签。

`script_unit` 是单集剧本唯一可编辑真源；`scripts` 只由最新有效的 `script_unit` 聚合刷新。

## 持久化

作品、消息、附件与运行态分别存入本地 SQLite。数据库启用 WAL、`busy_timeout=5000`、`synchronous=FULL` 和版本号保护；高于当前程序支持版本的数据库会被拒绝，避免新数据被旧程序损坏。

服务启动时恢复作品、Run、Artifact、事件和待处理审批，并校正中断状态。运行态保存失败必须返回错误，不能对用户宣称修改成功。
