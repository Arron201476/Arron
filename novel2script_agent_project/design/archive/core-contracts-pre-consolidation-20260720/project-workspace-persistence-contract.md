# 作品工作区与持久化合同

版本：2026-07-13  
状态：当前实施合同

## 1. 产品定义

一个 `Project` 对应一个独立作品工作区。用户只能在进入某个作品后查看或操作该作品的数据。

每个作品独立拥有：

- 作品名称和来源模式。
- 完整聊天记录。
- 上传材料和正文内容。
- 生成配置。
- run、task、event 和 approval。
- artifact 及其全部版本。
- 剧本正文和编辑状态。

任何作品都不能读取、引用或修改另一作品的数据。

## 2. 用户入口

应用启动后先进入作品入口，而不是静默选择最近作品。

作品入口至少支持：

- 查看作品列表。
- 新建作品。
- 打开作品。
- 从工作台切换作品。
- 显示作品状态、最近更新时间和来源类型。

删除和重命名属于后续能力；在实现完整级联删除前，不提供伪删除按钮。

## 3. 恢复规则

- 浏览器刷新：恢复当前作品、完整聊天、附件、run、artifact 和待确认状态。
- 后端重启：恢复结果必须与重启前一致。
- 没有 run 的空作品也必须保留。
- 运行中的任务若无法恢复执行，必须转为可重试失败，不能永久保持 running。
- 最近打开作品 ID 可以保存在浏览器，但后端是作品是否存在的唯一事实来源。

## 4. 数据隔离

- 所有 Message、File、Run、Artifact 和 Approval 必须关联 `project_id`，或可经 run 唯一反查到 project。
- 项目级 API 必须先验证 Project 存在。
- `file_id`、`run_id`、`artifact_id` 必须验证属于当前 Project。
- 任意未知 `project_id` 返回 404，不能由消息或上传接口自动创建。
- 只有 `POST /api/projects` 可以创建作品。

## 5. ID 规则

- ID 必须跨重启保持唯一。
- 不能从“当前内存对象数量”推导下一个 ID。
- Project、Message、File 使用独立 ID 域，或使用全局 UUID/ULID。
- 从旧运行态恢复数据时不得重命名已有 `project_id`。

## 6. API 补充

```text
GET  /api/projects
POST /api/projects
GET  /api/projects/{project_id}
GET  /api/projects/{project_id}/messages
GET  /api/projects/{project_id}/files
POST /api/projects/{project_id}/files
POST /api/projects/{project_id}/messages
```

`GET /api/projects/{project_id}/messages` 按创建时间返回完整对话。第一版本地应用可返回全量；正式大数据版本增加 cursor 分页。

## 7. 前端状态边界

- 切换作品前，必须处理当前过程产物或剧本的未保存修改。
- 打开作品时一次清空旧作品的 message、file、run、event、artifact、approval 和 selection，再加载新作品。
- 不允许旧作品的轮询结果写入新作品界面。
- 每个异步刷新都校验请求发起时的 `project_id/run_id` 仍是当前值。

## 8. 持久化最低要求

- 写入必须原子化，失败必须返回错误，不能向前端报告成功。
- 存储必须有 schema version。
- 文件正文和聊天内容不能只保存在内存。
- 服务启动时必须验证存储可读写，并在 health 中暴露持久化状态。
- 旧 runtime JSON 在迁移完成前继续兼容读取，但新作品元数据不得继续依赖 run 反向拼装。

## 9. 本批验收

1. 新建两个作品，分别上传材料并聊天，数据互不出现。
2. 刷新浏览器，当前作品完整恢复。
3. 切换作品，聊天、附件、run、artifact 和确认卡全部切换。
4. 重启后端，两个作品及完整数据仍存在。
5. 空作品在重启后仍存在。
6. 新建作品 ID 不与任何旧作品冲突。
7. 使用作品 A 请求作品 B 的文件时返回错误。
8. 不存在的 project ID 不会被自动创建。
