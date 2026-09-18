# Main Agent Operations

## 技术路线决策：Main Agent 运行在 Go + Eino

Main Agent 的业务状态、执行计划、artifact 写入、run_event 推送和审批中断都由 Go 后端管理。Agent 执行框架定为 CloudWeGo Eino。

```text
TypeScript Frontend
-> Go Backend
-> Eino Agent Runtime
-> Model Provider
```

这替代此前“Go 后端调用 TypeScript Claude Agent SDK Worker”的路线。Claude 仍可作为模型供应商，但不作为主 Agent 框架。

第一阶段先实现 Go mock agent runtime，接口按 Eino runtime 的未来形态设计：

```text
StartRun(project_id, user_message)
ContinueRun(run_id, approval_response)
RerunStep(run_id, step_id)
StreamRunEvents(run_id)
```

mock runtime 的职责是验证产品闭环：用户输入、计划、事件流、artifact 更新、审批暂停、继续执行、最终 scripts 产物。

本文档定义 Main Agent 如何管理 Novel2Script 的完整流程。Main Agent 不是单个 prompt，也不是固定按钮流；它是项目状态、用户意图、artifact、审批点和执行步骤之间的调度者。

## Main Agent 职责

```text
1. 识别用户意图
2. 读取项目状态和 artifact 版本
3. 生成或更新执行计划
4. 选择下一步 action
5. 拼装 prompt、skills 和 artifact payload
6. 调用 Eino runtime / mock runtime / LLM
7. 保存 artifact 和 run events
8. 触发审批、暂停、继续、重跑
9. 处理局部修改和失效传播
10. 向前端输出稳定的过程事件
```

## 操作铁律

Main Agent 的默认行为不是“收到指令就生成”，而是“先读现状，再决定最小可执行动作”。这是从 laper.ai 架构里可以直接借鉴的核心经验。

```text
先读，不猜
先看当前项目状态和相关 artifact，再执行生成或修改。

最小改动
用户要求局部修改时，只改被选中的节点、场景、集卡或 artifact 片段，不重写整条链路。

分批落笔
剧本输出不要整页突然刷满，应按场景、动作、对白等稳定意义单元逐批写入。

结构化引用
剧本、角色、地点、集卡和来源材料都用稳定 id 或 artifact ref 串联，不靠名字文本硬匹配。

错误自修
工具或 schema 返回错误时，Main Agent 先修正参数或请求用户补充，不把 raw error 直接丢给用户。

人话收尾
给用户的回复只说产品语言，例如“第 3 集尾钩改强了”，不说 node_id、tool、schema 字段。
```

## 主循环

```text
user_message
-> route_intent
-> load_project_state
-> inspect_active_artifacts
-> build_or_update_plan
-> choose_next_action
-> decide_approval_policy
-> execute_action
-> persist_artifacts_and_events
-> update_project_state
-> wait_for_user_or_continue
```

Main Agent 每轮只决定下一步，不把整条链路写死。

## 用户输入到剧本输出主流程

第一版只关注用户输入到剧本输出，不纳入分镜、镜头卡、计费、订阅等旁路流程。

### 小说输入

```text
用户输入小说原文
-> Main Agent 判断 source_mode=novel
-> 读取/保存 source_input
-> build_story_bible
-> 请求确认 story_bible 或自动继续
-> split_episodes
-> 请求确认 episode_split
-> plan_episode_cards
-> build_script_context
-> generate_script_unit
-> 输出 scripts
```

### 非小说输入

```text
用户输入灵感/梗概/素材
-> Main Agent 判断 source_mode=non_novel
-> 读取/保存 source_input
-> build_material_bank
-> build_story_seed
-> build_series_blueprint
-> 请求确认 series_blueprint 或 episode_cards
-> plan_episode_cards
-> build_script_context
-> generate_script_unit
-> 输出 scripts
```

### 局部修改

```text
用户选中文本或指定某集/某段
-> Main Agent 读取 selection_ref 和 surrounding_context
-> 判断修改范围
-> 选择 revise_selection / revise_artifact
-> 只更新目标范围
-> 如影响 continuity_delta 或上游设定，再触发 dependency_invalidation
```

## next_action 类型

```text
create_project
ingest_source
build_story_bible
split_episodes
build_material_bank
build_story_seed
build_series_blueprint
plan_episode_cards
build_script_context
generate_script_unit
revise_artifact
revise_selection
summarize_artifact
ask_user
pause_run
resume_run
rerun_step
```

## Prompt 模块加载

Main Agent 不应该把所有 prompt、skills 和规则一次性塞给 LLM。应按当前 `next_action` 裁剪上下文。

```text
公共常驻：
- Main Agent 身份和操作铁律
- artifact 外壳规则
- run_event 规则
- 用户语言规则

小说链动作才加载：
- step1_story_bible
- step2_episode_split
- step3_episode_cards
- 相关 skills

非小说链动作才加载：
- step1_material_bank
- step2_story_seed
- step3_series_blueprint
- step4_episode_cards
- 相关 skills

剧本生成动作才加载：
- script_context
- episode_card
- script_generate
- 剧本写作、对白、格式、平台适配 skills

局部修改动作才加载：
- selection_ref
- target artifact
- surrounding_context
- revise instruction
```

## 执行计划

```json
{
  "plan_id": "",
  "run_id": "",
  "goal": "",
  "source_mode": "novel | non_novel",
  "steps": [
    {
      "step_id": "",
      "capability_id": "",
      "input_artifacts": [],
      "output_artifacts": [],
      "approval_policy": "auto | checkpoint | every_step",
      "status": "pending"
    }
  ],
  "current_step_id": ""
}
```

## 审批策略

Main Agent 不应该每一步都强制打断，也不能默认全自动覆盖关键产物。

```text
auto：低风险动作自动执行
checkpoint：关键节点请求确认
every_step：用户要求高控制时每步确认
```

默认使用 `checkpoint`。

必须请求审批的情况：

```text
覆盖 confirmed artifact
改变集数或拆集边界
重排主线结构
批量重写多集剧本
用户修改会导致多个下游 artifact 失效
source_mode 判断置信度低
素材不足但 Agent 准备补全关键设定
```

## 分批剧本输出

借鉴 laper.ai 的“流式书写”体验，剧本生成不要一次性把整集剧本刷满。第一版可以按较粗的意义单元分批：

```text
batch 1：场景标题 + 开场动作
batch 2：第一组冲突动作 + 对白
batch 3：第二组动作/对白/转折
batch 4：集尾钩子
batch 5：continuity_delta 和自检
```

每一批都产生 `run_event`，前端可以展示：

```text
Creating scene opening - done
Writing dialogue beat - done
Inserting episode hook - done
```

这不是后端性能限制，而是产品体验约束：用户需要看到剧本逐步生成，而不是等待后突然出现一整页。

## 局部修改管理

前端传给 Main Agent 的局部修改上下文必须包含：

```json
{
  "artifact_id": "",
  "artifact_type": "",
  "version": 1,
  "selection_ref": {
    "node_id": "",
    "episode_id": 1,
    "scene_id": "",
    "start_offset": 0,
    "end_offset": 0,
    "selected_text": ""
  },
  "surrounding_context": "",
  "user_instruction": ""
}
```

Main Agent 先判断修改范围：

```text
selection_only：只改选区
node_level：改一个结构节点
episode_level：改一集
artifact_level：改整个 artifact
upstream_change：影响上游设定或结构
```

只有 `upstream_change` 或影响 `continuity_delta` 时，才触发下游失效传播。

## 结构化引用规则

所有内部引用都必须有来源，不能靠模型编造。

```text
character_id 来自 characters artifact 或创建角色后的返回值
location_id 来自 locations artifact 或创建地点后的返回值
episode_id 来自 episode_split 或 episode_cards
scene_id 来自 script_unit.scenes
source_ref 来自 source_input、story_bible 或 episode_split
artifact_id 来自 artifact store
```

如果找不到引用来源，Main Agent 必须先读、创建或询问用户，不能生成看似合理的假 id。

## 与前端交互

前端不理解复杂 agent 内部逻辑，只消费稳定事件。

```text
Agent chat：显示用户和 Main Agent 对话
Run timeline：显示步骤 running/done/failed/waiting_approval
Approval card：显示审批请求和操作按钮
Artifact editor：显示当前 focus artifact
Version panel：显示版本和失效状态
```

Main Agent 需要在关键事件中提供 `focus_artifact`，让前端中间区域自动切换。

```json
{
  "type": "artifact_updated",
  "message": "故事圣经已生成",
  "focus_artifact": {
    "artifact_id": "",
    "artifact_type": "story_bible",
    "version": 1
  }
}
```

## 与 Eino Runtime 的边界

Main Agent 的业务状态由 Go 后端保存。Eino Runtime 负责执行模型任务、工具调用、workflow 编排和返回结构化事件。

```text
Go Backend
-> 读取 project_state / artifacts
-> 决定 next_action
-> 调用 Go mock runtime 或 Eino runtime
-> 接收 streaming events
-> 保存 artifacts / run_events
-> 推送给前端
```

Eino Runtime 负责：

```text
调用模型 provider
传入 prompt、skills、artifact payload
处理 streaming messages
返回结构化 llm_output、tool events、approval hints
```

Runtime 不负责：

```text
长期保存项目状态
决定 artifact 是否覆盖 confirmed 版本
直接操作浏览器 UI
绕过 Go 后端向前端发最终状态
```

## 用户语言规则

Main Agent 对用户说话时，不暴露内部技术词。

```text
不要说：
node_id
artifact_id
tool_call
schema
payload
selection_ref
dependency_invalidation

应该说：
这一段
第 3 集
刚才选中的对白
故事圣经
分集方案
剧本正文
我会先确认拆集，再继续写剧本
```

## 失败处理

失败时 Main Agent 必须保留可恢复状态：

```text
失败步骤
失败原因
已生成但未确认的 artifact
可重试的 next_action
是否需要用户补充输入
```

前端展示为：

```text
Step failed
Retry
Revise instruction
Pause
Inspect artifacts
```
## 审计补强：入口确认

当 Main Agent 对入口判断置信度低，或输入同时像小说原文和灵感梗概时，必须先问用户确认，不直接开跑。

```text
低置信度 novel/non_novel 判断
-> 询问：这是小说原文改编，还是灵感/梗概开发？

用户只说“帮我写个短剧”
-> 询问：你是要从小说原文改编，还是从一个想法开始开发？

用户选中内容但 selection_ref 缺失
-> 询问：你想改哪一段？请先选中内容或说明集数/场次。
```

对用户说法：

```text
我先确认一下入口：这是小说原文改编，还是从灵感梗概开发？
```

## 审计补强：第一刀读取策略

不同意图的第一步读取不同，不能默认全文读取，也不能不读就写。

```text
generate_novel
-> 读取 source_input；如已有 story_bible/episode_split，先读取当前 confirmed 版本再决定继续或重跑。

generate_non_novel
-> 读取 source_input；如已有 material_bank/story_seed/series_blueprint，先读取当前 confirmed 版本。

revise_selection
-> 读取 selection_ref、target artifact、surrounding_context；只在需要时读取同集或相邻集。

revise_artifact
-> 读取目标 artifact 最新版本、confirmed 版本、derived_from 和下游依赖。

continue_run
-> 读取 run_state.next_action、current_step_id、latest confirmed artifacts。

rerun_step
-> 读取被重跑 step 的 input_artifacts、原 llm_input 摘要、当前 confirmed artifact 版本。

chat
-> 默认不读取全项目；只有用户问具体内容时再读相关 artifact。
```

## 审计补强：剧本分批事件

```json
{
  "type": "script_batch_inserted",
  "message": "第一组对白已写入",
  "focus_artifact": {
    "artifact_type": "script_unit",
    "episode_id": 1,
    "version": 1
  },
  "payload": {
    "batch_index": 2,
    "batch_role": "dialogue_beat",
    "visible_text_delta": "",
    "is_final_batch": false
  }
}
```

## 审计补强：选区来源

`selection_ref.source` 必须由前端提供或由 Main Agent 推断后确认：

```text
script：剧本正文选区
episode_card：分集卡选区
story_bible：故事圣经选区
episode_split：拆集方案选区
material_bank：素材库选区
series_blueprint：剧集蓝图选区
```

## 生成前配置确认

Main Agent 在 `start_run` 前必须确认三类信息：
1. `source_mode`：小说 `novel` 或非小说 `non_novel`。
2. `generation_config.target_episode_count`：目标集数。
3. `generation_config.episode_duration_minutes`：单集时长。

文件上传、粘贴原文或发送素材本身不等于生成请求。只有用户明确表达“生成、改编、写成、拆成、转成、继续完成某类产物”等意图时，Main Agent 才能进入 `start_run`。

如果用户说“根据这个附件生成剧本”，但没有目标集数或单集时长，Main Agent 必须先追问或展示配置确认卡，不能静默默认 20 集。

小说链配置处理：
- 如果原文已有分集/章节标记，Main Agent 需要判断是否要保留，并把结果写入 `generation_config.preserve_existing_episode_marks`。
- `episode_split` 根据目标集数、剩余原文字数和 `boundary_detection_window_chars` 进行动态拆集。
- 每个关键产物完成后暂停确认：`story_bible`、`episode_split`、`episode_cards`、`scripts`。

非小说链配置处理：
- `material_bank` 判断素材能否支撑目标体量。
- `story_seed` 设计能支撑目标体量的核心冲突和关系引擎。
- `series_blueprint` 决定 `resolved_episode_count` 和阶段结构。
- `episode_cards` 必须严格按确认后的 `resolved_episode_count` 输出。

运行时传递：
- 前端把配置作为 `generation_config` 传给 Go 后端。
- Go 后端写入 `source_input.payload.generation_config` 和 `run.metadata.generation_config`。
- Prompt runner 每步都把 `source_input` 与上游 artifact 作为上下文传入模型。
