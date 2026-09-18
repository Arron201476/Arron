# Local File Attachments

## 当前实现

前端 Agent 输入区新增“添加文件”入口，行为接近本地 add files：

- 点击“添加文件”打开系统文件选择器。
- 选中文件后，文件会显示在输入框上方的附件列表中。
- 发送消息时，附件随同本次 Agent 消息一起发给后端。
- 后端把附件正文合并进本次 source input，生成模型可以消费文件内容。

## 支持范围

当前支持：

- 文本类：`.txt`、`.md`、`.json`、`.csv`、`.log`、`.html`、`.xml`、`.yaml`、`.yml`、`text/*`
- Word 文档：`.docx`

当前限制：

- 单文件最大 5MB。
- `.docx` 使用 Go 标准库解压并抽取 `word/document.xml` 文本。
- 暂不支持 PDF、图片 OCR、复杂 Word 批注/样式保真。

## 接口方式

正式接口以 `api-event-contract.md` 为准：

1. 前端先调用 `POST /api/projects/{project_id}/files` 上传文件。
2. 后端返回 `FileRef`，包括 `file_id`、解析状态、文件名、类型、大小和文本预览。
3. 用户发送 Agent 消息时，在 `POST /api/projects/{project_id}/messages` 中携带 `file_ids`。
4. Main Agent 决定这些文件是否进入本次模型上下文。

规则：

- 文件名、解析状态和移除入口显示为 `AttachmentChip`。
- 文件内容不直接塞进 textarea。
- 上传文件不等于启动 run。
- 上传文件不等于自动进入模型上下文。
- 旧的 message 内联附件方式只允许作为兼容适配层，不作为新前端实现目标。
