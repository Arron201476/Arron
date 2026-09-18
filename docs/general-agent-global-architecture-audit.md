# 通用 Agent 全局架构审计与整改基线

状态：整改权威基线  
日期：2026-08-12  
适用范围：阶段 9 继续验收前的框架纠偏

## 1. 审计结论

当前系统已经具备可用的生产型 Runtime，但它只用三个重型领域工作流验证了扩展性。现有合同正确规定了“一个 Run 只绑定一个 Capability”，却遗漏了反向边界：

```text
一次 Skill Invocation 不一定创建 Run。
只有启动 stateful_workflow，且用户完成必要确认后，才创建业务 Run。
```

因此，当前实现可以承载三个剧本生产工作流，但还不能据此宣称已经形成完整的通用 Skill 执行框架。阶段 9 暂停收口，先按本文件整改。

## 2. 统一概念模型

| 概念 | 权威含义 | 是否持久化 |
|---|---|---|
| Agent Shell | 对话、意图、能力发现、上下文路由和 Guard | 是 |
| Skill | 用户可引用或 Agent 可路由的可复用能力说明 | Definition 持久化 |
| Rule | 某一步按需加载的领域约束，不是用户能力 | Definition 资产 |
| Tool | 模型、文件、搜索、OCR、MCP 等执行资源 | 注册信息与调用审计 |
| Skill Invocation | 某一轮消息实际选用一次 Skill | 是，轻量记录 |
| Background Task | 单目标异步任务，可重试但没有多检查点业务流程 | 按需持久化 |
| Business Run | 多步骤、可审批、可暂停恢复的有状态生产流程 | 是 |
| Step Run | Business Run 内的一步执行 | 是 |
| Artifact | 有稳定业务身份和版本的生产产物 | 是 |

以下推导必须成立：

```text
Skill Definition 1 -> N Skill Invocations
Skill Invocation 0 -> 1 Background Task
Skill Invocation 0 -> 1 Business Run
Business Run 1 -> N Step Runs
```

禁止使用 `Skill == Run`、`Capability == Run` 或“调用 Skill 就创建 Run”的隐含假设。

## 3. Skill 执行模式

后端 Capability Definition 必须声明 `execution_mode`：

| execution_mode | 用途 | 创建 Run | 示例 |
|---|---|---:|---|
| `inline` | 当前 Agent turn 内完成的只读或轻量操作 | 否 | 图片理解、内容解释、格式整理、规则辅助 |
| `background_task` | 单目标异步任务，需要进度、取消或重试 | 否 | 长文 OCR、批量转码、异步导出 |
| `stateful_workflow` | 多步骤、产物版本、审批、暂停恢复 | 是 | 小说转剧本、非小说转剧本、视频参考创作 |

`creates_run` 是服务端根据 `execution_mode == stateful_workflow` 派生的公开字段，禁止成为第二份可独立配置的真源。

第一版三个用户 Skill 全部是 `stateful_workflow`。这只是当前产品集合的事实，不是通用 Agent 的全局规则。

## 4. Run 创建门禁

创建 Business Run 必须同时满足：

1. Skill 的 `execution_mode` 为 `stateful_workflow`；
2. 用户显式选择或 Agent 路由得到已注册 Skill；
3. 输入和配置通过 Schema；
4. 必要 Asset/Asset Set 已封存；
5. 用户完成该工作流要求的启动确认；
6. Project 没有冲突的活动写 Run；
7. Runtime Guard 通过。

以下操作不得创建 Run：

- 点击 Skill 菜单；
- 仅发送 Capability 引用；
- 普通聊天；
- 图片理解；
- 查询当前进度；
- 查看或解释 Artifact；
- 在既有 Run 中审批、重试、暂停、恢复或修改产物；
- `inline` Skill Invocation；
- `background_task` Skill Invocation。

## 5. 状态归属

### 5.1 Project

保存作品级长期状态：主 Conversation、Assets、Run Index、候选剧本、最终稿和当前活动写 Run。

### 5.2 Conversation

一个 Project 第一版只有一条连续主对话。Skill 和 Run 都不能创建独立人格或替换主对话。

### 5.3 Skill Invocation

记录本轮选用了哪个 Skill、执行模式、来源消息、状态以及可选 `task_id/run_id`。它是调用审计，不是工作流状态机。

### 5.4 Business Run

只保存 `stateful_workflow` 的业务状态。Run 固定 Capability ID/Version，不在中途切换顶层 Skill。

### 5.5 View 与 Focus

- `active_run`：Runtime 权威的当前活动写 Run；
- `viewed_run`：用户当前查看的历史生成记录；
- `active_artifact`：当前中间工作区展示的 Artifact Version；
- `selection/focus`：本轮明确选中的字段、节点或文本；
- 这些值可以不同，禁止互相覆盖。

## 6. 上下文路由

主 Agent 每轮只获得：

```text
Project Routing Context
+ 当前请求
+ 最近相关 project messages
+ compact Run Index
+ Active Run summary（如存在）
+ Viewed Run summary（如存在）
+ immutable Selection / resolved target（如存在）
```

不得默认注入所有 Run 的消息和 Artifact Payload。

消息归属使用：

```text
project
invocation
run
artifact
```

- 普通聊天属于 `project`；
- Skill 选择到 Run 创建前属于 `invocation`；
- 工作流执行、进度和检查点属于 `run`；
- 明确查看、引用或修改某产物属于 `artifact`；
- 被动打开一个 Artifact 不足以把“你好”等普通聊天归入该 Artifact。

当用户明确选择新 Skill 时，只读取项目级通用消息和本次 Invocation，不继承历史 Run 的配置、Rules 或 Artifact。只有用户显式引用历史产物时，Runtime 才组装对应最小上下文。

## 7. 并发和冲突

第一版每个 Project 最多一个活动写 Business Run，但不等于运行中禁聊：

- 普通聊天：允许；
- 只读查询和解释：允许；
- 查看历史 Run/Artifact：允许；
- 对当前产物提出修改：在安全检查点执行或排队；
- `inline` 只读 Skill：允许；
- 不写作品状态的 `background_task`：允许；
- 新 `stateful_workflow`：阻止启动，要求先结束当前写 Run；
- 任何并发写操作：必须经过资源锁和版本校验。

旧合同中的“运行中禁聊”和“普通消息 API 返回冲突”删除，由以上规则取代。

## 8. 前端映射

### 8.1 Agent 对话

- 始终显示一条项目级连续对话；
- 不因存在多个 Run 而隐藏全部消息；
- 可以用轻量标签标识消息所属 Skill/Run；
- 切换生成记录只改变产物和进度视图，不切换聊天会话。

### 8.2 生成记录

- 只展示 Business Run；
- `inline` Skill 调用不进入左侧生成记录；
- `background_task` 进入任务/活动区，不伪装成剧本生产 Run；
- 同一 Skill 多次正式启动可以产生多个 Run。

### 8.3 Skill 菜单

菜单来自 Public Capability Registry。前端可以读取 `execution_mode` 和 `creates_run` 决定交互，但不自行决定是否创建 Run。

### 8.4 内部产物

`source_manifest`、`script_context`、`script_handoff` 等内部 Artifact 默认不进入用户目录。业务需要说明材料范围时，应投影为用户语言，不直接展示 `kind`、`parent`、内部 ID 或原始 JSON。

## 9. 必须保留

1. 一个 Project 一条主 Conversation；
2. Agent Shell 与领域 Capability 分离；
3. Rules 按 Step 加载；
4. Runtime 是 Run、Artifact、Approval 和版本的权威；
5. 一个 Business Run 固定一个顶层 Capability；
6. 一个 Project 一个活动写 Run；
7. View、Selection、Artifact Version 和 Target Resolution 的服务端校验；
8. Eino 编排与业务 Runtime 分离；
9. 三个现有剧本生产 Skill 的状态流程和产物合同。

## 10. 必须修改

1. Capability Manifest 增加 `execution_mode`；
2. Public Manifest 增加派生的 `creates_run`；
3. Guard 只有在 `stateful_workflow` 下才允许提出 `collect_run_configuration/start_run`；
4. `StartRun` 再次校验 Capability 执行模式；
5. 增加轻量 `SkillInvocation` 归属，替换“Capability 临时消息等于未来 Run”的隐含逻辑；
6. Runtime Context 分离 Active Run、Viewed Run、Run Index；
7. Recent Messages 按 project/invocation/run/artifact 过滤；
8. 前端恢复统一连续对话；
9. 内部 Artifact 从用户目录移除；
10. 修订冲突文档和阶段验收矩阵。

## 11. 必须删除

1. “每次调用 Skill 都创建独立 Run”的规则；
2. “当前查看 Run 覆盖 Active Run”的处理；
3. “多个 Run 时默认隐藏全部对话”的前端逻辑；
4. “运行中禁聊”的旧合同；
5. 使用被动 View 把所有普通消息归入 Artifact 的逻辑；
6. 将 `source_manifest` 原始工程字段直接展示给用户的逻辑；
7. 把目标设计描述成已验收实现的结论。

## 12. 尚未实现

1. `inline` Skill 的通用执行器；
2. `background_task` Skill 的通用任务协议；
3. Main Agent 按需读取历史 Run 详情的受控 Query Tool；
4. 面向未来轻量 Skill 的完整前端投影；
5. 多执行模式的插件式测试 Fixture。

这些项目必须明确标记为未实现，不能仅凭当前三个工作流可运行就宣称框架已完整覆盖。

## 13. 验收反例

整改完成至少通过：

1. 一个对话连续调用 30 次 `inline` 测试 Skill，Run 数量仍为 0；
2. 同一对话先后正式启动三个生产 Skill，产生 3 个独立 Run；
3. 同一生产 Skill 正式启动两次，产生 2 个 Run，产物不覆盖；
4. Run A 运行时普通问候仍是 project chat；
5. Run A 运行时查看 Run B，不改变 Active Run；
6. Run A 运行时启动 Run B 被 Guard 阻止，但聊天和查看不受影响；
7. 新 Skill 不读取旧 Run 的消息和配置；
8. 显式引用旧 Artifact 时只装入该 Artifact 和最小依赖；
9. 多 Run 项目仍显示连续对话；
10. 内部 Artifact 不进入用户目录；
11. Registry 为空时普通聊天可用；
12. 所有设计、代码和测试对“Invocation 不等于 Run”使用同一语义。

## 14. 执行顺序

1. 修正 Capability/Invocation/Run 合同；
2. 完成 Manifest、Compiler、Public Registry 和 Guard；
3. 完成 Message Routing 与 Runtime Context；
4. 完成前端统一对话和生成记录投影；
5. 增加执行模式与多 Skill 反例测试；
6. 回归三个现有生产工作流；
7. 浏览器验收后恢复阶段 9。
