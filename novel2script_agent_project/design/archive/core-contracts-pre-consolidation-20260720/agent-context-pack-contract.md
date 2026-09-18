# Agent Context Pack Contract

本文档定义 Novel2Script Main Agent 每轮决策前应接收的上下文包。它是业务合同，不绑定当前 Go runtime，也不绑定未来 Eino；后续接 Eino 时应把本合同映射为 Eino Agent / Graph / Tool / Interrupt 的输入与读取策略。

## 1. 目标

Main Agent 不应只看当前一句话做判断。每次决策前，后端必须组装 `AgentContextPack`，让模型能理解：

- 用户现在说了什么。
- 用户最近几轮在说什么。
- 当前项目处在哪个流程状态。
- 当前步骤已经生成了什么内容。
- 当前用户正在看或选中哪个产物。
- 当前是否卡在 approval、failed、paused。
- 如果失败，失败的是哪个具体 task。
- 如果要修改，应该修改哪个 artifact / episode / scene / node。

## 2. 核心原则

### 2.1 先读事实，再决策

Main Agent 每轮先读项目事实，再输出动作。不能凭印象、文件名、按钮状态或单句关键词决定业务流程。

### 2.2 当前步骤内容必须进入上下文

当前停在哪个 step，就必须把该 step 的产物内容作为主上下文：

- 停在故事圣经：给故事圣经内容。
- 停在原文拆集：给拆集结果和对应源文范围。
- 停在素材库：给素材库内容。
- 停在故事种子：给故事种子内容。
- 停在剧集蓝图：给剧集蓝图内容。
- 停在分集卡：给分集卡内容。
- 停在剧本：给当前集、当前场、选区附近文本和相关分集卡。

如果当前步骤内容太大，不能直接全量塞给控制模型，应给“当前相关片段 + 字段摘要 + 可读取 artifact id”。

### 2.3 近 N 轮聊天必须进入上下文

默认带最近 8 轮项目内聊天。超过窗口后，旧聊天压缩成 `conversation_summary`。

近 N 轮聊天用于判断：

- 用户是否在延续上一句。
- “继续”“重来”“这个不对”等短句指向什么。
- 用户是否刚刚补充了修改要求。
- 用户是否针对失败、确认点或选区发起操作。

### 2.4 上下文按任务读取，不无脑全塞

控制模型负责决策，不负责写正文。它不需要所有产物全量内容，但必须能拿到足够事实判断下一步。

大内容采用三层策略：

1. 默认给摘要和索引。
2. 当前步骤和当前选区给相关片段。
3. 需要更多内容时，由 Main Agent 请求读取 artifact 详情或搜索定位。

项目附件也遵守按需读取：

1. 上传后永久保留在当前作品，普通聊天上下文只提供 `file_id`、文件名、类型、大小和截断预览。
2. 用户明确要求分析、总结、解释或评价附件时，Main Agent 输出 `inspect_source`，内容 Worker 才读取目标文件正文。
3. 当前作品只有一个附件时，“这个文件”“刚才的附件”可自动指向该文件；存在多个附件且无法从文件名或上下文定位时必须追问。
4. 上传本身、普通闲聊和状态询问都不能读取完整正文，也不能启动 run。
5. 附件类型由内容和用户意图共同判断，不能因为已上传就自动认定为小说。

### 2.5 用户表达开放，状态事实封闭

用户自然语言不能枚举；系统状态必须结构化。

模型根据开放表达理解意图，代码根据结构化状态守卫动作是否合法。

## 3. AgentContextPack Schema

```json
{
  "request": {
    "message": "",
    "source_mode_hint": "auto | novel | non_novel",
    "attachments": [],
    "selected_artifact_id": "",
    "selected_text": "",
    "selection_source": "script | artifact | input | unknown"
  },
  "conversation": {
    "recent_turns": [],
    "summary": ""
  },
  "project": {
    "project_id": "",
    "title": "",
    "source_mode": "auto | novel | non_novel",
    "status": ""
  },
  "run": {
    "run_id": "",
    "status": "",
    "current_step_id": "",
    "current_step_label": "",
    "approval_request": {},
    "active_task": {},
    "last_error": {},
    "next_action": {}
  },
  "current_step_context": {
    "step_id": "",
    "artifact_id": "",
    "artifact_type": "",
    "status": "",
    "version": 0,
    "payload_excerpt": {},
    "payload_summary": "",
    "read_policy": ""
  },
  "focused_context": {
    "artifact_id": "",
    "artifact_type": "",
    "episode_id": "",
    "scene_id": "",
    "node_id": "",
    "selected_text": "",
    "before_text": "",
    "after_text": ""
  },
  "upstream_context": {
    "required_artifacts": [],
    "summaries": {}
  },
  "artifact_index": {},
  "event_digest": [],
  "available_context_tools": []
}
```

## 4. 上下文层级

### 4.1 固定规则层

固定规则不放在每个 project 里，由 Main Agent system prompt 承载：

- Agent 定义。
- intent / next_action 合同。
- 状态守卫规则。
- checkpoint 规则。
- failed rerun 规则。
- 局部修改规则。

### 4.2 当前用户输入层

必须包含：

- 当前消息。
- source mode 选择。
- 附件文件名、类型、大小、文本摘要。
- 选区文本。
- 选区来源。

附件规则：

- 控制模型只看附件摘要或截断文本。
- 生成 Worker 才读取完整附件内容。
- base64 不进控制模型上下文。

### 4.3 近 N 轮聊天层

默认 N = 8。

每轮包含：

- role。
- content 摘要或短文本。
- attachment ids。
- selection context。
- decision intent。
- linked run id。

过滤规则：

- 只带当前 project 相关消息。
- 当前 run 相关消息优先。
- 与当前选区或 artifact 相关消息优先。
- 非业务闲聊可压缩。

### 4.4 当前流程状态层

必须包含：

- project status。
- run status。
- current_step_id。
- current_step_label。
- approval_request。
- active_task。
- last_error。
- next_action。

这层用于判断：

- 是否能 start_run。
- 是否能 approve_run。
- 是否能 revise_checkpoint。
- 是否能 rerun_step。
- 是否需要解释状态。

### 4.5 当前步骤内容层

这是上下文包的核心。

不同 step 的内容策略：

| 当前 step | 必带内容 | 不够时读取 |
| --- | --- | --- |
| story_bible | 故事主线、人物、冲突、风险、风格 | 完整 story_bible |
| episode_split | 分集切分表、源文范围、集数 | 对应源文片段 |
| material_bank | 素材分类、人物设定、冲突资源 | 完整素材库 |
| story_seed | 主线、核心冲突、人物起点 | 素材库 |
| series_blueprint | 剧集结构、阶段目标、主钩子 | 故事种子 |
| episode_cards | 每集核心冲突、人物状态、尾钩 | 上游故事结构 |
| script_context | 剧本约束、角色状态、连续性 | 分集卡和人物表 |
| script_unit | 当前集剧本、当前场、选区前后文 | 当前集完整剧本 |

### 4.6 聚焦上下文层

当用户有选区、光标、当前打开产物时，必须额外提供：

- artifact id。
- artifact type。
- episode id。
- scene id。
- node id。
- selected_text。
- before_text。
- after_text。

局部修改优先使用这层。没有定位就追问，不猜。

### 4.7 上游关键产物层

只提供当前 step 需要的上游摘要。

小说链：

- story_bible
- source_chunks
- episode_cards
- script_context

非小说链：

- material_bank
- story_seed
- series_blueprint
- episode_cards
- script_context

原则：

- 控制模型看摘要和关键字段。
- 生成模型看完整 payload 或按 prompt 需要裁剪后的 payload。

### 4.8 artifact 索引层

必须给 Main Agent 一个 artifact map：

```json
{
  "story_bible": "artifact_001",
  "episode_cards": "artifact_004",
  "scripts.episode_3": "artifact_009"
}
```

用途：

- 用户说“刚才那个分集卡”时能定位。
- 用户说“第 3 集”时能定位到 script / episode_card。
- 用户说“故事圣经里那个男主”时能定位到 story_bible。

### 4.9 event digest 层

不把所有 events 全量塞给模型，只给摘要：

- 最近完成了哪些 step。
- 当前卡在哪个 checkpoint。
- 哪个 task 正在运行。
- 哪个 task 失败。
- 失败原因摘要。

失败场景必须包含：

```json
{
  "step_id": "script_unit",
  "task_id": "task_generate_script_episode_3",
  "episode_id": 3,
  "task_cursor": 2,
  "task_total": 8,
  "error_summary": "model timeout"
}
```

## 5. 读取策略

### 5.1 普通生成

读取：

- 当前用户输入。
- 附件摘要。
- 最近聊天。
- source mode hint。

不读取：

- unrelated artifact 全量 payload。

### 5.2 等待确认

读取：

- approval_request。
- 当前待确认 artifact。
- 当前 step payload 摘要或全量。
- 最近聊天。

用途：

- 判断用户是确认、暂停、解释、还是修改。

### 5.3 失败恢复

读取：

- run.status。
- active_task。
- next_action。
- last_error。
- 最近聊天。
- 当前失败 step 的 artifact 状态。

用途：

- 判断用户是问原因还是要继续。
- 如果继续，触发 rerun_step。
- Runtime 从 active_task 继续。

### 5.4 局部修改

读取：

- selected_artifact_id。
- selected_text。
- before_text / after_text。
- 当前 artifact version。
- 当前 episode / scene / node。
- 近 8 轮聊天。

如果没有选区：

- 读取当前打开 artifact。
- 仍不明确就追问。

### 5.5 剧本编辑

读取：

- 当前 episode。
- 当前 scene。
- 分集目录。
- 当前选区前后文。
- 对应 episode_card。
- script_context。

不要默认读取整部剧本，除非：

- 剧本很短。
- 用户明确要求全局诊断。
- 当前任务需要全局结构判断。

### 5.6 全局诊断

读取顺序：

1. artifact index。
2. story_bible / series_blueprint 摘要。
3. episode_cards 摘要。
4. 必要时分段读取 scripts。

## 6. Main Agent 与 Worker 的上下文区别

### Main Agent

需要：

- 状态。
- 近 N 轮聊天。
- 当前步骤内容。
- artifact 摘要。
- approval。
- active_task。
- 选区。

目标：

- 判断意图。
- 判断下一步。
- 决定是否要读取更多上下文。
- 决定是否触发 runtime action。

### Generation Worker

需要：

- 对应 prompt。
- selected skills。
- 完整或裁剪后的 artifact payload。
- 当前 step 输入。
- 输出 schema。

目标：

- 生成或修改内容。
- 输出 artifact payload。

两者不能混用。Main Agent 不负责写正文，Worker 不负责产品流程总控。

## 7. Eino 映射

接 Eino 后，本合同映射如下：

| 本合同概念 | Eino 映射 |
| --- | --- |
| AgentContextPack | Graph input / Agent state |
| context assembler | pre-node state loader |
| current_step_context | state field / tool result |
| read artifact detail | Eino Tool |
| approval_request | Interrupt |
| active_task | Checkpoint state |
| rerun_step | Resume from checkpoint |
| event_digest | trace / state summary |

因此本合同不影响接 Eino，反而是接 Eino 前必须稳定的业务状态合同。

## 8. 运行时上下文编译上限（2026-07-17）

实现层必须在保持结构化定位的前提下限制输入规模：

- 控制模型最近对话最多 6 轮，事件摘要最多 8 条；已有摘要时不重复传完整事件。
- 附件只传文件索引与截断预览；base64 永不进入控制模型。
- 局部修改的目标 artifact 只传一次，不能同时出现在 target 和 artifact digest 中。
- 剧本选区只传目标场景附近 block；带 source offset 的原文目标只传目标范围与边界窗口。
- 结构化 target 的 artifact_id、episode_id、scene_id、node_id、field_path 优先级高于自然语言推断。
- 裁剪不能删除选区文本、稳定节点 ID、必要上游事实和生成配置。

## 8. 当前实现差距

当前 Go runtime 已有：

- 当前消息。
- 附件摘要。
- run。
- events。
- artifact digest。
- active_task。

仍需补齐：

- 最近 N 轮聊天。
- conversation summary。
- approval_request 直接进入 context。
- 当前 step artifact payload。
- focused_context。
- artifact index。
- 上游必要 artifact 摘要。
- 按需读取 artifact/detail/search 的工具接口。

## 9. 下一步实现顺序

1. 新增 `ContextAssembler`。
2. 在 `projectMessage` 中把最近聊天传给 assembler。
3. 从 runtime 获取 current step artifact。
4. 生成 artifact index。
5. 增加 focused_context 映射。
6. 把 `mainagent.Context` 扩展为 `AgentContextPack`。
7. 更新 Main Agent system prompt，要求先根据 pack 决策，不足时输出需要读取更多上下文。
8. 再接 Eino，把 assembler 作为 graph 前置节点。
