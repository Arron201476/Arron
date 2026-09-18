# Artifact 版本冲突保护实施记录

更新时间：2026-07-13

## 目标

避免旧浏览器页面、多个标签页或慢请求覆盖 Agent 或其他人工操作刚生成的新版本。

## 已实现

- `PATCH /api/artifacts/{artifact_id}` 强制要求 `base_version >= 1`。
- 后端在 runtime 同一把锁内定位 artifact、校验状态和版本、生成新版本。
- 当前 artifact 已 superseded / invalidated，或请求版本不等于当前版本时，返回 HTTP 409。
- 409 使用标准错误码 `CONFLICT`，`details.current_artifact` 返回该类型的最新有效版本；`script_unit` 按集号定位，不会串集。
- 冲突请求不会修改 payload、不会创建新版本、不会追加事件或触发下游确认。
- 过程产物编辑器和分集剧本编辑器均提交当前 artifact 的 `version`。
- 前端收到冲突后加载最新 artifact，停止旧版编辑，并在内容区显示明确提示；用户需要检查最新内容后重新编辑。

## 用户可见规则

1. 正常保存：生成 v2、v3 等新版本，旧版本进入 superseded。
2. 保存前内容已被更新：本次修改不保存，页面加载最新版本并提示冲突。
3. 系统不自动把旧草稿重放到新版本，避免再次覆盖 Agent 的更新。

## 自动化验证

- 首次以正确 `base_version` 保存成功并生成下一版本。
- 对同一旧 artifact 再次保存返回 409。
- 冲突响应带回第一次保存产生的当前 artifact。
- 冲突后 artifact 总数不增加、事件不增加、第一次保存内容保持不变。
- 缺少 `base_version` 返回 400。
- `go test ./...` 通过。
- 前端 `npm run build` 通过。

## 回归结果

- 后端重启后 `/healthz` 返回 runtime 与 SQLite store 均正常。
- 前端 8832 正常访问。
- 作品中心桌面布局正常。
- 390px 宽度下 `scrollWidth = 390`，无横向溢出。
- 浏览器无 error / warn 日志。
