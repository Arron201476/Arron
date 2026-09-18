# 作品工作区底座实施记录

日期：2026-07-13  
对应阶段：第 9 阶段 E2E 验证与缺陷修复

## 已实现

- 新增 SQLite 作品数据库 runs/workspace.db。
- Project、Message、File 的完整内容持久化。
- Project、Message、File 使用随机安全 ID，不再从内存对象数量推导编号。
- 新增 GET /api/projects/{project_id}/messages，支持恢复完整聊天。
- 未知 project_id 的消息和上传请求返回 404，不再自动创建作品。
- health 返回作品 Store 状态。
- 旧 runtime 中的项目自动迁移到作品数据库，保留原 project_id、run 和 artifact。
- 旧项目标题从 run 的原始用户请求生成，不再全部显示“已恢复作品”。
- 前端新增作品中心：作品列表、新建作品、打开作品、切换作品。
- 顶部栏和左侧目录均可进入作品中心。
- 打开作品时统一恢复聊天、附件、run、event、artifact 和 approval。
- 切换作品前检查过程产物未保存修改。
- 异步轮询校验 project_id，旧作品响应不能写入新作品。
- 桌面和 390px 移动宽度均完成浏览器回归。

## 数据迁移说明

- 旧版从未持久化的聊天和附件无法反向恢复。
- 已有 run、event、artifact 和 approval 继续从原 runtime JSON 恢复。
- 从本版本开始，新聊天和附件会写入 SQLite。
- 没有 run 的新空作品也会在后端重启后保留。

## 自动化验证

- SQLite 关闭并重新打开后，Project、Message、File 完整恢复。
- 作品 A 的消息和附件不会出现在作品 B。
- 未知作品不能通过消息或上传接口被创建。
- 150 个随机 ID 无重复且命名空间正确。
- 后端全量 go test ./... 通过。
- 后端 go vet ./... 通过。
- 前端 npm run build 通过。

## 浏览器回归

- 作品中心可列出 6 个由旧 run 迁移的作品。
- 打开最新已完成作品后，3 集剧本、68 条运行记录和全部主产物恢复。
- 后端重启后作品数量、ID、run 关联保持不变。
- 空聊天接口固定返回 []，不再返回 null。
- 新建作品表单在填写名称后正确启用。
- 390px 宽度下作品中心无横向溢出。
- 浏览器无 error/warn 日志。

## 本批未覆盖

- run、event、artifact、approval 仍在旧 runtime JSON，尚未迁入 SQLite。
- 运行中 job 的服务重启恢复仍未实现。
- 作品删除、重命名和归档尚未提供。
- artifact base_version 并发冲突保护已实现，详见 `artifact-version-conflict-implementation-20260713.md`。
- 剧本唯一真源和正式 Lexical 编辑器尚未实现。
- 长篇上下文预算和 Eino 接入尚未开始。

## 下一批建议

按照 0-1 审计优先级，下一批处理：

1. 运行中 job 重启恢复。
2. 剧本结构的唯一真源与 Lexical 正式接入。
3. 长篇 Context Assembler 与 token 预算。
