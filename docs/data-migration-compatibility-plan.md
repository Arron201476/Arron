# 数据迁移与兼容实施计划

状态：阶段 2 设计稿  
版本：1.0.0  
适用来源：`novel2script_agent_project`  
目标系统：内容生产 Agent

## 1. 目的

本文档定义如何把旧 Novel2Script Agent 的作品、消息、文件、Run、Artifact、Approval 和 Event 安全迁移到内容生产 Agent，同时满足：

- 新项目与旧 Agent 代码和数据解耦；
- 原项目数据库不被新项目原地升级；
- 历史作品、剧本和版本不丢失；
- 历史数据可以查看、编辑、导出和作为新 Run 的起点；
- 新 Runtime 不继续依赖 `source_mode`；
- 新 API、Capability Registry 和 Artifact Version 模型成为唯一新增路径；
- 迁移失败可以在接受新写入前完整回退；
- 旧格式只作为兼容输入，不继续扩展。

本文档不执行真实迁移，不停止旧服务，也不修改原数据库。

## 2. 现场基线

### 2.1 源代码与数据分离

当前新项目中的：

```text
novel2script_agent_project/
```

是用于迁移和复用的代码副本，不包含生产数据。

原项目数据仍位于：

```text
C:\Users\egois\Desktop\novel2script_agent_project\runs
```

新系统不得直接把该目录当作自己的运行目录。

### 2.2 当前数据文件

现场只读盘点结果：

| 文件 | 大小 | Schema Version | 说明 |
|---|---:|---:|---|
| `workspace.db` | 约 680 KB | 2 | Project、Message、File |
| `runtime.db` | 约 6 MB | 1 | Run、Event、Artifact、Approval JSON Payload |
| `mock_runtime_state.json` | 约 1.3 MB | Legacy | Runtime SQLite 为空时的历史回退来源 |

现场还存在：

```text
workspace.db-wal
workspace.db-shm
```

因此迁移时禁止只复制 `workspace.db` 主文件。必须在停写或受控锁下使用 SQLite Backup API 创建一致性快照。

### 2.3 Workspace 数据规模

只读统计：

| 对象 | 数量 |
|---|---:|
| Projects | 29 |
| Messages | 410 |
| Files | 16 |

Project 状态：

| Source Mode | 状态 | 数量 |
|---|---|---:|
| `auto` | `idle` | 4 |
| `non_novel` | `completed` | 5 |
| `novel` | `completed` | 10 |
| `novel` | `failed` | 2 |
| `novel` | `idle` | 2 |
| `novel` | `paused` | 3 |
| `novel` | `waiting_approval` | 3 |

16 个文件总声明大小约 266 KB，当前 `content_base64` 总长度为 0。迁移器不能假设旧系统保留了所有上传文件的原始字节。

### 2.4 Runtime 读取限制

盘点时旧桌面应用和旧后端均在运行，`runtime.db` 无法被独立只读连接安全打开。

结论：

- 当前不对 Runtime 数量做猜测；
- 正式迁移前必须进入停写窗口；
- 先确认旧进程停止或持有迁移专用锁；
- 再通过 SQLite Backup API 生成快照并执行 `quick_check`；
- 迁移容量以快照后的真实统计报告为准。

### 2.5 旧数据库结构

`workspace.db`：

```text
projects
messages
files
workspace_meta
```

`runtime.db`：

```text
runtime_runs
runtime_events
runtime_artifacts
runtime_approvals
runtime_meta
```

旧 Runtime 的关键问题：

- Run、Artifact、Approval 主要业务字段保存为 JSON Blob；
- Artifact Version 通过多个独立 `artifact_id` 和 Payload 内 Version 隐式表达；
- Dependency 只保存为 `derived_from` ID 数组；
- 每次持久化会删除并重写 Runtime 表；
- Project 和 Runtime 分属两个数据库；
- 关键关联没有统一外键；
- Run、Project、Artifact 和前端都直接依赖 `source_mode`；
- Event 没有 Project 级单调序列；
- 文件正文保存在 SQLite 文本列，原文件字节不一定存在。

## 3. 采用的行业迁移模式

采用：

```text
Expand
-> Consistent Snapshot
-> Idempotent Import / Backfill
-> Compatibility Read
-> Blue-Green Cutover
-> Observe
-> Contract
```

同时使用：

- Anti-Corruption Layer：旧 DTO/字段只能通过兼容适配器进入新域模型；
- Immutable ID Mapping：旧 ID 到新 ID 的映射不可修改；
- Append-only Migration Ledger：每次迁移和异常都有记录；
- Shadow Verification：切换前比较旧快照与新查询结果；
- Forward-only Schema Migration：数据库迁移按版本顺序前进；
- Expand-and-Contract：先增加新结构，稳定后再退役旧接口。

第一版不采用长期双写。

原因：

1. 旧 Runtime 写入是内存状态整表重写，不是可可靠捕获的行级事务；
2. 新 Runtime 是规范化对象、不可变版本、依赖边和 Append-only Event；
3. 双写失败时难以证明哪一侧是真源；
4. 当前只有 29 个 Project，短停写导入的风险和成本更低；
5. 用户已经要求新项目与旧 Agent 解耦。

## 4. 迁移不变量

迁移实现必须满足：

1. 原始 `workspace.db`、`runtime.db` 和 Legacy JSON 永不原地修改；
2. 迁移只读取一致性快照，不直接读取运行中的源数据库；
3. 每个旧 ID 有且只有一个稳定映射；
4. 同一源快照重复执行导入，目标结果完全一致；
5. 正式切换前不覆盖现有目标数据库；
6. 迁移失败不启动新服务写入；
7. 不能把推断出来的 Dependency、Final Script 或 Approval 当作原系统事实；
8. 不能把旧 Artifact 伪装成符合新 `1.0.0` Capability Definition 生成的 Artifact；
9. 历史原始 Payload 必须可审计，但不能绕过新 Schema 参与新 Run；
10. 新写入只进入新 Runtime；
11. 旧 `/api/...` 只是兼容投影，不是第二状态真源；
12. 未完成旧 Run 不自动恢复执行；
13. 用户必须能看到哪些内容是历史导入、哪些已被规范化；
14. 所有文件字节、Payload、消息正文和剧本不得写入普通迁移日志；
15. 删除、过期和 Tombstone 语义在迁移后保持。

## 5. 目标存储布局

目标第一版：

```text
data/
├─ content_agent.db
├─ blobs/
│  ├─ originals/
│  ├─ variants/
│  ├─ parsed/
│  └─ staging/
├─ backups/
│  └─ <migration_id>/
└─ migration-reports/
   └─ <migration_id>/
```

`content_agent.db` 使用 `runtime-domain-model.md` 定义的规范化表。

二进制和大文本 Blob 进入受控文件存储；SQLite 只保存：

- 对象元数据；
- 关联；
- 状态；
- Hash；
- 受控 `storage_ref`；
- Artifact Payload；
- Snapshot；
- Event 和审计。

服务端绝对路径不返回前端。

## 6. Migration Ledger

目标库增加迁移专用表。

### 6.1 `legacy_imports`

```json
{
  "migration_id": "mig_...",
  "source_system": "novel2script_agent",
  "source_workspace_schema_version": 2,
  "source_runtime_schema_version": 1,
  "source_snapshot_hash": "",
  "importer_version": "1.0.0",
  "status": "planned | running | verified | committed | failed | rolled_back",
  "started_at": "",
  "verified_at": null,
  "committed_at": null,
  "report_ref": ""
}
```

### 6.2 `legacy_id_map`

```json
{
  "migration_id": "",
  "source_object_type": "project | message | file | run | artifact | approval | event",
  "source_id": "",
  "target_object_type": "project | message | asset | run | artifact_version | approval | event",
  "target_id": "",
  "mapping_reason": "preserved | generated_logical_parent | recovered | normalized",
  "created_at": ""
}
```

唯一约束：

```text
(migration_id, source_object_type, source_id)
```

### 6.3 `legacy_import_issues`

```json
{
  "issue_id": "",
  "migration_id": "",
  "project_id": "",
  "source_object_type": "",
  "source_id": "",
  "severity": "info | warning | blocking",
  "code": "",
  "message": "",
  "safe_metadata": {},
  "resolution": null
}
```

禁止在 Issue 中保存文件正文、剧本正文、API Key 或完整 Payload。

### 6.4 `legacy_payload_records`

仅在确有审计需要时保存：

- 源对象类型和 ID；
- Payload Hash；
- 加密/受控 Blob 引用；
- 导入器版本；
- 规范化结果 ID。

新 Runner 不允许读取该表作为正式输入。它只服务审计、恢复和人工排查。

## 7. 源快照与备份

### 7.1 迁移前条件

必须满足：

- 用户确认维护窗口；
- 停止旧桌面应用和旧后端的写入；
- 确认没有旧进程继续持有数据库；
- 记录旧二进制版本、Commit 和配置摘要；
- 校验磁盘剩余空间；
- 创建迁移目录；
- 迁移工具以只读方式打开源库。

### 7.2 一致性快照

不能使用普通 `Copy-Item workspace.db`。

流程：

```text
stop writes
-> open source DB
-> PRAGMA quick_check
-> SQLite Backup API to snapshot DB
-> close source DB
-> hash snapshot
-> mark source snapshots read-only
```

若 WAL 存在：

- Backup API 必须包含已提交 WAL 内容；
- 不手工拼接 `.db`、`.wal`、`.shm`；
- 不删除源 WAL；
- 不在源库上执行破坏性 Vacuum。

### 7.3 备份内容

```text
backups/<migration_id>/
├─ source/
│  ├─ workspace.snapshot.db
│  ├─ runtime.snapshot.db
│  └─ mock_runtime_state.snapshot.json
├─ manifests/
│  ├─ source-files.json
│  ├─ source-schema.json
│  └─ source-hashes.json
└─ reports/
```

Legacy JSON 只用于交叉验证或 Runtime SQLite 为空时的回退，不能与 Runtime SQLite 重复导入。

### 7.4 源选择优先级

```text
runtime.snapshot.db 有 Run
-> 使用 Runtime SQLite

runtime.snapshot.db 无 Run 且 Legacy JSON 有 Run
-> 使用 Legacy JSON

两者都有 Run
-> Runtime SQLite 为权威，Legacy JSON 只做差异报告
```

## 8. ID 策略

### 8.1 可直接保留的 ID

在不存在目标冲突时，保留：

- `project_id`
- `message_id`
- `file_id` 作为新 `asset_id`
- `run_id`
- 旧 `artifact_id` 作为新 `artifact_version_id`
- `approval_id`
- `event_id`

保留 ID 可以减少消息、选择上下文、Event 和旧链接的重写风险。

### 8.2 新增逻辑 Artifact ID

旧系统每个版本都有独立 `artifact_id`，新系统区分：

```text
Artifact Logical ID
Artifact Version ID
```

按以下 Key 分组：

```text
(project_id, run_id, artifact_type, scope_key)
```

`scope_key`：

| Artifact | Scope |
|---|---|
| `script_unit` | `episode:<episode_no>` |
| `video_script_unit` | `video_asset:<asset_id>` |
| 其他第一版 Artifact | `singleton` |

为每组生成一个确定性逻辑 ID：

```text
art_legacy_<stable_hash>
```

旧 `artifact_id` 保留为该组的 `artifact_version_id`。

### 8.3 冲突

若目标已有相同 ID：

1. 比较 Source Hash 和对象类型；
2. 完全相同视为幂等重复；
3. 不同则生成 Namespaced Target ID；
4. 写入 `legacy_id_map`；
5. 所有引用统一重写；
6. 不能覆盖目标对象。

## 9. Project 迁移

### 9.1 字段映射

| 旧字段 | 目标 |
|---|---|
| `project_id` | 保留 |
| `title` | 保留 |
| `status` | 由迁移后 Run/Candidate 状态投影 |
| `source_mode` | 不写入 Project 领域字段 |
| `active_run_id` | 仅新 Runtime 可执行 Run 才可成为 Active Pointer |
| `current_focus_artifact_id` | 转为精确 Artifact Version Pointer |
| `active_artifacts_json` | 转为 Project Artifact Selection/Projection |
| `created_at/updated_at` | 保留 |

### 9.2 `source_mode`

Project 在新系统中是中立 Workspace，不永久属于小说或非小说。

旧 `source_mode` 仅保存为：

```text
legacy_project_metadata.last_source_mode
```

兼容 API 可以读取该值生成旧 DTO，但新 Capability Router 不使用它作为执行授权。

### 9.3 Active Run

旧 Run 若是：

- `completed`、`failed`、`cancelled`：不设为 Active；
- `paused`、`waiting_approval`、`running`：导入为 Legacy Read-only Run；
- `running`：迁移时转为 `interrupted` 的恢复说明，不声称仍在执行。

Legacy Read-only Run 不占用新系统“同 Project 一个活动写 Run”的约束。

## 10. Conversation 与 Message 迁移

### 10.1 Conversation

旧消息直接挂 Project。每个 Project 创建：

```text
conversation_id = conv_legacy_<stable_project_hash>
```

全部旧消息按：

```text
created_at, message_id
```

排序导入。

### 10.2 Message

保留：

- Message ID；
- Role；
- 用户可见 Content；
- Created At；
- 原 Run 关联；
- Intent；
- Attachments；
- Selection Context。

`decision_context_json` 中的旧控制字段：

| 旧字段 | 新处理 |
|---|---|
| `source_mode` | 转为历史 Routing Snapshot |
| `next_action` | 只做审计，不作为当前 Available Action |
| `requires_generation_config` | 转为历史提示状态 |
| `generation_config` | 引用对应 Config Snapshot |
| `target_file_ids` | 转为 Asset 引用 |

### 10.3 Selection Context

映射：

```text
episode_id -> episode_no
artifact_id -> artifact_version_id
node_id -> 保留
scene_id -> 保留
line_id -> 保留
selected_text -> 保留
selection_hash -> 保留或重算并记录算法版本
```

无法映射到正式 Artifact Version 的 Selection 仍可随消息显示，但标记：

```text
selection_resolution = unresolved_legacy
```

不能把它当作可直接执行的新修订目标。

## 11. File 到 Asset 迁移

### 11.1 基本映射

```text
file_id -> asset_id
filename -> original_name
mime_type -> declared_mime_type
size_bytes -> declared_size
status -> asset status mapping
created_at -> uploaded_at
```

### 11.2 有原始 Base64

若 `content_base64` 非空：

1. 严格 Base64 解码；
2. 检测真实 MIME；
3. 计算 SHA-256；
4. 写入 Staging Blob；
5. `fsync`；
6. 原子移动到正式 Blob；
7. 创建 Original Blob 和 Asset Version；
8. 保存 Source File ID 映射。

解码失败是 Blocking Issue，不静默改用文本。

### 11.3 只有 `text_content`

当前现场 16 个 File 的 Base64 总长度为 0。

如果只有 `text_content`：

- 创建 `normalized_text` Blob；
- 创建 Parse Result；
- Asset Metadata 保留原文件名和 MIME 声明；
- 设置 `original_bytes_available=false`；
- 设置 `origin_fidelity=extracted_text_only`；
- 不声称已保留原 DOCX/PDF 原文件；
- UI 显示“历史导入，仅保留已提取文本”；
- 允许查看、编辑、导出 TXT；
- 不允许重新执行依赖原始二进制的解析。

### 11.4 只有 `text_preview`

若正文和 Base64 都为空：

- 保留 Asset Tombstone/Metadata；
- 保存 Preview；
- 标记 `content_availability=preview_only`；
- 创建 Warning；
- 不作为新 Run 的完整来源，除非用户明确确认并补充材料。

### 11.5 消息附件

旧 Message Attachment ID 必须通过 ID Map 转为 Asset Ref。

一个附件不能因为在多条消息中出现而复制多份 Blob。

## 12. Run 迁移

### 12.1 Capability 映射

| 旧 Source Mode | 目标 Capability |
|---|---|
| `novel` | `novel_to_script` |
| `non_novel` | `non_novel_to_script` |
| `auto/unknown` 且已有小说独有 Artifact | `novel_to_script` |
| `auto/unknown` 且已有非小说独有 Artifact | `non_novel_to_script` |
| 仍无法判断 | `legacy_unresolved`，只读 |

不能只根据自然语言标题猜 Capability。

### 12.2 Capability Version

历史 Run 不标记为新 `1.0.0` Definition 生成。

使用只读归档定义：

```text
novel_to_script@0.9.0-legacy-import
non_novel_to_script@0.9.0-legacy-import
```

该 Definition：

- 只用于历史展示和字段解释；
- 不允许创建新 Run；
- 不允许继续执行旧 Step；
- 不加载旧 Provider Key；
- 不进入用户 Skill 菜单；
- 在所有引用它的历史 Run 删除前随发布包保留。

### 12.3 Run 状态

| 旧状态 | 迁移状态 |
|---|---|
| `completed` | `completed` |
| `failed` | `failed` |
| `cancelled` | `cancelled` |
| `paused` | `paused` + `execution_mode=legacy_read_only` |
| `waiting_approval` | `waiting_approval` + `execution_mode=legacy_read_only` |
| `running` | `failed/interrupted` + 恢复建议 |
| `pending` | `paused` + 恢复建议 |

不新增无法被公共状态机识别的 Run Status。Legacy 差异放入 `execution_mode` 和 `migration_state`。

### 12.4 Config Snapshot

从旧：

```text
run.metadata.generation_config
run.metadata.generation_config_version
artifact.payload.generation_config_ref
```

生成不可变 Config Snapshot。

映射：

| 旧字段 | 新字段 |
|---|---|
| `target_episode_count` | 保留 |
| `episode_duration_minutes` | 保留 |
| `preserve_existing_episode_marks` | 保留 |
| `target_script_chars` | Legacy Execution Metadata |
| `target_source_chars_per_episode` | Legacy Execution Metadata |
| `boundary_detection_window_chars` | Legacy Execution Metadata |
| `existing_episode_markers_detected` | Input Analysis Metadata |
| `detected_episode_count` | Input Analysis Metadata |

缺少集数或时长时，Config Snapshot 可以标记 `legacy_incomplete=true`，但不能用于启动新生成 Step。

### 12.5 Run Input Snapshot

优先从：

1. `source_input` Artifact；
2. Run Start Message Attachments；
3. Project File 关联；
4. Source Text；

恢复。

旧 `source_input.payload.text` 若没有对应 Asset：

- 创建 Synthetic Text Asset；
- Hash 内容；
- 创建 Asset Snapshot；
- 把 Run Input Snapshot 绑定该 Asset；
- 不继续把全文复制进新 `source_input` Payload。

无法证明具体 File 的情况记录：

```text
input_lineage_completeness = partial
```

## 13. Step Run 与 Task 迁移

旧系统没有完整 Step Run、Task Item 和 Attempt 表。

迁移器根据 Event 和 Artifact 只恢复可证明的信息：

- Event 中明确出现的 Step；
- Artifact 对应的生成 Step；
- Approval 对应的等待 Step；
- 批量 Episode/Task Metadata。

不能证明时：

- 不伪造 Provider Attempt；
- 不伪造 Token、Duration 或 Retry 次数；
- Step Run 标记 `legacy_reconstructed=true`；
- 状态来源记录为 `event`、`artifact` 或 `approval`；
- 冲突时按保守状态处理并写 Warning。

历史 Step Run 只读。新 Continuation Run 使用新 Definition 创建全新 Step Run。

## 14. Artifact 与 Version 迁移

### 14.1 逻辑分组

按第 8 节创建 Logical Artifact 和不可变 Version。

每个旧 Artifact：

```text
old artifact_id -> target artifact_version_id
old version -> version number
old payload -> legacy normalized payload
old status -> version status
old created_at/updated_at -> preserve
```

### 14.2 Payload Schema

历史导入分两层：

1. `legacy_payload_record` 保存旧 Payload 审计引用；
2. Artifact Version Payload 经过 Artifact Adapter 规范化。

Adapter 必须显式版本化，例如：

```text
legacy_story_bible_v1_to_story_bible_v1
legacy_script_unit_v1_to_script_unit_v1
```

### 14.3 关键字段转换

```text
episode_id -> episode_no
source_mode -> 删除，来源由 Run Capability 表达
episode_cards -> episodes
file_id -> asset_id
derived_from -> Artifact Dependency
generation_config_ref -> Config Dependency
next_action -> 不进入 Payload
```

### 14.4 Script Unit

转换：

- `episode_id` 转 `episode_no`；
- 保留 Title、Script Text、Scenes、Source Refs；
- 缺 `line_id` 时按 Scene 顺序生成确定性 ID；
- 缺 `scene_id` 时生成确定性 ID；
- 保留旧 ID Map；
- `source_mode` 从 Payload 删除；
- 补齐空的 `continuity_delta`、`risk_notes` 和 `self_check`；
- 服务端重新从 Scenes 渲染 Script Text 并比较。

若 `script_text` 和 Scenes 不一致：

- 不静默覆盖；
- 标记 `SCRIPT_REPRESENTATION_DIVERGED`；
- 保留两份 Hash；
- 默认以旧前端实际编辑真源为准；
- 进入新编辑器前要求一次规范化确认。

### 14.5 Scripts Aggregate

旧 `scripts.script_units` 可能复制完整单集内容。

目标只保存：

```text
unit_refs[{episode_no, artifact_version_id}]
```

解析顺序：

1. 使用 `script_unit_artifact_id`；
2. 使用 Episode No + Version 匹配正式 Script Unit；
3. 若只有嵌入副本，创建 `legacy_recovered` Script Unit Version；
4. 无法恢复则标记 Aggregate 不完整；
5. 不因为嵌入副本不同而覆盖已有正式 Script Unit。

### 14.6 Schema 不通过

不能为了让导入成功而放宽正式 Schema。

失败处理：

```text
preserve legacy payload
-> create import issue
-> mark artifact version legacy_read_only
-> exclude from direct new Runner input
```

用户仍可查看和导出。要继续创作时，通过 Normalization/Continuation Run 生成新的正式版本并确认。

## 15. Dependency 迁移

### 15.1 可证明的 Dependency

迁移：

- `derived_from` Artifact IDs；
- Config Reference；
- Source Input 到 Asset Snapshot；
- Scripts Aggregate 到 Script Unit Versions；
- Episode Script 到 Episode Card；
- Event/Step 锁定元数据中的 Artifact ID + Version。

### 15.2 不完整 Lineage

旧系统没有完整 Dependency Graph。

每个导入 Version 增加：

```text
lineage_completeness = complete | partial | unknown
```

不允许根据 Artifact Type 顺序自动伪造精确依赖。

可以创建 `inferred_legacy_order` 诊断记录，但不能作为正式 `derived_from` Edge。

### 15.3 新下游

新 Run 若使用历史 Artifact：

1. 历史 Artifact 必须已规范化；
2. 用户确认采用该版本；
3. 新 Run Input Snapshot 绑定精确 Version；
4. 新 Artifact 创建完整 Dependency；
5. 历史 Lineage 不完整不影响新边的完整性，但 UI 必须显示来源限制。

## 16. Approval 迁移

保留：

- Approval ID；
- Run ID；
- Step ID 原值；
- Title、Reason、Options；
- Affected Artifacts；
- Status；
- User Response；
- Created/Resolved At。

旧 `affected_artifacts` 可能混合 Artifact Type 和 ID：

- 能映射 ID 的转 Approval Subject Snapshot；
- 只有 Type 的保留为 Legacy Scope；
- 无法解析的保留原审计值并告警。

### 16.1 已解决 Approval

导入为历史审计，不重新执行副作用。

### 16.2 Pending Approval

不允许直接调用新 `resolve approval` 执行旧 Step。

前端提供：

```text
查看历史待确认内容
-> 选择采用当前版本
-> 创建新 Continuation Run
-> 在新 Runtime 创建新的 Approval
```

原 Pending Approval 保持历史记录，并标记 `legacy_read_only=true`。

## 17. Event 迁移

### 17.1 顺序

旧 Event 有 Run Position，但没有 Project Event Seq。

生成：

```text
run_event_seq = old position + 1
```

Project Event Seq 按：

```text
created_at
-> source run stable order
-> old run position
-> event_id
```

确定性排序后分配。

### 17.2 Event Type

已知 Event Type 映射到新 Event Schema。

未知类型：

- 原类型写入 `legacy_event_type`；
- 新类型使用 `legacy_event_imported`；
- Payload 放在受控 Legacy Metadata；
- 前端可忽略但 Cursor 必须前进。

### 17.3 导入事件

迁移本身不冒充历史业务事件。

另外写入新的：

```text
project_legacy_imported
legacy_run_frozen
legacy_artifact_normalized
migration_issue_detected
```

这些 Event 的时间是迁移时间，不改写历史时间。

## 18. Candidate 与 Final Selection

旧系统没有可靠的 Final Selection 真源。

不能因为某个 `scripts` 是最新或 Run 已完成，就自动宣称它是用户最终稿。

迁移规则：

- 每个可完整展开的 `scripts` 创建 Legacy Script Candidate；
- 若只有确认的 Script Units，可创建 Candidate；
- Candidate 记录精确 Unit Versions；
- Candidate 状态为 `legacy_imported`；
- 不创建 Active Final Selection；
- 用户在新系统中首次明确选择后，才创建 Final Selection。

这符合“一个作品只有一个最终稿，但确认前保留多个历史版本”的产品边界。

## 19. 未完成 Run 的继续策略

### 19.1 不原地续跑

旧 Step ID、Context、Prompt、Schema 和 Runtime Cursor 与新 Definition 不同。

因此禁止：

```text
load old run
-> replace source_mode with capability_id
-> continue old cursor in new runner
```

### 19.2 Continuation Offer

对 `paused`、`waiting_approval`、`failed` 和迁移时 `running` 的 Run：

1. 找到最后一个可证明的主要 Artifact Version；
2. 尝试通过 Adapter 规范化；
3. 计算可继续的最早新 Step；
4. 创建 `continuation_offer`；
5. 展示来源、缺口、受影响下游和需要重新确认的内容；
6. 用户确认后创建新的 Run；
7. 新 Run 的 `parent_run_id` 指向旧 Run；
8. 新 Run 使用当前正式 Capability Version；
9. 不修改旧 Run 历史。

### 19.3 继续位置

| 最后可用产物 | 新 Run 起点 |
|---|---|
| `source_input` | Story Bible 或 Material Bank |
| `story_bible` | Episode Split |
| `episode_split` | Episode Cards |
| `material_bank` | Story Seed |
| `story_seed` | Series Blueprint |
| `series_blueprint` | Episode Cards |
| `episode_cards` | Script Units |
| 部分 `script_unit` | 缺失/受影响集生成 |
| `scripts` | Candidate/Final Selection 或 Revision |

所有起点仍需满足新 Schema、Config Snapshot 和 Dependency 约束。

## 20. API 兼容

### 20.1 新 API

所有新功能只进入：

```text
/api/v1
```

视频 Capability 不实现旧 API 专用入口。

### 20.2 旧 API

旧 `/api/...` 在兼容期由 Adapter 投影新状态：

| 旧接口 | 新实现 |
|---|---|
| Project List/Get | Project Query Projection |
| Project Messages | Conversation Message Query |
| Project Files | Asset Query |
| Upload File | Asset Upload/Commit |
| Project Message | 新 Message Command Adapter |
| Run Get | Run Snapshot Projection |
| Pause/Resume/Rerun | 新 Command API |
| Run Events | Event Query Projection |
| Run Event Stream | Project SSE 过滤为旧 Run Stream |
| Run Artifacts | Artifact Version Projection |

### 20.3 `source_mode` 兼容

旧响应中的 `source_mode` 按以下投影：

```text
novel_to_script -> novel
non_novel_to_script -> non_novel
video_reference_creation -> 不投影到旧 source_mode，旧客户端返回 capability_not_supported
无 Run -> legacy last_source_mode 或 auto
```

`video_reference_creation` 只存在于 `/api/v1`。兼容层不得把它伪装成 `unknown` 后继续执行旧分支，否则旧前端可能错误进入文本链。

旧请求中的 `source_mode_hint`：

- 只转为 Capability Routing Hint；
- 不写入 Project；
- 不直接启动 Run；
- 与显式 Capability Ref 冲突时返回错误，不猜测。

### 20.4 Artifact 兼容

旧 API 可以：

```text
episode_no -> episode_id
artifact_version_id -> artifact_id
Capability -> source_mode
```

只在响应 Adapter 中转换。数据库和新 API 不保存双字段。

### 20.5 兼容边界

旧 API：

- 不返回视频新功能；
- 不支持 Asset Set；
- 不支持新分析/Brief UI；
- 不允许绕过新 Idempotency 和 Version Check；
- 不维护独立状态；
- 不读取 `legacy_payload_records` 作为业务真源。

### 20.6 退役

兼容期至少跨：

- 新前端完整切换；
- 一轮内部真实样本回归；
- 无旧客户端访问观察期；
- 一次可回滚发布。

退役时先返回 Deprecation 信息，再在确认无调用后移除。

## 21. 前端兼容

### 21.1 第一阶段：宽读

旧前端类型适配器先接受：

- `episode_id` 或 `episode_no`；
- 有或无 `source_mode`；
- 旧 Artifact ID 或新 Artifact Version ID；
- 未知 Event Type；
- 未知 Artifact Type 的通用只读展示。

宽读只发生在 Adapter 层，领域组件只使用 Canonical View Model。

### 21.2 第二阶段：切换新 Shell

前端改为读取：

- Capability Public Manifest；
- Project Snapshot；
- Run Snapshot；
- Artifact Presentation Contract；
- Available Actions；
- Project SSE。

删除：

- Project 创建时选择永久 Source Mode；
- `SourceMode` 驱动导航；
- 固定 Novel/Non-novel Artifact 列表；
- Step ID 到 UI 文案的硬编码；
- `App.tsx` 中的双链分支。

### 21.3 本地状态

旧 LocalStorage：

```text
n2s.activeProjectId
n2s.workspace.<project_id>.view
n2s.workspace.<project_id>.scrollTop
```

迁移：

1. 新 Key 不存在时读取旧 Key；
2. 转换为新 Workspace UI State；
3. 写入新 Key；
4. 标记本地迁移版本；
5. 两个稳定版本后再停止读取旧 Key；
6. 不删除无法识别的旧 Key。

### 21.4 历史只读提示

旧 Run/Artifact 必须显示：

- 历史导入；
- 是否已规范化；
- 是否可以继续；
- 原文件是否仍可用；
- Lineage 是否完整；
- 当前可用操作。

不向普通用户展示数据库版本、迁移表名或内部 ID。

## 22. 分阶段实施

### M0：迁移工具和目标 Schema

交付：

- 新 DB Migration Framework；
- Target Schema；
- Migration Ledger；
- Source Snapshot Tool；
- Importer；
- Artifact Adapters；
- Verification Report；
- Dry-run 模式。

不接 UI，不切流量。

### M1：空库验证

使用合成 Fixture 覆盖：

- 小说完整 Run；
- 非小说完整 Run；
- 多版本 Artifact；
- 部分 Script Units；
- Pending Approval；
- Paused/Failed Run；
- 缺原始文件字节；
- Legacy JSON fallback；
- ID 冲突；
- 损坏 Payload；
- 重复导入。

### M2：真实快照 Dry Run

在维护窗口生成快照，但不切换：

```text
snapshot
-> import to staging
-> verify
-> generate report
-> destroy staging or retain encrypted
-> restart old service
```

目标是暴露真实字段差异和异常，不接受新系统写入。

### M3：兼容查询

新服务读取 Staging Import，执行 Shadow Query：

- Project 数量；
- Message 数量和顺序；
- Asset 数量；
- Run 数量和状态；
- Artifact 类型、版本和状态；
- Approval 数量和状态；
- Candidate 展开；
- 旧 API Projection。

不对用户开放写入。

### M4：前端切换验证

新前端连接新服务：

- 打开 29 个 Project；
- 查看历史聊天；
- 查看文件状态；
- 查看所有主要 Artifact；
- 打开 Script Editor；
- 检查多版本；
- 检查待确认/暂停/失败提示；
- 创建 Continuation Offer，但不正式执行模型。

### M5：正式 Cutover

```text
announce maintenance
-> stop old writes
-> final source backup
-> final import to fresh target staging
-> verify
-> atomically promote target data path
-> start new backend
-> health and smoke
-> start new frontend
-> enable writes
```

旧数据目录保持只读，不移动、不删除。

### M6：观察期

观察：

- 旧/新 Project 数差异；
- API 兼容调用；
- Import Issue；
- Continuation 成功率；
- Artifact Schema 错误；
- Blob 读取错误；
- SSE Seq；
- Idempotency Conflict；
- 用户反馈。

### M7：Contract

满足退役门禁后：

- 停止旧 API 写入；
- 删除旧前端 SourceMode 分支；
- 旧 Schema Adapter 保留只读；
- 不再打包旧 Runtime Executor；
- 不删除源备份；
- 后续按保留策略归档。

## 23. 为什么不长期双写

不采用：

```text
old runtime write
-> old DB
-> best-effort mirror to new DB
```

也不采用：

```text
new command
-> independently write old and new DB
```

原因：

- 两个事务无法原子提交；
- 旧 Runtime 会整表重写；
- Artifact Identity 语义不同；
- Event 顺序语义不同；
- Approval 副作用可能重复；
- 失败恢复会产生双真源。

兼容期间的原则是：

```text
one write path
+ multiple read projections
```

Cutover 前旧库是写真源；Cutover 后新库是写真源。

## 24. 校验报告

每次 Import 生成：

### 24.1 数量

- Project；
- Conversation；
- Message；
- File/Asset；
- Run；
- Artifact/Version；
- Approval；
- Event；
- Candidate；
- Issue。

### 24.2 关联

- Message Project 存在；
- Attachment Asset 存在或明确 Tombstone；
- Run Project 存在；
- Artifact Run/Project 一致；
- Artifact Version 分组正确；
- Dependency Ref 可解析；
- Approval Subject 可解析；
- Event Run/Project 可解析；
- Active Pointer 指向有效对象。

### 24.3 内容

只比较 Hash 和结构，不在日志输出正文：

- Message Content Hash；
- Asset Content Hash；
- Artifact Payload Hash；
- Script Scene/Line Count；
- Script Text Hash；
- Episode Count；
- Version Count。

### 24.4 状态

- Completed Run 不变成 Running；
- Running Run 转 Interrupted；
- Pending Approval 不执行；
- Superseded/Invalidated 状态保留；
- Legacy Read-only 不占活动写锁；
- 不自动创建 Final Selection。

### 24.5 Schema

- 规范化 Artifact 通过目标 Schema；
- 不通过的进入 Legacy Read-only；
- Import Issue 数量明确；
- Blocking Issue 为 0 才可 Cutover。

## 25. Cutover 门禁

必须全部满足：

- 两个源 Snapshot `quick_check=ok`；
- Source Hash 已记录；
- Importer 对同一 Snapshot 重跑结果一致；
- 29 个 Project 全部可查询；
- 410 条 Message 数量和顺序一致；
- 16 个 File 全部映射为 Asset/Metadata；
- Runtime 实际 Run/Artifact/Approval/Event 数量对账通过；
- 所有 ID Map 唯一；
- Blocking Issue 为 0；
- 所有旧 Script 可阅读或明确列入 Issue；
- Pending/Paused/Failed Run 有明确继续路径；
- 旧 API Smoke 通过；
- 新 API Snapshot/SSE Smoke 通过；
- 新前端关键流程通过；
- 原项目数据备份可恢复；
- 新服务尚未接受用户写入前完成最终 Go/No-Go。

## 26. 回滚

### 26.1 接受新写入前

可以完整回滚：

1. 停止新服务；
2. 取消目标数据路径 Promotion；
3. 保留失败目标库和报告；
4. 重启旧服务；
5. 旧服务继续使用未修改的源数据库；
6. 写入 Migration Rollback 记录。

### 26.2 接受新写入后

不能简单重启旧二进制，否则会丢失新系统写入。

此时回滚定义为：

- 回滚到仍支持新数据库 Schema 的上一版新服务；
- 或使用目标数据库切换前备份；
- 必要时 Fix-forward；
- 不自动把新数据反向降级写回旧 Runtime；
- 不承诺视频、Asset Set、Dependency 等新对象可降级到旧系统。

因此：

```text
旧系统回退门
=
正式开放新写入之前
```

开放新写入之后，只做新架构内的应用版本回滚。

### 26.3 数据库 Contract 延迟

为支持新架构内版本回滚：

- Schema Migration 先 Add，不立即 Drop；
- 新服务至少兼容前一数据库 Minor Version；
- 字段/表删除延后两个稳定发布；
- 删除前确认无代码和查询使用；
- 所有 Migration 有向前修复脚本，不依赖危险 Down Migration。

## 27. 安全与隐私

- Snapshot 和备份目录权限最小化；
- Migration Report 不含正文；
- 不打印 API Key、Provider URL 和完整 Prompt；
- Blob 使用随机/Hash Storage Key，不用原文件名；
- 禁止把客户端绝对路径写入目标 Asset；
- 临时 Staging 最长保留 24 小时；
- 迁移失败清理 Staging，但保留安全审计；
- 备份保留周期由发布策略确认，不能自动随视频 7 天策略删除；
- 迁移工具只接受受控源目录；
- 防止路径穿越和 Symbolic Link 越界；
- 任何删除使用明确 Migration ID 和白名单目录。

## 28. 可观测性

指标：

```text
legacy_import_objects_total{type,status}
legacy_import_issues_total{severity,code}
legacy_import_duration_seconds
legacy_adapter_read_total{adapter,result}
legacy_continuation_offer_total{result}
legacy_api_requests_total{route,status}
artifact_normalization_total{type,result}
```

日志只记录：

- Migration ID；
- Object Type；
- Safe ID；
- Adapter Version；
- Issue Code；
- Duration；
- Result。

不记录 Payload。

## 29. 实现包划分

建议模块：

```text
internal/migrations/
├─ schema/
├─ snapshot/
├─ legacyimport/
│  ├─ workspace/
│  ├─ runtime/
│  ├─ artifacts/
│  └─ verification/
└─ reports/

internal/compat/
├─ legacyapi/
├─ legacydto/
├─ legacyartifacts/
└─ continuation/
```

边界：

- Migration 代码不进入 Agent Prompt；
- Compatibility Adapter 不写第二份业务状态；
- Artifact Adapter 不执行模型调用；
- Continuation 由新 Runtime 创建新 Run；
- 旧 Executor 不注册为可执行 Capability。

## 30. 实施顺序

1. 建立 Target Schema Migration Framework；
2. 建立 Migration Ledger；
3. 建立源快照工具；
4. 建立 Workspace Importer；
5. 建立 Runtime Blob Reader；
6. 建立 ID Mapper；
7. 建立 Asset/Blob Importer；
8. 建立 Run/Config/Input Snapshot Importer；
9. 建立 Artifact Adapters；
10. 建立 Dependency、Approval 和 Event Importer；
11. 建立 Candidate Importer；
12. 建立 Verification Report；
13. 建立 Legacy Query Adapter；
14. 建立 Continuation Offer；
15. 建立前端宽读 Adapter；
16. 合成 Fixture 回归；
17. 真实快照 Dry Run；
18. 新前端 Shadow 验收；
19. 正式 Cutover；
20. 观察后退役旧写路径。

## 31. 第一版明确不做

- 原地 ALTER 旧数据库到新结构；
- 长期双写两个 Runtime；
- 自动把旧 Pending Approval 当作已确认；
- 自动恢复旧 Running Cursor；
- 自动推断最终稿；
- 自动补造缺失 Dependency；
- 把历史 Payload 校验失败直接丢弃；
- 把旧 Source Mode 保留为新 Project 永久属性；
- 为旧 API 增加视频功能；
- 在未备份时清理原项目数据；
- 在 Cutover 后把新数据降级回旧 Runtime。

## 32. 本项完成门禁

- 已明确源数据、目标数据和解耦边界；
- 已明确原库不原地修改；
- 已明确 WAL 一致性快照方法；
- 已明确 Project、Message、File、Run、Artifact、Approval、Event 映射；
- 已明确 Artifact Logical ID 与 Version ID 的转换；
- 已明确 Source Mode 到 Capability 的转换；
- 已明确历史 Schema 不通过时的处理；
- 已明确未完成 Run 的 Continuation，而不是强行续跑；
- 已明确旧 API 和前端兼容边界；
- 已明确不长期双写；
- 已明确 Cutover 前后不同的回滚能力；
- 已明确数据对账、隐私和观察指标；
- 已明确实现顺序和验收门禁。
