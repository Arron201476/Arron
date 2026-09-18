# 原文拆集分批运行合同

更新时间：2026-07-16

## 1. 目标

将小说链 `episode_split` 从“一次模型请求返回全部集数”重构为：

```text
原文索引 -> 全局拆集骨架 -> 3-5 集顺序分批定界 -> 确定性覆盖校验 -> 用户确认
```

本合同解决：

- 多集拆分的大请求稳定超时；
- 失败后只能从头重试；
- 模型自行编写 offset，后端无法验证；
- 用户长时间看不到有效进度；
- 上游版本在拆集期间变化，导致下游使用旧版本。

## 2. 用户可见边界

前端仍只展示一个“原文拆集”节点，不展示内部 task ID、cursor、candidate ID、offset 或 Eino 节点。

用户可见进度：

```text
正在规划整体拆分结构
正在确定第 1-5 集边界
已完成 5/30 集
正在检查原文覆盖
拆集完成，等待确认
```

生成期间只读；暂停或生成结束后允许编辑。内部批次完成结果只作为生成中预览，不能逐批确认。

## 3. 权威输入与版本锁

拆集启动时记录并锁定：

```json
{
  "source_input_artifact_id": "artifact_xxx",
  "source_input_version": 1,
  "story_bible_artifact_id": "artifact_xxx",
  "story_bible_version": 2,
  "generation_config_version": 1
}
```

`run=running` 时，后端拒绝 artifact 手动保存。暂停后可以编辑；若上游版本变化，旧拆集 checkpoint 不得继续使用。

## 4. 原文索引

原文索引由 Go 后端确定性生成，不调用模型。

```json
{
  "unit_id": "U0001",
  "start_offset": 0,
  "end_offset": 42,
  "text": "原文句子或段落",
  "boundary_after_allowed": true
}
```

索引按换行和句末标点建立稳定单元。模型不得生成 offset，只能选择后端提供的边界候选。

## 5. 全局骨架阶段

如果用户选择“保留原有分集”，后端必须先检测到从 1 开始、连续且可靠的原文分集标记。此时：

- 配置卡不再要求用户填写集数，只读展示识别结果；
- `target_episode_count` 等于 `detected_episode_count`；
- 后端根据原文标记确定性建立骨架，不调用模型重新划分全局边界；
- 每个 episode 只提供原标记对应的唯一合法边界，模型只能补充摘要、核心事件和风险字段；
- 未检测到可靠标记时，不展示该选项；伪造或过期的保留请求由后端拒绝。

### 输入

- 原文结构单元索引；
- 当前确认版故事圣经的必要字段；
- run 级生成配置；
- 用户锁定边界和修改说明；
- 目标集数。

### 输出

```json
{
  "episode_skeletons": [
    {
      "episode_id": 1,
      "approx_end_unit_id": "U0018",
      "core_event": "本集核心事件",
      "character_turn": "人物变化",
      "desired_hook": "期望尾钩"
    }
  ],
  "global_risks": []
}
```

骨架必须覆盖 `1..target_episode_count`，`approx_end_unit_id` 必须存在并严格递增，最后一集必须落到原文末单元。

该阶段只做全局分配，防止前段消耗过多素材；不输出最终 `episode_split`。

## 6. 分批定界阶段

默认每批 5 集，最后一批可少于 5 集。批次严格串行。

每批输入：

- 当前批次骨架；
- 上一批最终边界；
- 当前批次相关原文单元；
- 每一集合法边界候选；
- 剩余字数和剩余集数；
- 故事圣经关键事实；
- 前一集尾钩摘要。

每集候选窗口按剩余体量自适应：

```text
平均原文字数 = 剩余原文字数 / 剩余集数
窗口总长度 = clamp(平均原文字数 * 1.2, 300, 1200)
```

后端从结构单元边界中生成候选。候选示例：

```json
{
  "candidate_id": "E03-B04",
  "episode_id": 3,
  "offset": 1172,
  "cut_after_anchor": "断点前原文",
  "cut_before_anchor": "断点后原文"
}
```

模型输出：

```json
{
  "episodes": [
    {
      "episode_id": 3,
      "end_candidate_id": "E03-B04",
      "source_summary": "",
      "core_event": "",
      "character_turn": "",
      "boundary_reason": "",
      "hook_strength": "high | medium | low",
      "hook_type": "conflict | secret | decision | danger | source_supported_preview | weak_source_boundary",
      "information_density": "high | medium | low",
      "pacing_risk": "none | weak_source_boundary | low_information_density | likely_padding | source_too_short",
      "split_confidence": "high | medium | low",
      "requires_user_attention": false
    }
  ],
  "batch_risks": []
}
```

模型只能选择候选 ID。后端根据候选填充真实 `source_refs`、offset 和 `boundary_check`。

## 7. 确定性校验

每批完成后校验：

- episode ID 与当前批次完全一致；
- candidate ID 属于对应 episode；
- 边界严格递增；
- 当前批次第一集从上一批终点继续；
- 不产生空范围；
- 不超出原文。

全部批次完成后校验：

- 第 1 集从原文 offset 0 开始；
- 相邻集首尾连续；
- 最后一集结束于原文末尾；
- 没有遗漏、重复或顺序问题；
- `actual_episode_count` 等于目标集数；
- episode ID 完整覆盖 `1..N`。

只有通过校验才创建一个用户可见的 `episode_split` artifact。

## 8. Task 与 checkpoint

30 集默认任务：

```text
task_split_global_plan
task_split_batch_01_05
task_split_batch_06_10
task_split_batch_11_15
task_split_batch_16_20
task_split_batch_21_25
task_split_batch_26_30
task_split_validate_coverage
```

每个阶段完成后将内部 checkpoint 写入 run metadata：

```json
{
  "episode_split_progress": {
    "locked_inputs": {},
    "global_plan": {},
    "completed_episodes": [],
    "next_episode_id": 11,
    "completed_count": 10,
    "target_count": 30
  }
}
```

失败时保留已完成批次。从失败 task cursor 恢复，不重做已完成批次。全局计划或锁定输入版本不一致时，checkpoint 作废并重新开始。

## 9. Eino Graph

内容执行图增加专用拆集图：

```text
validate_split_stage_input
-> invoke_split_stage_model
-> validate_split_stage_output
```

Eino 负责阶段编排和输入输出节点；Go runtime 继续负责持久化、task cursor、候选边界、版本锁和确定性覆盖校验，避免双状态源。

## 10. 验收

必须覆盖：

1. 2 集、5 集、30 集拆分；
2. 全局骨架非法 ID 被拒绝；
3. 批次选择不存在的 candidate 被拒绝；
4. 第 11-15 集失败后从该批恢复；
5. 已完成前 10 集不重复调用；
6. 最终范围连续、无遗漏、无重复；
7. 运行中 artifact API 编辑被拒绝；
8. 暂停后修改上游会使旧 checkpoint 失效；
9. 前端只展示产品进度，不展示内部 task 名。
