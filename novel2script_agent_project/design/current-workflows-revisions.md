# 当前流程、审批与修改规范

更新时间：2026-07-20

## 两条主链路

小说链：

```text
source_input -> story_bible -> episode_split -> episode_cards
-> script_context -> script_unit（逐集）-> scripts
```

非小说链：

```text
source_input -> material_bank -> story_seed -> series_blueprint
-> episode_cards -> script_context -> script_unit（逐集）-> scripts
```

`script_context` 是内部辅助产物，不向用户展示确认卡；`scripts` 是全部 `script_unit` 的聚合阅读视图，不是另一份可独立编辑的剧本真源。

## 批处理策略

小说原文拆集先生成全局骨架，再按每批最多 5 集执行边界定界，最后做确定性全文覆盖、顺序、重叠和缺口校验。每个边界只给模型局部窗口，当前代码将边界窗口限制在 300 至 1200 字符。失败时保留检查点，从失败 task cursor 恢复。

小说和非小说的分集卡都按每批最多 5 集串行生成，最后合并并验证目标集数、集号唯一性、连续性和必要字段。前端仍把它显示为一个“分集卡”步骤。

剧本按分集顺序生成 `script_unit`。每集使用本集分集卡、统一剧本上下文、上一集结尾和累积连续性；失败时从失败集继续，不重做已成功集。

## 审批

除 `source_input`、内部 `script_context` 和聚合 `scripts` 外，主要业务 Artifact 完成后进入 `pending_approval`。用户确认后变为 `confirmed` 并继续下一节点；用户暂停则保留当前 cursor。

没有用户确认时不能越过 checkpoint。模型回复中的建议动作不能自动确认。

## 修改粒度

过程产物支持字段、实体、集合、段落和整个 Artifact；集合支持插入、删除、移动、拆分与合并。剧本支持单行局部、跨行范围、场景和单集修改。

局部修改只允许返回目标范围的 sparse patch；整体重生成才允许替换目标 Artifact。所有成功修改都会创建递增版本，旧版本标记 `superseded`，不能重置为 v1。

用户手动编辑和 Agent 修改使用同一版本冲突规则：必须携带 `base_version`；版本过期返回 HTTP 409 和最新 Artifact，不能静默覆盖。

## 下游影响

修改当前 Artifact 时先生成并保存当前新版本，不立即删除下游：

- 没有实际下游内容：不展示额外确认，未来节点自然读取新版本。
- 已有下游内容：旧下游继续可见，展示“保留现有后续内容 / 重新生成受影响内容”。
- 选择保留：下游不变，`invalidated_artifacts` 不增加。
- 选择重生成：只把实际受影响的旧版本标记为 `stale`，同步记录 ID，并从最早受影响节点继续；新版本成功后逐项替换旧内容。

最后一个业务节点没有下游，不展示下游处理确认。

## 运行中编辑

Run 为 `running` 时禁止手动编辑过程产物和剧本，也禁止发送新的执行消息。用户可以先暂停，待当前任务安全停在 cursor 后再修改，避免下游读取到执行中途变化的上游版本。

失败步骤重试只允许用于失败状态，从失败 task cursor 继续剩余任务；不能把未失败步骤当作“重试”重新运行。
