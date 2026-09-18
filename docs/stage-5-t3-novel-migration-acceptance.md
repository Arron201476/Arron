# 阶段 5 T3 小说链迁移验收

日期：2026-08-03  
状态：通过  
下一步：T4 非小说链迁移与共享质量审核闭环

## 1. 迁移结论

旧项目的小说专用 Eino Worker 不整体复制。当前项目采用统一边界：

```text
Manifest：步骤、Prompt、Rules、输入输出和审批声明
Business Runtime：来源单元、批次、顺序、覆盖、状态、版本和依赖
Eino Worker Graph：单个 Task 的模型调用、解析、一次修复和结果预检
Runtime Commit：Adapter、完整 Schema、业务约束和持久化
```

该结构保留了旧图的有效约束，但不把 `SourceMode` 和旧 Runtime 带入新项目。

## 2. 固定样本路径

固定样本：`acceptance/fixtures/novel/stable-source-sample.txt`，12 章。

已验证：

1. `source_input -> source_manifest`；
2. 来源单元分析与 `story_bible` 聚合；
3. 体量判断，体量不足时必须先确认扩写策略；
4. `split_episodes` 全局骨架和分批边界选择；
5. 拆集必须连续覆盖 1-12 集，来源边界存在、递增且覆盖原文末尾；
6. `episode_cards` 3 个顺序批次聚合为 12 集；
7. `script_context` 生成 12 个精确版本；
8. 12 个 `script_unit + script_handoff` 原子提交；
9. 整个剧本步骤统一确认；
10. 单集编辑只使对应 `script_handoff` 失效并刷新；
11. 上游修改先生成 Impact Preview；
12. 用户确认后按依赖组重生成，旧版本保留并记录替代关系；
13. 剧本确认后进入共享 `review_script_set`，审核执行由 T4 统一闭环。

## 3. 专项测试

- `TestBatchStepPlanningAndSequentialClaim`：通过；
- `TestVolumeFitInsufficientRequiresTransitionApproval`：通过；
- `TestEpisodeSplitInternalStagesRejectInvalidModelChoices`：通过；
- `TestImpactReviewLifecycle`：通过；
- `TestImpactReviewResolution`：通过；
- `TestRunnerEndToEnd`：通过；
- Worker Eino 正常与修复路径：通过；
- 阶段 2 机器矩阵：96 项、10 个 Fixture、17 个 Area，通过；
- Capability 合同：3 个 Manifest、34 个 Step、14 个 Adapter，通过。

T3 不宣称真实模型内容质量已通过；真实 Provider 内容抽查属于后续发布门禁。
