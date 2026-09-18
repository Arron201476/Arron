# Agent 平台完整收敛实施计划

状态（2026-09-09接续）：用户于 2026-09-06 已恢复开发并启动 Goal，完整顺序仍为补齐全部功能、自测回归验收、最终独立全局 review 并修复。实现期间已持续跨模块审查和修复，但全量能力验收及最终独立review未通过，不能再把当前状态写成“尚未开始任何review”。批次与接续状态见 [Goal 进度](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-goal-progress.md)。下方v37/v38及执行记录均为原核验时的历史，不代表当前运行环境；未经授权不访问或切换用户环境。
日期：2026-09-06
范围：将当前内容生产平台收敛为 OpenAI Agents SDK 驱动、Skill 可插拔、MCP 可扩展、Go Runtime 保持业务权威的生产型 Agent 平台。

2026-09-05 验收口径校正：下方 W0-W9 执行记录保留为历史实现与测试记录，不代表本轮已经确认所有承诺能力可用。以本计划原有承诺和用户要求的 SDK 核心能力为交付基线；补齐缺失能力属于原任务，不得归为可选扩展，也不得通过缩小适配范围重新宣称完成。代码接入、实际执行、前端操作和端到端验证分别核对，现状见 `agent-platform-post-convergence-review.md`。未经用户授权不部署或操作测试机。

2026-09-06 完整性校正：用户作品暴露对话创建并安装 Skill、普通文档交付及错误操作指引的缺口；原生压缩依然阻塞多轮对话。此前遗漏不仅是验收问题，也涉及规划和实现。新增 `agent-platform-capability-rebaseline.md` 统一登记问题，分别核对官方 SDK 核心能力与用户期待的 Codex 基础任务；技术归属差异不能用来排除原目标。完成覆盖核验前不得宣称全部核心能力已齐。

2026-09-06 能力覆盖核验结果：[agent-platform-capability-audit-20260906.md](/C:/Users/egois/Desktop/内容生产Agent项目/docs/agent-platform-capability-audit-20260906.md) 已补充固定 SDK、生产代码与前端路径的逐项映射及 GAP-08 至 GAP-17。特别纠正：固定 SDK 已有 SandboxAgent/文件/Shell/Skills 能力，平台尚未接入；stateful Worker 的工具能力也未与其他模式统一。恢复开发以该报告的补齐顺序和本计划原承诺共同作为输入，不把报告完成等同功能完成；本次未恢复应用代码开发或重启服务。

## 1. 完成态定义

目标不是复刻 Codex App，也不是删除现有业务 Runtime。完成态为：

```text
React Workspace
    -> Go API / Runtime
        -> Agent Host interface
            -> OpenAI Agents SDK + Responses API
                -> Skills / Function Tools / MCP / Hosted Tools

Go Runtime owns:
Project / Conversation / Skill Installation / Skill Invocation / Task / Run /
Artifact / Version / Approval / Event / Identity / Policy / Audit

Agents SDK owns:
Agent loop / model calls / tool selection / handoffs / Session /
SDK-native compaction / streamed model and tool events
```

完成后必须同时满足：

1. 新增标准 `SKILL.md` 包不需要修改 Sidecar、Go 核心或 `App.tsx`。
2. Skill 可以显式选择，也可以由 Agent 根据 `name + description` 按需匹配。
3. 只有 `stateful_workflow` 创建 Business Run；`inline` 和 `background_task` 不伪装成 Run。
4. MCP、Function Tool 和 Hosted Tool 经过同一工具策略、审批与审计层。
5. 前端消费服务端 Workspace Projection，不再复制 Capability 流程真源。
6. Agent turn 使用真实流式事件，支持取消、重连和唯一终态。
7. SDK Session 与 SDK 原生 Responses compaction 保留，不使用本地摘要或 recent-N 替代。
8. 用户、工作区、项目、Skill、密钥和产物均有明确隔离边界。
9. 用户可通过对话创建和修改标准 Skill 包，经校验与适当确认后保存、安装并真实调用；新增 Skill 不要求修改核心代码。
10. 通用产物与 Skill 包具有真实可用的文件交付路径；平台持久保存、浏览器下载和经授权的主机目录写入必须明确区分，不能回复不存在的操作入口。
11. SDK 能力目录和基础用户任务均有逐项映射与实际证据；未核验、未实现、未通过和外部阻塞保持可见，不能使用历史 completed 或测试数量代替完整性结论。

## 2. 统一 Skill Package v1

兼容 OpenAI Skill 的基础目录：

```text
skill-name/
|- SKILL.md                         # 必需：name、description、指令
|- agents/openai.yaml               # 可选：UI、调用策略、MCP 依赖
|- content-agent/manifest.json      # 可选：平台执行和展示扩展
|- content-agent/workflow.json      # 可选：状态化工作流定义
|- references/                      # 可选：按需读取资料
|- assets/                          # 可选：模板和静态资源
|- schemas/                         # 可选：输入、输出、配置 Schema
`- scripts/                         # 可选：默认隔离且禁用
```

规则：

- 只有 `SKILL.md`：默认是 `inline` 指令型 Skill。
- 声明 `background_task`：走通用 Task 协议。
- 声明 `stateful_workflow`：必须提供可验证 Workflow，走现有 Run/Artifact/Approval Runtime。
- `agents/openai.yaml` 负责展示、隐式调用策略和工具依赖，不承载业务状态机。
- `content-agent/*` 是平台扩展，不破坏标准 Skill 在其他 OpenAI Host 中的可读性。
- 安装版本不可变；Invocation、Task 和 Run 固定到具体版本。

## 3. 工作包与完成门禁

### W0 基线与合同

- 冻结四条现有 Capability 的注册、Run、Artifact、Approval 和 UI Golden Fixture。
- 建立第五个纯指令 Skill Fixture。
- 建立第六个最小状态化 Workflow Fixture。
- 记录当前 SDK、Responses、compaction、stream 和 MCP 网关兼容矩阵。

完成门禁：基线测试可重复运行；失败项有明确环境原因，不能只看 healthz。

### W1 Skill 包解析与动态注册

- 解析 `SKILL.md` YAML frontmatter 和正文。
- 解析可选 `agents/openai.yaml` 与平台扩展 manifest。
- 支持 system/workspace/project/user 四级来源和确定性优先级。
- 支持扫描、刷新、启用、禁用、版本固定、重复名诊断。
- 使用渐进披露：初始只给 Agent 名称、描述和路径；选中后再加载完整说明。
- 删除 Sidecar 三个固定专家 Agent、固定 `Literal` 和固定 consult 工具清单。

完成门禁：第五个 Fixture 仅增加 Skill 目录即可被发现、路由和执行。

### W2 安装、上传与供应链安全

- 新增 Skill list/detail/install/enable/disable/upgrade/uninstall API。
- 支持目录和 ZIP；限制总大小、文件数、单文件大小和压缩比。
- 拒绝绝对路径、`..`、符号链接、设备文件、重复路径和大小写碰撞。
- 上传先进入 quarantine，校验成功后原子安装。
- 保存内容哈希、来源、安装者、版本、状态和诊断。
- 第一阶段只允许 instructions/references/assets/schemas；脚本需单独授权。

完成门禁：恶意 ZIP、损坏包、重名版本和中断安装不会污染已启用版本。

### W3 通用执行模式

- `inline`：在当前 Agent turn 中读取完整 Skill 指令并完成回复或通用 Artifact 提交。
- `background_task`：统一 Task 状态、进度、取消、重试、结果和事件。
- `stateful_workflow`：复用现有 Go Run/Step/Artifact/Approval 引擎。
- 增加通用 Agent Artifact Tool，不要求每个普通生产请求都写新工作流。
- 执行器通过注册描述匹配，不在核心代码按 Skill ID switch。

完成门禁：三种模式均有 Fixture；Invocation 与 Task/Run 的 0..1 关系正确。

### W4 MCP 与工具平面

- 建立 `AgentToolProvider`：Runtime Function Tools、MCP、Hosted Tools 使用同一描述模型。
- 先把现有 Runtime 查询/命令工具纳入 Provider，再逐步暴露第一方 MCP Server。
- 支持 remote Streamable HTTP、HTTP/SSE，以及受控本地 stdio。
- 支持 `allowed_tools`、延迟加载、超时、重试、取消和结果大小限制。
- 敏感工具默认审批；记录发往 MCP 的参数摘要和返回状态。
- Skill 缺少 MCP 依赖时显示可操作诊断，不静默忽略。

完成门禁：一个 Fixture Skill 能声明并调用测试 MCP；未授权写工具会进入审批。

### W5 Workspace Projection 与通用前端

- 服务端编译版本化 Workspace View Model。
- 建立 Artifact、Navigation、Task、Approval、Interaction、Composer Registry。
- 前端只接受受信任 `view_key`，未知类型进入只读 Inspector。
- Skill 菜单、默认 Prompt、输入约束、配置表单和依赖状态来自公开 manifest。
- 从 `App.tsx` 移除决定核心流程的 Capability ID 分支。
- 新增 Skill 管理页：上传、版本、状态、诊断、依赖和作用域。

完成门禁：新增 Fixture 不修改 `App.tsx`；四条现有流程展示与交互无回归。

### W6 Agent turn 真实流式协议

- POST message 快速返回 `agent_turn_id`，后台执行 SDK `run_streamed`。
- 将文本 delta、工具开始/完成、审批请求、Artifact commit、失败、取消映射到项目事件流。
- 每轮只有一个 terminal event；支持 Last-Event-ID、断线回补和幂等取消。
- UI 展示安全的执行说明和工具状态，不展示隐藏思维链。
- 同一 Conversation 的写 turn 串行；只读活动按策略并行。

完成门禁：慢工具、断网重连、浏览器刷新、取消竞态和 Sidecar 重启均有测试。

### W7 身份、授权与多租户

- 替换 `shared_internal_user`，建立 request principal。
- 所有 Project、Asset、Skill、Run、Artifact、Approval、Task、MCP credential 带 workspace/user 边界。
- 服务间令牌与最终用户身份分开；内部 API 不接受客户端伪造 Actor。
- 建立角色、配额、审计、数据保留和删除传播。
- 数据库查询补齐 tenant predicate，并以跨租户反例测试。

完成门禁：两用户同名项目、同名 Skill 和并发请求不能互相读取或修改。

### W8 脚本沙箱

- 上传脚本默认禁用；启用需要管理员策略和用户确认。
- 每次执行使用隔离工作区、只读 Skill 包和显式输出目录。
- 限制 CPU、内存、时间、进程数、磁盘和网络；环境变量采用 allowlist。
- 禁止宿主凭据继承；保留执行摘要、退出码和产物清单。
- Windows 本地、Linux 线上分别实现并测试隔离适配器。

完成门禁：路径逃逸、fork/process bomb、超时、网络访问和凭据探测测试均被阻断。

### W9 SDK、网关、可观测性与发布

- 升级 SDK 前先跑兼容矩阵，不直接解除 `0.21.x` 固定。
- 验证 `/responses`、流式事件、工具调用、MCP、Skills 和 `/responses/compact`。
- 记录 turn/response/tool/task/run 关联 ID、延迟、token、失败阶段和取消原因。
- Tracing 默认做隐私过滤；关键路径建立 eval 和质量样例。
- 建立数据库迁移、功能开关、灰度、回滚和旧路径删除清单。
- 清理 Eino 遗留依赖和过期文档只能在 SDK 路径回归通过后进行。

完成门禁：预发布环境完整回归，旧四流程与新 Fixture 同时通过，具备回滚演练证据。

## 4. 实施顺序

```text
W0
 -> W1
 -> W2 + W3
 -> W4
 -> W5
 -> W6
 -> W7
 -> W8
 -> W9 final gate
```

并行边界：

- W5 的类型和 Registry 可以在 W2/W3 后半段并行，但不能早于公开 manifest 稳定。
- W6 可先定义事件合同，但落地必须复用 W3/W4 的真实执行事件。
- W7 从新表和新 API 开始即执行，最终迁移在功能闭环后完成。
- W8 独立于指令型 Skill；不得阻塞 instruction-only Skill 上线。

## 5. 首个 10 小时检查点

10 小时不是范围截止，只是第一次审计点。优先次序：

1. 完成 W0 的自动化基线和兼容探针。
2. 完成 W1 的标准 Skill 解析、动态目录、渐进披露和动态路由。
3. 让第五个 instruction-only Skill 达到零核心代码改动验收。
4. 开始 W2 的持久化模型与安全 ZIP 导入。
5. 若前四项提前完成，进入 W3 inline executor；不为赶进度跳过测试。

检查点必须报告：

- 已完成的代码与合同；
- 实际通过的测试和 live probe；
- 仍失败的用例及根因；
- 当前数据迁移和回滚状态；
- 下一工作包的准确入口。

## 6. 全量验收矩阵

### Skill

- 安装、升级、禁用、卸载、回滚、重复名和版本固定。
- 显式调用、隐式匹配、拒绝误触发、缺失依赖和渐进披露。
- 本地包与 OpenAI 托管 Skill 的统一元数据映射。

### Agent

- 普通聊天、项目读取、资产读取、通用产物创建、修改和重新生成。
- 30+ 连续回合、SDK compaction、历史搜索和并发 turn。
- 工具失败、模型超时、无 terminal commit、取消和恢复。

### Runtime

- inline 不创建 Run；background task 不创建 Business Run；workflow 固定版本创建 Run。
- Artifact 血缘、版本冲突、审批失效、暂停恢复、失败重试和幂等。
- 两个用户、两个项目和多个历史 Run 的隔离。

### UI

- 未知 Skill、未知 Artifact、缺少 View、缺少 MCP 和不可用 Provider。
- 1280x720、1440x900、1920x1080 及移动窄屏。
- 断线重连、刷新、取消、长文本、50 集目录和并发活动。

### Security

- ZIP traversal/bomb/symlink/collision。
- MCP prompt injection、敏感写工具审批、恶意 URL 和数据外发记录。
- 脚本资源限制、网络限制、凭据隔离和输出目录限制。

## 7. 当前执行记录

| 时间 | 工作包 | 状态 | 证据 |
|---|---|---|---|
| 2026-09-04 | 方案与官方能力核对 | completed | OpenAI Skill、Skills API、MCP 与 App Server 文档已核对 |
| 2026-09-04 | 当前代码基线扫描 | completed | 已确认静态 Go Registry、Sidecar 固定三 Skill Router、前端 Capability ID 分支 |
| 2026-09-04 | W0 基线与合同 | completed | 本地 Git 基线与离线 bundle；旧四流程完整 Golden；第五个 inline 与第六个隔离 stateful Fixture；当前网关实测兼容矩阵 |
| 2026-09-04 | W1 Skill 包解析与动态注册 | completed | 第五个 instruction-only Skill 仅新增目录即可完成发现、渐进披露、显式/隐式路由与 inline 执行；无核心 ID 分支 |
| 2026-09-04 | W2 安装、上传与供应链安全 | completed | schema v25；目录/ZIP quarantine；不可变版本与哈希双检；install/detail/enable/disable/upgrade/rollback/uninstall/diagnostic API；恶意包、篡改与重启恢复门禁；Go 16 包通过 |
| 2026-09-04 | W3 通用执行模式 | completed | schema v26；inline 不建 Run、background_task 使用可租约/进度/取消/重试的 Agent Task、上传 stateful Skill 动态编译 Workflow 并复用 Run/Artifact/Approval；Invocation 与 Task/Run 双向 0..1 约束；独立通用 Artifact；Worker 内部鉴权；Go 16 包与 Sidecar 206 tests 通过 |
| 2026-09-04 | W4 MCP 与工具平面 | completed | schema v27；统一 Runtime Function/MCP/Hosted Tool 描述与 `AgentToolProvider`；受信任目录、HTTPS/stdio allowlist、环境密钥引用、allowed_tools、延迟连接、超时/重试/取消/结果上限；SDK `tool_call_id` 贯穿脱敏审计；写/敏感工具审批门禁；MCP Fixture Skill 零核心注册；真实 stdio 读调用与 SDK 写 interruption；Go 16 包、Sidecar 210 tests 通过 |
| 2026-09-04 | W5 Workspace Projection 与通用前端 | completed | 服务端版本化 Workspace Projection；六类 Registry 驱动导航、Artifact、Task、Approval、Interaction 与 Composer；未知 View 只读降级；动态 Skill 管理与依赖诊断；新增 Fixture 无需修改 `App.tsx`；前端回归与 production build 通过 |
| 2026-09-04 | W6 Agent turn 真实流式协议 | completed | schema v28；POST 快速接受并持久化 AgentTurn；SDK `run_streamed`、Sidecar 内部 SSE 与项目事件流贯通；同会话串行、跨会话并发；Last-Event-ID 回补、刷新恢复、幂等取消、取消/提交竞态和重启恢复均有门禁；原始控制 JSON 不作为用户文本 delta；Go 16 包、Sidecar 217 tests、前端 131 tests 与 production build 通过 |
| 2026-09-04 | W7 身份、授权与多租户 | completed | schema v29；request principal、角色和 SHA-256 bearer 配置；HttpOnly session 与 CSRF；最终用户和 Sidecar/worker 服务身份分离；Project/Turn 用户归属、workspace Registry/Skill 包/MCP 密钥引用隔离；项目/存储/Turn/Skill 配额；安全审计与保留；工作区删除传播；同名资源、并发跨租户、Actor 伪造和 worker 认证门禁；Go 全包、Sidecar 217 tests、前端 131 tests 与 production build 通过 |
| 2026-09-04 | W8 脚本沙箱 | code-complete / real OCI gate pending | schema v30；Skill 声明式 Python entrypoint；默认关闭的管理员策略；SDK 本轮 Skill 脚本白名单；参数指纹绑定的一次性用户审批；Windows/Linux fail-closed OCI 适配器；digest 固定与禁止拉取；网络、权限、进程、CPU、内存、时间、磁盘、输出和环境变量限制；输入脱敏审计、产物 SHA-256 下载校验与工作区删除传播；Go 全包与 vet、Sidecar 220 tests、前端 133 tests 和 production build 通过；本机无 Docker/Podman，3 个真实 OCI 攻击门禁明确 SKIP，未伪报实机通过 |
| 2026-09-05 | W9 SDK、可观测性与发布 | local implementation complete / external gates pending | schema v32；固定 `openai-agents==0.21.1` 与 `openai==3.3.1`；本地 SDK surface、真实 stdio MCP 和 Local/OpenAI Skill 元数据映射通过；SDK 原生 `RunState` 支持精确 tool call、多审批、批准/拒绝、进程重启后的持久暂停恢复，私有 checkpoint 不进入 Workspace Projection；同一 Turn 的 response/request/trace、token 与分阶段延迟跨审批恢复累计，失败阶段和取消原因持久化；Tracing 默认脱敏；11 项 Agent eval 合同覆盖旧四流程和三种执行模式；workspace canary fail-closed；旧 Eino、同步 Go Agent、Go 内容/视频模型 Worker、`modelprovider` 和 Sidecar spike 路由已删除，状态化生成及完整视频模型调用统一由 SDK Agent/Runner 执行；Go 全包与 vet、Sidecar 221 tests、前端 143 tests、production build、桌面/手机审批浏览器烟测均通过；完整本地 release gate 通过；v24 -> v32 -> migration backup -> v24 基线恢复演练通过；Live Gateway、真实 OCI 与预发布回归因当前本地环境/禁止远端操作保持待授权，不伪报发布完成 |

## 8. 明确排除

- 当前主线不接入 Codex App Server。
- 不让模型生成或加载任意 React/HTML/组件路径。
- 不把 MCP 当作 Session、Memory、Approval 或 Workflow 状态机。
- 不为了表面通用化删除现有领域工作流所需的确定性执行器。
- 不以 healthz、单元测试数量或 Agent 文本自述替代真实端到端验收。

## 9. 官方基线

- OpenAI Skill：`SKILL.md` 必需，`scripts/references/assets` 可选，支持渐进披露；`agents/openai.yaml` 可声明 UI、调用策略和 MCP 依赖。
- OpenAI Skills API：支持 Skill 创建、ZIP/目录上传、不可变版本与默认版本指针。
- Responses MCP：支持 remote MCP、工具过滤、延迟加载和审批；敏感操作必须经过审批与审计。
- Codex App Server 仅保留为未来本地客户端可选适配器，不属于本计划主线。
