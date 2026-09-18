# 验收用例与测试清单

本文档定义 Novel2Script Agent 进入前后端开发后的验收标准。它不是简单功能列表，而是把产品规则、Agent 行为协议、API 事件协议、共享 schema 和前端交互要求转成可验证用例。

## 测试分层

```text
Unit
  - router / intent
  - schema validation
  - artifact dependency invalidation

API
  - projects
  - messages
  - runs
  - events
  - artifacts
  - approvals
  - files

Runtime
  - mock runtime
  - future Eino runtime
  - model fallback / failure

Frontend
  - left navigation
  - artifact content rendering
  - Agent message panel
  - approval card
  - composer
  - local file attachment

E2E
  - chat only
  - non-novel generation
  - novel generation
  - waiting approval
  - pause / resume
  - local revise
  - failure / retry
```

## 验收数据

至少准备以下 fixture：

### fixture_chat

```text
用户输入：你好
预期：只返回 Agent 回复，不创建 run。
```

### fixture_non_novel_seed

```text
用户输入：一个宫斗复仇短剧灵感：被废弃的贵女重回宫廷，利用旧案反杀仇人。
预期：source_mode=non_novel，生成素材库、故事种子、剧集蓝图、分集卡，并在分集卡等待确认。
```

### fixture_novel_excerpt

```text
用户输入：一段带章节、连续叙事、人物行动的小说原文。
预期：source_mode=novel，生成故事圣经、原文拆集、分集卡，并在关键 checkpoint 等待确认。
```

### fixture_file_upload_txt

```text
文件：txt 小说或梗概
预期：上传后显示 attachment chip，message 通过 file_id 引用，不把文件名或正文塞进 textarea。
```

### fixture_selection_script

```text
artifact：scripts
selection：第 1 集第 2 场某句对白，及第 1 集跨第 2、3 场的连续多行文本
预期：单行 selection_context 含 artifact_id、version、node_id、scene_id、selected_text、before_context、after_context、selection_hash；多行还含 start/end scene_id、start/end line_id、有序 line_ids、首尾 offset 和 selection_scope=range。
```

### fixture_script_editor_spike

```text
文件：fixtures/script-editor-spike-fixture.json
用途：Tiptap / Lexical 两个 ScriptEditor spike 必须共用这份数据。
预期：可以导入、编辑、生成选区引用、展示 AI patch、添加批注、导出 JSON 并恢复。
```

## 01. 闲聊与意图识别

### TC-INTENT-001 闲聊不启动 run

步骤：

1. 创建空项目。
2. 发送 `你好`。

预期：

- `POST /api/projects/{project_id}/messages` 返回 `run: null`。
- project.status 仍为 `idle`。
- 不创建 artifact。
- 左侧目录不展开小说 / 非小说完整链路。
- 右侧 Agent 只显示普通回复。

### TC-INTENT-002 问号不启动 run

步骤：

1. 发送 `?`。

预期：

- 不创建 run。
- Agent 可以追问或说明可输入小说 / 灵感 / 素材。
- 不出现分集卡确认卡。

### TC-INTENT-003 否定生成不启动 run

步骤：

1. 发送 `先不要生成，我只是想问问流程`。

预期：

- intent 为 `chat` 或 `project_control`。
- 不创建 run。
- Agent 解释流程，不生成 artifact。

### TC-INTENT-004 明确非小说生成启动 run

步骤：

1. 发送非小说灵感，并明确说生成短剧。

预期：

- intent 为 `generate_non_novel`。
- source_mode 为 `non_novel`。
- 创建 run。
- 创建 source_input artifact。

### TC-INTENT-005 明确小说改编启动 run

步骤：

1. 上传或粘贴小说原文。
2. 发送 `把这段小说改成短剧`。

预期：

- intent 为 `generate_novel`。
- source_mode 为 `novel`。
- 创建 run。
- 创建 source_input artifact。

## 02. Project / Run 状态

### TC-RUN-001 run 创建后的状态

步骤：

1. 发送明确生成请求。

预期：

- project.status 从 `idle` 变为 `running`。
- project.active_run_id 指向新 run。
- run.status 为 `running`。
- run_event 包含 `run_started` 和 `intent_detected`。

### TC-RUN-002 run 完成后状态

步骤：

1. 让 mock runtime 从 approval 后继续到 scripts 完成。

预期：

- run.status 为 `completed`。
- project.status 为 `completed` 或 `idle`，但必须能查看 latest run。
- 左侧剧本状态为 `已生成`。
- 中间自动切到 scripts 或保持用户当前视图但有完成提示。

### TC-RUN-003 同项目 active run 冲突

步骤：

1. 在 run.running 时再次发送明确新生成请求。

预期：

- 后端不静默创建第二个 active run。
- 返回提示：当前任务正在运行，可暂停、取消或等待完成。
- 如果允许排队，必须返回明确 queued 状态；第一版可以不支持排队。

## 03. 非小说主流程

### TC-NONNOVEL-001 非小说生成到分集卡等待确认

步骤：

1. 使用 fixture_non_novel_seed。

预期 artifact：

- source_input: `已生成`
- material_bank: `已生成`
- story_seed: `已生成`
- series_blueprint: `已生成`
- episode_cards: `待确认`
- scripts: `待生成`

预期 run：

- run.status 为 `waiting_approval`。
- approval_request 存在。
- approval_request.title 是用户可理解文案。

预期 UI：

- 左侧只显示非小说链路。
- 中间展示分集卡字段化内容。
- 右侧 Agent 对话中嵌入 approval card。

### TC-NONNOVEL-002 非小说链路不展示小说专属目录

步骤：

1. 非小说 run 创建后查看左侧目录。

预期：

- 不展示 `故事圣经`、`原文拆集` 作为当前链路主条目。
- 展示 `素材库`、`故事种子`、`剧集蓝图`、`分集卡`。

## 04. 小说主流程

### TC-NOVEL-001 小说生成到分集卡等待确认

步骤：

1. 使用 fixture_novel_excerpt。

预期 artifact：

- source_input: `已生成`
- story_bible: `已生成` 或 `待确认`，取决于 approval_mode。
- episode_split: `已生成` 或 `待确认`。
- episode_cards: `待确认`。
- scripts: `待生成`。

预期 UI：

- 左侧只显示小说链路。
- 中间能够查看故事圣经 / 原文拆集 / 分集卡。
- 不展示非小说素材库、故事种子、剧集蓝图作为当前链路主条目。

### TC-NOVEL-002 source_chunks 不作为主 artifact

步骤：

1. 小说拆集完成。

预期：

- 主 artifact 是 `episode_split`。
- `source_chunks` 不出现在左侧主目录。
- 来源引用在 `episode_split.payload.episodes[].source_refs` 或等价字段中。

## 05. Approval

### TC-APPROVAL-001 分集卡确认后继续剧本生成

步骤：

1. run 到 episode_cards waiting_approval。
2. 点击 `确认继续`。

预期：

- `POST /api/approvals/{approval_request_id}/resolve` action 为 `approve`。
- run.status 从 `waiting_approval` 变为 `running`。
- events 包含 `approval_resolved`、`run_resumed` 或 `step_started`。
- 后续生成 scripts。

### TC-APPROVAL-002 补充要求不创建新 run

步骤：

1. run 到 waiting_approval。
2. 用户输入 `第 2 集女主要更主动一点`。

预期：

- 不创建新 run。
- 当前 approval resolve 为 `revise_instruction` 或产生 approval update。
- 当前 run 继续使用同一个 run_id。
- 分集卡被更新或当前步骤重跑。

### TC-APPROVAL-003 暂停后保持审批上下文

步骤：

1. run 到 waiting_approval。
2. 点击或输入 `暂停`。

预期：

- run.status 为 `paused`。
- approval card 或暂停卡仍能说明当前停在哪。
- 用户后续可以继续、取消或补充说明。

## 06. Pause / Resume

### TC-PAUSE-001 运行中暂停

步骤：

1. run.running 时发送 `暂停`。

预期：

- run.status 变为 `paused`。
- 不删除已生成 artifact。
- 右侧 Agent 说明已暂停和可继续方式。

### TC-PAUSE-002 暂停时补充说明不自动继续

步骤：

1. run.paused。
2. 发送 `后面男主身份不要太早暴露`。

预期：

- 说明被记录为 pending instruction。
- run 仍为 paused。
- 不自动生成下一步。

### TC-PAUSE-003 暂停后继续

步骤：

1. run.paused。
2. 发送 `按刚才补充继续`。

预期：

- run.status 变为 running 或 waiting_approval。
- 补充说明进入当前 run 上下文。
- 不创建新 project。

## 07. 文件上传

### TC-FILE-001 上传 txt 文件

步骤：

1. 点击添加文件。
2. 上传 txt。

预期：

- 返回 FileRef。
- 附件显示为 chip。
- textarea 不插入文件名。
- 文件状态为 `uploaded` 或 `parsed`。

### TC-FILE-002 发送带附件消息

步骤：

1. 上传文件后输入 `按这个生成短剧`。
2. 发送。

预期：

- message.request 包含 `file_ids`。
- message.content 不包含文件正文。
- source_input artifact 引用 file_id。

### TC-FILE-003 不支持文件类型

步骤：

1. 上传不支持的二进制文件。

预期：

- 返回 `FILE_TYPE_UNSUPPORTED`。
- 前端显示可恢复错误。
- 不创建 source_input artifact。

### TC-FILE-004 后续消息按意图读取已上传附件

步骤：

1. 上传一个 txt 文件，但不随上传消息发起生成。
2. 发送普通问候。
3. 再发送“分析一下刚才的文件”。

预期：

- 上传和普通问候都不启动 run，也不把完整正文发送给模型。
- Main Agent 的项目上下文持续包含轻量文件索引，附件不会因消息轮次变化而丢失。
- 分析请求被识别为 `inspect_source`，内容 Worker 只读取该文件并直接回复。
- 项目保持 `idle`，不创建 `source_input` 或其他 artifact。
- 不自动把附件标记为小说。

### TC-FILE-005 多附件指代不明确时追问

步骤：

1. 在同一作品上传两个文件。
2. 发送“分析一下这个文件”，且当前消息没有附带 file_id、文件名或其他唯一定位信息。

预期：

- Agent 询问要分析哪个文件。
- 不读取两个文件的完整正文。
- 不启动 run，不创建 artifact。

## 08. Artifact 渲染与编辑

### TC-ARTIFACT-001 中间不展示裸 JSON

步骤：

1. 打开 material_bank、story_seed、episode_cards、scripts。

预期：

- 中间展示字段化内容。
- 技术 JSON 只允许在调试详情或运行记录中查看。

### TC-ARTIFACT-002 artifact patch 版本冲突

步骤：

1. 前端读取 artifact version=1。
2. 后端已有 version=2。
3. 前端提交 base_version=1 patch。

预期：

- 返回 `CONFLICT`。
- 不覆盖 version=2。
- 前端提示刷新后再修改。

### TC-ARTIFACT-003 上游变更触发下游处理确认

步骤：

1. 修改 story_bible 或 episode_split，且下游已有内容。

预期：

- 修改完成后下游先保持原状态和可见性，并显示“保留现有后续内容 / 重新生成受影响内容”。
- 选择保留时下游不变，`invalidated_artifacts` 为空。
- 选择重新生成时，仅实际受影响的下游标记 `stale` 并记录 ID；新版本到达前旧内容继续可见。

## 09. 局部修改

### TC-REVISION-000 问候不得复用历史选区

1. 用户框选剧本并完成一次局部修改。
2. 上游 artifact 产生新版本并等待确认。
3. 用户发送“你好”。

预期：

- 返回 `chat_idle + reply`。
- 不产生 revision task，不调用内容模型。
- 原待确认 artifact 和 approval 保持不变。

### TC-REVISION-000A 模糊修改先澄清

1. 用户框选一句剧本。
2. 用户只发送“修改这句话”。

预期：

- Agent 询问希望改成什么方向。
- 不执行 patch，不产生新 artifact 版本。
- 当前选区保留，供下一轮明确要求使用。

### TC-REVISION-000B 跨产物审批隔离

1. 故事圣经新版本处于 `pending_approval`。
2. 用户要求修改某集剧本。

预期：

- Agent 提示先处理故事圣经确认。
- 故事圣经 approval 不被 resolve、替换或删除。
- run 不进入剧本 revision task。

### TC-REVISION-000C 修改失败恢复原审批

1. 某 artifact 正在等待确认。
2. 用户修改同一 artifact，但 worker 返回无变化或失败。

预期：

- 原 artifact 仍可见且保持 `pending_approval`。
- 原 approval 恢复为当前确认卡。
- 不产生空的新版本，不进入不可恢复的 failed 状态。

### TC-REVISE-001 有选区的局部改写

步骤：

1. 在 scripts 中选中一句对白。
2. 输入 `这句更像女主，不要太软`。

预期：

- message 携带 selection_context。
- Agent intent 为 `revise_selection`。
- 修改范围为 `selection_only` 或 `node_level`。

### TC-REVISE-001B 明确选区修改不重复调用控制模型

1. 在已完成剧本中选中一行。
2. 输入明确方向，例如“把这个动作夸大一些”。
3. 记录控制模型与内容模型调用。

预期：

- Main Agent 确定性守卫直接返回 `revise_checkpoint + patch_script_span`。
- 控制模型调用为 0，内容模型调用为 1。
- 内容模型仍获得目标整集和必要上游上下文。
- 新版本只修改目标行，其他行逐字不变。

### TC-REVISE-001C 控制模型超时后重试选区请求

1. 选区修改在控制模型判断阶段超时。
2. 用户输入“重试”。

预期：

- Agent 回复明确说明控制模型超时，不显示“不支持”或“前端断线”。
- 重试继承原选区和原修改方向，不把“重试”当成内容指令。
- 不出现只承诺修改但没有 revision task 的消息。

### TC-REVISE-001D 选区引用随消息发送

1. 选中文字后观察输入区引用。
2. 点击发送并刷新页面。

预期：

- 发送后输入区引用清空。
- 用户消息显示引用选区、可读位置和原始版本号。
- 刷新后历史引用仍存在，不重新占用输入区。
- 不重写整集。

### TC-REVISE-001A 同集跨行和跨场景局部改写

步骤：

1. 在同一集内从某行中部拖选到后续场景某行中部。
2. 输入明确的局部改写要求。
3. 分别用正向和反向拖选重复验证。

预期：

- selection_context 按文档顺序携带完整 `line_ids`，反向拖选结果一致。
- 首行只替换起点之后的选区，末行只替换终点之前的选区，中间行替换整行选区。
- 行数、line_id、scene_id、speaker 和 block_type 不变。
- scripts 聚合正文同步更新，但同集未选中内容和其他集不变。

### TC-REVISE-002 无选区但指向明确 artifact

步骤：

1. 当前打开 episode_cards。
2. 输入 `第 2 集尾钩加强`。

预期：

- intent 为 `revise_artifact`。
- Agent 判断范围为 episode_level。
- 如果影响 scripts，说明 scripts 会失效或需要后续重写。

### TC-REVISE-003 高影响修改请求确认

步骤：

1. 输入 `把 20 集改成 12 集`。

预期：

- 不直接覆盖分集卡。
- 请求 approval。
- approval 说明会影响 episode_split、episode_cards、scripts。

### TC-REVISE-004 两条链路目标与上下文矩阵

步骤：

1. 在已完成 run 中依次指定小说链路的 story_bible、episode_split、episode_cards、某集 script_unit。
2. 依次指定非小说链路的 material_bank、story_seed、series_blueprint、episode_cards、某集 script_unit。
3. 当前页面保持在另一个 artifact，分别发起字段、实体、区块和整步重生成请求。

预期：

- 明确目标不借用当前页面的 artifact_id；每个目标解析到其最新有效版本。
- 上下文按目标节点读取必要上游，不按 run 当前步骤读取。
- 内容模型不能改写 Main Agent 已确认的 field_path、entity、scene_id 或 episode_id。
- 局部修改只改变目标范围，完整重生成只替换目标 artifact；版本递增且旧版本 superseded。

### TC-REVISE-005 命名实体与扩展字段

步骤：

1. 修改 `characters[主角].goal` 或 `characters[name=主角].goal`。
2. 新增、删除一个列表实体。
3. 修改目标 payload 中真实存在但未进入固定字段表的扩展字段。

预期：

- 命名选择器只命中目标人物，兄弟实体不变。
- 新增和删除只改变目标列表项，不把数组替换成对象。
- 真实扩展字段允许局部修改；不存在的路径被拒绝。

### TC-REVISE-006 剧本场景与整集刷新

步骤：

1. 重写包含增删行的一个场景。
2. 重写指定集剧本，同时保留其他集。

预期：

- 场景结构与 `script_text` 完整同步，不残留旧行。
- 指定集生成 v+1，其他集版本和正文不变。
- `scripts` 聚合生成新版本并包含最新目标集，不额外弹出下游处理确认。

## 10. Agent 对话与执行步骤

### TC-AGENT-001 内部 step done 不用绿色确认勾

步骤：

1. 查看 Agent 执行步骤。

预期：

- completed step 使用低权重 done 文本或状态点。
- 不使用绿色圈勾表示内部步骤完成。
- 绿色勾只用于用户确认或提交成功等强成功事件。

### TC-AGENT-002 审批卡嵌入对话流

步骤：

1. run 到 waiting_approval。

预期：

- approval card 显示在 Agent 消息中。
- 不脱离对话流浮在独立区域。
- 卡片含标题、原因、影响产物、操作按钮。

### TC-AGENT-003 输入框 Enter 发送

步骤：

1. 在 Agent 输入框输入文本。
2. 按 Enter。

预期：

- 发送 message。
- Shift + Enter 换行。
- 发送后输入框清空。

## 11. 错误恢复

### TC-ERROR-001 模型调用失败

步骤：

1. mock runtime 模拟 model timeout。

预期：

- run_event 包含 `step_failed`。
- run.status 为 `failed` 或 step.status 为 `failed`。
- 前端显示错误卡。
- 用户可以重试或暂停。

### TC-ERROR-002 模型输出结构错误

步骤：

1. mock runtime 返回缺失必要字段的 artifact payload。

预期：

- 后端先尝试自修或 schema validation。
- 如果仍失败，返回 `MODEL_OUTPUT_INVALID`。
- 不保存不合法 artifact 为 active version。

### TC-ERROR-003 SSE 断开后补拉事件

步骤：

1. 运行中断开 SSE。
2. 重新连接。

预期：

- 前端用 `after_event_id` 拉取缺失事件。
- 不重复显示已展示事件。
- run 状态以最新后端状态为准。

## 12. 前端信息架构

### TC-UI-001 左侧目录只显示当前链路

步骤：

1. 运行非小说生成。

预期：

- 左侧不同时摊开小说链和非小说链。
- 未识别前只显示基础目录。

### TC-UI-002 中间不放 AI 操作按钮

步骤：

1. 查看分集卡、剧本、素材库等中间内容区。

预期：

- 中间不出现 `生成剧本`、`局部改写`、`重生成` 这类 AI 指令按钮。
- AI 需求从右侧 Agent 发起。

### TC-UI-003 发送按钮文案

步骤：

1. 查看 Agent 输入框。

预期：

- 主发送按钮文案为 `发送`。
- 不出现 `发送给 Agent`。
- 主按钮不换行。
- 按钮文案水平、垂直居中；菜单项、列表项、表格行、树节点等明确左对齐控件除外。

## 13. API 合同

### TC-API-001 Message 闲聊响应

请求：

```text
POST /api/projects/{project_id}/messages
content=你好
```

预期：

- HTTP 200。
- response.run 为 null。
- response.agent_message 存在。

### TC-API-002 Message 生成响应

请求：

```text
POST /api/projects/{project_id}/messages
content=根据这个灵感生成短剧...
```

预期：

- HTTP 200。
- response.run 存在。
- response.project.active_run_id 等于 run_id。
- events 至少包含 run_started。

### TC-API-003 Approval resolve

请求：

```text
POST /api/approvals/{approval_request_id}/resolve
action=approve
```

预期：

- HTTP 200。
- approval.status 为 approved。
- run.status 不再是 waiting_approval。

## 14. 开发完成判定

正式进入前后端主开发后，每次完成一个大阶段至少要通过：

1. 闲聊不启动 run。
2. 非小说主流程到分集卡等待确认。
3. approval approve 后生成 scripts。
4. 等待确认时补充说明不创建新 run。
5. 文件上传显示 chip 且通过 file_id 进入 message。
6. 局部修改携带 selection_context。
7. step failed 可恢复。
8. 左侧目录链路正确。
9. 中间 artifact 字段化展示。
10. 右侧审批卡嵌入对话流。

这些是第一版最小 E2E 验收，不代表完整产品测试。
## 10. 结构修改与上下文预算补充（2026-07-17）

### TC-REVISE-COLLECTION-001 通用列表操作

分别对小说和非小说主要 artifact 的列表字段执行拆分、合并、插入、删除和移动。

预期：

- Main Agent 输出 `patch_artifact_collection` 和准确 field_path；
- worker 只返回一个 collection operation；
- 运行时只改变目标区间，相邻项和无关字段保持不变；
- episode 列表自动连续编号并同步权威集数配置；
- 新 artifact 版本递增，旧版本 superseded。

### TC-CONTEXT-002 局部修改不传全文

对长原文拆集、长剧本单句和任意中间产物字段执行局部修改。

预期：目标 artifact 不重复；剧本只传附近 block；原文只传目标 offset 窗口；历史对话和事件受限；输出与替换范围仍准确。

### TC-RETRY-004 恢复审批后的失败任务重试

局部修改调用失败并恢复原审批点后，用户明确发送“重新试一下”。

预期：从 `last_failed_task` 游标恢复同一 revision，不丢 instruction，不重跑未失败步骤；询问失败原因时不自动重试。

## 15. 发布准备与可观测性（2026-07-17）

### TC-RELEASE-001 正式包单地址运行

执行 `scripts/package.ps1`，从独立目录启动正式包。

预期：前端和 API 使用同一本地地址；首页与前端路由 HTTP 200；未知 API 返回 404；浏览器无资源 403、控制台错误和横向溢出；发布包不包含 `.env`、开发数据库或模型日志。

### TC-RELEASE-002 安全退出

通过正式启动脚本启动，再执行停止脚本。

预期：正确 token 返回 202；错误 token 和未配置 token 返回 404；后端先关闭 HTTP、取消并等待 runtime job，再退出；PID 文件被清理。

### TC-BACKUP-001 备份恢复闭环

应用停止后备份两个 SQLite 数据库，再恢复到另一个数据目录。

预期：备份包含 manifest、workspace 和 runtime 数据；不包含 API Key 和 trace；恢复前自动备份当前数据；恢复后两个数据库均通过 SQLite 文件校验并可被服务读取。

### TC-MIGRATION-001 拒绝未来数据库版本

分别把 workspace 和 runtime 数据库 `user_version` 设置为高于当前程序支持版本。

预期：服务拒绝打开数据库并给出版本不兼容错误，不覆盖或降级数据库。

### TC-OBSERVE-001 脱敏日志查看

执行 `scripts/view-logs.ps1`。

预期：只展示模型调用元数据和定位字段；不展示完整 prompt、response、API Key 或 provider 原始错误正文。
