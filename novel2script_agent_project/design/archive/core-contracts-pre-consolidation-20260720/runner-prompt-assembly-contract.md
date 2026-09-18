# Runner Prompt 拼装协议

本文档定义 Novel2Script Agent 中 prompt runner 如何把 `prompt`、`selected skills`、`artifact payload`、`script_context`、`context_pack`、`user_notes` 组装成一次可执行 LLM 调用，并把输出校验成 artifact。

它承接：

- `api-event-contract.md`
- `shared-schema-contract.md`
- `agent-behavior-contract.md`
- `artifact-schemas.md`
- 小说 prompts
- 非小说 prompts
- 公共 `script_generate.md`
- skills 目录

## 目标

Runner 的职责不是“按固定 Step1-4 硬跑”，而是执行 Main Agent 选中的下一步 action：

```text
Main Agent
-> choose next_action
-> Runner builds PromptJob
-> LLM call
-> validate output
-> persist artifact
-> emit events
-> update run_state
```

Runner 必须做到：

1. prompt 路径可由 source_mode 和 next_action 决定。
2. prompt 输入字段必须由当前 project_state / artifact store / user message 提供。
3. skill 只作为准则注入，不作为任务 schema。
4. 输出必须校验后写入 artifact。
5. 失败必须返回可恢复错误，不把 raw LLM 输出直接当作正式产物。

## PromptJob

Runner 每次执行前构造 `PromptJob`：

```json
{
  "job_id": "job_xxx",
  "run_id": "run_xxx",
  "step_id": "step_xxx",
  "source_mode": "novel | non_novel",
  "action_type": "execute_prompt | generate_script | revise_artifact | revise_selection",
  "prompt_id": "non_novel.step1_material_bank",
  "prompt_path": "design/非小说-prompts/step1_material_bank.md",
  "selected_skills": [],
  "input_artifacts": [],
  "script_context": {},
  "context_pack": {},
  "user_notes": [],
  "expected_output": {
    "artifact_type": "material_bank",
    "root_keys": ["material_bank", "source_trace", "next_action"]
  }
}
```

## Step Registry

第一版 runner 使用显式 step registry，不从文件名临时猜。

### 小说链

| action | prompt | input artifacts | output artifact | approval |
|---|---|---|---|---|
| build_story_bible | `小说-prompts/step1_story_bible.md` | source_input | story_bible | 必须 |
| split_episodes | `小说-prompts/step2_episode_split.md` + `step2a_episode_split_global.md` + `step2b_episode_split_batch.md` | source_input, story_bible | episode_split | 必须 |
| plan_episode_cards | `小说-prompts/step3_episode_cards.md` | story_bible, confirmed episode_split | episode_cards | 必须 |
| build_script_context | `prompts/script_context.md` | confirmed 上游产物 | script_context | 内部，不展示 |
| generate_script | `prompts/script_generate.md` | episode_cards, script_context, context_pack | script_unit / scripts | 每个 script_unit 必须；scripts 不单独确认 |

### 非小说链

| action | prompt | input artifacts | output artifact | approval |
|---|---|---|---|---|
| build_material_bank | `非小说-prompts/step1_material_bank.md` | source_input | material_bank | 必须 |
| build_story_seed | `非小说-prompts/step2_story_seed.md` | material_bank | story_seed | 必须 |
| build_series_blueprint | `非小说-prompts/step3_series_blueprint.md` | material_bank, story_seed | series_blueprint | 必须 |
| plan_episode_cards | `非小说-prompts/step4_episode_cards.md` | story_seed, series_blueprint | episode_cards | 必须 |
| build_script_context | `prompts/script_context.md` | confirmed 上游产物 | script_context | 内部，不展示 |
| generate_script | `prompts/script_generate.md` | episode_cards, script_context, context_pack | script_unit / scripts | 每个 script_unit 必须；scripts 不单独确认 |

注意：

- 小说链拆集产物统一是 `episode_split`，不是 `source_chunks`。
- `source_chunks` 只能作为 `episode_split.payload.episodes[].source_refs` 或 `source_spans` 的内部字段。
- `step2_episode_split.md` 定义最终产物合同；2a/2b 是运行时内部阶段提示词，不创建额外用户可见 artifact。
- 小说链和非小说链都在 `episode_cards` 后汇合到 `script_generate`。

## Prompt 选择

Runner 不根据前端按钮选 prompt，只根据 Main Agent 的 `next_action` 和 `source_mode`。

```text
source_mode=novel + next_action=build_story_bible
-> step1_story_bible.md

source_mode=non_novel + next_action=build_material_bank
-> step1_material_bank.md

source_mode=novel|non_novel + next_action=generate_script
-> prompts/script_generate.md
```

如果缺少必要 artifact：

- 不调用 LLM。
- 返回 `VALIDATION_FAILED` 或请求用户补充。
- 写入 `step_failed` event。

## Skill 注入规则

### 基本规则

skill 只作为准则注入，不参与：

- 输入字段定义。
- 输出 schema 定义。
- 参数定义。
- 路由判断。

Prompt 引用 skill 时，runner 需要读取对应 skill 文本，并作为 `reference_guidelines` 注入。

### 注入格式

```text
## 可参考准则

### 02_人物关系与声口.md
用途：...
本次消费边界：用于保持人物关系、人物声口、角色目标一致；不用于新增剧情任务。
准则：
...
```

### 消费边界

Runner 需要在 PromptJob 中记录每个 skill 的消费边界：

```json
{
  "skill_path": "skills/02_人物关系与声口.md",
  "consumed_for": [
    "character_voice",
    "relationship_consistency"
  ],
  "not_consumed_for": [
    "output_schema",
    "routing"
  ]
}
```

如果 prompt 没引用某个 skill，runner 不注入。

如果 prompt 只消费 skill 的一部分，PromptJob 必须记录边界。

## 输入装配

Runner 输入必须来自以下来源：

```text
Project
Message
Artifact
Approval resolution
SelectionContext
FileRef parsed content
Runtime context_pack
```

不能来自：

- 前端临时 UI 状态。
- 未保存 artifact。
- 已 invalidated artifact，除非用于对比。
- 旧项目目录产物。

## source_input 装配

### 小说

```json
{
  "project_id": "",
  "source_text": "完整小说原文",
  "adaptation_context": {
    "target_format": "小程序短剧",
    "must_keep": [],
    "avoid": []
  }
}
```

来源：

- message.content
- file parsed text
- source_input artifact

### 非小说

```json
{
  "project_id": "",
  "user_material": {
    "raw_text": "",
    "attachments": [],
    "structured_notes": {}
  },
  "material_context": {
    "target_format": "小程序短剧",
    "must_keep": [],
    "avoid": []
  }
}
```

## context_pack 装配

`context_pack` 是跨集连续性上下文，不等于把所有历史 artifact 全量塞进 prompt。

第一版包含：

```json
{
  "previous_episode_cards": [],
  "previous_script_summaries": [],
  "recent_script_summaries": [],
  "continuity_state": {},
  "character_state": [],
  "open_foreshadowing": [],
  "open_hooks": []
}
```

装配规则：

- 只取当前 planning_scope 或 episode_id 附近需要的上下文。
- 不把完整 scripts 全量塞入每次调用。
- 已失效 artifact 不进入 context_pack。
- continuity_delta 在每次成功输出后合并进 continuity_state。

## script_generate script_context

公共 `script_generate.md` 不直接消费小说链或非小说链的零散上游产物。Runner 必须先把上游产物转换成统一 `script_context`，再调用剧本生成 prompt。

### novel

```json
{
  "source_mode": "novel",
  "episode_card": {},
  "script_context": {
    "source_mode": "novel",
    "must_follow_facts": [],
    "allowed_additions": [],
    "forbidden_changes": [],
    "character_state": [],
    "relationship_state": [],
    "continuity_state": {},
    "source_material": {
      "text": "当前 episode_card.source_refs 对应原文片段",
      "refs": [],
      "basis": []
    },
    "style_constraints": {},
    "user_notes": []
  }
}
```

要求：

- 必须提供 `script_context.source_material.text` 或 `refs`。
- 必须把 `story_bible` 中需要遵守的人物关系、必须保留事实、世界规则转换进 `must_follow_facts`、`character_state`、`relationship_state` 和 `forbidden_changes`。
- 不注入非小说链 material_bank / story_seed / series_blueprint。
- 不扩大到当前集 source_refs 以外的原文范围。

### non_novel

```json
{
  "source_mode": "non_novel",
  "episode_card": {},
  "script_context": {
    "source_mode": "non_novel",
    "must_follow_facts": [],
    "allowed_additions": [],
    "forbidden_changes": [],
    "character_state": [],
    "relationship_state": [],
    "continuity_state": {},
    "source_material": {
      "text": "",
      "refs": [],
      "basis": []
    },
    "style_constraints": {},
    "user_notes": []
  }
}
```

要求：

- 必须把 `story_seed` 和 `series_blueprint` 中已确定的主线、人物关系、爽点承诺和禁改项转换进 `script_context`。
- episode_card 必须含 `source_basis`。
- material_bank 只用于构造 `source_material.basis` 和追溯用户素材 / 模型推断边界，不直接传给 script_generate。
- 不能把 generated_additions 写成用户素材。

## user_notes 装配

user_notes 来源：

- 当前 message。
- approval revise_instruction。
- project constraints。
- 手动编辑备注。

规则：

- 不把全部聊天历史都塞入 user_notes。
- 只保留与当前 step 相关的用户要求。
- 如果用户要求与上游 artifact 冲突，Main Agent 必须先判断是否需要 approval。

## Prompt 文本结构

Runner 组装给 LLM 的文本结构：

```text
<system>
你是 Novel2Script 的某步骤 Agent。
</system>

<prompt_spec>
原始 prompt markdown
</prompt_spec>

<reference_guidelines>
selected skills with consumption boundaries
</reference_guidelines>

<input_payload>
JSON input
</input_payload>

<output_contract>
必须只输出 JSON，根字段必须符合 expected_output.root_keys。
</output_contract>
```

## LLM 输入记录

每次调用前写入 `llm_input_prepared` event。

event 中只记录摘要：

```json
{
  "prompt_id": "non_novel.step1_material_bank",
  "prompt_path": "design/非小说-prompts/step1_material_bank.md",
  "selected_skills": ["09_非小说素材与故事种子.md", "02_人物关系与声口.md"],
  "input_artifact_refs": [],
  "output_artifact_type": "material_bank",
  "payload_summary": {
    "source_mode": "non_novel",
    "text_chars": 1200
  }
}
```

默认不记录完整 prompt、完整 source_text、完整模型输出。

## 输出校验

Runner 收到模型输出后必须：

1. 提取 JSON。
2. 校验根字段。
3. 校验必需字段。
4. 校验枚举值。
5. 校验 `next_action` 是否允许。
6. 校验 artifact_type 与 step registry 一致。
7. 失败时不写 active artifact。

### 输出到 artifact

示例：

```json
{
  "artifact_type": "episode_cards",
  "payload": {
    "episode_cards": [],
    "continuity_delta": {},
    "next_action": "script_generate"
  },
  "derived_from": [
    {
      "artifact_id": "artifact_story_seed",
      "artifact_type": "story_seed",
      "version": 1
    }
  ]
}
```

## next_action 处理

prompt 输出中的 `next_action` 只是建议，不直接驱动前端。

处理规则：

1. Runner 校验 `next_action` 是否在 registry 允许范围内。
2. Main Agent 根据 run_state、approval_policy、artifact status 决定是否继续。
3. 如果当前步骤是 checkpoint，先创建 approval_request。

示例：

```text
episode_cards.next_action = script_generate
-> Main Agent 检查 approval_policy
-> 创建 approval_request
-> run.status = waiting_approval
```

## Approval 与 Runner

Runner 不直接弹审批卡；Runner 只返回：

- output artifact
- suggested next_action
- risk_notes
- invalidated_artifacts

Main Agent 根据规则创建 approval_request。

## 失败恢复

### JSON 解析失败

行为：

- 尝试一次 JSON repair。
- 仍失败：`MODEL_OUTPUT_INVALID`。
- 不写 artifact。

### schema 校验失败

行为：

- 如果缺失字段可由空数组 / 空对象安全补齐，runner 可修复并记录 warning。
- 如果关键字段缺失，返回 failed。

### skill 缺失

行为：

- design validate 阶段应失败。
- runtime 阶段返回 `VALIDATION_FAILED`。

### 输入 artifact 缺失

行为：

- 不调用 LLM。
- 返回 `ARTIFACT_NOT_FOUND` 或 `VALIDATION_FAILED`。

## Mock Runtime 对齐

Go mock runtime 虽然可以生成假内容，但必须模拟同一套 PromptJob：

- 生成相同 step_id。
- 输出相同 artifact_type。
- 写相同 run_event。
- 在 episode_cards 后触发 approval。
- approve 后生成 script_unit / scripts。

Mock 不能再使用旧的 `source_chunks` 主 artifact。

## Eino Runtime 对齐

通用 Eino runtime 节点：

```text
LoadProjectState
ResolveNextAction
BuildPromptJob
LoadPromptAndSkills
AssembleInputPayload
CallModel
ValidateOutput
PersistArtifact
UpdateRunState
EmitRunEvent
MaybeRequestApproval
```

小说拆集使用专用阶段图：

```text
validate_split_stage_input
-> invoke_split_stage_model
-> validate_split_stage_output
```

Go Runtime 在图外负责原文索引、候选边界、输入版本锁、每批 checkpoint、失败 cursor 和最终连续覆盖校验。Eino 不维护第二套业务状态。

每个节点都应该可以接收和返回结构化 state，避免把流程藏在 prompt 文本里。

## 禁止事项

- runner 根据前端按钮决定 prompt。
- runner 注入所有 skills。
- skill 定义输入输出 schema。
- 把已 invalidated artifact 当作有效输入。
- script_generate 只支持小说链。
- 非小说 generated_additions 伪装成用户素材。
- prompt 输出未校验就写入 artifact。
- LLM raw output 直接展示给用户。
- 继续使用 `source_chunks` 作为主 artifact。
