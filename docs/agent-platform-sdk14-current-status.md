# 固定 SDK 能力状态复核

日期：2026-09-14，G4.216。目的：纠正旧审计中的接入状态，不新增产品范围，不替代原 SDK-01 至 SDK-14、UX、GAP 和 W0-W9 验收。

## 证据边界

2026-09-15范围更新：用户已确认Computer、PTC和语音/实时全部纳入本次交付。下表对应“待定”文字保留为9月14日的历史结论；当前状态为必交、尚未实现，验收要求见结项清单。此次确认不代表user_stop语义已确定，也不解除外部环境和操作权限边界。

- 固定环境为 openai-agents 0.21.1 / openai 3.3.1；未升级。SDK 目录取自本地 `.tools/openai-agents-sidecar-venv/Lib/site-packages/agents`。生产路径下文以 `experiments/openai-agents-sidecar/src/content_agent_sidecar` 为根。
- 本轮读取 SDK 工具联合类型、公开类和实际生产装配；接口存在或 import 命中不作为执行通过。没有启动模型、容器、浏览器或用户服务，没有迁移数据。
- 最近专项报告的范围见 `agent-platform-goal-progress.md`。真实 Go、网关、OCI、跨重启及用户整链路的未通过状态保留。当前安全子集不能替代受限测试，也不能借用旧版本通过报告。

## 当前能力族

| 能力族 | 当前源码接入 | 仍需交付或验证 |
| --- | --- | --- |
| SandboxAgent / 文件 / 工作区快照 | `native_capabilities.py` 用原 Agent 字段构造 SandboxAgent，`native_runner.py` 绑定受管会话；`native_session.py`、`native_snapshot.py` 连接 Runtime 工作区。不是仅有未调用的 SDK import。 | 代码已接；G4.208 的 64 项是 SDK 和本地 HTTP 夹具，不是实际 Go/OCI/模型联合。持久恢复、隔离、取消和产物交付仍需真实证明。 |
| Sandbox Skills | manifest 含 `.skills` 时装配 `Skills(from_=Dir(), skills_path=".skills")`；平台负责受管安装版本和资源映射，不使用 LocalDirLazySkillSource 直接扫描任意宿主路径。 | 原生 Skills 路径已接，不再是“未接”；用户多文件编写、确认安装、目录刷新后同轮调用及重启后复用仍需完整联合验收。 |
| Memory 读取 | 存在版本化私有 memory 文件时装配 `Memory(..., read=MemoryReadConfig(live_update=False), generate=None)`，要求 summary；没有额外建立第二个 compaction owner。 | 读取有生产装配；不应把 generate=None 单独解释成整个产品无生成，也不能把读取当作自动生成闭环已经通过。 |
| Memory 生成 / 发布 | `native_memory.py` 复用 SDK 提取、整合提示与 RolloutExtractionArtifacts 契约；`memory_worker.py`、`memory_execution.py` 负责持久阶段运行，`memory_workspace.py` 绑定受管工作区。不是直接启用 SDK 默认关闭时自动生成 manager。 | 平台适配承担授权、阶段检查点、归档和批准发布；实际重启、遗忘、私有数据清理与真实 Shell/Go 联合仍待验。不能声称所有 Memory 默认选项均已原样启用。 |
| Shell / LocalShell | `native_capabilities.py` 装配 SDK Shell capability，只暴露当前受管策略允许的 exec_command/write_stdin；非 PTY provider 明确拒绝 tty=true。 | 当前是沙箱能力工具接入，不是每一种独立 ShellTool/LocalShellTool transport 都使用。真实 OCI/交互支持仍未验，不开放任意宿主命令。 |
| ApplyPatch / CustomTool grammar | 原产物 revision 的 ApplyPatch 与原生多文件补丁并存；`native_patch_tool.py` 返回 CustomTool，保留 SDK grammar、原调用和审批绑定，并审计执行结果。 | 已有明确 raw-string 场景，不再是“CustomTool 未接”。这不等于任意用户 grammar 注册产品已经实现；受限多文件修改需真实验收。 |
| ToolSearch / 延迟加载 | `agent_tools.py` 为延迟工具装配 ToolSearchTool，原生 workspace binding 也补上缺失的 ToolSearchTool。 | 原“仅开 defer_loading 就违反契约”的装配状态已变化；真实网关搜索、筛选、权限撤销和跨执行模式调用仍需验证。固定 SDK 的 client search 不由标准 Runner 自动执行，不能混同当前 server 搜索路径。 |
| ProgrammaticToolCallingTool（PTC） | 固定 SDK Tool 联合类型含该类；生产源码未发现装配，也未发现对应 caller 配置链。普通 function calls、as_tool 并行不是 PTC。 | 明确未实现，范围与使用任务仍待定，不静默删除。接入前需确定受管只读工具集合、模型/网关支持、program 输出保存及取消/恢复；不能简单给全部写工具设置 programmatic caller。 |
| ComputerTool | 固定 SDK 含 ComputerTool；当前生产源码未发现装配。开发助手自己的浏览器工具不属于本平台。 | 明确未实现，执行环境、权限、截图数据边界、审批和用户入口待确定；保持原范围待定项，不能宣称外部阻塞掩盖尚无代码。 |
| Voice / Realtime | 固定 SDK 存在 VoicePipeline 和 RealtimeAgent；生产源码未发现使用这两类。 | 明确未接语音流水线及实时语音。上传音频资产、转写和实时双向语音是不同任务，需分别界定；不能用视频帧/图片能力替代。 |
| HostedMCPTool | 固定 SDK 有该类；生产未使用该服务端托管路径，平台已有 SDK-managed MCP 连接与受管工具链。 | 不把“未用 HostedMCPTool”算成“没有 MCP”；现有路径的传输、身份/凭据和三模式实际执行仍需各自验收。是否另支持托管 MCP 要对应使用场景。 |
| Responses WebSocket | 固定 SDK 有 OpenAIResponsesWSModel；平台未装配，使用现有 Responses HTTP 流。 | 替代传输，不要求为了覆盖类名重复执行器。断线恢复、运行中追加和取消是独立行为，不能因已有流式而视为完成。 |
| Session / providers / hooks | 已有受管 Session/Responses 路径；不需同时引入每个存储或模型适配器，也不以某个 hook 未 import 判定能力缺失。 | 当前选择必须满足身份隔离、持久恢复、用量/观测；SDK 原生 compaction 的真实网关阻塞仍在，不能降级为本地摘要。 |

PTC 官方配置需要同时启用托管 programmatic tool 并逐工具指定 allowed_callers；官方建议写入或审批敏感动作默认采用直接调用。该约束只用于说明尚缺的适配，不是本项目已经完成该能力的证据。[官方 PTC 文档](https://developers.openai.com/api/docs/guides/tools-programmatic-tool-calling)

## 不能混用的状态

1. “代码已接但未真实验收”：工作区、Skills、Memory、受管 Shell/patch、ToolSearch。继续按原任务和三模式验收，不能重复开发来替代外部门禁。
2. “当前未实现且适用范围待定”：PTC、Computer、语音/实时。保持原待办，不等于用户同意排除；也不能把未开发统称为环境不可用。
3. “已有选定实现，其他为替代路径”：Hosted MCP、Responses WS、Session/provider 适配器。按用户可完成的任务验收，不以使用所有 SDK 类为目标。
4. `user_stop` 不是 SDK 类缺失，而是平台 workflow 条件：`backend/internal/capability/compiler.go` 仍拒绝该保留条件，取消不运行后继。普通取消按钮可用不能证明停止后分支已经实现；原语义待定项保留。

结论：本轮完成这份能力族状态复核，不代表 SDK-14 完整签收，更不代表整体“补齐 + 同版回归 + 独立全局 review”完成。旧审计其他 SDK/UX/GAP 表仍为历史快照，当前交付状态以结项清单和 Goal 进度的逐项证据为准。
