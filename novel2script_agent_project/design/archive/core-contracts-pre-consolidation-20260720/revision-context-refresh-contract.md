# Artifact Revision Matrix and Refresh Contract

状态：开发准入合同

本文档定义 Novel2Script Agent 如何承接用户对过程产物和剧本产物的修改、重写、重新生成、失败重跑和继续执行需求。

核心原则：**先识别修改粒度，再组装上下文，再执行 patch 或 regenerate，最后刷新可见产物并标记下游影响。**

## 1. 目标

本合同解决 5 个问题：

1. 用户说“改一下”时，Agent 如何判断是改字段、改实体、改区块、改单集，还是重写整个 artifact。
2. 每个 artifact 支持哪些修改粒度。
3. 每种修改粒度需要带哪些上下文。
4. 修改后哪些前端区域要刷新，哪些下游产物要 stale 或 invalidated。
5. 什么时候必须追问用户，不能擅自扩大或缩小修改范围。

## 2. 用户可感知步骤

左侧主目录只展示用户需要理解和确认的步骤。

小说链路：

```text
输入材料 -> 故事圣经 -> 原文拆集 -> 分集卡 -> 剧本 -> 运行记录
```

非小说链路：

```text
输入材料 -> 素材库 -> 故事种子 -> 剧集蓝图 -> 分集卡 -> 剧本 -> 运行记录
```

`script_context` 是内部辅助 artifact。它用于给剧本生成提供连续性、禁改事实、人物声口和格式约束，不作为左侧主目录步骤，默认不要求用户确认。

## 3. 修改粒度模型

Agent 不直接判断“patch 还是重写”，而是先判断 4 层粒度。

| 粒度 | 含义 | 示例 | 默认动作 |
| --- | --- | --- | --- |
| `field` | 单个字段 | 修改主角目标、加强第 2 集尾钩 | patch 字段 |
| `entity` | 一个结构实体 | 修改主角角色信息、重写反派、调整第 2 集分集卡 | patch 整个实体 |
| `section` | 一个区块或数组集合 | 人物关系不对、改编风险重做、全部人物声口统一 | patch 区块 |
| `artifact` | 整个产物 | 故事圣经不行，重新做；分集卡全部重排 | regenerate artifact |

判断原则：

- 用户表达越具体，scope 越小。
- 用户表达越泛，scope 越大。
- 用户只说“不对”“不好”“再改改”，且没有选区、当前焦点或明确对象时，必须追问。
- 用户选中内容时，选区优先于当前页面。
- 用户当前正在确认某个 artifact 时，修改请求默认指向该 checkpoint，除非用户明确指向其他内容。

### 3.1 修改触发与选区生命周期

- Main Agent 只能根据本轮用户语义和有效上下文决定是否修改，不能因为自己的回复里出现“修改”就反推执行动作。
- 普通问候、状态询问和解释请求在任何 run 状态下都不能继承历史选区并启动修改。
- “修改这句话 / 修改这里”只确定了目标，没有确定修改方向；必须先追问，用户补充清楚后才执行。
- 短确认只能承接紧邻的、尚未解决的修改澄清问题；不能承接任意历史选区。
- 选区在修改成功、确认继续、启动新任务、切换产物或取消后失效。历史记录仍保留用于审计，但不再参与执行定位。

### 3.2 待确认产物隔离

- `pending approval` 是当前 run 的最高优先级交互状态。
- 用户可以继续闲聊、询问状态，或修改同一个待确认 artifact。
- 用户要求修改其他 artifact 时，Main Agent 必须先提示处理当前确认，不得替换、取消或串改当前 approval。
- 同一 artifact 的修改执行期间暂存原 approval；新版本成功后才替换，修改失败则恢复原 approval。
- 服务恢复时，如发现最新 artifact 仍是 `pending_approval` 且下游影响记录存在，但 approval 丢失，应自动重建确认请求。

## 4. Revision Plan

所有修改类请求先生成 `revision_plan`，再执行。

```json
{
  "intent": "revise_artifact",
  "confidence": 0.0,
  "target_artifact_id": "",
  "target_artifact_type": "",
  "target_scope": "field | entity | section | artifact | selection | scene | episode | task",
  "target_path": "",
  "operation": "refine | rewrite | append | delete | reorder | regenerate | rerun",
  "needs_clarification": false,
  "clarification_question": "",
  "reason": "",
  "downstream_policy": "none | mark_stale_only | invalidate_downstream | rerun_affected_downstream"
}
```

`revision_plan` 是 Main Agent 的决策输出，不是 worker 的内容生成输出。后端必须校验：

- `target_artifact_id` 是否存在。
- `target_artifact_type` 是否和当前 artifact 匹配。
- `target_path` 是否能落到 schema。
- `target_scope` 是否被该 artifact 的矩阵允许。
- `downstream_policy` 是否符合矩阵约束。

校验失败时，不调用 worker，先追问或返回可解释错误。

## 5. 修改意图类型

| 意图 | 使用场景 | 结果 |
| --- | --- | --- |
| `explain_or_locate` | 用户问当前状态、为什么失败、某字段什么意思 | 只回答，不改 artifact |
| `clarify_revision_target` | 修改目标不清楚 | 追问，不改 artifact |
| `add_requirement_to_checkpoint` | 当前等待确认，用户补充要求 | 记录要求并重算当前 checkpoint |
| `patch_artifact_field` | 修改某个字段 | 最小字段 patch |
| `patch_artifact_entity` | 修改某个角色、关系、风险项、单集卡等实体 | 替换该实体 |
| `patch_artifact_section` | 修改一个区块或数组集合 | 替换该 section |
| `regenerate_artifact` | 整个 artifact 重写 | 新版本 supersede 旧版本 |
| `patch_script_span` | 修改剧本选区 | 返回选区替换 patch |
| `patch_script_scene` | 修改一场戏 | 返回 scene patch |
| `regenerate_script_episode` | 重写一集剧本 | 返回完整 script_unit |
| `rerun_failed_task` | 失败后从失败 task 继续 | 从 task cursor 重跑 |
| `resume_after_revision` | 修改完成后继续流程 | 从最新 confirmed artifact 继续 |
| `replace_source_input` | 替换输入材料 | 下游全部 invalidated |

## 6. 上下文包原则

上下文不能“全塞”，也不能只给当前 artifact。上下文由 `target_scope` 决定。

通用 `RevisionContextPack`：

```json
{
  "revision_plan": {},
  "user_request": "",
  "source_mode": "novel | non_novel",
  "current_checkpoint": {},
  "focused_context": {
    "artifact_id": "",
    "artifact_type": "",
    "artifact_version": 0,
    "path": "",
    "episode_id": "",
    "scene_id": "",
    "node_id": "",
    "selected_text": "",
    "before_text": "",
    "after_text": ""
  },
  "recent_turns": [],
  "target_artifact_payload": {},
  "required_upstream": {},
  "neighbor_context": {},
  "generation_config": {},
  "downstream_impact": {}
}
```

硬规则：

- `target_artifact_payload` 必须是目标 artifact 的最新可用版本。
- `recent_turns` 默认最多 8 轮，只用于理解承接关系，不能替代 artifact 正文。
- 选区修改必须带 `selected_text`、`before_text`、`after_text`、`artifact_id`、`episode_id`、`scene_id` 或 `node_id`。
- 剧本多行选区还必须带有序 `line_ids`、首尾 `line_id` / `scene_id` 和首尾行 offset；只允许同一集内连续跨行或跨场景。
- 修改剧本必须带对应 `episode_card` 和 `script_context`。
- 修改上游 artifact 必须计算下游影响。
- 修改决策必须持久化审计摘要：模型原始意图、守卫后动作、revision target、focused context 来源、apply policy 和下游刷新范围。

## 7. 小说链路 Revision Matrix

### 7.1 输入材料 `source_input`

输入材料默认只预览，不直接手动编辑。

| 用户意图 | scope | 上下文 | 输出 | 刷新 | 下游 |
| --- | --- | --- | --- | --- | --- |
| 替换原文、重新上传 | `artifact` | 新 source、旧 source 摘要、generation_config | 新 `source_input` | 输入材料刷新 | 故事圣经及以后全部 invalidated |
| 补充原文说明 | `field` | 当前 source、用户说明 | notes patch | 输入材料 notes 刷新 | 下游 mark_stale_only |

如果用户只是上传文件但没有要求生成，不启动主流程，只保存附件并等待明确指令。

### 7.2 故事圣经 `story_bible`

故事圣经的关键可修改对象：

- `story_overview`
- `characters[]`
- `relationships[]`
- `major_plotline`
- `must_keep_facts`
- `adaptation_risks`
- `world_rules`
- `short_drama_assets`
- `climax_map`
- `foreshadowing_and_payoff`

| 用户说法 | scope | target_path 示例 | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- | --- |
| 修改主角的目标 | `field` | `characters[主角].goal` | 主角对象、story_overview、source_evidence、最近相关要求 | field patch | episode_split 以后 mark_stale_only |
| 修改主角角色信息 | `entity` | `characters[主角]` | 主角对象、相关关系、must_keep_facts、source_evidence | entity patch | episode_split 以后 mark_stale_only |
| 人物关系不对 | `section` | `relationships` | 全部 characters、relationships、source_evidence | section patch | episode_split 以后 mark_stale_only |
| 改编风险重做 | `section` | `adaptation_risks` | source_input、must_keep_facts、平台风险规则 | section patch | episode_split 以后 mark_stale_only |
| 故事圣经整体不行 | `artifact` | `story_bible` | source_input、generation_config、旧 story_bible、用户要求 | regenerate | episode_split 以后 invalidated |

追问条件：

- “主角不太对”但没有说明改目标、动机、声口、关系还是整个人设。
- “人物这里重做”但当前页面没有定位到哪个角色或关系。
- 修改会导致主线变化，但用户没有确认是否接受下游重算。

### 7.3 原文拆集 `episode_split`

关键可修改对象：

- `generation_config`
- `episodes[].source_refs`
- `episodes[].source_summary`
- `episodes[].core_event`
- `episodes[].boundary_check`
- `split_strategy`
- `coverage_check`
- `global_risks`

| 用户说法 | scope | target_path 示例 | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- | --- |
| 第 2 集断点往后挪 | `entity` | `episodes[2]` | 第 1/2/3 集边界、原文窗口、story_bible 摘要 | episode entity patch | 第 2 集以后 episode_cards/script_unit stale |
| 目标原文字数改成 700 重新拆 | `artifact` | `episode_split` | source_input、story_bible、generation_config、新目标字数 | regenerate | episode_cards 以后 invalidated |
| 保留原文分集 | `artifact` | `episode_split` | source_input、检测到的分集标记、generation_config | regenerate | episode_cards 以后 invalidated |
| 第 1 集尾钩弱，换切点 | `entity` | `episodes[1].boundary_check` | 第 1/2 集边界、原文窗口、hook 证据 | entity patch | 第 1 集 episode_card/script_unit stale |

追问条件：

- 用户说“重新拆”但没有集数、每集时长、是否保留原文分集，且当前没有可用 generation_config。
- 用户要求集数和源文本体量明显冲突，需要确认是降低时长、压缩剧情还是允许弱集。

### 7.4 分集卡 `episode_cards`

关键可修改对象：

- `episodes[].opening_pressure`
- `episodes[].main_conflict`
- `episodes[].character_state`
- `episodes[].scene_plan`
- `episodes[].hook`
- `episodes[].continuity_delta`
- `episodes[].risk_notes`

| 用户说法 | scope | target_path 示例 | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- | --- |
| 加强第 2 集尾钩 | `field` | `episodes[2].hook` | 第 2 集卡、前后集卡、episode_split 第 2 集、story_bible 相关人物 | field patch | 第 2 集 script_unit stale |
| 重写第 2 集分集卡 | `entity` | `episodes[2]` | 第 1/2/3 集卡、episode_split 第 2 集、story_bible | entity patch | 第 2 集 script_unit stale |
| 全部分集卡节奏太平 | `artifact` | `episode_cards` | story_bible、episode_split、generation_config、旧 episode_cards | regenerate | script_context/script_unit/scripts invalidated |

追问条件：

- 用户说“这集不行”但没有当前选中或当前打开集。
- 用户要求修改第 N 集，但 episode_cards 中不存在第 N 集。

### 7.5 剧本上下文 `script_context`

`script_context` 默认内部使用，用户不直接编辑。

刷新条件：

- episode_cards confirmed 后自动生成。
- story_bible 的人物声口、禁改事实、关系发生影响剧本的修改后，标记 stale。
- episode_cards 整体或目标集发生修改后，相关 script_context stale。

上下文：

- story_bible 的人物、关系、声口、禁改事实。
- episode_cards。
- generation_config。
- 剧本格式、平台和对白规则。

### 7.6 剧本 `script_unit` / `scripts`

关键可修改对象：

- `script_unit.script_text`
- `script_unit.scenes[]`
- `scenes[].blocks[]`
- `continuity_delta`
- `risk_notes`

| 用户说法 | scope | target_path 示例 | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- | --- |
| 选中一句，让 OS 更狠 | `selection` | selection snapshot | selected_text、before/after、episode_card、script_context、角色声口 | `patch_script_span` | script_unit 刷新，scripts 聚合刷新 |
| 这场戏重写 | `scene` | `scenes[scene_id]` | 目标 scene、前后 scene、episode_card、script_context | scene patch | script_unit/scripts 刷新 |
| 第一集重写 | `episode` | `episode_id=1` | 第 1 集 episode_card、script_context、旧 script_unit、相邻集摘要 | full script_unit | scripts 聚合刷新 |
| 第 3 集生成失败，继续 | `task` | failed task cursor | failed_task_id、episode_id、已完成 episode、episode_cards、script_context | rerun task | 从失败 episode 继续 |

剧本局部 patch 禁止：

- 返回整份 `scripts`。
- 修改未选中的其他集。
- 改动 episode_card 已确认结构，除非用户明确要求上游也改。

## 8. 非小说链路 Revision Matrix

### 8.1 素材库 `material_bank`

| 用户说法 | scope | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- |
| 增加一个反派素材 | `entity` | source_input、当前 material_bank、用户新增说明 | 新素材条目 patch | story_seed 以后 mark_stale_only |
| 删除无关素材 | `entity` | 当前 material_bank、被删条目 | delete patch | story_seed 以后 mark_stale_only |
| 素材库整体重整 | `artifact` | source_input、generation_config、旧 material_bank | regenerate | story_seed 以后 invalidated |

### 8.2 故事种子 `story_seed`

| 用户说法 | scope | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- |
| 主线换成复仇 | `field` | material_bank、当前 story_seed、用户要求 | field patch | series_blueprint 以后 mark_stale_only |
| 故事种子整体不对 | `artifact` | material_bank、source_input、generation_config、旧 story_seed | regenerate | series_blueprint 以后 invalidated |

### 8.3 剧集蓝图 `series_blueprint`

| 用户说法 | scope | 上下文 | 输出 | 下游 |
| --- | --- | --- | --- | --- |
| 第二阶段节奏加快 | `entity` | story_seed、series_blueprint、目标阶段 | phase patch | episode_cards 以后 mark_stale_only |
| 全部集数重排 | `artifact` | story_seed、material_bank、generation_config、旧 blueprint | regenerate | episode_cards 以后 invalidated |

### 8.4 非小说分集卡 `episode_cards`

规则同小说分集卡，但上游上下文替换为：

```text
material_bank + story_seed + series_blueprint + generation_config
```

### 8.5 非小说剧本

规则同小说剧本，但上游上下文替换为：

```text
material_bank + story_seed + series_blueprint + episode_cards + script_context
```

## 9. Worker 输出合同

Worker 不能自由发挥输出整块内容。输出格式由 `revision_plan.operation` 决定。

字段 patch：

```json
{
  "operation": "patch_field",
  "artifact_id": "",
  "base_version": 1,
  "path": "characters[0].goal",
  "old_value": "",
  "new_value": "",
  "reason": "",
  "downstream_impact": []
}
```

实体 patch：

```json
{
  "operation": "patch_entity",
  "artifact_id": "",
  "base_version": 1,
  "path": "characters[0]",
  "old_entity": {},
  "new_entity": {},
  "reason": "",
  "downstream_impact": []
}
```

区块 patch：

```json
{
  "operation": "patch_section",
  "artifact_id": "",
  "base_version": 1,
  "path": "relationships",
  "old_section": [],
  "new_section": [],
  "reason": "",
  "downstream_impact": []
}
```

整块重生成：

```json
{
  "operation": "regenerate_artifact",
  "artifact_type": "",
  "supersedes": "",
  "payload": {},
  "downstream_invalidations": []
}
```

剧本选区 patch：

```json
{
  "operation": "patch_script_span",
  "artifact_id": "",
  "base_version": 1,
  "episode_id": 1,
  "scene_id": "",
  "node_id": "",
  "start_scene_id": "",
  "end_scene_id": "",
  "start_line_id": "",
  "end_line_id": "",
  "line_ids": [],
  "selection_start": 0,
  "selection_end": 0,
  "old_text": "",
  "new_text": "",
  "replacement_lines": [],
  "continuity_note": ""
}
```

多行 patch 的 `replacement_lines` 必须与 `line_ids` 一一对应，不得新增、删除或重排剧本行，也不得改变人物、行类型和场景归属。

失败重跑：

```json
{
  "operation": "rerun_failed_task",
  "run_id": "",
  "failed_task_id": "",
  "resume_cursor": 0,
  "resume_from_episode_id": 0
}
```

## 10. 前端刷新规则

| 操作 | 中间区域 | 右侧 Agent | 左侧状态 |
| --- | --- | --- | --- |
| field patch | 只刷新字段所在卡片/区块 | 显示修改摘要和确认点 | 当前 artifact 保持待确认或已生成 |
| entity patch | 刷新实体卡片 | 显示修改摘要 | 相关下游显示可能过期 |
| section patch | 刷新区块 | 显示影响范围 | 相关下游显示可能过期 |
| regenerate artifact | 整个 artifact 重渲染 | 显示新版本确认卡；已有下游时显示处理选择 | 下游先保持可见，用户选择重新生成后才标记待更新 |
| script span patch | 替换选区或显示 suggestion | 显示接受/拒绝或已应用 | scripts 聚合刷新 |
| script episode regenerate | 刷新目标集 | 显示目标集生成完成 | 总稿刷新 |
| failed task rerun | 完成一个 task 出一个状态 | 显示失败点和恢复点 | 当前步骤运行中 |

硬规则：

- 不能只在聊天里说“已修改”，中间区域必须同步刷新。
- 用户发送请求后，旧 checkpoint 按钮必须禁用。
- 已完成的 task 不能继续显示 loading。
- 如果 patch 影响已有下游，前端必须显示影响确认；用户选择重新生成后显示 stale，不能在确认前隐藏或覆盖旧内容。

## 11. 下游影响策略

| 修改位置 | 默认策略 |
| --- | --- |
| source_input 替换 | 有已有下游时先确认；选择重新生成后全下游 stale |
| story_bible field/entity/section/artifact 修改 | 有已有下游时先确认；选择重新生成后从 episode_split 开始 stale |
| episode_split field/entity/section/artifact 修改 | 有已有下游时先确认；选择重新生成后从 episode_cards 开始 stale |
| episode_cards field/entity/section/artifact 修改 | 有已有下游时先确认；单集目标只影响对应 script_unit 和 scripts |
| script_context regenerate | 内部流程使用；script_unit/scripts 按依赖刷新 |
| script_unit span/scene patch | scripts 聚合刷新 |
| script_unit episode regenerate | scripts 聚合刷新 |

`mark_stale_only` 表示不自动覆盖下游，只提示用户下游可能需要重算。

`invalidated` 表示下游逻辑已经不可靠，继续前必须重算。

`rerun_affected_downstream` 只在用户明确要求“改完继续生成/重新生成受影响部分”时触发。

### 11.1 已有下游确认规则

修改当前 artifact 时，runtime 必须先计算理论影响范围，再与当前已经存在的有效下游 artifact 求交集：

- 交集为空：不显示下游处理配置。当前 artifact 确认后，正常流程直接使用最新版本继续生成。
- 交集非空：当前 artifact 新版本生成后，下游保持原状态和可见性，不得立即 stale、invalidated 或隐藏。
- 当前 artifact 确认卡增加“保留现有后续内容”和“重新生成受影响内容”。
- 选择保留：下游不覆盖、不失效，本次修订结束。
- 选择重新生成：才把实际受影响 artifact 标记为 stale，写入 `run.invalidated_artifacts`，并从第一个受影响节点继续生成。
- 新下游版本生成成功前，旧版本继续可见并显示“待更新”；新版本成功后再 supersede 对应旧版本。
- 单集修改只处理依赖矩阵命中的单集及聚合总稿。
- 最终 `scripts` 没有下游，不显示该配置。

`run.invalidated_artifacts` 记录用户明确选择重生成后实际进入待更新状态的 artifact ID；仅有理论影响或用户选择保留时不得写入。

### 11.2 定位、上下文与刷新实现约束

- `artifact_type` 先于 `artifact_id` 生效；当前页面 artifact 类型与用户明确目标不一致时，禁止借用当前页面 ID。
- 后端按目标 artifact 类型解析最新有效版本；`superseded`、`invalidated` 或类型不匹配的 ID 不得作为修改目标。
- 剧本必须同时按 `episode_id` 定位，不能用“最新一集”代替用户指定集数。
- 上下文依赖按修改目标组装，不按 run 当前停留步骤组装。
- Main Agent 给出的字段、实体、场景和集数定位具有最终控制权；内容模型返回的定位只能被校验，不能改写控制目标。
- `characters[主角].goal`、`characters[name=主角].goal` 等命名选择器必须解析到唯一实体；未命中时保持原 payload，不产生残缺新版本。
- schema 已知字段和目标 payload 中真实存在的扩展字段都可局部修改；不存在的任意路径必须拒绝。
- entity patch 支持替换、新增和删除，且不得改变兄弟实体。
- 每次修改落库保存完整 payload，旧版本 superseded，新版本号递增；前端选择最新非 superseded、非 invalidated 版本。
- 下游进入 stale 后旧内容继续可见；对应新版本生成成功后才 supersede 旧内容。

## 12. 必须追问的情况

以下情况不能猜：

- 用户说“这个不行”“再优化一下”，但没有选区、当前 artifact、字段或集数。
- 用户说“修改主角”，但无法判断是改目标、声口、关系还是整个人设。
- 用户说“重写一集”，但没有说明第几集，且当前视图不在某一集。
- 用户要求改变集数、分集边界、主线、人物关系，但没有确认是否接受下游失效。
- 用户同时提出互相冲突的目标。
- 目标 path 无法映射到 artifact schema。

追问时要给可选项，不要泛泛问“请补充”。

示例：

```text
你是想只修改主角的目标，还是重写主角整个人设？
```

```text
你要重写第几集？如果是当前正在看的第 2 集，我可以直接按第 2 集处理。
```

## 13. Main Agent 决策顺序

每次用户输入必须按这个顺序处理：

1. 读取当前 project、run、approval、active_task。
2. 读取当前视图、当前 artifact、selection_context。
3. 判断是否是闲聊、解释、生成、确认、暂停、修改、重写、重跑。
4. 如果是修改，产出 `revision_plan`。
5. 用 revision matrix 校验 plan。
6. 如果 plan 不可落地，追问。
7. 如果 Agent 回复正在追问，即使 next_action 被错误输出为执行动作，也必须强制降级为 reply，不调用 worker；禁止改写追问文案掩盖矛盾。
8. 如果 plan 可落地且回复明确执行，组装 `RevisionContextPack`。
9. 调用对应 worker。
10. runtime 应用 patch 或 regenerate。
11. version bump，当前 artifact 旧版本 superseded 或保留历史。
12. 如果存在有效下游，等待用户选择保留或重新生成；确认前不修改下游状态。
13. 仅在用户选择重新生成时标记 stale、记录 invalidated artifact ID 并启动后续生成。
14. 推送前端刷新和右侧 Agent 消息。

## 14. 实现顺序

1. Main Agent 输出 `revision_plan`，不要直接让 worker 改。
2. 后端新增 revision matrix 校验器。
3. 后端新增 `RevisionContextPack` assembler。
4. Worker 增加 patch-only prompt。
5. Runtime 增加 apply patch、version bump、stale/invalidated 标记。
6. 前端按 patch/regenerate 类型刷新对应区域。
7. 小说链路自测：field/entity/section/artifact/selection/episode/rerun。
8. 非小说链路自测：material_bank/story_seed/series_blueprint/episode_cards/script_unit。

## 15. 验收用例

必须覆盖：

- “修改主角目标”只改 `characters[主角].goal`。
- “修改主角角色信息”重写整个主角对象，不改其他角色。
- “人物关系不对”改 relationships 区块。
- “故事圣经整体不行”重生成 story_bible，并让下游 invalidated。
- “第 2 集尾钩加强”只改第 2 集分集卡 hook。
- “第 2 集分集卡重写”只重写第 2 集卡。
- “目标原文字数改成 700 重新拆”重生成 episode_split，并让下游 invalidated。
- 选中剧本文本后说“OS 情绪增强”，只替换选区或目标节点。
- 同集跨多行或跨场景选中后提出修改，只替换连续选区；首尾保留未选中文本，中间行保持原结构。
- 第 3 集剧本失败后说“继续”，从第 3 集 task 继续，不从第 1 集重跑。
- 用户说“这个不行”但无定位时，Agent 追问。
- Agent 回复包含追问但 next_action 为 revise 时，只显示原始追问且不调用 worker。
- 修改上游但尚无下游内容时，不显示下游处理配置，确认后正常继续生成。
- 修改上游且已有下游内容时，确认前所有下游保持可见和原状态。
- 选择“保留现有后续内容”时，下游不变且 `invalidated_artifacts` 为空。
- 选择“重新生成受影响内容”时，只记录实际受影响 ID，旧版本显示待更新，新版本完成后逐项替换。

## 16. 选区修改运行时守卫

- 当前请求携带有效 `selection_context` 时，选区是修改目标的最高优先级事实；控制模型不能把目标改写成其他 artifact、集、场景或节点。
- 剧本选区统一执行 `patch_script_span`。用户说“情绪增加”“动作变大一些”等明确要求时直接执行，不再先回复确认话术，也不能只创建聊天消息而不创建 revision task。
- “改啊”“赶紧改”“你倒是改”等短追问，只能在最近四轮内继承尚未被成功消费的选区；已有新选区时，新选区立即替换旧目标。
- 上一次局部修改失败且 run 保留 `active_revision` 时，新的明确选区修改可以替换失败任务；普通生成失败仍必须按失败 task cursor 恢复，不能借此跳过失败守卫。
- worker 的 patch 响应允许纯 JSON 或 Markdown JSON 代码块，解析前必须提取唯一 JSON 对象；无效、空 patch 或越界 patch 不创建新 artifact 版本。
- 局部修改成功后只允许目标 span 变化；旧版本标记 `superseded`，新版本号递增，聚合 `scripts` 在确认后刷新。

### 16.1 真实回归基线（2026-07-16）

- 第 14 集 v1 -> v2：“情绪增加”只替换目标台词，其他 38 行逐字不变。
- 第 14 集 v2 -> v3：“动作变大一些”只替换目标动作行，其他 38 行逐字不变。
- 两次修改均命中当前选区、生成待确认新版本，未暴露 SQLite 原始错误，也未出现只回复不执行。

### 16.2 主控快速路径与超时恢复

- 当前请求同时具备有效选区和明确修改方向时，由 Main Agent 的确定性守卫直接产出 revision decision，不再调用控制模型重复判断意图。
- 快速路径不绕过 Main Agent、RevisionContextPack、内容 worker、Runtime、版本、审批或下游刷新；内容模型仍读取目标 artifact 完整正文和必要上游上下文，只返回目标 patch。
- 无选区、范围不清、复合要求、解释/比较、整场整集重写及流程控制仍交给控制模型判断。
- 控制模型上下文只携带决策所需摘要；run metadata、当前产物 excerpt、artifact ID 列表均限深、限项、限长，禁止把批次 checkpoint 或完整剧本再次塞入意图识别输入。
- 控制模型超时必须记录为 `unsupported + control_model_timeout`，回复明确说明“控制模型请求超时”，不能伪装成系统不支持。
- 超时后用户说“重试”时，如果最近未消费请求包含有效选区，则恢复原始选区和原始修改指令执行；不能把“重试”本身发给内容模型。
- 所有可执行动作统一检查 Agent 回复：只要回复在询问、要求补充或要求确认，本轮强制降级为 reply，不创建 run/task。
- `next_action=reply` 时禁止使用“我会修改”“生成新版后确认”等执行承诺；没有任务就明确告诉用户尚未执行。

### 16.3 选区引用消息合同

- 发送前，输入区显示“已引用选区”。
- 点击发送后，用户消息必须携带并展示选区引用，包括可读 artifact、集/场、选中文字和引用版本。
- 请求提交后输入区立即清空选区，表示该引用已随消息发出；普通请求失败时恢复输入区引用，`WORKSPACE_BUSY` 等结果未知场景先刷新状态，不自动重复提交。
- 刷新页面后从 message.selection_context 恢复历史引用卡，但不把已经被 revision/approve/generate 消费的历史选区重新放回输入区。
- 历史引用显示原始 `vN`，不能悄悄改指向新版本文字。

### 16.4 发布回归（2026-07-16）

- 正式 Eino 服务上，明确选区修改返回 `runtime=rule`、`reason=forced_revision_from_current_selection`，Claude 控制模型调用为 0。
- Gemini 内容模型调用 1 次，v1 -> v2 只替换目标动作行，其余剧本逐字不变；确认后聚合 scripts 递增到 v2。
- 用户消息落库保留 artifact_id、version、episode_id、scene_id、line_id、selected_text、before/after context 和 selection offsets。
# 2026-07-17 补充：集合结构修改

所有包含列表字段的主要 artifact 统一支持 `patch_artifact_collection`，不局限于原文拆集或分集卡。适用场景包括人物/素材/阶段/分集的拆分、合并、插入、删除和移动。

内容模型只返回一个结构操作和受影响 items；运行时执行索引边界校验并保持区间外内容。`episodes` 数量变化时，由运行时统一重编号并同步 run 级目标集数，模型不得自行重写相邻集或整份 artifact。

如果目标 artifact 已有下游内容，新版本生成后先保留下游，向用户展示统一的下游处理确认；只有用户选择重新生成后，才失效受影响的后续 artifact。
