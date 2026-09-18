# Asset and Retention Contract

状态：Stage 3 第 10 步实施基线  
版本：v0.2  
适用范围：内容生产 Agent 第一版  
上游文档：

- `stage-1-product-definition.md`
- `stage-2-architecture-baseline.md`
- `runtime-domain-model.md`
- `artifact-dependency-contract.md`
- `capability-context-pack-contract.md`

## 1. 文档目标

本合同定义通用输入材料从进入系统到删除的完整业务语义：

1. 文本、文档、图片和视频如何上传；
2. 上传成功、文件可用、解析成功和 Capability 可执行如何区分；
3. 文件元数据与二进制内容如何分离保存；
4. 原文件、处理副本、通用解析结果和业务 Artifact 如何区分；
5. 同一部参考剧分批上传时如何归组、排序和明确结束上传；
6. 视频原文件如何在 7 天后自动删除；
7. 用户主动删除文件时如何计算影响、确认和保留来源关系；
8. 文件删除后已有剧本、分析、Brief 和历史版本如何继续存在；
9. Runtime、Generic Tool、Capability、Provider Adapter 和前端分别负责什么；
10. 第一版如何配置格式、大小、数量、时长和并发限制。

本文是业务和数据协议，不定义具体页面视觉样式，也不定义模型 Prompt。

## 2. 核心结论

### 2.1 上传属于 Agent Shell

上传文本、文档、图片和视频是 General Content Agent Shell 的通用能力。

上传本身：

- 不代表用户已经选择 Domain Capability；
- 不自动创建生成 Run；
- 不自动开始小说转剧本、非小说转剧本或视频参考创作；
- 不代表模型已经读取文件；
- 不代表文件已经成功解析。

只有当用户明确调用 Skill，或 Agent 已完成意图识别并取得必要确认后，Runtime 才启动对应 Run。

### 2.2 Asset 不是 Artifact

```text
Asset
= 用户提供的来源材料

Artifact
= Capability 执行后产生的业务产物
```

例如：

| 对象 | 类型 |
|---|---|
| 用户上传的小说 DOCX | Asset |
| 从 DOCX 提取的纯文本缓存 | Asset Parse Result |
| 用户上传的一集 MP4 | Asset |
| 为模型生成的压缩视频 | Asset Variant |
| 从视频生成的单集高还原剧本 | Artifact Version |
| 整剧剧本分析 | Artifact Version |
| `adaptation_brief` | Artifact Version |

不得用 Asset 表模拟业务产物，也不得把视频剧本当作文件解析缓存。

### 2.3 二进制与业务元数据分离

第一版使用：

```text
SQLite
-> Asset、Blob、Variant、Parse Result、Asset Set、Retention Job 元数据

受控文件存储
-> 原文件、处理副本、解析缓存和临时导出文件
```

禁止：

- 把文件 Base64 写入 JSON；
- 把视频、图片或文档写入 SQLite BLOB；
- 把客户端原始路径保存为服务端可访问路径；
- 让浏览器直接提交任意 `storage_ref`；
- 把真实服务端绝对路径返回前端。

### 2.4 原文件不可被处理副本覆盖

压缩、转码、OCR、文本提取和 Provider 适配只能创建派生对象。

```text
Asset
├─ Original Blob
├─ Variant Blob
│  ├─ provider_upload_copy
│  ├─ compressed_video
│  ├─ normalized_document
│  └─ preview_image
└─ Parse Result
```

用户上传的原文件必须保持原始字节和原始校验值，直到其保留期届满或用户确认删除。

### 2.5 视频 7 天保留

视频原文件和由视频派生的处理副本自上传完成后保存 168 小时。

到期后：

- 删除原视频和视频处理副本；
- Asset 元数据变为 `expired`；
- 保留文件名、大小、时长、校验值、集号、来源状态和删除时间；
- 保留已经生成的 `video_script_unit`、`reference_scripts`、`script_analysis`、`adaptation_brief` 和新剧本；
- 保留 Artifact Dependency；
- 禁止再次基于已过期源视频解析或重新生成依赖画面的结果。

访问、查看、下载或模型调用不会自动续期。

### 2.6 文件删除不反向删除正式产物

用户确认删除已被引用的 Asset 后：

- 原文件和处理副本进入删除任务；
- 通用解析缓存按本合同删除；
- 已经保存为正式 Artifact Version 的业务产物继续保留；
- Dependency 中的来源节点继续保留为 Tombstone；
- 前端明确显示“源文件已删除”或“原视频已过期”；
- 不允许把来源缺失伪装成来源仍可访问。

### 2.7 限制配置化

格式、文件大小、视频时长、单批数量、单作品数量、并发数和 Provider 处理规格必须配置化。

第一版暂定默认值不是模型永久能力声明：

| 配置 | 第一版暂定值 |
|---|---:|
| 单视频时长 | 3 分钟 |
| 单视频大小 | 50 MB |
| 单批视频数量 | 20 |
| 单作品视频数量 | 100 |
| 视频解析并发 | 1（真实网关验收后的部署默认值，可配置） |
| 视频处理副本目标 | 8 MiB（硬边界 10 MiB，可配置） |
| 视频原文件保留期 | 168 小时 |

接入正式模型网关后，运行时有效限制取以下约束的最小值：

```text
产品配置
Provider Adapter 能力
公司网关限制
当前部署负载保护
```

## 3. 责任边界

### 3.1 Agent Shell

负责：

- 接收通用上传意图；
- 识别当前 Project；
- 展示可接受文件类型和当前限制；
- 在意图不明确时追问文件用途；
- 让用户查看、重命名和删除材料；
- 将显式选择的 Asset ID 传给 Runtime；
- 在删除、过期或解析失败时说明能力缺口。

不负责：

- 直接写文件系统；
- 根据文件名决定业务真相；
- 绕过 Runtime 启动模型调用；
- 自行延长视频保留期；
- 将上传等同于启动 Skill。

### 3.2 Asset Service

负责：

- 创建上传事务；
- 流式接收文件；
- 校验扩展名、MIME、签名、大小和校验值；
- 原子写入受控存储；
- 维护 Asset、Blob、Variant 和 Parse Result；
- 生成受控下载或读取句柄；
- 执行逻辑删除；
- 创建物理删除任务；
- 执行保留策略。

Asset Service 是文件状态权威。

### 3.3 Generic Tool

负责通用解析或理解：

- TXT/MD 文本读取；
- DOCX/PDF 文本提取；
- 图片元数据读取；
- 按用户意图进行通用图片理解；
- 视频容器元数据读取；
- 文件名集号候选提取。

Generic Tool 不生成领域 Artifact，不决定视频改编链路。

### 3.4 Capability

负责解释 Asset 的领域用途：

- `novel_to_script` 选择小说 Asset；
- `non_novel_to_script` 选择非小说 Asset；
- `video_reference_creation` 选择视频 Asset Set；
- 为模型请求创建必要的 Provider Variant；
- 将模型结果保存为 Artifact Version；
- 按 Step/Task 显示进度和失败；
- 使用 Artifact Dependency 记录来源。

Capability 不能直接删除文件，也不能修改 Asset 的保留期限。

### 3.5 Runtime

负责：

- 将 Asset Snapshot 固定进 Run Input Snapshot；
- 检查 Project 归属；
- 检查 Asset 状态；
- 管理 Run、Step、Task 和 Approval；
- 计算删除影响；
- 保存来源 Dependency；
- 阻止已删除或过期来源进入新执行；
- 管理 Asset Set Seal 与 Run 的关系；
- 发布业务 Event。

### 3.6 Provider Adapter

负责：

- 声明 Provider 接受的 MIME、大小、时长和传输方式；
- 根据需要创建处理副本；
- 通过公司内网网关发送请求；
- 返回 Provider Request ID；
- 记录实际使用的 Asset Variant；
- 报告 Provider 侧限制错误；
- 遵循公司批准的远端数据保留策略。

浏览器不能直接持有 Provider Key，也不能直接调用 Provider。

### 3.7 Retention Worker

负责：

- 扫描到期对象；
- 领取幂等删除任务；
- 删除物理 Blob；
- 更新 Blob 和 Asset 状态；
- 重试暂时失败；
- 记录永久失败；
- 发布审计 Event；
- 不删除正式 Artifact。

## 4. 对象关系

```text
Project
├─ Asset
│  ├─ Asset Blob (original)
│  ├─ Asset Variant
│  │  └─ Asset Blob (derived)
│  ├─ Asset Parse Result
│  └─ Retention Job
├─ Upload Session
│  └─ Upload Item
├─ Asset Set
│  ├─ Asset Set Version
│  └─ Asset Set Member
└─ Run
   └─ Input Snapshot
      └─ Asset Snapshot / Asset Set Snapshot
```

`Asset Set` 是一次业务输入选择的通用集合，不等于新 Skill，也不等于“主参考剧”。

第一版主要用于：

- 视频参考剧分批上传；
- 一次 Run 选择多份非小说材料；
- 固定用户确认过的输入顺序。

## 5. Asset

### 5.1 Canonical Schema

```json
{
  "asset_id": "ast_...",
  "project_id": "prj_...",
  "kind": "video",
  "source_type": "upload",
  "display_name": "穷剑修第01集.mov",
  "original_filename": "穷剑修第01集.mov",
  "extension": "mov",
  "declared_mime_type": "video/quicktime",
  "detected_mime_type": "video/quicktime",
  "size_bytes": 41943040,
  "checksum_algorithm": "sha256",
  "checksum": "",
  "status": "available",
  "parse_status": "ready",
  "original_blob_id": "blb_...",
  "metadata": {
    "duration_ms": 87450,
    "width": 1080,
    "height": 1920,
    "page_count": null,
    "character_count": null
  },
  "retention_policy_id": "video_source_7d_v1",
  "uploaded_at": "",
  "expires_at": "",
  "deleted_at": null,
  "delete_reason": null,
  "created_at": "",
  "updated_at": ""
}
```

### 5.2 字段规则

| 字段 | 规则 |
|---|---|
| `asset_id` | 服务端生成，不从文件名派生 |
| `project_id` | 必填，创建后不可迁移 |
| `kind` | `text/document/image/video` |
| `source_type` | 第一版为 `upload/paste` |
| `display_name` | 用户可修改，不影响原文件名 |
| `original_filename` | 上传时固定，只做展示和审计 |
| `extension` | 规范化为小写，不作为唯一类型依据 |
| `detected_mime_type` | 服务端检测结果 |
| `size_bytes` | 上传完成后的实际字节数 |
| `checksum` | 原始字节 SHA-256 |
| `status` | 文件总体可用状态 |
| `parse_status` | 通用解析状态 |
| `original_blob_id` | 指向物理原文件 |
| `metadata` | 只放跨能力通用字段 |
| `retention_policy_id` | 创建时固定的策略版本 |
| `expires_at` | 可为空；视频必填 |

集号、人工排序和集合状态不写进 Asset 顶层，而写进 Asset Set Member。

原因：

- 同一文件在不同输入集合中可能有不同顺序；
- 文件名识别结果只是候选；
- Run 需要固定集合版本，而不是读取可变 Asset 元数据。

### 5.3 Asset Kind

第一版：

```text
text
document
image
video
```

独立音频不支持。

### 5.4 Asset Status

沿用 Runtime 状态：

```text
uploading
available
processing
failed
expired
deleted
```

语义：

| 状态 | 含义 |
|---|---|
| `uploading` | 上传事务尚未完成，不能用于 Run |
| `available` | 原文件可读取 |
| `processing` | 正在执行文件级处理；原文件可按具体操作判断是否读取 |
| `failed` | 文件写入或完整性校验失败 |
| `expired` | 自动保留期届满，源 Blob 不再可用 |
| `deleted` | 用户或 Project 删除导致逻辑不可用 |

`failed` 不表示模型解析失败。模型执行失败属于 Task/Attempt。

通用 Parse 失败只更新 `parse_status=failed`，Asset 原文件仍保持 `available`。只有上传完整性、物理文件写入或文件级处理导致来源不可用时，Asset 才进入 `failed`。

### 5.5 Parse Status

```text
not_required
pending
processing
ready
failed
```

上传成功不等于 `parse_status=ready`。

### 5.6 状态机

```text
uploading
├─ available
└─ failed

available
├─ processing
├─ expired
└─ deleted

processing
├─ available
├─ failed
├─ expired
└─ deleted

failed
└─ deleted

expired
└─ deleted

deleted
└─ terminal
```

视频到期时，即使某个模型 Task 仍在运行，也不得继续创建新的源读取。

已经取得的 Provider Request 可以等待返回，但返回结果必须记录其请求发生在过期前。

## 6. Asset Blob

### 6.1 Schema

```json
{
  "blob_id": "blb_...",
  "project_id": "prj_...",
  "asset_id": "ast_...",
  "blob_role": "original",
  "storage_backend": "local",
  "storage_ref": "projects/prj_.../assets/ast_.../blobs/blb_...",
  "size_bytes": 41943040,
  "checksum_algorithm": "sha256",
  "checksum": "",
  "content_type": "video/quicktime",
  "status": "available",
  "created_at": "",
  "expires_at": "",
  "delete_requested_at": null,
  "deleted_at": null,
  "delete_failure": null
}
```

### 6.2 Blob Role

```text
original
variant
parse_payload
temporary_export
staging
```

### 6.3 Blob Status

```text
staging
available
delete_pending
deleted
delete_failed
rejected
```

Asset 的逻辑状态和 Blob 的物理状态必须分开。

用户确认删除后：

1. Asset 立即进入 `deleted`；
2. API 不再提供读取；
3. Blob 进入 `delete_pending`；
4. Worker 异步物理删除；
5. 删除失败不恢复 Asset 可见性。

### 6.4 Storage Ref

`storage_ref`：

- 是 Asset Service 生成的受控引用；
- 只能由 Storage Adapter 解析；
- 不能包含用户文件名；
- 不能使用 `..`；
- 不能由 API 客户端提交；
- 不能直接成为公开 URL；
- 日志中只记录 ID，不记录绝对路径。

本地存储建议：

```text
data/
├─ content_agent.db
└─ assets/
   └─ projects/
      └─ <project_id>/
         └─ <asset_id>/
            └─ blobs/
               └─ <blob_id>
```

未来切换对象存储时，Asset 和 Runtime Schema 不变，只替换 Storage Adapter。

## 7. Asset Variant

### 7.1 Schema

```json
{
  "asset_variant_id": "var_...",
  "project_id": "prj_...",
  "asset_id": "ast_...",
  "source_blob_id": "blb_...",
  "blob_id": "blb_...",
  "kind": "provider_upload_copy",
  "spec_version": "doubao_video_v1",
  "transform": {
    "container": "mp4",
    "video_codec": "h264",
    "audio_codec": "aac",
    "max_size_bytes": null,
    "max_duration_ms": null
  },
  "status": "ready",
  "created_by_task_id": "tsk_...",
  "created_at": "",
  "expires_at": ""
}
```

### 7.2 Variant Kind

第一版允许：

```text
provider_upload_copy
compressed_video
normalized_document
preview_image
extracted_keyframes
```

`extracted_keyframes` 只能作为处理副本，不是用户图片 Asset。

### 7.3 生成规则

- 只有 Adapter 或 Generic Tool 可以创建 Variant；
- 必须绑定原 Blob；
- 必须保存 Transform Spec Version；
- 必须重新计算校验值；
- 不能覆盖已存在 Variant；
- 同一 Spec + Source Checksum 可以在同一 Project 内复用；
- 复用必须检查 Blob 仍可用；
- 不做跨 Project 的物理去重；
- Variant 不能比原视频更晚过期；
- 删除原视频时同时删除其视频处理副本。

### 7.4 Provider 处理规格

第一版不把“所有视频压缩到 10 MB”写死。

Adapter 依据实际 Provider Contract 决定：

```text
无需处理
-> 直接使用原 Blob

需要转换容器/编码
-> 创建 provider_upload_copy

超过 Provider 限制
-> 创建压缩副本，或拒绝并返回明确错误

超过 Provider 时长限制
-> 按已批准的分段策略处理，或拒绝
```

任何分段策略都必须保证时间码可以映射回原视频。

## 8. Asset Parse Result

### 8.1 定位

Parse Result 是通用文件解析缓存，不是 Domain Artifact。

适用：

- TXT/MD 解码结果；
- DOCX/PDF 提取文本；
- 图片尺寸和基础元数据；
- 视频时长、分辨率和编码；
- 文件名集号候选；
- 未来通用 OCR 缓存。

不适用：

- 小说 `story_bible`；
- 非小说 `material_bank`；
- 视频高还原剧本；
- 剧本分析；
- 改编 Brief。

### 8.2 Schema

```json
{
  "asset_parse_result_id": "apr_...",
  "project_id": "prj_...",
  "asset_id": "ast_...",
  "parser_id": "document_text_extract",
  "parser_version": "1.0.0",
  "source_checksum": "",
  "status": "ready",
  "summary": {
    "character_count": 120000,
    "page_count": 320,
    "language": "zh-CN"
  },
  "payload_blob_id": "blb_...",
  "failure": null,
  "created_at": "",
  "updated_at": ""
}
```

### 8.3 缓存规则

缓存键：

```text
project_id
+ asset_id
+ source_checksum
+ parser_id
+ parser_version
```

Parser 版本变化不会覆盖旧结果，而是创建新 Parse Result。

### 8.4 删除规则

Parse Result 属于来源材料的派生缓存：

- 用户主动删除 Asset 时一起删除 Payload Blob；
- 视频自动过期时，视频 OCR 和容器解析 Payload 一起删除；
- 只保留最小来源元数据和已经固化进正式 Artifact 的内容；
- 不以 Parse Result 代替正式 Artifact 的长期保存。

## 9. 支持格式

### 9.1 第一版白名单

| Kind | 扩展名 | MIME |
|---|---|---|
| text | `.txt` | `text/plain` |
| text | `.md` | `text/markdown`, `text/plain` |
| document | `.docx` | `application/vnd.openxmlformats-officedocument.wordprocessingml.document` |
| document | `.pdf` | `application/pdf` |
| image | `.jpg`, `.jpeg` | `image/jpeg` |
| image | `.png` | `image/png` |
| image | `.webp` | `image/webp` |
| video | `.mp4` | `video/mp4` |
| video | `.mov` | `video/quicktime` |

### 9.2 校验顺序

```text
客户端预检查
-> 服务端扩展名检查
-> 流式大小检查
-> 文件签名/MIME 检查
-> 完整性和校验值
-> 类型专用元数据探测
-> 原子提交
```

客户端检查只用于快速反馈，不是安全边界。

### 9.3 类型不一致

扩展名、Declared MIME 和 Detected MIME 不一致时：

- 不自动按扩展名信任；
- 可确定真实类型且在白名单内时，按真实类型规范化；
- 无法确定或存在风险时拒绝；
- 保留 Upload Item 失败记录；
- 不创建可用 Asset；
- 返回稳定错误码。

### 9.4 文本编码

TXT/MD：

- 优先识别 UTF-8；
- 可检测常见中文编码；
- 解析结果统一为 UTF-8；
- 原始字节不改变；
- 无法可靠解码时 `parse_status=failed`；
- 不用替换字符静默掩盖乱码。

## 10. 上传协议

### 10.1 第一版传输

第一版网页端通过后端上传接口流式接收文件。

要求：

- 不把完整文件读入内存；
- 不使用 Base64 JSON；
- 写入 staging 文件；
- 上传过程中计算 SHA-256；
- 超限立即终止；
- 校验完成后原子移动到正式 Blob；
- 失败或断开后清理 staging；
- 支持请求幂等。

对象存储直传和可恢复分片上传不是第一版必须项，但接口不应阻止未来加入。

### 10.2 Upload Session

一次用户提交一批文件创建一个 Upload Session。

```json
{
  "upload_session_id": "upl_...",
  "project_id": "prj_...",
  "idempotency_key": "",
  "status": "open",
  "declared_item_count": 3,
  "completed_item_count": 0,
  "failed_item_count": 0,
  "created_at": "",
  "closed_at": null
}
```

状态：

```text
open
completed
partial_failed
failed
aborted
```

### 10.3 Upload Item

```json
{
  "upload_item_id": "upi_...",
  "upload_session_id": "upl_...",
  "client_item_key": "local-selection-1",
  "original_filename": "",
  "declared_size_bytes": 0,
  "received_size_bytes": 0,
  "status": "receiving",
  "asset_id": null,
  "failure": null,
  "created_at": "",
  "completed_at": null
}
```

状态：

```text
pending
receiving
validating
completed
failed
aborted
```

### 10.4 幂等

同一：

```text
project_id + idempotency_key + client_item_key
```

重复提交时：

- 已完成返回同一 Asset；
- 正在上传返回当前状态；
- 已失败允许显式创建新 Upload Session；
- 不能重复创建相同逻辑上传的 Asset。

### 10.5 原子提交

只有以下步骤全部成功后才创建 `status=available` 的 Asset：

1. 字节接收完成；
2. 实际大小符合限制；
3. MIME/签名通过；
4. SHA-256 完成；
5. Blob 已原子提交；
6. Asset 与 Blob 元数据事务提交；
7. 保留任务已创建。

任一步失败都不能让前端看到“上传成功”。

### 10.6 粘贴文本

用户直接粘贴长文本时：

- 创建 `kind=text`、`source_type=paste` 的 Asset；
- 服务端编码为 UTF-8 原始 Blob；
- 使用消息 ID 记录来源；
- 不把聊天 Message 当作长期文本文件替代；
- 后续 Run 仍绑定 Asset Snapshot。

短暂普通聊天不自动创建 Asset。

## 11. 上传限制

### 11.1 配置模型

```json
{
  "policy_version": "upload_policy_v1",
  "allowed_types": {},
  "max_file_size_bytes_by_kind": {},
  "max_items_per_upload_session": 20,
  "max_assets_per_project_by_kind": {
    "video": 100
  },
  "max_video_duration_ms": 180000,
  "active_video_parse_concurrency": 3
}
```

### 11.2 生效值

上传接口返回当前生效限制：

```json
{
  "policy_version": "upload_policy_v1",
  "effective_limits": {},
  "reason": "product_and_gateway_minimum"
}
```

Run Input Snapshot 保存：

- Upload Policy Version；
- Provider Capability Version；
- 实际使用的 Provider Variant Spec。

### 11.3 限制变化

配置收紧时：

- 已上传 Asset 不自动删除；
- 新 Run 必须重新检查当前 Provider 可执行性；
- 已有 Artifact 不受影响；
- Retry 使用原 Run Snapshot，除非原 Provider Contract 已不可执行；
- 不可执行时要求用户确认迁移策略。

### 11.4 限制错误

稳定错误码：

```text
ASSET_TYPE_NOT_SUPPORTED
ASSET_MIME_MISMATCH
ASSET_FILE_TOO_LARGE
ASSET_VIDEO_TOO_LONG
UPLOAD_BATCH_TOO_LARGE
PROJECT_ASSET_LIMIT_REACHED
UPLOAD_INCOMPLETE
ASSET_INTEGRITY_FAILED
```

错误必须返回：

- 当前值；
- 允许值；
- 失败文件；
- 是否已保存其他成功文件；
- 可执行操作。

## 12. Asset Set

### 12.1 目的

Asset Set 用于表达“本次要一起使用的材料”，解决：

- 同一部视频分批上传；
- 用户如何表示已经传完；
- 多个视频如何确定集号和展示顺序；
- 一个 Run 到底使用哪些 Asset；
- 后续补传或排序变化如何形成新快照。

它是 Generic Runtime 对象，不是 Domain Capability。

### 12.2 Schema

```json
{
  "asset_set_id": "aset_...",
  "project_id": "prj_...",
  "purpose": "video_reference_source",
  "display_name": "参考剧视频",
  "status": "collecting",
  "current_version": 3,
  "created_by_message_id": "msg_...",
  "created_at": "",
  "updated_at": "",
  "sealed_at": null
}
```

### 12.3 Purpose

第一版：

```text
run_source_materials
video_reference_source
```

### 12.4 Status

```text
collecting
sealed
superseded
deleted
```

`sealed` 表示用户已明确结束当前输入集合，不表示所有视频解析成功。

### 12.5 Asset Set Version

每次以下操作创建新版本：

- 添加成员；
- 移除成员；
- 修改人工集号；
- 修改展示顺序；
- 确认按缺集继续；
- 确认忽略失败项；
- Seal；
- 已 Seal 后补传。

```json
{
  "asset_set_version_id": "asv_...",
  "asset_set_id": "aset_...",
  "version": 3,
  "status": "draft",
  "member_count": 18,
  "completeness": {
    "recognized_episode_count": 17,
    "unrecognized_count": 1,
    "duplicate_episode_numbers": [],
    "missing_episode_numbers": [6],
    "failed_asset_ids": []
  },
  "continuation_policy": null,
  "created_at": ""
}
```

旧版本不可修改。

Version 状态：

```text
draft
sealed
superseded
```

Set 当前指针可以移动到新 Version，但已被 Run Input Snapshot 引用的 Version 永远不可原地修改。

### 12.6 Asset Set Member

```json
{
  "asset_set_member_id": "asm_...",
  "asset_set_version_id": "asv_...",
  "asset_id": "ast_...",
  "episode_order": 7,
  "episode_no": 8,
  "episode_label": "第8集",
  "episode_source": "filename",
  "filename_candidate": {
    "episode_no": 8,
    "confidence": "high",
    "pattern": "chinese_episode"
  },
  "included": true,
  "exclusion_reason": null
}
```

`episode_order`：

- 必填；
- 从 1 开始；
- 在同一 Set Version 内唯一；
- 不用数组位置代替；
- 人工拖动后重新分配稳定顺序。

`episode_no`：

- 可为空；
- 不可靠时不能猜；
- 人工值优先；
- 单视频收集和提取阶段可以为空，但 `episode_order=1`；
- 进入 `reference_scripts` 聚合前必须在批次确认中形成唯一正整数业务集号；单视频可由界面建议 `episode_no=1`，仍需用户确认留痕。

## 13. 视频分批上传

### 13.1 两种入口

#### 仅上传

用户只上传视频但没有明确创作意图：

- Asset 进入当前 Project；
- Agent 询问或等待用途；
- 不创建 Run；
- 不自动生成高还原剧本。

#### 已选择视频参考创作，尚未启动 Run

用户已经选择或确认 `video_reference_creation`：

- Runtime 创建或绑定 `video_reference_source` Asset Set；
- 此时仍不创建 Domain Run；
- 每批视频加入新的 Asset Set Version；
- 用户可以继续上传、排序和修正集号；
- Set Seal 后，用户确认启动才创建一个固定 Run Input Snapshot；
- 每个单集解析 Task 绑定该固定 Run Input Snapshot 和精确 Asset Snapshot。

Run 启动后不移动来源 Snapshot 指针。整剧聚合、分析和 Brief 只读取启动时固定的 sealed 版本。

### 13.2 如何知道上传完成

系统不根据：

- 等待时间；
- 浏览器关闭；
- 上传批次数；
- 文件名最后一集；
- 一段时间没有新文件；

猜测用户已经传完。

结束条件必须是：

- 用户自然语言明确表达“传完了”“就这些”“开始处理”；或
- 用户点击“上传完成”。

两者进入同一个 `seal_asset_set` Command。

### 13.3 Seal 前

允许：

- 继续分批上传；
- 删除未被正式 Artifact 引用的视频；
- 修改集号；
- 调整顺序；
- 重试上传失败文件；
- 查看文件和完整度状态。

禁止：

- 启动逐集解析 Task；
- 生成整剧聚合；
- 生成整剧剧本分析；
- 生成改编方案；
- 生成 `adaptation_brief`。

### 13.4 Seal 检查

至少检查：

```text
无成员
无法识别集号
重复集号
明显缺集
上传失败
视频元数据解析失败
单集高还原剧本失败
人工顺序未确认
Asset 已过期或已删除
```

阻断级问题：

- 集合为空；
- 存在 Asset 已删除或过期；
- 存在重复集号且未人工处理；
- 展示顺序不完整；
- 用户尚未确认缺集继续策略。

单集剧本解析失败可以在 Seal 后重试，但进入整剧分析前必须：

- 全部成功；或
- 用户显式确认忽略失败项并按不完整材料继续。

### 13.5 缺集继续

```json
{
  "continuation_policy": {
    "mode": "continue_incomplete",
    "confirmed_by": "user",
    "missing_episode_numbers": [6],
    "excluded_asset_ids": [],
    "failed_episode_numbers": [],
    "confirmed_at": ""
  }
}
```

该确认进入：

- Asset Set Version；
- Run Input Snapshot；
- Approval；
- `reference_scripts` 完整度元数据；
- `script_analysis` 完整度说明。

### 13.6 Seal 后补传

若尚未启动整剧聚合：

- 允许显式重新打开收集；
- 创建新 Asset Set Version；
- 原 Seal Version 保留；
- 再次 Seal。

若已经生成整剧聚合或后续 Artifact：

- 不原地修改旧 Run Input Snapshot；
- 新视频加入新 Asset Set Version；
- 计算 Artifact Impact Preview；
- 用户选择保留旧结果或创建 Revision Run 重生成受影响下游；
- 旧 Candidate 和旧 Artifact Version 不删除。

## 14. 视频集号与排序

### 14.1 文件名候选识别

按优先级识别明确模式：

```text
第12集
第012集
EP12
E12
episode_12
集12
```

实现必须：

- 使用自然数排序，不使用字典序；
- 保存命中的 Pattern；
- 保存原始候选；
- 将全角数字规范化；
- 忽略扩展名；
- 不把日期、分辨率、版本号自动当作集号；
- 多个冲突数字时标记 ambiguous。

### 14.2 Confidence

```text
high
medium
ambiguous
none
```

只有 `high` 可以默认写入 `episode_no`。

`medium/ambiguous/none` 必须：

- 先按上传顺序暂排；
- 显示待确认；
- 允许人工填写。

### 14.3 默认排序

```text
有可靠 episode_no
-> episode_no ASC

无可靠 episode_no
-> uploaded_at ASC, asset_id ASC
```

混合情况：

- 不把无集号文件静默插到任意集之间；
- 在确认界面单独标记；
- 用户确认后形成完整 `episode_order`。

### 14.4 重复

相同 `episode_no`：

- 不自动覆盖；
- 不根据上传时间猜哪个正确；
- 显示文件名、时长、大小和解析状态；
- 用户必须改集号、排除其中一个或替换；
- 排除只影响当前 Asset Set Version，不删除 Asset。

### 14.5 缺口

当已识别集号范围存在中断时：

- 生成 `missing_episode_numbers`；
- 不把缺口自动补成内容；
- 用户可以继续上传；
- 用户可以确认按不完整材料继续；
- 分析和后续 Brief 必须携带完整度。

### 14.6 Run 中的顺序

Run 只读取 `Asset Set Version` 固定的：

- Member ID；
- Asset ID；
- Asset Checksum；
- Episode Number；
- Episode Order；
- Included；
- Completeness；
- Continuation Policy。

后续修改当前 Asset Set 不改变已启动 Run。

## 15. 视频单集任务

### 15.1 一个视频一个 Task

第一版默认：

```text
一个视频 Asset
-> 一个视频解析 Task
-> 一个 video_script_unit Artifact Scope
```

多个视频通过任务队列并发，Provider 单请求仍按其实际接口限制执行。

### 15.2 并发

第一版全局视频解析部署默认值为 1，必须配置化。真实连续样本在 2/3 并发下出现 HTTP 504；单并发仍可能发生集级网关失败，因此提高并发前必须重新完成正式网关负载验收。

并发控制至少区分：

- 全局 Provider 并发；
- 单 Project 活跃任务数；
- 单 Run 活跃任务数；
- Provider 限流退避；
- 用户手动重试。

### 15.3 任务状态

每集独立显示：

```text
等待上传
等待解析
处理中
已完成
失败
源视频已过期
源视频已删除
已排除
```

用户可以：

- 查看；
- 编辑已生成单集剧本；
- 重试失败项；
- 重新生成单集；
- 在整步确认前调整顺序。

视频解析步骤仍只统一确认一次。

### 15.4 输入快照

每个 Task 固定：

```json
{
  "asset_snapshot_id": "ass_...",
  "asset_id": "ast_...",
  "source_checksum": "",
  "asset_set_version_id": "asv_...",
  "asset_set_member_id": "asm_...",
  "episode_no": 8,
  "episode_order": 7,
  "provider_variant_id": "var_...",
  "provider_contract_version": "",
  "requested_at": ""
}
```

## 16. Retention Policy

### 16.1 Schema

```json
{
  "retention_policy_id": "video_source_7d_v1",
  "applies_to": ["video"],
  "trigger": "uploaded_at",
  "duration_seconds": 604800,
  "delete_original": true,
  "delete_variants": true,
  "delete_parse_payloads": true,
  "preserve_asset_tombstone": true,
  "preserve_formal_artifacts": true,
  "version": 1
}
```

### 16.2 时间计算

```text
expires_at
= uploaded_at UTC + 604800 seconds
```

采用绝对时间，不按本地自然日计算。

### 16.3 不续期

以下操作不改变 `expires_at`：

- 查看；
- 下载；
- 解析；
- Retry；
- 创建新 Variant；
- 创建 Artifact；
- 打开作品；
- 继续聊天。

### 16.4 新上传

用户在原视频过期后重新上传相同文件：

- 创建新 Asset ID；
- 重新计算保留期；
- 可以记录与旧 Asset Checksum 相同；
- 不复活旧 Asset；
- 不重写旧 Run Input Snapshot；
- 新 Run 或 Revision Run 显式引用新 Asset。

### 16.5 Retention Job

```json
{
  "retention_job_id": "rtj_...",
  "project_id": "prj_...",
  "asset_id": "ast_...",
  "policy_id": "video_source_7d_v1",
  "action": "expire_video_source",
  "due_at": "",
  "status": "scheduled",
  "attempt_count": 0,
  "lease_until": null,
  "last_failure": null,
  "completed_at": null,
  "created_at": ""
}
```

状态：

```text
scheduled
running
retry_wait
completed
failed
cancelled
```

### 16.6 幂等键

```text
asset_id + policy_id + action + due_at
```

重复 Worker、服务重启或重复 Event 不得重复产生逻辑副作用。

### 16.7 到期执行顺序

```text
claim job
-> verify current asset and policy
-> mark asset expired
-> revoke read access
-> mark blobs delete_pending
-> delete original blob
-> delete video variants
-> delete parse payload blobs
-> preserve tombstone
-> publish completed event
```

逻辑过期先于物理删除，防止删除失败时文件仍可被业务读取。

### 16.8 运行中任务

到期时：

- 已开始读取并已发给 Provider 的请求允许完成；
- 尚未读取源文件的排队任务失败为 `ASSET_SOURCE_EXPIRED`；
- 不创建新的 Provider Variant；
- 返回结果必须绑定原 Request Snapshot；
- Retry 必须要求重新上传源视频；
- 已成功 Artifact 保留。

### 16.9 第一版实施状态

阶段 3 第 10 步已经实现：

- 视频上传事务内创建唯一 Retention Job；
- `scheduled/running/retry_wait/completed/failed/cancelled` 状态；
- Worker ID 与租约所有权校验；
- 过期租约恢复；
- 逻辑撤销访问先于物理删除；
- 文件不存在时幂等成功；
- 删除失败后有界退避和最大次数；
- 用户删除 Preview/Confirm；
- 活动 Run Guard；
- Tombstone、Artifact 和 Dependency 保留；
- Runtime Server 启动扫描和周期扫描。

当前代码只生产 Original Blob，尚未生产 Asset Variant 和 Parse Payload。后续增加这两类 Blob 时必须写入同一 `asset_blobs` 归属并由现有 Retention Job 一并删除，不能另建绕过 Runtime 的清理路径。

## 17. 保留矩阵

| 数据 | 默认期限 | 用户删 Asset | 视频 7 天到期 | 删除 Project |
|---|---|---|---|---|
| 文本/文档原文件 | 随 Project | 删除 | 不适用 | 删除 |
| 图片原文件 | 随 Project | 删除 | 不适用 | 删除 |
| 视频原文件 | 168 小时 | 删除 | 删除 | 删除 |
| 视频处理副本 | 不晚于原视频 | 删除 | 删除 | 删除 |
| 文档规范化副本 | 随源文件 | 删除 | 不适用 | 删除 |
| Parse Payload | 随源文件 | 删除 | 视频类删除 | 删除 |
| Asset 最小元数据 | 随 Project | Tombstone | Tombstone | 最小审计 Tombstone |
| Upload 失败 staging | 最长 24 小时 | 删除 | 不适用 | 删除 |
| 临时导出文件 | 最长 24 小时 | 不适用 | 不适用 | 删除 |
| 正式 Artifact Version | 随 Project | 保留 | 保留 | 删除 |
| Artifact Dependency | 随 Artifact | 保留 Tombstone 引用 | 保留 Tombstone 引用 | 删除或最小审计 |
| Approval/Event | 随 Project | 保留审计 | 保留审计 | 保留最小审计 |

“随 Project”表示直到用户删除 Project；第一版不做自动 Project 过期。

## 18. 用户主动删除 Asset

### 18.1 未被引用

若 Asset：

- 不在活动 Run Input Snapshot；
- 不在任何正式 Artifact Dependency；
- 不在待确认 Asset Set Version；

可以直接确认普通删除操作，不需要下游影响选择。

删除仍记录 Event。

### 18.2 已被引用

必须：

```text
request delete
-> calculate impact
-> show exact references
-> create Approval
-> user confirms
-> logical delete
-> async physical delete
-> preserve tombstone and formal artifacts
```

### 18.3 删除影响预览

```json
{
  "asset_id": "ast_...",
  "asset_status": "available",
  "active_run_impacts": [],
  "asset_set_impacts": [],
  "artifact_impacts": [
    {
      "artifact_version_id": "av_...",
      "artifact_type": "video_script_unit",
      "scope_key": "episode:8",
      "effect": "source_will_be_unavailable"
    }
  ],
  "capability_impacts": [
    {
      "capability_id": "video_reference_creation",
      "action": "retry_or_regenerate_from_source",
      "available_after_delete": false
    }
  ],
  "existing_artifacts_will_be_preserved": true,
  "snapshot_hash": ""
}
```

### 18.4 活动 Run

若 Asset 被活动 Run 使用：

- 阻止直接删除；
- 用户必须先暂停或取消相关执行；
- 已发出的 Provider 请求按 Attempt 记录；
- 删除确认后未开始任务失败；
- 已完成产物保留；
- 不让删除与新读取并发穿透。

### 18.5 Approval

Approval 必须固定：

- Asset ID；
- Asset Checksum；
- 当前 Asset Status；
- 受影响 Run；
- 受影响 Artifact Version；
- 受影响 Asset Set Version；
- Preview Hash；
- “已有正式产物保留”的明确说明。

Approval 过期条件：

- Asset 状态变化；
- 新增引用；
- 活动 Run 状态变化；
- Preview Hash 变化。

### 18.6 删除后 Tombstone

保留：

```json
{
  "asset_id": "ast_...",
  "project_id": "prj_...",
  "kind": "video",
  "display_name": "穷剑修第08集.mov",
  "size_bytes": 41943040,
  "checksum": "",
  "status": "deleted",
  "source_available": false,
  "deleted_at": "",
  "delete_reason": "user_confirmed",
  "original_blob_id": null
}
```

不保留：

- 可读取 `storage_ref`；
- 原始字节；
- 处理副本；
- Parse Payload；
- Provider 上传临时文件。

## 19. Project 删除

### 19.1 语义

Project 删除是第一版不可恢复的破坏性操作。

执行前：

- 活动 Run 必须停止；
- 显示作品、文件、候选剧本、最终稿和历史版本数量；
- 要求再次确认。

### 19.2 删除顺序

```text
confirm project delete
-> mark project deleted
-> revoke project access
-> cancel pending uploads
-> cancel queued tasks
-> schedule all blobs for deletion
-> delete parse payloads
-> delete artifact payloads
-> retain minimal audit tombstone
```

第一版没有回收站，因此逻辑删除后不提供恢复入口。

### 19.3 最小审计

可保留：

- Project ID；
- 删除时间；
- 删除动作 Event；
- 删除任务状态；
- 不含作品内容的数量统计。

不得把完整剧本、原文件内容或 Provider Payload 作为“审计”长期保留。

## 20. 读取与下载

### 20.1 授权

第一版没有用户登录，但仍必须校验：

- 请求处于内部共享工作区；
- Asset 属于指定 Project；
- Project 未删除；
- Asset 未删除或过期；
- Blob 属于该 Asset；
- 请求用途允许读取该 Blob Role。

共享工作区不等于允许绕过 Project 边界。

### 20.2 前端读取

浏览器通过 Asset API 获取短期受控响应，不接触服务端路径。

第一版本地存储可以由后端流式返回；未来对象存储可使用短期签名 URL。

### 20.3 过期响应

```text
ASSET_SOURCE_EXPIRED
HTTP 410
```

响应包含：

- Asset Tombstone；
- 已有正式产物是否可用；
- 是否需要重新上传；
- 可执行操作。

### 20.4 删除响应

```text
ASSET_SOURCE_DELETED
HTTP 410
```

不能返回 404 掩盖已有来源关系，也不能返回可读取路径。

## 21. Provider 数据边界

### 21.1 服务端透传

模型请求只能：

```text
Capability
-> Provider Adapter
-> 公司内网网关
-> Approved Provider
```

前端：

- 不持有 API Key；
- 不直接上传到 Provider；
- 不读取 Provider Credential；
- 不在日志中获得请求头。

### 21.2 Request Audit

每次包含文件的模型请求记录：

```json
{
  "provider_request_audit_id": "pra_...",
  "project_id": "prj_...",
  "run_id": "run_...",
  "task_id": "tsk_...",
  "provider_id": "doubao",
  "provider_contract_version": "",
  "asset_id": "ast_...",
  "asset_variant_id": "var_...",
  "source_checksum": "",
  "sent_at": "",
  "provider_request_id": "",
  "remote_retention_policy_id": ""
}
```

不记录：

- API Key；
- Authorization Header；
- 文件 Base64；
- 完整 Prompt；
- 完整模型响应。

完整 Prompt/Response 的业务追踪按模型调用审计协议单独定义，并使用受控 Payload。

### 21.3 远端保留

本地删除不能被描述为已经删除 Provider 远端副本，除非 Provider 或公司网关提供并成功执行可验证删除接口。

产品必须区分：

```text
本地源文件保留期
公司网关缓存保留期
Provider 远端保留策略
```

第一版只能调用公司批准的目标 Provider。

## 22. 安全与完整性

### 22.1 文件名

- 文件名只用于展示；
- 去除路径分隔符；
- 限制显示长度；
- 保存原始展示名时做安全编码；
- 不用文件名作为磁盘路径；
- 不执行文件名中的命令或模板表达式。

### 22.2 文件内容

- 检查 MIME 和签名；
- DOCX 按压缩容器安全解析；
- PDF 解析设置页数和资源限制；
- 图片解码设置像素上限；
- 视频元数据读取设置超时；
- 不执行上传文件中的宏、脚本或嵌入程序；
- 解析进程设置 CPU、内存和超时边界。

### 22.3 日志

禁止日志记录：

- 文件内容；
- 视频字节；
- 文档全文；
- 图片 Base64；
- Provider Key；
- 完整受控路径；
- 用户剧本全文。

允许记录：

- ID；
- 大小；
- MIME；
- 状态；
- 错误码；
- 耗时；
- 校验值的短前缀。

### 22.4 内容校验

第一版不做：

- 自动版权判断；
- 自动查重；
- 任意多剧融合；
- 业务内容安全自动裁决。

第一版视频参考创作只做：

- 来源标记；
- 禁止照搬 Rules；
- 用户确认改编 Brief。

## 23. 事件

### 23.1 Event Names

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

asset_set.created
asset_set.member_added
asset_set.member_removed
asset_set.order_changed
asset_set.completeness_checked
asset_set.sealed
asset_set.reopened
asset_set.superseded
```

### 23.2 Event Payload

```json
{
  "event_id": "evt_...",
  "project_id": "prj_...",
  "event_type": "asset.expired",
  "subject_type": "asset",
  "subject_id": "ast_...",
  "actor_type": "system",
  "actor_id": "retention_worker",
  "payload": {
    "policy_id": "video_source_7d_v1",
    "formal_artifacts_preserved": true
  },
  "occurred_at": ""
}
```

Event Payload 不复制文件内容。

## 24. API 语义入口

本节只定义行为，不冻结最终 URL。

### 24.1 上传

```text
create upload session
upload item stream
complete item
close upload session
get upload status
```

### 24.2 Asset

```text
list project assets
get asset
rename display name
get parse status
retry generic parse
request delete
confirm delete
read/download available source
```

### 24.3 Asset Set

```text
create set
add assets
remove member
update episode number
reorder members
check completeness
seal
reopen before downstream execution
get immutable version
```

### 24.4 Command Guard

所有写 Command 必须携带：

- Project ID；
- Idempotency Key；
- Expected Version；
- Actor；
- 当前 Client Request ID。

版本冲突返回：

```text
ASSET_SET_VERSION_CONFLICT
ASSET_STATUS_CONFLICT
DELETE_PREVIEW_EXPIRED
```

## 25. 前端必须展示的状态

### 25.1 通用材料列表

至少展示：

- 显示名称；
- 类型；
- 大小；
- 上传时间；
- 文件状态；
- 解析状态；
- 所属 Project；
- 视频到期时间；
- 是否被 Run/Artifact 引用；
- 可用操作。

### 25.2 视频列表

至少展示：

- 集号；
- 文件名；
- 人工顺序；
- 时长；
- 上传状态；
- 单集解析状态；
- 失败原因；
- 原视频到期时间；
- 是否缺集/重复；
- 是否包含在当前 Set Version；
- 单集剧本状态。

### 25.3 删除提示

删除提示必须回答：

- 会删除什么；
- 不会删除什么；
- 哪些已有产物仍保留；
- 哪些重试或重新生成能力会失去；
- 是否存在活动任务；
- 是否需要再次确认。

### 25.4 过期提示

```text
原视频已于 <time> 到期删除。
已有剧本和分析仍可使用，但无法基于原视频重新解析。
如需重新解析，请重新上传视频。
```

不能只显示模糊的“文件不可用”。

## 26. Context Pack 接入

### 26.1 Routing Context

只提供：

- Asset ID；
- Kind；
- Display Name；
- Status；
- Parse Status；
- 简短摘要；
- 可用 Capability；
- 是否过期/删除。

不提供文件全文或视频字节。

### 26.2 Capability Start Context

提供：

- 用户选中的 Asset/Asset Set；
- 当前不可变 Version；
- 文件元数据；
- Parse Summary；
- 完整度；
- 当前限制；
- 缺失配置。

### 26.3 Execution Context

Worker 只获得：

- 当前 Task 的 Asset Snapshot；
- 必需 Parse Payload 或 Provider Variant；
- 精确版本；
- 最小必要上下文。

### 26.4 Inspection Context

过期或删除后仍可提供 Tombstone 和 Dependency Path，但不能生成可读取 Source Ref。

## 27. Artifact Dependency 接入

### 27.1 Snapshot

Run 启动时创建：

```json
{
  "asset_snapshot_id": "ass_...",
  "project_id": "prj_...",
  "asset_id": "ast_...",
  "asset_status": "available",
  "checksum": "",
  "size_bytes": 0,
  "parse_result_id": "apr_...",
  "asset_set_version_id": "asv_...",
  "asset_set_member_id": "asm_...",
  "episode_no": 8,
  "episode_order": 7,
  "source_available_at_snapshot": true,
  "created_at": ""
}
```

### 27.2 删除后

Dependency 不删除，而是解析为：

```json
{
  "source_status": "deleted",
  "source_available": false,
  "formal_artifact_available": true
}
```

### 27.3 过期后

同理：

```json
{
  "source_status": "expired",
  "source_available": false,
  "formal_artifact_available": true
}
```

### 27.4 新 Run Guard

新 Run、Revision Run 或 Retry 在读取源文件前必须再次检查：

- Asset Status；
- Blob Status；
- Checksum；
- Project；
- Expiration；
- Provider Variant 可用性。

Snapshot 不能授权读取已经过期或删除的文件。

## 28. 一致性与事务

### 28.1 上传提交事务

文件系统和 SQLite 不能形成真正单事务，因此采用：

```text
write staging
-> fsync/close
-> atomic promote
-> DB transaction create blob + asset
-> on DB failure enqueue orphan cleanup
```

定期扫描：

- 无 DB 记录的物理 Blob；
- 指向不存在 Blob 的数据库记录；
- 超时 staging；
- 卡住的 delete_pending；
- 到期但未执行的 Retention Job。

### 28.2 删除事务

```text
DB transaction:
  mark asset logically unavailable
  create delete jobs
  publish outbox event

worker:
  delete physical blobs
  update blob status
```

禁止先删文件、后尝试写数据库。

### 28.3 Outbox

Asset 状态变化和 Event 使用同一 SQLite Transaction 写入 Outbox，避免：

- 文件已删除但前端仍显示可用；
- Asset 已过期但没有通知 Runtime；
- Run 继续读取已经撤销的来源。

## 29. 后台清理

### 29.1 Worker Lease

Retention Job 领取时写：

- Worker ID；
- Lease Until；
- Attempt Count；
- Started At。

Worker 崩溃后 Lease 到期可被其他 Worker 重新领取。

### 29.2 Retry

暂时错误：

- 文件被短暂占用；
- 对象存储超时；
- 网络错误；
- 服务重启。

采用有上限指数退避。

永久错误：

- Storage Ref 非法；
- Project/Asset 归属不一致；
- 校验表损坏；
- 多次删除失败超过阈值。

永久失败：

- Asset 仍保持逻辑不可用；
- 记录 `delete_failed`；
- 创建可观测告警；
- 不把状态恢复为 `available`。

### 29.3 Staging Cleanup

未完成上传和临时文件最长保留 24 小时。

清理条件：

- Upload Session 已失败/中止；或
- 最后更新时间超过阈值；或
- 无对应 Upload Item。

### 29.4 Export Cleanup

服务端临时 TXT/DOCX 导出文件最长保留 24 小时。

正式剧本 Artifact 不因导出文件删除而受影响。

## 30. 错误合同

| 错误码 | 含义 | 用户动作 |
|---|---|---|
| `ASSET_NOT_FOUND` | 当前 Project 不存在该 Asset | 重新选择 |
| `ASSET_PROJECT_MISMATCH` | Asset 不属于当前 Project | 切换 Project |
| `ASSET_SOURCE_EXPIRED` | 源视频已到期 | 重新上传 |
| `ASSET_SOURCE_DELETED` | 源文件已删除 | 使用已有产物或重新上传 |
| `ASSET_PARSE_FAILED` | 通用解析失败 | 重试或重新上传 |
| `ASSET_TYPE_NOT_SUPPORTED` | 类型不支持 | 改用白名单格式 |
| `ASSET_FILE_TOO_LARGE` | 超过当前大小限制 | 压缩或拆分 |
| `ASSET_VIDEO_TOO_LONG` | 超过当前时长限制 | 拆分或更换文件 |
| `ASSET_SET_NOT_SEALED` | 视频集合尚未确认完成 | 继续上传或完成上传 |
| `ASSET_SET_INCOMPLETE` | 缺集/失败尚未确认 | 补传或确认继续 |
| `ASSET_SET_DUPLICATE_EPISODE` | 集号重复 | 人工处理 |
| `ASSET_SET_ORDER_UNCONFIRMED` | 顺序不可靠 | 人工确认 |
| `ASSET_DELETE_BLOCKED_BY_RUN` | 活动 Run 正在使用 | 暂停/取消后删除 |
| `DELETE_PREVIEW_EXPIRED` | 删除影响已变化 | 重新查看影响 |
| `PROVIDER_ASSET_LIMIT_EXCEEDED` | Provider 当前限制更小 | 按 Adapter 建议处理 |

错误响应不得只返回字符串，必须包含结构化 `available_actions`。

## 31. 配置项

建议配置：

```text
ASSET_STORAGE_BACKEND
ASSET_STORAGE_ROOT
ASSET_UPLOAD_STAGING_ROOT
ASSET_UPLOAD_POLICY_VERSION
ASSET_ALLOWED_EXTENSIONS
ASSET_ALLOWED_MIME_TYPES
ASSET_MAX_SIZE_TEXT
ASSET_MAX_SIZE_DOCUMENT
ASSET_MAX_SIZE_IMAGE
ASSET_MAX_SIZE_VIDEO
ASSET_MAX_ITEMS_PER_UPLOAD
ASSET_MAX_VIDEO_PER_PROJECT
ASSET_MAX_VIDEO_DURATION_MS
VIDEO_PARSE_GLOBAL_CONCURRENCY
VIDEO_SOURCE_RETENTION_SECONDS
UPLOAD_STAGING_RETENTION_SECONDS
EXPORT_TEMP_RETENTION_SECONDS
RETENTION_WORKER_INTERVAL
RETENTION_WORKER_LEASE_SECONDS
RETENTION_MAX_ATTEMPTS
```

配置读取失败不能默认为无限制。

启动时：

- 校验配置完整；
- 打印安全的有效限制摘要；
- 不打印 Credential；
- 保存 Policy Version。

## 32. 数据库约束

至少建立：

- `asset(project_id, asset_id)` 唯一；
- `asset(checksum)` 普通索引；
- `asset(status, expires_at)` 索引；
- `asset_blob(asset_id, status)` 索引；
- `asset_variant(asset_id, kind, spec_version)` 索引；
- `asset_parse_result(asset_id, parser_id, parser_version, source_checksum)` 唯一；
- `upload_session(project_id, idempotency_key)` 唯一；
- `upload_item(upload_session_id, client_item_key)` 唯一；
- `asset_set_version(asset_set_id, version)` 唯一；
- `asset_set_member(asset_set_version_id, episode_order)` 唯一；
- `retention_job(asset_id, policy_id, action, due_at)` 唯一。

外键删除策略：

- 不使用级联删除抹掉 Dependency；
- Asset 逻辑删除保留 Tombstone；
- Blob 元数据物理清理前保留删除状态；
- Project 删除由显式删除流程控制。

## 33. 验收测试

### 33.1 上传

- TXT、MD、DOCX、PDF、JPG、PNG、WebP、MP4、MOV 可按白名单上传；
- 伪装扩展名被拒绝；
- 超限文件不会创建可用 Asset；
- 网络中断不会留下可见的半文件；
- 相同幂等请求不会创建两个 Asset；
- 文件内容不进入 SQLite BLOB/Base64；
- 上传成功不自动创建 Run。

### 33.2 Project 隔离

- Project A 不能读取 Project B 的 Asset；
- 前端伪造 Asset ID 被拒绝；
- Context Pack 不包含其他 Project 的 Asset；
- 同名文件不造成串用。

### 33.3 原文件与 Variant

- 视频转码不覆盖原 Blob；
- Variant 保存 Transform Spec；
- Provider Request 记录实际 Variant；
- Variant 不晚于原视频到期；
- 删除源视频会删除处理副本；
- 已生成 Artifact 不被删除。

### 33.4 分批视频

- 多批视频加入同一 Asset Set；
- 系统不因等待超时自动 Seal；
- 用户明确完成后才 Seal；
- Seal 前不启动单集解析；
- Seal 前不能生成整剧分析；
- 文件名按自然集号排序；
- `第2集` 排在 `第10集` 前；
- 无集号时按上传顺序暂排；
- 重复、缺集和失败被明确展示；
- 人工排序创建新 Set Version；
- 缺集继续必须留下确认记录；
- 已启动 Run 不受后续 Set 修改影响；如需补传，Reopen 后形成新版本并另起生成 Run。

### 33.5 视频任务

- 每个视频有独立 Task 状态；
- 部分失败保留成功结果；
- 只重试失败项；
- 整个解析步骤统一确认；
- 单集剧本可编辑、保存和重新生成；
- 聚合顺序使用固定 Episode Order。

### 33.6 7 天保留

使用可控时钟验证：

- `expires_at = uploaded_at + 604800 seconds`；
- 到期前可读取；
- 到期后立即逻辑不可读；
- Worker 重复执行无副作用；
- 物理删除失败不会恢复读取；
- Tombstone 保留；
- 剧本、分析和 Brief 保留；
- Retry 要求重新上传；
- 查看和下载不延长时间；
- 相同文件重传创建新 Asset。

### 33.7 主动删除

- 未引用 Asset 可删除；
- 被引用 Asset 先显示影响；
- 活动 Run 阻止直接删除；
- Preview 变化使旧 Approval 失效；
- 删除后 Dependency 仍可解释来源；
- 已有 Artifact 继续可见；
- Blob、Variant 和 Parse Payload 被删除；
- Storage Ref 不再返回。

### 33.8 Project 删除

- 显示完整影响并二次确认；
- 活动 Run 先停止；
- Project 删除后无法打开；
- 文件和 Artifact Payload 进入删除；
- 只保留最小审计信息；
- 第一版没有恢复入口。

### 33.9 服务重启

- 上传完成 Asset 不丢失；
- 未完成 Upload Session 可识别为中断；
- 到期 Retention Job 可继续；
- Lease 到期任务可重新领取；
- 不重复删除；
- 不重复创建 Artifact；
- Asset Set Version 和顺序保持稳定。

## 34. 可观测指标

至少记录：

```text
asset_upload_total
asset_upload_failed_total
asset_upload_bytes
asset_parse_duration
asset_variant_duration
asset_storage_bytes_by_kind
asset_expiration_due_total
asset_expiration_lag_seconds
asset_delete_failed_total
upload_staging_orphan_total
asset_blob_orphan_total
video_asset_active_total
video_parse_queue_depth
video_parse_active
video_parse_failed_total
```

指标标签不能包含：

- 文件名；
- 作品名；
- 用户输入内容；
- Prompt；
- API Key。

## 35. 第一版不做

- 独立音频上传；
- 用户自定义保留期；
- 用户手动延长视频 7 天期限；
- 跨 Project 共用同一个 Asset；
- 跨 Project 二进制去重；
- 浏览器直传第三方 Provider；
- 普通用户上传可执行 Parser；
- 自动推断用户已经传完；
- 自动选择重复集中的“正确文件”；
- 自动补写缺失集；
- 自动版权判定；
- 回收站和删除恢复；
- 多人权限和个人文件隔离；
- 永久保存视频处理副本；
- 把视频处理规格固定为统一 10 MB。

## 36. 实施顺序

### 36.1 P0

1. Asset、Blob、Upload Session Schema；
2. 流式上传和格式校验；
3. 受控本地存储；
4. 文档/图片/视频基础元数据；
5. Asset 列表和状态；
6. 视频 `expires_at`；
7. Retention Job 和 Worker；
8. 主动删除影响检查；
9. Tombstone；
10. Project 隔离测试。

### 36.2 P1

1. Asset Set 和不可变版本；
2. 视频集号候选识别；
3. 人工排序和缺集确认；
4. 视频 Variant Adapter；
5. 单集任务状态；
6. Provider Request Audit；
7. Context Pack/Dependency 接入。

### 36.3 P2

1. Storage Reconciliation；
2. Orphan Cleanup；
3. 完整指标和告警；
4. 对象存储 Adapter 预留验证；
5. Provider 限制动态发现；
6. 大规模负载测试。

P0/P1/P2 是本协议内部实施优先级，不改变项目总阶段编号。

## 37. 完成门禁

进入 API/Event Contract 前，本合同必须满足：

- [x] 上传和启动 Skill 已分离；
- [x] Asset、Variant、Parse Result 和 Artifact 已分离；
- [x] 原文件与处理副本已分离；
- [x] 视频分批上传和明确完成语义已定义；
- [x] 集号、顺序、重复和缺集规则已定义；
- [x] 视频 7 天保留已定义；
- [x] 主动删除影响和 Tombstone 已定义；
- [x] Provider 数据边界已定义；
- [x] Project 删除语义已定义；
- [x] 配置、错误、事件和验收测试已定义；
- [ ] 根据正式模型网关确认实际视频限制；
- [ ] 通过真实几十集视频做负载和准确性验收。

后两项是实现和验收门禁，不阻止继续完成阶段 2 的 API/Event 与前端协议设计。
