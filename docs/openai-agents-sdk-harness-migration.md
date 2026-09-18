# OpenAI Agents SDK Harness 隔离迁移合同

状态：本地架构收敛完成；Live Gateway、OCI 与预发布环境门禁待授权/环境
适用范围：通用主 Agent、OpenAI Agents SDK Sidecar、Go Runtime、前端 Agent 事件层  
原则：Skill 是主 Agent 的可选插件；SDK 负责通用 Agent 编排，Go Runtime 负责业务事实与受控执行。

## 1. 目标链路

迁移前：

`前端 -> Go 自建上下文/记忆/目标判断 -> SDK 解释 -> Go Guard -> Go Runtime`

迁移后：

`前端 -> SDK Runner + Session -> SDK 按需调用 Runtime 工具 -> SDK 形成结构化动作 -> Go Runtime 校验并执行 -> 统一事件流 -> 前端`

SDK 不直接访问 SQLite，不绕过版本、审批、权限、幂等和 Run 状态机。Go Runtime 不再先替 SDK 选择上下文、意图或目标。

## 2. 职责归属

| 职责 | 目标所有者 | 迁移结论 |
| --- | --- | --- |
| 多轮对话 Session、短期历史、摘要触发 | SDK Session/Memory adapter | 从 Go 手工预组装迁出 |
| 本轮意图、是否调用 Skill、目标候选搜索、工具选择 | SDK Agent | 从 Eino/正则/静态评分迁出 |
| 项目、对话、消息的持久化事实 | Go Runtime | 保留 |
| Artifact、Version、Lineage、Revision | Go Runtime | 保留并工具化 |
| Run、Step、暂停/恢复/重试、Approval | Go Runtime | 保留并工具化 |
| Capability/Skill 注册表和工作流合同 | Go Runtime | 保留，SDK 动态读取和调用 |
| 权限、状态、版本、幂等、安全检查 | Go Runtime Guard | 保留；只校验，不替 Agent 判意图 |
| 项目事实检索和精确版本读取 | SDK read tools + Go Runtime API | 扩充 |
| 创建/修改/重生成/启动 Skill | SDK write tools + Go Runtime command API | 受控接入 |
| 流式 token、tool、run、approval、artifact 事件 | SDK/Runtime 统一事件协议 | 迁移后单一前端投影 |
| 旧 Eino Main Agent、同步 Decide/Execute、Go 模型 Worker、Sidecar spike 路由 | 无 | 已从当前代码删除；状态化生成和完整视频模型调用均由 SDK Agent/Runner 执行；回滚依赖 Git 基线和迁移备份，不在运行时保留双 Agent 或直连模型 Worker |

## 3. 八步执行门禁

1. **冻结边界与基线**：完成本合同、机器可读门禁和当前行为回归基线。
2. **只读 Runtime 工具**：SDK 可列举/搜索/读取项目、Artifact、Run、Approval、Skill，不接收 Go 预选目标。
3. **Session/Memory**：SDK 使用持久 Session；Go 仍保存权威消息，Memory adapter 可重建、可隔离、不可串项目。
4. **意图与目标判断**：SDK 通过工具检索目标；用户显式选区优先，自然语言可定位；否定目标不得被误选。
5. **受控写工具**：创建、修改、重生成和确认均经 Runtime 命令、幂等键、版本和审批校验。
6. **Skill/Run 编排**：三个 Skill 均作为插件被 SDK 发现和启动；一个调用对应一个业务 Run，普通请求不强制挂 Skill。
7. **统一事件与本地回归**：前端消费统一事件；公开消息只接受 durable AgentTurn，完整回归和故障注入覆盖单终态、取消、恢复与幂等。
8. **单路径与可恢复发布**：删除重复运行时代码；通过 Git 基线、数据库自动迁移备份、功能开关和 workspace canary 回滚，不保留静默 fallback。

## 4. 不可破坏的不变量

- 不同 Project、Conversation、Skill Run 的 Session 和检索结果不得串联。
- 用户上传的 Asset 独立于 Skill，之后任意轮可被明确选用。
- 同一作品可有多个独立 Artifact；新建请求不得覆盖当前聚焦产物。
- 所有修改先生成候选版本；采用、放弃、重试和历史版本可追踪。
- 运行中可取消；结束后不显示暂停；重试不得复制已完成任务。
- SDK/模型超时或 Sidecar 重启不能伪造成功，不能写入半成品。
- 日志、事件和错误响应不得包含密钥、完整模型请求或大段用户内容。
- 三个 Skill 的 Prompt、Rules、Schema 和业务执行图不因 Harness 迁移被改写。

## 5. 自动回归矩阵

| 编号 | 场景 | 必须验证 |
| --- | --- | --- |
| G1 | 普通聊天 | 不启动 Skill，不创建 Artifact |
| G2 | 普通内容生产 | 不挂 Skill 也可创建多个独立、正确命名的通用 Artifact |
| G3 | 显式选区修改 | 精确定位版本和字段，生成候选，不覆盖其他 Artifact |
| G4 | 自然语言跨产物修改 | 正确实体和标题胜过界面焦点；否定提及不得成为目标 |
| G5 | 多轮记忆 | 记得相关事实；不相关旧消息不污染本轮；摘要后仍可恢复来源 |
| G6 | 项目隔离 | 同名 Artifact、同名人物和历史消息不跨 Project 泄漏 |
| S1 | 小说转剧本 | 配置、检查点、编辑、重生成、暂停恢复、最终剧本均正常 |
| S2 | 非小说转剧本 | 完整度确认、扩写策略、分析、Brief、分集和剧本链正常 |
| S3 | 视频参考创作 | ZIP/批次、逐集解析、重试、合集、分析与后续链正常 |
| R1 | 并发与幂等 | 重复发送/重复工具调用只产生一次业务写入 |
| R2 | 审批与版本冲突 | 过期版本、错误状态、未授权操作被 Runtime 拒绝并可恢复 |
| R3 | 流式/取消 | 用户消息立即显示；token/tool/run 状态有序；取消只有一个终态 |
| R4 | 故障回退 | 模型 5xx、超时、Sidecar 重启、Runtime 失败均无脏写和假成功 |
| U1 | 前端投影 | 左侧目录、中间 Artifact、右侧对话来自同一事实事件且刷新不抖动 |

## 6. 发布关口

本地代码只保留 SDK 执行路径，`CONTENT_AGENT_SDK_AGENT_ENABLED=false` 或
workspace 不在 canary allowlist 时直接拒绝新 Turn，不回退旧 Agent。发布必须依次满足：

1. `.\scripts\run-agent-platform-local-gate.ps1` 全部通过；
2. Live Gateway 验证 `/responses`、stream、function tool、Skills 和 `/responses/compact`；
3. 有 OCI runtime 的机器通过脚本沙箱攻击用例；
4. 预发布环境完成旧四流程、新 Fixture、取消/恢复和多租户回归；
5. 执行并保存数据库备份恢复演练证据后，才允许扩大 workspace canary。

当前用户明确禁止部署或操作测试机，因此第 2-4 项保持待授权，不以本地测试冒充。
