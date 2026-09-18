# 阶段 5 最终交互基线审计

状态：T0 交互基线已冻结并通过；阶段 5 总状态见 `stage-5-final-acceptance.md`
日期：2026-08-04  
审计方式：最终 Figma 实图、阶段 4 机读状态、规范合同和现有后端代码交叉核查

## 1. 结论

阶段 5 的核心架构不需要推倒重来，但不能直接沿用提前开发资产继续写代码。

必须先完成一次交互合同收口：

```text
最终 Figma 状态
-> Public Action ID
-> API typed command
-> Runtime command / Guard
-> 前端组件状态
```

Runtime 状态机、Artifact/Version、Approval Snapshot、暂停/恢复/重试和消息与 Run 分离的基本方向成立。本审计最初识别的 UI 投影、Approval Option、Agent Message 合同和服务端 `available_actions` 缺口已在 T0 关闭；Eino Main Agent、通用对话、Skill 路由和零 Capability 验收已在 T1 关闭。

## 2. 最终交互基线

1. Composer 固定在 Agent 面板底部，使用 Empty / Active / Context / Running / Paused / Error 六状态。
2. 运行中锁定自由聊天，但 Composer 不消失；右侧动作显示 Pause。
3. 暂停后右侧动作显示 Resume；失败且可重试时显示 Retry。
4. 业务确认主操作只出现在 Agent Timeline ApprovalCard，中间工作区不复制确认按钮。
5. “让 Agent 修改”只通过选区、子项上下文、Approval Option 或 Composer 发起，不在每个步骤常驻。
6. Composer 工具栏入口显示“能力”，菜单显示三个具体 Skill；协议始终传结构化 `capability_ref`。
7. 普通消息只产生 AgentDecision / proposed_action，不隐式启动 Run；用户在确认卡显式确认后调用 Start Run API。
8. Cancel 进入 More Menu 并二次确认，不占 Composer 主动作位。

## 3. 已修正的历史资产

| 历史问题 | 处理 |
|---|---|
| 阶段 4 仍标记“未通过/待总验收” | 已记录产品负责人通过并把主阶段切换为阶段 5 |
| 实现交接仍引用旧 v2.4、方向 B 候选和已删除 Frame ID | 已重写为最终组件、页面、状态和 Node ID |
| 前端合同把业务确认放在 Active Workspace | 已改为 Agent Timeline ApprovalCard 唯一主操作 |
| 前端合同把运行态写成整个 Composer 禁用 | 阶段 5 当时改为只锁自由文本；该历史规则已被 2026-08-11 的通用定位与修改设计升级为运行中保留输入、写请求等待安全检查点 |
| API 合同允许 Message Command 与确认卡两条启动路径 | 第一版已收敛为确认卡显式调用 Start Run API |
| 阶段 4 视觉合约仍是候选和未勾选门禁 | 已更新为最终通过状态 |

## 4. 尚未修正的代码和机器合同

以下内容属于阶段 5 T0/P0。2026-08-03 已按最终交互基线落入业务代码与合同测试；状态以各项结论为准。

### P0-A：Approval 与 Run Control 已拆分

Capability Manifest 与 Compiler 已禁止把 `pause`、`resume`、`cancel` 和 `retry_failed` 编译进 Approval `allowed_actions`。

目标：

- Approval Options 只承载业务决策；
- Pause/Resume/Retry/Cancel 由 Run/Task `available_actions` 单独下发；
- 等待审批时如允许暂停，也通过 `pause_run` 控制动作显示在 Composer，不作为 Approval Resolution。

### P0-B：Agent Message 合同已实现

Message Request 与 Message Exchange 已支持：

- `capability_ref`；
- `attachment_refs`；
- `selection_snapshot`；
- `client_context`；
- AgentDecision；
- Guard 后的 Agent Message；
- proposed_action / clarification / confirmation card payload；
- 用户消息、上下文、Agent 消息、决策和待确认动作的原子持久化。

### P0-C：服务端 Action Projection 已实现

Run Snapshot 与 HTTP 响应已经返回受控 `available_actions`。Resume 与 Retry 不只按状态判断，还校验能力版本、输入快照、来源可用性、写锁、失败码和 Step Retry Policy。

目标公共 Action ID：

```text
send_message
pause_run
resume_run
retry_failed_task
retry_failed_step
cancel_run
```

Approval Action 继续使用 `approve`、`request_ai_revision`、`edit_artifact`、`regenerate_artifact` 等独立集合。

### P0-D：Start Run 确认绑定已实现

HTTP Start Run 已绑定：

- `confirmation_message_id`；
- Guard 通过的 proposed_action；
- 精确输入和配置快照 Hash；
- 防止旧确认卡重放的版本字段。

待确认动作消费与 Run 创建处于同一事务。旧卡重放、版本变化、消息不一致、Capability 不一致以及 Input/Config 篡改都有独立拒绝路径。

### P0-E：通用 Agent Shell 最小闭环已完成

`backend/internal/shell` 已有 Eino Main Agent Graph、OpenAI-compatible 控制模型适配、确定性 Guard、结构化 `AgentDecision`、显式/推断 Skill 路由和零 Capability 追问回退。普通消息仍不创建 Run。

通用图片输入需求不删除，但其 Generic Image Tool 依赖 T2 冻结的 Provider Part、`ImageUnderstandingProvider` 和授权 Asset Resolver，因此不误报为 T1 已实现；完成后仍以普通 Agent Message 返回，不自动启动 Domain Run。

### P1-A：用户术语与内部术语需要分层

不做全局机械替换：

- UI 按钮：能力；
- 菜单和业务名称：具体 Skill；
- 协议：`capability_ref`；
- 内部执行：Capability / Run / Step / Rule / Worker。

阶段 1-3 历史文档可保留当时用词，但阶段 5 Public Manifest、API Label 和阶段 6 UI 必须执行这一分层。

## 5. 未发现需要回退的部分

- Business Runtime 继续作为业务状态唯一真源；
- Eino 只承担 Main Agent 判断图和单个 Executor 内的模型/工具编排；
- Capability Manifest 继续作为流程声明真源；
- 同一 Project 只允许一个活动写 Run；
- 暂停保留 Cursor，恢复不自动读取最新上游；
- 重试不重做成功 Task；
- Artifact Version、Approval Snapshot 和下游影响模型可继续使用；
- 视频按 Asset Set、逐集 Task、人工排序、缺集确认和聚合处理的方向不变。

## 6. 阶段 5 整改顺序

1. 冻结 Public Action Registry 和 Composer/Approval 状态映射。
2. 从 Manifest 与 Compiler 中拆出 Approval Action 和 Run Control Action。
3. 冻结 Message Request、AgentDecision、proposed_action 和 confirmation token/schema。
4. 补 Project/Run Snapshot 的 `available_actions` 投影。
5. 增加 API Contract Test：六种 Composer 状态、审批唯一入口、旧确认卡、活动 Run 禁聊和零 Capability。
6. 完成以上 T0 后，实现 Agent Shell/Eino 的 T1 最小闭环。

执行结论：第 1-5 项在 T0 完成；第 6 项在 T1 完成。T1 新增主控图、路由 Guard 和 HTTP 零能力/自动路由闭环测试，后端各包与 Capability validator 均已通过。

## 7. 阶段边界

本审计与独立 T1 验收只证明 T0、T1 已完成，不证明整个阶段 5 已完成。当前正式状态为：

```text
12 阶段总流程：阶段 5 / 12
阶段 5 当前步骤：T6 视频执行模式确认、性能收敛与整剧样本验收
```
