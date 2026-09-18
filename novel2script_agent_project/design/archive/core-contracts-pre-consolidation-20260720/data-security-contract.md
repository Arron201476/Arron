# 权限、数据与安全协议

本文档定义 Novel2Script Agent 在文件、用户输入、模型上下文、日志、密钥、数据保存与删除方面的安全边界。它是接入真实模型、文件上传和持久化存储前必须具备的开发合同。

## 设计原则

1. 用户输入、上传文件、生成产物都属于项目数据。
2. 文件上传不等于自动进入模型上下文。
3. 日志默认不记录完整原文、完整 prompt、完整模型输出。
4. API key、模型 endpoint、provider 配置不进入前端包和普通日志。
5. 所有数据进入模型前必须有可追踪的 source_ref。
6. 删除 project 时必须能删除关联 run、artifact、message、file metadata。
7. 第一版先按单用户本地项目设计，但字段和边界要能扩展到多用户。

## 数据分类

| 分类 | 示例 | 敏感级别 | 默认处理 |
|---|---|---|---|
| 用户文本输入 | 小说原文、灵感、梗概、补充说明 | 高 | 保存为 project 数据，日志脱敏 |
| 上传文件 | txt、docx、pdf、xlsx | 高 | 保存 file_ref，正文解析后受控进入上下文 |
| 中间产物 | story_bible、material_bank、episode_cards | 中 | 保存 artifact，允许前端展示 |
| 最终产物 | scripts | 中 | 保存 artifact，允许导出或编辑 |
| 对话消息 | user / agent message | 中 | 保存 message，不记录敏感 debug payload |
| 运行事件 | run_event | 低到中 | 记录事件摘要，技术 payload 可裁剪 |
| 模型请求 | prompt、上下文、工具参数 | 高 | 默认不落完整日志 |
| 模型响应 | 原始 LLM output | 高 | 默认不落完整日志，必要时保存摘要 |
| 密钥配置 | API key、provider token | 最高 | 只在后端环境变量或密钥系统中存在 |

## 文件上传安全

### FileRef

文件上传后先形成 `FileRef`：

```json
{
  "file_id": "file_xxx",
  "project_id": "project_xxx",
  "filename": "测试用书.txt",
  "mime_type": "text/plain",
  "size_bytes": 1024,
  "status": "uploaded | parsed | failed | deleted",
  "storage_path": "internal_only",
  "text_preview": "",
  "created_at": ""
}
```

`storage_path` 不返回前端，只保存在后端。

### 上传限制

第一版建议：

```text
txt / md：支持
docx：支持解析后文本
pdf：可支持，但需要解析失败提示
xlsx / csv：后续支持
图片 / 视频：暂不作为剧本生成输入
```

文件大小第一版：

```text
单文件默认上限：20MB
单项目文件总量默认上限：100MB
```

超限返回：

```text
FILE_TOO_LARGE
FILE_TYPE_UNSUPPORTED
FILE_PARSE_FAILED
```

### 文件进入模型上下文

文件不会因为上传自动进入模型上下文。必须满足至少一个条件：

1. 用户发送 message 时引用该 `file_id`。
2. Agent 判断当前 run 需要该文件，并写入 `script_context.source_material` 或当前步骤的输入引用。
3. 用户明确要求使用该文件。

进入模型上下文时只传解析文本或必要片段，不传 storage_path。

## 模型上下文安全

### source_ref

所有进入 prompt 的用户材料必须保留来源：

```json
{
  "source_ref_id": "source_ref_xxx",
  "type": "message | file | artifact | selection",
  "id": "message_xxx",
  "span": {
    "start": 0,
    "end": 120
  }
}
```

用途：

- 追踪模型使用了哪些用户材料。
- 局部修改时定位上下文。
- 出错时能判断是 prompt 问题、artifact 问题还是输入问题。

### 上下文裁剪

进入模型前必须裁剪：

- 不传无关历史消息。
- 不传所有 artifact 全量内容。
- 不传已失效 artifact，除非明确用于对比。
- 不传文件全文，除非该步骤确实需要全文理解。

### 禁止进入模型上下文

默认禁止：

- API key。
- provider endpoint。
- 本地 storage_path。
- 后端内部错误堆栈。
- 用户本地绝对路径。
- 与当前 project 无关的其他项目数据。

## 日志策略

### 可以记录

```text
project_id
run_id
step_id
event_type
artifact_type
artifact_id
model_name
token usage 摘要
错误码
耗时
```

### 默认不记录

```text
完整小说原文
完整上传文件正文
完整 prompt
完整 LLM raw output
完整剧本正文
API key
用户本地文件路径
```

### Debug 模式

如果本地开发需要记录完整 prompt / output：

- 必须显式开启 debug。
- debug 日志只写本地开发目录。
- 不进入普通 run_event。
- 不能默认上传或提交。
- 日志中必须遮蔽 API key。

## API Key 与模型配置

### 存放位置

第一版：

```text
backend/.env
```

未来生产：

```text
server secret manager / environment variables
```

### 前端限制

前端不能接触：

- API key。
- provider token。
- 完整模型 endpoint 配置。

前端只能看到：

```json
{
  "provider_status": "configured | mock | error",
  "control_model": "configured",
  "generation_model": "configured"
}
```

不返回真实 key 和完整敏感 endpoint。

## 数据保存策略

第一版本地开发：

```text
runs/
artifacts/
uploads/
```

建议结构：

```text
projects/{project_id}/
  project.json
  messages.jsonl
  runs/{run_id}.json
  events/{run_id}.jsonl
  artifacts/{artifact_id}_v{version}.json
  files/{file_id}/metadata.json
  files/{file_id}/content
```

开发阶段可以使用本地文件系统；后续生产再替换为数据库 / 对象存储。

## 删除策略

### 删除 project

必须删除或标记删除：

- project metadata
- messages
- runs
- run_events
- artifacts
- file metadata
- uploaded file content

删除后：

- API 返回 `PROJECT_NOT_FOUND` 或 `deleted` 状态。
- 不能继续读取 artifact。
- 不能继续 resume run。

### 删除 file

删除 file 后：

- FileRef.status = `deleted`。
- storage content 删除。
- 已生成 artifact 可保留，但 source_ref 标记文件已删除。
- 不能再把该文件送入模型上下文。

## 权限模型

第一版本地单用户：

```text
无登录
所有 project 属于本地用户
```

但对象需预留：

```json
{
  "owner_id": "",
  "created_by": ""
}
```

未来多用户需要补：

- 项目 owner。
- 项目成员。
- 读写权限。
- 文件访问权限。
- 生成任务操作权限。

## 前端安全规则

前端不能：

- 显示 API key。
- 显示本地 storage_path。
- 把文件名插入 textarea 当正文。
- 在错误卡里展示 raw stack trace。
- 在运行记录默认展示完整 prompt。

前端应该：

- 文件显示为 attachment chip。
- 错误显示人话和恢复动作。
- 运行记录提供技术详情折叠，但默认只展示摘要。
- 对删除、覆盖、重生成等高影响动作请求确认。

## 后端安全规则

后端必须：

- 校验 file_id 是否属于当前 project。
- 校验 artifact_id 是否属于当前 project。
- 校验 run_id 是否属于当前 project。
- 拒绝跨 project 读取数据。
- 上传文件做大小和类型限制。
- 写日志时过滤 key 和大段正文。
- 模型调用失败时返回错误码，不返回 provider 原始敏感报错。

## 必测用例

### SEC-001 文件名不进入 textarea

上传文件后：

- textarea 保持用户输入。
- 文件以 chip 显示。

### SEC-002 文件不自动进入模型上下文

只上传不发送：

- 不创建 source_input。
- 不调用模型。

### SEC-003 删除 file 后不可再引用

删除 file 后发送引用该 file_id 的 message：

- 返回 `FILE_NOT_FOUND` 或等价错误。

### SEC-004 日志不包含 API key

触发一次模型调用失败：

- 日志和 run_event 中不出现 key。

### SEC-005 跨 project 访问被拒绝

用 project A 请求 project B 的 artifact：

- 返回 `NOT_FOUND` 或 `FORBIDDEN`。

### SEC-006 raw prompt 默认不展示

打开运行记录：

- 默认只展示事件摘要。
- prompt 详情需要 debug 权限或本地显式开关。

## 未决问题

1. 生产环境是否需要账号系统。
2. 上传文件是否需要病毒扫描。
3. 是否允许用户导出完整 run 日志。
4. 是否需要项目级加密。
5. 是否需要模型 provider 数据不留存设置。

## 本地发布补充（2026-07-17）

- 正式包不复制 `.env`，首次启动由用户在包内单独配置。
- 数据备份只包含 `workspace.db`、`runtime.db` 及必要 WAL 文件，不包含 API Key 和模型 trace。
- 恢复前必须停止应用，并自动保存一份恢复前备份。
- 正式停止接口需要启动器生成的随机本地 token；无 token 时按不存在处理。
- 数据库版本高于当前程序支持版本时拒绝启动，不能用旧程序写入新格式数据。
