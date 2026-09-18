# Agent 核心能力覆盖审计

日期：2026-09-06。性质：官方目录、固定 SDK、当前生产源码及用户操作路径的覆盖核验，不是全局代码 review，也不是全量端到端验收。

> 状态更新（2026-09-14，G4.216）：下文保留 9 月 6 日审计时的事实，不应作为今天的实现状态。特别是 Sandbox、Memory、ToolSearch、CustomTool 已有生产接线，不能继续引用旧表称其全部未接。当前 SDK-14 差异见 [固定 SDK 能力状态复核](agent-platform-sdk14-current-status.md)；完整验收仍未通过，历史问题不因代码存在自动关闭。

## 1. 结论与纠正

**当前状态是 SDK 驱动了主要 Agent 执行循环，但平台没有完整开放 SDK 能力，也没有完成承诺的 Codex 基础任务闭环。问题不仅在前端。**

对话创建 Skill 的遗漏不是孤立事件。本次还确认了通用工作文件环境缺失、状态化 Worker 工具能力不同、延迟加载配置失配、对话控制任务的适配缺失、Hosted Tool 输入材料未打通、用户 MCP 凭据未进入执行链路，以及工具结果未完整呈现在前端等问题。下方逐项区分缺失、局部实现与未验收，不能把它们都叫作“补一个按钮”。

需要纠正此前技术判断：本项目安装的 `openai-agents==0.21.1` 已有 `agents.sandbox`，包括 `SandboxAgent`、`Filesystem`、`Shell`、`Skills`、工作区保存/恢复和 `Memory` 等实现。不能再笼统地说 SDK 只有模型循环，文件与 Skill 执行都必须由平台从头编写。官方将 Sandbox Agents 标为 beta，接入仍需选择执行环境并验证隔离、审批和持久化，并非换一个类就交付完成。[官方 Sandbox Agents](https://developers.openai.com/api/docs/guides/agents/sandboxes)

同样，不能得出“接 SDK 就自动得到整个 Codex 产品”。SDK 提供执行基础，平台必须连接用户资源、权限、可持久保存的状态与交付入口。**这些接入责任不减少原承诺，也不应演变成为每个 Skill 新写一套核心执行器。** 标准 Skill 的指令可以由 Agent 执行；它并不天然等于平台可暂停、审批和重试的 Business Run 图。

现有改造不是无效：主对话、后台任务和状态化文本任务都实际调用 SDK Runner；Function Tool、局部 MCP、结构化输出、审批恢复、Session、guardrails、观测和管理界面已有实现。缺口在于这些能力没有形成一致、完整、可由用户操作的系统。

## 2. 核验边界

- 代码快照：分支 `feature/agent-platform-convergence`，HEAD `6f964d108ce888ed413dc3e883eb71d0e08e9631` 上的当前工作区改动，不把 HEAD 当作完整源码版本。
- 本次重新读取安装元数据：`openai-agents=0.21.1`、`openai=3.3.1`。当前官方文档用于能力目录；具体接口是否存在以安装包源码核对，未升级 SDK。
- 用户案例：作品 `prj_ae22501b6fafc5118bccf4b76466ef63`，需求“帮我写一个爆款短篇小说的skill”。其文档产物、错误指引及压缩失败沿用本次讨论前已取得的只读记录，本轮未重放对话。
- 环境：此前记录的 8860/v37 与隔离 8880/v38 不混用。本轮未重启、重新部署或重新验证两套服务版本；源码结论不代表 8860 已加载这些代码。
- 本轮执行：源码/目录读取、官方文档读取、安装版本检查，以及不发模型请求的 SDK 配置校验。没有新增应用代码、修改作品、调用收费模型、启动容器、变更凭据或操作测试机。
- 状态含义：`缺失` = 未接通目标行为；`局部实现` = 某范围可达但不完整；`代码已接/待验收` = 有实现，不宣称真实通过；`外部阻塞` = 原目标仍未完成；`局部实测` = 只承认注明的样本和模式。

## 3. 已确认缺口

优先级是后续恢复开发时的处理顺序，不代表已授权切换架构或服务。已有 GAP-01 至 GAP-07 保留于基线待办。

| ID / 优先级 | 可观察的问题 | SDK、后端与前端证据 | 尚需完成 |
| --- | --- | --- | --- |
| GAP-01/02/03，P1 | “编写 Skill，保存并调用”只得到普通文档；普通文档也没有对应下载入口，回复却指向导出 | E01、E02、E07；已有用户案例 | 打通标准包草稿、校验、版本、确认安装、目录发现、真实调用和下载；回复只引用真实可用操作。修改已有 Skill 同样要走完整链路。 |
| GAP-04，P1 | 同一作品后续普通对话在原生压缩阶段失败，尚未发出本轮模型请求 | E10；既有案例错误 `NATIVE_COMPACTION_UNAVAILABLE` | 验证并解决网关与原生压缩兼容性；用原会话继续验收。不得以本地摘要、recent-N 或新建作品代替。 |
| GAP-08，P1 | Agent 没有通用的、可持久恢复的工作文件环境；不能通过统一文件工具编写一个多文件 Skill 包 | E01、E02、E11；现有 ApplyPatchTool 仅编辑虚拟 `artifact.json` | 先评估复用固定 SDK 的 Sandbox/Filesystem/Shell/Skills；连接受限项目输入、工作文件、快照及输出。保持平台安装版本与沙箱工作文件的区别，不授予任意宿主机写权限。 |
| GAP-09，P1 | 状态化 workflow 的文本 Worker 没有主对话/后台任务的 MCP、Skill 资源和脚本工具集 | E03；普通与兼容 Agent 都仅接 `context_tools`，其唯一工具是上游产物读取 | 统一可用工具与权限/恢复协议，或明确执行模式限制；不能以主对话能调用 MCP 证明 workflow 也能调用。需真实带依赖的 workflow 验收。 |
| GAP-10，P1 | Hosted Tool 配置 `defer_loading=true` 后当前包装违反 SDK ToolSearch 契约 | E04；本轮离线复现见第 6 节 | 接通真正按需发现/加载和对应权限，或在完成前准确拒绝不支持配置。仅拒绝配置不是完成能力。非延迟配置不能据此判断也会失败。 |
| GAP-11，P1 | 暂停、继续、取消、重试有 API/按钮，但主 Agent 对话工具与决策契约没有完整接通 | E01、E05；Go 接受 `control_run`，Python `ControlDecision` 没有该 intent，也无对应运行控制工具 | 用户“暂停这个任务”等请求能解析目标并走同一授权、幂等命令；按钮与对话不能各有一套行为。后台 Task 控制也要覆盖。 |
| GAP-12，P1 | Hosted 代码解释器/图片工具没有项目文件或图片的结构化输入桥接 | E06；包装参数只有 `instruction`，代码容器不带项目 file IDs，图片调用不带输入图 | 将用户选中资产安全地传给对应工具，绑定项目/版本/大小及生命周期；验收读取上传表格生成新文件、修改输入图片，不只验收纯文本指令生成。 |
| GAP-13，P1 | workspace MCP credential 有 CRUD，却没有进入 MCP 实际连接链路 | E08；执行端只解析受信配置的进程环境变量映射，前端没有凭据管理调用 | 打通工作区/用户凭据选择、受限解析、连接与撤销；验证同一 MCP 在不同工作区不会串凭据。不得把任意 `env://` 引用解析权交给用户。 |
| GAP-14，P2 | 工具输出/引用有后端数据，但没有统一的前端结果呈现与下载路径 | E06、E07；`AgentToolCall.result_summary` 已声明，执行记录只渲染状态与错误，未消费 outputs/citations | 展示经过授权的真实输出文件和引用，支持再次作为输入。已有资产 content API 和剧本导出不能算“所有文件交付已完成”；也不能说所有文件都没有后端下载能力。 |
| GAP-15，P2 | 运行中无法提交追加要求或排队下一条消息；现有输入控件忙时直接返回 | E07；当前存在停止操作，不存在同义的中途追加通道 | 明确追加到本轮、排到下轮、停止后重试的状态与副作用语义。该产品场景不能仅靠放开输入按钮，不能未经固定版本核验声称 SDK 已提供可直接调用的 steer API。 |
| GAP-16，P2 | 多 Agent 协作只接了所选 inline Skill 的单向 handoff，以及 Hosted 包装的 Agent.as_tool | E09；handoff 子 Agent 清空自身 handoffs | 原生 handoff 真调用、转交后的工具权限/审批恢复尚需验收；通用任务拆分、子任务并行、汇总与取消没有形成用户闭环。不得把已有 handoff 宣称为完整子 Agent 管理。 |
| GAP-17，P2 | Session 历史与显式 Goal 已有，但跨会话用户规则/偏好和项目指令的生效、修改与撤销没有明确闭环 | E01、E10、E11；主上下文与工具中未发现对应规则管理，Sandbox Memory 未接入 | 将会话历史、项目指令、长期偏好分别定义权限和生命周期，并做生效/遗忘验证。Memory 不能作为原生 compaction 的替代方案。 |

GAP-05/06/07 仍成立：运行版本不一致、原覆盖基线不足、全量真实验收未完成。以上不是把每个 SDK 参数都变成功能需求，而是记录会让用户任务中断或能力声明不实的具体差异。

## 4. SDK 核心目录映射

官方主线目录覆盖 Agent 配置、Runner、工具、协作、状态、人工介入和观测；固定版本的工具类型、sandbox、voice/realtime 等再单列，避免只从已实现部分反推范围。[Agent 配置](https://developers.openai.com/api/docs/guides/agents/define-agents)、[运行与会话状态](https://developers.openai.com/api/docs/guides/agents/running-agents)、[MCP 与观测](https://developers.openai.com/api/docs/guides/agents/integrations-observability)

| 基线 ID | 固定 SDK 与当前使用情况 | 可操作/自动路径 | 结论与验收边界 |
| --- | --- | --- | --- |
| SDK-01 Agent/Runner | `Agent`、`Runner.run_streamed`、`OpenAIResponsesModel` 被实际使用；主对话还有 SDK 路由和终态修正调用 | 发送消息、启动 Task/Run | 执行循环已接；不是“仍全部用 Go 自己调模型”。平台提交契约仍限制能采取的行动；完整任务能力未达标。E01/E03 |
| SDK-02 Function Tool | `function_tool`、参数契约、工具包装、超时、结果上限、审计与审批已接 | 模型自动调用项目/资源/脚本工具 | 局部实现/局部实测；错误恢复、原生多模态结果在各执行模式仍需验证；GAP-09/10/11。E04 |
| SDK-03 MCP | SDK stdio、SSE、Streamable HTTP 连接实现存在；非延迟工具可直接装载，延迟服务器依所选 Skill 依赖装载 | Skill 依赖、工具管理启用、运行时自动调用 | stdio 部分路径已实测；不能据此宣布所有传输/认证/动态发现/Worker 均完成；GAP-09/13。E04/E08 |
| SDK-04 Hosted/工具目录 | WebSearch、FileSearch、CodeInterpreter、ImageGeneration 经 `Agent.as_tool` 包装；其他类型见下表 | 受信工具目录启用后由模型调用，不要求每类有按钮 | 代码已接/待真实工具验收，文件输入、输出交付与延迟加载有已知缺口；GAP-10/12/14。E06 |
| SDK-05 多 Agent | 原生 handoff、克隆 Agent、Agent.as_tool 已使用 | 自动路由至 inline Skill；Hosted 专项调用 | 有实现不等于真实发生 handoff；通用子任务协作局部缺失；GAP-16。E09 |
| SDK-06 Session/compaction | Session 持久化、历史查询、SDK 原生 Responses compaction 及失败保护已接 | 同会话继续、刷新/重启恢复 | 原生压缩外部阻塞，连续对话未过；不能把“保存了历史”当作“会话已可用”。E10 |
| SDK-07 Streaming | SDK 事件归一化，前端订阅 Agent 事件并展示进度 | 对话和执行记录自动更新 | 主对话局部实现/实测；workflow Worker 消耗流事件但不逐项向用户传递；断线恢复和唯一终态仍需跨模式验收。E01/E03/E07 |
| SDK-08 人工介入/RunState | 原生 interruptions、RunState 序列化/恢复、approve/reject 与平台审计连接 | 工具审批卡、停止、Task 控制 | 主对话 stdio 写入的批准跨重启、拒绝、取消有局部证据；后台/子 Agent/多审批/权限撤销不能借用这些结果。E01/E04 |
| SDK-09 输出契约 | Pydantic/output_type；主 Agent 用 commit 工具形成平台终态；兼容输出路径存在 | 聊天、文档、Run/Task 产物 | 已接且有局部真实产物；格式正确不证明文件、Skill 或动作真的被创建。E01/E02/E03 |
| SDK-10 Guardrails | 输入、输出、Function Tool 保护已接，stateful Worker 也调用 protect | 自动触发，错误应在执行记录可见 | 不能误报为“没接 guardrails”；真实阻断、交接覆盖与无副作用验收仍未完成。保护不是对任意提示注入的完整保证。E12 |
| SDK-11 Context/Hooks/ModelSettings | AgentContext 与恢复验证、ModelSettings、RunConfig 已用；未发现 RunHooks/AgentHooks 的生产接线 | 上下文自动传入；模型/预算主要为服务端配置 | 不用某个 hook 类本身不是功能缺失；用户配置可见性和可调范围需对应目标。当前主/后台/workflow 模型轮次上限为 8/16/4，不可等同无限自主执行。E01/E03/E12 |
| SDK-12 Tracing/usage/evals | SDK RunConfig tracing + 平台 TurnObservation，用量与错误阶段可见，敏感内容记录关闭 | 执行历史展示模型、接口、Token 和失败阶段 | 局部实现；没有证据证明跨 handoff/审批/Hosted/恢复全关联，质量评估也不能由单元测试数量代替。E07/E12 |
| SDK-13 多模态/文件 | 主对话可构造原生图片输入、视频代表帧；Skill 资源支持有界读取；Hosted 结果可持久化资产 | 上传素材、读取资源、编辑产物 | 不是“完全不能看图/读文件”；输入格式、音频、Hosted 材料桥接、可下载/再利用分别验收，GAP-12/14。E01/E06/E07 |
| SDK-14 其余固定版本目录 | 已核对下列公开能力族及生产代码接入情况 | 见下表 | 覆盖审计已从空白目录推进到具体映射；未完成运行证明的项继续保持未通过，不声称 SDK 所有方法/选项都已逐一审计。 |

### 固定版本的其余能力与替代路径

此表用于杜绝静默遗漏。`未接` 不意味着允许自动移出原目标；也不意味着必须同时使用每种互相替代的 transport、storage adapter 或工具格式。

| SDK 能力族 | 固定版本证据 | 当前接入/责任 |
| --- | --- | --- |
| SandboxAgent/工作文件/快照/挂载 | `agents/sandbox/__init__.py`、`sandbox_agent.py`、`session/base_sandbox_session.py` | 未接。通用文件与 Skill 工作区是必须补齐的目标；执行环境选型需先验证 beta API、隔离、持久恢复。E11 |
| Sandbox Skills 与渐进发现 | `capabilities/skills.py` 的 `Skills`、`LocalDirLazySkillSource`、`load_skill` | SDK 原生路径未接；平台已有自己的标准包解析、发现、按需资源读取，因此不能说“没有 Skill 支持”。应核对复用/映射，保留平台安装权限和固定版本语义。E02/E11 |
| Sandbox Memory | `capabilities/memory.py:18` | 未接。与现有 Session/Goal 不是一回事；规则和长期偏好待完成，不替代原生压缩。 |
| ShellTool/LocalShellTool | `agents/tool.py:1363`、`:1167`；sandbox Shell capability | 未接通用 Shell；现有 `execute_skill_script` 仅为所选 Skill 中受管脚本。受控执行目标仍待真实 OCI 验收，不开放宿主机任意命令。 |
| ApplyPatchTool | `agents/tool.py:1418`；项目 `revision.py:438` | 已在产物修改器使用，但只针对 `artifact.json`；不等于通用文件编辑完成。E11 |
| ToolSearchTool | `agents/tool.py:1511` | 未接且存在条件性配置错误，GAP-10；当前 MCP 按依赖过滤不能代表原生通用工具发现。 |
| ProgrammaticToolCallingTool | `agents/tool.py:1528` | 未接。生成代码编排工具与 SDK 普通 function calls 不同；需要明确可用模型/网关、caller 权限、审批与恢复，不能标成现有能力。 |
| CustomTool / grammar | `agents/tool.py:1456` | 未接 raw-string 自定义工具；已有 JSON Function Tool 是另一条路径。是否需同时支持 grammar 要在能力使用场景中明确，不以未 import 自动判定已有工具调用无效。 |
| ComputerTool | `agents/tool.py:842` | 未接，没有浏览器/桌面执行端与用户操作入口。列为能力范围待明确项，不能把当前开发助手能操作浏览器算作平台能力，也不擅自加进本次实现。 |
| HostedMCPTool | `agents/tool.py:1087` | 未接服务端托管 MCP 路径；已有 SDK-managed MCP。两种接入方式不应重复算成两个“完全缺失的 MCP”。 |
| Realtime/Voice | `agents/realtime/agent.py:28`、`agents/voice/pipeline.py:21` | 模块存在，产品未接。音频输入、语音流水线与实时语音需分别界定验收；不是上传视频代表帧就完成。未授权擅自扩大为复制整个 Codex App。 |
| Responses WebSocket | `agents/models/openai_responses.py:1075` | 固定 SDK 有 WSModel，产品未用；当前 HTTP 流式是另一种 transport。没有 WS 不等于没有流式能力，运行中追加请求仍是独立的产品缺口。 |
| Session 存储/模型 provider 适配器/生命周期 hooks | `agents/memory`、`agents/extensions`、`agents/lifecycle.py` | 当前自有持久 Session 和 Responses provider 已接；不需为宣称“用全 SDK”同时上所有存储后端/供应商。必须验证当前选择能完成状态、隔离和观测任务。 |

## 5. 用户任务与执行模式

| 用户任务 | 前端或自动触发路径 | 后端可达状态 | 验收缺口 |
| --- | --- | --- | --- |
| UX-01 创建 Skill 并直接用 | 目前只有对话文档生成、独立 Skill 上传页 | 标准包创建/安装的对话链路缺失 | 原用户任务必须原样通过，不能由开发者手工注册代替。 |
| UX-02 修改 Skill 再用/撤回 | 管理页已有上传升级、目录升级、版本及启停操作 | 对话改包与安装修订缺失 | 验证新旧版本、重名、撤回、禁用及正在执行的版本固定。 |
| UX-03 上传普通本地 Skill | `/skills`，Composer 选择或自动匹配 | 标准解析/注册/资源工具已接 | 新 Skill 零核心代码修改；带资源/脚本/依赖的包分别验收。只有 SKILL.md 不自动生成持久 workflow 图。 |
| UX-04 读写编辑文件并交付 | 素材上传、产物编辑器；剧本导出已有 | 通用文件、Skill 包与 Hosted 交付不完整 | 文件确实存在、内容一致、下载及再次上传；主机路径写入另需授权。 |
| UX-05 实际执行脚本和工具 | 脚本沙箱状态/开关、审批卡 | 受管脚本与工具适配已有 | 真实 OCI 不可用的既有阻塞仍保留；不能用模拟执行器宣布通过。 |
| UX-06 连续对话/记忆 | 同作品继续、历史记录 | Session 已接，压缩阻塞 | 以原失败作品在压缩后继续，不丢历史、不要求换作品。 |
| UX-07 澄清、计划、执行 | clarify 回复、配置/确认卡、Goal | 终态澄清与显式 Goal 已有；通用计划变为可执行多任务未形成完整闭环 | 普通推理步骤与持久 workflow 严格区分；暂停等待答案后继续不得重复写入。 |
| UX-08 中断、追加、取消、重试 | 停止、Run/Task 控制按钮 | 按钮命令局部可用；对话控制和运行中追加缺失 | 对话/按钮相同命令语义；异步副作用、取消竞态、重试范围需真实验收。 |
| UX-09 后台任务与持久流程 | 配置卡、Task 卡、Run 工作区 | SDK Task/Worker 已接，但工具能力不一致 | 三种模式分别验证工具、产物、等待审批、失败恢复及原业务流程。 |
| UX-10 规则、配置与过程可见 | Skill 管理、工具目录、执行记录 | Scope/版本/策略部分可见；模型设置主要服务端，MCP 凭据未消费、输出未完整展示 | 用户可辨认当前模型、Skill 版本、权限、工具输出、有效规则；自动能力不强加按钮，但必须可检查。 |

三种执行模式不能互相借用通过结果：

| 行为 | 主对话/inline | background_task | stateful_workflow 文本 Worker |
| --- | --- | --- | --- |
| SDK Runner | 已接 | 已接 | 已接 |
| 统一 AgentToolProvider | 已接 | 已接 | 未接，仅上游正文读取 |
| 所选 Skill 资源/受管脚本/MCP | 代码已接、部分 MCP 实测 | 代码已接、全面真工具待验收 | 没有同等工具执行接口 |
| SDK RunState 工具审批恢复 | 已接、局部实测 | 已接、全面待验收 | 没有同等工具审批恢复；已有业务产物审批不是同一个功能 |
| 输入/输出 guardrails | 已接 | 已接 | 已接 |
| 工具流式可见与用量 | 已有主对话事件/观测 | 有任务/尝试状态，细粒度待核验 | Worker 消耗 SDK stream，未统一投影所有工具事件 |

## 6. 本轮离线验证与既有实测

### 延迟加载条件性失败

本轮用实际 `AgentToolProvider._hosted_tools` 创建一个仅用于内存校验的 WebSearch 包装，分别设置 Hosted descriptor 的 `defer_loading` 为 false/true，并调用安装 SDK 的 `validate_responses_tool_search_configuration`。未调用 Runner、模型、WebSearch 或 backend 写入。

```json
{
  "False": {"validation": "passed", "wrapped_defer_loading": false},
  "True": {
    "validation": "failed",
    "type": "UserError",
    "message": "Deferred-loading Responses tools require ToolSearchTool() when using OpenAI Responses models."
  },
  "sdk_search_control": "passed"
}
```

对照组只在内存列表追加 `ToolSearchTool()` 即通过配置校验；这证明缺少 SDK 要求的配套配置，不证明接上以后网关或产品任务已通过。本次没有应用该修复。此前对 runtime Function Tool 包装的同类校验得到相同错误；Hosted 样本进一步对应到 Go 公开配置字段。

### 已有证据不丢失，但不扩大结论

- 主对话 stdio MCP：项目 `prj_439bcec93b36acc922829d0ec3e948a0` 有只读调用及批准跨重启后的单次写入记录；写调用 `tcall_b666105102985456668235ef410ec765`。拒绝样本 `prj_3ab4055ec9fd1883e7646c5dde6e034a` 未发生对应写入。
- inline Skill：项目 `prj_6dff8bc48535cd9cd398160bef03fe23` 有读取与写审批取消记录，不能冒充已经实际发生原生 handoff。
- background_task：项目 `prj_a5911a8078d570ed4e1788948c75bf49` 的任务 `agt_e48a78c4cbd3f098aba910cdcc402e47` 有完成样本；仅证明该样本，不代替后台真实脚本/MCP/审批恢复的全套验收。
- stateful_workflow：项目 `prj_64a985564b5ba2797f3abe2c35ceda0a` 曾确认启动；本轮没有读取最新终态，不列为完成。
- 以上属于先前隔离验收记录的索引，本轮未重新联网执行，也未用其推断用户 8860 的当前版本行为。

## 7. 恢复开发后的补齐顺序

1. 固定本报告与原承诺作为验收输入。把每项绑定最小正向任务、拒绝/失败/恢复任务、适用执行模式和环境；不从实现列表反推完成率。
2. 优先验证 SDK 原生工作区能力如何接入现有 Host。结果需包括 provider 可用性、项目/用户隔离、审批、取消、快照与产物导入，未验证前不整体替换现有 Runtime。
3. 完成统一文件与 Skill 编写/修改/安装/调用闭环，同时修复文档下载和虚假成功/操作指引。安装确认、签名/哈希、不可变版本和管理权限仍归平台。
4. 补齐执行能力一致性：stateful Worker 工具接线、对话控制 Task/Run、延迟发现、用户 MCP 凭据、Hosted 文件输入与输出交付；每项附有拒绝和恢复证明。
5. 解决原生压缩外部阻塞，补齐追加请求、子 Agent 协作、规则/配置可见性。外部阻塞可与其他开发并行，但保持验收未通过，未经授权不改网关和运行环境。
6. 跑基础用户任务、三执行模式、原有业务工作流的隔离真实验收；用户正在使用的环境升级另行获得授权。语音、Computer、PTC 等已列明未接，必须明确适用性与交付安排，不默默删掉或默默承诺全做。
7. 达标并验收后，再开展独立全局代码 review：审查架构重复、安全、并发、恢复、资源回收、兼容回退及前后端契约。当前能力审计不能当作这一步已经完成。

## 8. 源码证据索引

这些引用是本轮工作区的可定位证据，不是永久版本链接；后续改动会移动行号。

- E01：主 Agent 工具与循环：[runtime.py:1553](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/runtime.py:1553)；决策边界：[contracts.py:46](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/contracts.py:46)。同文件的 `commit_agent_action` 只投影受支持的决策，不提供通用文件/安装/任务控制调用。
- E02：Skill 包与执行模式：[skill.go:323](/C:/Users/egois/Desktop/内容生产Agent项目/backend/internal/capability/skill.go:323)；MCP 依赖解析：[skill.go:610](/C:/Users/egois/Desktop/内容生产Agent项目/backend/internal/capability/skill.go:610)；管理入口：[SkillManagement.tsx:79](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/components/skills/SkillManagement.tsx:79)。已有上传/目录安装与升级，不等于模型能创建并安装包。
- E03：workflow 工具：[task_worker.py:363](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/task_worker.py:363)、同文件 `:595`、`:635`；后台路径：[background_worker.py:235](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/background_worker.py:235)。前者正常和兼容 Agent 都只有 context tools；后者使用准备后的统一工具集。
- E04：工具装载与包装：[agent_tools.py:375](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/agent_tools.py:375)、同文件 `:521`、`:547`、`:655`；Go Hosted 延迟配置：[registry.go:82](/C:/Users/egois/Desktop/内容生产Agent项目/backend/internal/agenttool/registry.go:82)。Hosted 工具同样进入 `_wrap_runtime_tools`；生产工具集合未添加 ToolSearchTool。
- E05：Go 支持的决策：[message_exchange.go:18](/C:/Users/egois/Desktop/内容生产Agent项目/backend/internal/runtime/message_exchange.go:18)；API 控制入口：[runtime_handlers.go:1708](/C:/Users/egois/Desktop/内容生产Agent项目/backend/internal/httpapi/runtime_handlers.go:1708)；前端：[api.ts:151](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/api.ts:151)。与 E01 对照可见主 Agent 的控制接线缺口。
- E06：Hosted 请求、native tool 和产物持久化：[hosted_tools.py:16](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/hosted_tools.py:16)、同文件 `:38`、`:75`、`:112`、`:119`。输出可存 asset 并返回 content URL，但入参仍只有 instruction。
- E07：工具结果 UI：[AgentExecutionHistory.tsx:15](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/components/agent/AgentExecutionHistory.tsx:15)；普通文档编辑：[DocumentArtifactEditor.tsx:17](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/artifacts/DocumentArtifactEditor.tsx:17)；剧本导出：[App.tsx:1062](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/App.tsx:1062)；忙时输入：[App.tsx:1885](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/App.tsx:1885)；`types.ts:171` 声明 result_summary，UI 未消费其文件/引用。
- E08：凭据元数据：[identity.go:369](/C:/Users/egois/Desktop/内容生产Agent项目/backend/internal/runtime/identity.go:369)；连接环境解析：[agent_tools.py:627](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/agent_tools.py:627)、同文件 `:881`；管理 UI：[AgentToolInventory.tsx:19](/C:/Users/egois/Desktop/内容生产Agent项目/frontend/src/components/skills/AgentToolInventory.tsx:19)。生产引用搜索仅发现凭据 CRUD/权限/删除路径，未发现执行消费。
- E09：原生 inline Skill handoff：[skill_agents.py:9](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/skill_agents.py:9)，筛选范围和子 Agent 的 `handoffs=[]` 明确限制了现有协作形态。
- E10：Session 与原生压缩：[session.py](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/session.py)；用户失败案例与历史局部验收：[agent-platform-post-convergence-review.md](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-post-convergence-review.md)。会话持久化与跨会话规则记忆须分开。
- E11：固定 SDK 工作区：[sandbox/__init__.py:19](/C:/Users/egois/Desktop/内容生产Agent项目/.tools/openai-agents-sidecar-venv/Lib/site-packages/agents/sandbox/__init__.py:19)；Skill 原生实现：[skills.py:522](/C:/Users/egois/Desktop/内容生产Agent项目/.tools/openai-agents-sidecar-venv/Lib/site-packages/agents/sandbox/capabilities/skills.py:522)；平台仅产物补丁：[revision.py:410](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/revision.py:410)。对 Sidecar 生产目录的 SandboxAgent/agents.sandbox/ShellTool 搜索未发现接线。
- E12：防护：[guardrails.py](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/guardrails.py)；观测：[observability.py:40](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/observability.py:40)；模型/预算配置：[config.py:32](/C:/Users/egois/Desktop/内容生产Agent项目/experiments/openai-agents-sidecar/src/content_agent_sidecar/config.py:32)。未使用 hooks 类不代表这些机制都未实现。

## 9. 官方参照与完成声明门禁

- [Agents SDK 总览](https://developers.openai.com/api/docs/guides/agents)：执行循环与工具/状态/控制的基线。
- [Sandbox Agents](https://developers.openai.com/api/docs/guides/agents/sandboxes)：原生执行工作区与 beta 边界；具体本地支持已读取固定版本源码，不只依据最新网页。
- [Skill 编写与使用](https://learn.chatgpt.com/docs/build-skills)：标准包和创建后可调用的用户任务参照。Skill 作者工具/安装 UI 不是 SDK 自动替平台完成的部分，但属于原交付目标。
- [原始收敛计划](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-full-convergence-plan.md)与[能力基线待办](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-capability-rebaseline.md)继续生效。

**本报告完成的是当前已核对能力族的代码/API/UI 映射和缺口登记。未完成真实环境全矩阵、网关兼容修复、SDK beta 集成验证或全局代码 review，因此不得用本报告宣布“全部核心功能已实现/可用”。** 后续出现官方或固定版本差异时应追加目录和证据，不能要求用户逐项发现遗漏。

## 10. Goal 开发增量

2026-09-06 G1/G2.1/G2.2 之后的代码状态由 `agent-platform-goal-progress.md` 逐项记录，以上初审定位保留其历史语义，不冒充最新运行环境。当前新增 ToolSearch 条件装载、版本化文本工作文件、Skill 草稿校验/ZIP/安装/升级及 SDK 审批和前端确认入口，schema 40；无新工具依赖的 inline Skill 已有当前轮加载的隔离证明。

原生 SandboxAgent 完整工作区、二进制/Shell、普通 Artifact 下载、跨模式工具一致性、真实模型作者任务及其余 GAP 仍需继续。局部新增测试、HTTP fixture 和 UI 截图不替代三执行模式的真实端到端验收，更不等于独立全局 review 完成。未重启/升级用户 8860，也未改网关或模型。

G2.3 增量：普通 Artifact 精确版本下载已有后端/API、主对话/后台 SDK 查询回执及前端入口，覆盖 JSON、文档 TXT/MD/纯文本 DOCX、合法表格 CSV；修复新建普通产物 JSON 大整数精度丢失。隔离 Go/Python/前端回归和桌面/手机下载检查通过，详见 goal-progress。上述“普通 Artifact 下载仍需继续”为 G2.2 结束时记录；本次新增不等于真实网关下载任务、Hosted 文件/引用复用或完整沙箱已经验收，不更改其他缺口状态。

G2.4 增量：Hosted 产出按权威资产调用绑定显示、流式下载和加入材料，URL/文件引用有界持久化；作品材料选择器传递精确快照，CSV/JSON 原文件复用及 DOCX/PDF kind/MIME 契约已修复。Go 全包、Python 102 项、前端 215 项及桌面/手机隔离 fixture 通过。GAP-14 未关闭：MCP 产出、提供方引用映射、任意二进制与真实三模式未验收；前端材料发送不等于 GAP-12 Hosted 输入已经接通。当前仍阶段 1，非全局 review。

G2.5 增量：GAP-12 的代码/图片工具已接原生结构化文件/图片输入，精确项目快照及 SHA-256 复查，沿用 SDK 审批和恢复。相关 Python 151 项、Go HTTP 全包与工具回归通过，SDK 原生 wire JSON 字节已由模拟传输验证。尚非真实网关/三模式 E2E；状态化 Worker 统一工具、任意二进制入口等仍未完成。上述“GAP-12 未接通”是 G2.4 结束时状态，不能用于覆盖本批进展，也不能以本批局部通过宣布完整项验收。

G4.4 增量：G3/G4 的中间批次及证据以 goal-progress 为准，不以本报告初审表格覆盖后续已接线代码。本批源码 schema 44 已接后台当前执行文本追加、原生 after_turn/RunState/add_input、权限和审批边界及前端收件回执；Go 全包最终重跑、Sidecar 502 项、前端 252 项、Go/SDK 跨进程四分支与桌面/手机隔离 fixture 通过。主对话/状态化追加和全量真实三模式验收仍未完成，未进入最终全局 review，未升级用户环境。

G4.5 增量：补主对话 Sidecar 的原生暂停/修复阶段恢复和内部控制，验证自定义终止工具的审批处理后原生追加路径，修复零模型暂停的初始 guardrail/Session 与候选回复恢复问题。证据是实际固定 SDK 加确定性模型/Backend 和 ASGI fixture，详见 goal-progress；主对话 Go 状态/协议、用户入口和持久追加尚未接通，不能以本批宣称完整可用。schema 44 未变，用户环境未更新，最终全局 review 仍未开始。

G4.6 增量：源码 schema 45 已接主对话暂停的 Go 状态/用户命令、原轮恢复调度、shell 协议与前端图标/事件，补较早暂停记录、精确私有 checkpoint、审批和删除边界。前端 268 项、桌面/手机模拟 API、Go 全包最终重跑及后台/状态化既有联合回归通过；新增主对话 Go+实际 SDK 写文件/暂停/重建/继续确定性联跑也通过，完整失败与中断记录见 goal-progress。主对话/状态化当前执行追加、主对话完整审批/失败联合矩阵和三模式真实网关验收仍未完成，不能将局部通过合并为整体达标；用户环境未更新，最终全局 review 未开始。

G4.7a 增量：源码 schema 46 增加主对话 Go 不可变输入、领取代次和回执前缀，校验原作者/权限、自动与手动暂停、旧审批、完成竞争、并发和重建。相关 Runtime 扩展回归、HTTP/shell 全包、全包编译及既有 Go+SDK 暂停联跑通过，具体证据见 goal-progress。此批是内部状态基础，追加 API/UI 未开放，主对话 Sidecar 输入透传、原生 add_input 与模型收件尚未接通；不计作主对话追加用户任务完成，GAP-15 及其他缺口保留，用户环境与最终全局 review 状态未变。

G4.7b 增量：已接主对话追加用户 API/UI、输入快照透传、原生 add_input/Session/暂停恢复、旧审批处理与 received/included 收件。Go+SDK 确定性联跑通过手动重建、自动继续和迟到输入，前端 278 项、类型/构建和桌面/手机模拟 API 通过，HTTP/shell 修正新测试契约预期后全包通过，SDK 回归及失败证据见 goal-progress。仍处阶段 1；状态化追加、主对话完整审批/失败联合矩阵、完整真实三模式验收和其他原承诺未完成。GAP-15 不勾选，用户环境未更新，最终独立全局 review 未开始。

G4.8 增量：状态化 SDK 生成与源分析批次已接原生 after_turn 暂停、无审批 checkpoint、原 attempt/冻结 ContextPack 恢复和 Run 用户控制，补多个任务边界、旧审批、取消/删除与输入 guardrail 失败分类。Go+SDK 写文件/暂停/重建/批准或拒绝的确定性联跑、分层回归及失败记录见 goal-progress。状态化追加账本/API/UI 仍未接通，输出修复/视频执行的阶段边界、失败后安全继续及真实三模式验收仍在范围内；不能把本批暂停前置能力当作 GAP-11/15 完成，仍处阶段 1，用户环境未更新，最终独立全局 review 未开始。

G4.9 增量（2026-09-07）：本地输出解析/合同修复已接原生流式阶段恢复，保存候选/冻结批次/用量，修复输出 guardrail 和长度拒收错误分类。固定 SDK 全量 608 项、真实 Go+SDK 关闭重建/继续/输入或输出拒绝及非重试持久化专项通过，完整范围与失败日志见 goal-progress。**新确认 P1：真正 Go 业务拒收位于 CommitExecutionResult，Sidecar 既有捕获却位于 SubmitExecutionResult；实际正式提交失败仍不会进入修复，且 result_received 禁止不同结果覆盖。** repair_backend 的 Sidecar 分支测试不能当作该通道完成，下一步补权威拒收记录、受控结果修订/领取和原 attempt 续跑，再推进状态化追加。GAP-11/15 保持未完成，仍处阶段 1；未改用户环境，最终独立全局 review 未开始。

G4.10 增量（2026-09-07）：上述实际正式提交拒收缺口已补。schema 47 不可变拒收账本、有界原 attempt 修复与原生暂停/重建、累计用量和输入哈希、正式写事务授权、迁移/删除/损坏队列/批次失败策略，以及公开修复状态和前端入口均有分层证据。实际 Go+Python SDK 确定性联跑验证 Schema 有效但业务覆盖无效的正式拒收、成功修复、暂停重建及第二次拒收终止；Sidecar 618 项、前端 288 项和桌面/手机 mock API 交互通过，完整报告及失败记录见 goal-progress。较早 Go 全包与最终专项分开记载，不宣称同版全部或外部模型三模式同场验收。仍处阶段 1，紧接状态化追加输入，其余原承诺和 GAP-11/15 未关闭，用户环境与最终独立全局 review 状态未变。

G4.11a 增量（2026-09-07）：状态化追加已接固定 SDK 原生输入/阶段 checkpoint，内容和身份冻结校验、连续暂停、审批、修复及剩余批次处理通过实际 SDK 加确定性 Backend 替身测试；Sidecar 最终 644 项通过。Go+SDK 152.418s 联跑验证无追加的既有状态化功能兼容，不冒充新追加端到端验收。当前模型消费仅记录于 Worker/私有 checkpoint，Go 追加账本、持久收件与 API/UI 尚未接通，用户入口未开放，GAP-15 及整体目标继续未完成。详细证据和下一动作见 goal-progress；未改用户环境，仍处阶段 1，最终全局 review 未开始。

G4.11b/G4.12 增量（2026-09-07）：状态化追加 Go 账本/模型收件/API/UI 已补，schema 48；真实 Go+SDK 确定性三分支、前端 298 项及 desktop/mobile mock API 交互通过。继续复核并修复首次领取饥饿、事务内追加权限、后台幂等回执和未预路由 inline Skill 发现限制。已开展跨模块源码 review，记录于 `agent-platform-global-review-20260907.md`，但原能力清单仍有未完成代码和外部验收条件，不能称为达标后的最终独立 review。最终回归遇到 Windows Application Control 与 MCP 超时，具体终态以新报告为准；未改用户环境，不勾选整体完成。

G4.13/G4.14 增量（2026-09-07）：MCP 文件输出已接持久化/下载/材料复用；用户与工作区 MCP 凭据已接加密存储、精确执行身份解析、原生 SDK 连接消费、版本撤销和前端管理，schema 49。Go+SDK 三执行身份确定性联跑、独立 HTTP/SSE/stdio 认证及分层 UI 有通过证据，详见 goal-progress。旧 workspace 引用仅保留元数据，不能解析任意 env://。这更新 GAP-13/14 的“完全未接”描述，不等于真实外部模型/账户全流程已达标；完整原生工作区、协作、规则、其余输入与失败恢复、最终全量回归和全局 review 均保留。用户再次明确必须持续到完整目标，不能以本次局部完成结束。
