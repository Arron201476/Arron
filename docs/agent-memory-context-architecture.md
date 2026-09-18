# Agent Memory 与 Context 架构合同

状态：第一版实现基线  
适用范围：通用 Agent、所有 Skill、Artifact 修改和后续新增能力

## 1. 目标

系统只保留四个一级概念：

1. `System of Record`：不可由模型记忆替代的业务事实真源；
2. `Project Memory`：从事实真源派生、可重建、可检索的作品长期记忆；
3. `Context Engine`：按本轮任务检索、隔离、裁剪和组装模型输入；
4. `Retention & Audit`：生命周期、失效、删除和追溯策略。

不得把完整聊天、Runtime 状态、Artifact 版本和模型输入包笼统称为同一种 Memory。

## 2. System of Record

以下数据以 SQLite Runtime 为权威：

- Project、Conversation 和完整 Message History；
- Run、Step、Task、Approval、Config 和 Decision Snapshot；
- Asset、Asset Snapshot、Artifact、Artifact Version 和 Dependency；
- Target Resolution、Revision Request、Revision Attempt；
- Context Pack、Execution Attempt 和审计事件。

模型回复、Conversation Summary 和 Project Memory 与上述数据冲突时，必须服从当前 Runtime 和 Artifact Version。

## 3. Project Memory

Project Memory 是派生索引，不直接驱动业务状态迁移。第一版包含：

- `conversation_summary`：较早 Project Scope 消息的滚动提取摘要；
- `artifact_catalog`：当前 Artifact 的动态名称、类型、范围和版本引用；
- `confirmed_decision`：已密封的 Run Decision Snapshot 检索入口。

每条记忆必须保存来源类型、来源 ID、来源摘要、置信度和有效状态。Artifact 更新后同一目录记忆更新来源版本；Memory 表可从业务真源重建。

第一版不自动保存模型推断的事实、人物关系或用户偏好。未经用户确认的推断不能成为长期记忆。

## 4. Context Engine

主 Agent 每轮通过统一 Context Engine 获得：

```text
当前请求
+ 最近 12 条相关消息
+ 较早消息的检索结果
+ Project Scope 滚动摘要
+ 相关 Project Memory
+ Runtime Context
```

Context Engine 必须先执行 Project/Run/Artifact Scope 隔离，再执行历史检索。不得为了召回历史而重新混入无关 Skill Run。

Skill Worker 继续使用 `step_execution` Context Pack；Artifact 修改继续使用 `revision` Context Pack。三者共享作用域和权威优先级，但不共享同一输入模板。

## 5. 权威优先级

发生冲突时按以下顺序处理：

```text
用户本轮明确 Selection
> 当前 Runtime / Current Artifact Version
> 已确认 Decision Memory
> 历史消息检索和 Conversation Summary
> 模型推断
```

摘要只帮助恢复上下文，不得覆盖精确版本、审批或用户本轮指令。

## 6. 作用域

上下文只保留四级：

```text
Project -> Run -> Artifact -> Selection
```

Skill 是插件定义，Invocation 是调用审计记录，均不形成独立人格或独立长期记忆空间。

- Project 消息可进入同一作品的通用 Agent 上下文；
- Run/Artifact 消息只在查看、引用或修改对应目标时进入；
- Skill Worker 只读取当前 Run 中 Capability 声明的依赖；
- Project 之间不得自动共享消息、素材、Artifact 或 Memory。

## 7. 动态 Artifact 目录

Artifact 对 Agent 和用户的显示名称优先读取当前 Payload 的：

1. `artifact_label`；
2. `title`；
3. 注册 Artifact Type 的默认标签。

通用文档和通用表格必须以动态名称分别进入目录，不得统一显示或聚合成“文档”“表格”或 `generic_document`。

自然语言修改定位优先匹配动态名称，再使用 Artifact Type 别名和集数范围。多个候选无法稳定区分时必须澄清。

## 8. 生命周期

- 原视频按 Retention Policy 删除；
- Artifact、决策和来源元数据随作品保留；
- Message History 完整持久化，但较早消息不默认直接送入模型；
- Conversation Summary 增量覆盖较早 Project 消息；
- Project Memory 失效时标记并允许重建；
- 删除作品后，来源文件按删除任务处理，派生审计记录按现有策略保留。

## 9. 第一版非目标

- 跨作品自动引用；
- 用户级永久画像；
- 自动保存模型猜测的事实；
- 默认引入向量数据库；
- 让 Project Memory 直接修改 Run 或 Artifact 状态。

## 10. 验收要求

1. 超过近期窗口后，Agent 仍可通过摘要或检索找到早期明确消息；
2. 新 Skill 不继承无关历史 Run 的过程消息；
3. “大纲”和“小说第一集”等通用 Artifact 能独立展示和定位；
4. 当前 Artifact 与摘要冲突时，以当前 Artifact Version 为准；
5. Memory 条目均能追溯到 Message、Artifact 或 Decision Snapshot；
6. 关闭服务并重新打开后，业务状态、摘要和目录记忆仍可恢复。
