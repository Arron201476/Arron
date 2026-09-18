# Agent Platform 发布与回滚手册

状态：本地实现及全局 review 进行中，尚未完成完整验收；远端和预发布操作未获授权
基线：`pre-agent-convergence-20260904` / `0e841cc3a346ac9c8292977ae76cbfc3a474360f`

## 1. 唯一运行链路

```text
Frontend
  -> Go API accepts durable AgentTurn
  -> OpenAI Agents SDK Sidecar execute-stream
  -> SDK Session / compaction / tool orchestration / Skills
  -> authenticated Runtime tools
  -> Go Runtime validation, approval, idempotency and commit
  -> persisted event stream and Workspace Projection
```

旧 Eino Main Agent、Go 同步 `Decide/ExecuteTurn`、Go 内容/视频模型 Worker、
`modelprovider` 直连适配器、Sidecar decision-only 和 synchronous spike routes
均已删除。状态化工作流由 Sidecar 的 `SDKTaskWorker` 执行模型调用；Go Runtime
只负责租约、状态、验证、审批、幂等和提交。完整视频分析也通过独立 SDK Agent/Runner
调用兼容模型，不保留 Sidecar 内的 `chat.completions.create` 旁路。

## 2. 固定版本

- `openai-agents==0.21.1`
- `openai==3.3.1`
- Runtime schema `63`（2026-09-09 源码；脚本运行时从源码读取，不依据旧报告推定）

升级任一 SDK 前必须先生成新的本地兼容矩阵，再获准执行 Live Gateway 探针。

## 3. 本地门禁

以下是获准且满足运行条件后使用的命令，不是当前环境已通过的证明。当前 Go 测试程序受到 Application Control 限制，浏览器、真实 OCI 与网关验收未获相应条件；不得为运行门禁改名测试程序、改系统策略或操作用户正在使用的服务。

```powershell
.\scripts\run-agent-platform-local-gate.ps1
```

该命令设计为操作当前仓库和 loopback 临时服务，包含：发布报告故障路径自测、Capability/Fixture 合同、
旧路径扫描、本地 SDK surface、真实 stdio MCP、Go test/vet、Sidecar pytest、
前端 test/build、桌面与手机尺寸的工具审批浏览器烟测，以及
基线 schema -> 当前源码 schema -> 基线 schema 的业务数据回滚夹具。前端构建输出至本次运行独立的 `.tmp` 子目录，审批烟测只使用该目录且要求本次构建成功，不覆盖用户的 `frontend/dist` 或连接手工运行的开发服务器。

结果与判读：

- `docs/evidence/agent-platform-local-release-gate.json` 是最新运行报告，schema 为 `agent_platform_local_release_gate.v2`。开始即写 `running`，逐项保存 pending/running/终态；预检查失败也更新为 failed，不沿用上次成功。
- `.tmp/agent-platform-release-gate/<run_id>/` 保存本次报告、SDK 兼容报告、报告自测、回滚报告和隔离前端产物。旧的 `openai-sdk-local-compatibility-20260904.json` 及 Markdown 不再被新门禁覆盖。
- 除 Git HEAD 和 dirty 标记外，记录运行前后源码内容 SHA-256，涵盖未提交/未跟踪的源码、测试、schema、prompt、Skill 和能力定义。运行数据、凭据文件、安装依赖和生成证据不属于该源码指纹；输入矩阵另有哈希。源码或矩阵变化则本次失败。它不是不可变源码快照，也不证明运行时依赖或外部部署版本一致。
- 缺少、重复或降级任一必需检查均拒绝。`local_status=passed` 仅表示所列本地检查通过；`release_ready` 和兼容字段 `local_release_candidate_ready` 始终为 false。外部矩阵中的 `passed` 只作为 declared_status，实际仍为 unverified；本地脚本不能签署完整发布或最终 review 通过。
- 报告只允许写入仓库 `.tmp` 或 `docs/evidence` 下的 JSON，拒绝越界和 reparse point；原子替换受执行环境写权限限制。写入失败会失败退出，不降级为非原子覆盖。

只验证报告逻辑的夹具命令如下。它使用自建临时文件、隔离 Git 目录和无工具链的脚本副本，在预检查阶段停止，不执行 Go/浏览器/远端检查；其通过不能替代上方完整门禁。

```powershell
.\scripts\test-agent-platform-release-evidence.ps1
```

业务数据辅助函数另有标准库 unittest 夹具；发布门禁也会运行它们。其SQLite初始DDL来自固定基线源码，迁移回执是显式构造的测试数据，不是执行Go迁移器。

```powershell
.\.tools\openai-agents-sidecar-venv\Scripts\python.exe -m unittest discover -s scripts -p test_agent_platform_rollback_data.py -v
```

## 4. 灰度规则

- `CONTENT_AGENT_SDK_AGENT_ENABLED=false`：拒绝所有新 Agent Turn。
- `CONTENT_AGENT_SDK_AGENT_ENABLED=true` 且 canary 列表非空：只允许列出的 workspace。
- `CONTENT_AGENT_SDK_AGENT_ENABLED=true` 且 canary 列表为空：允许全部 workspace。
- `CONTENT_AGENT_RELEASE_ID`：写入 Turn 观测记录，必须使用不含路径和凭据的稳定标识。

任一拒绝模式都返回明确 503，不存在旧 Agent fallback。灰度扩大顺序为单 workspace、
少量 workspace、全量；每一步都必须检查失败阶段、取消原因、token、延迟和关联 ID。

## 5. 数据库回滚

Runtime 从旧 schema 启动升级时会先用 SQLite `VACUUM INTO` 生成不可变备份，并在
`migration_history.backup_ref` 记录位置。旧二进制会拒绝打开更高版本数据库，防止
误写。回滚步骤：

1. 停止接收新 Turn，并等待或取消运行中 Turn；
2. 停止前端、Go、Sidecar 和 Worker；
3. 保留迁移后数据库与 WAL/SHM 作为故障证据；
4. 将对应 migration backup 恢复为运行数据库；
5. 切换 `current` 到基线 release；
6. 启动基线 Sidecar、Go、Worker 和前端；
7. 验证 health、登录、项目读取和一个只读 Artifact；
8. 未查清原因前保持 rollout disabled。

本地演练命令：

```powershell
.\scripts\run-agent-platform-rollback-drill.ps1
```

G4.50已将空库检查改为受控业务数据夹具：两个作品/工作区、两个会话、四条包含相同中文正文的独立消息、两个产物的四个历史版本，以及配置/输入快照、事件、幂等回执和两个原始文件。仅允许向演练目录中的空schema24数据库写夹具，拒绝已有业务数据、其他schema和已有清单；不支持拿用户数据库直接填充测试记录。

演练先验证基线API能读取夹具，再迁移。`migration_history`必须有唯一、完成且版本匹配的回执，`backup_ref`必须指向该migration_id下的确切数据库；不再取修改时间最新的文件。备份与迁移前数据库比较全部业务表、schema、类型、重复行及逻辑摘要，并进行integrity/foreign_key检查。物理页布局和VACUUM后的文件哈希不同不等于丢数据；迁移后的原业务字段及数量还须保留。

恢复使用SQLite backup API写入全新的`restored-data`目录，再从独立文件备份恢复原始字节，不复用迁移后原文件目录。基线和恢复后各执行12项只读API检查，覆盖作品、消息、产物/历史版本、文件下载；拒绝重定向、代理、超限响应和8860/8880端口。最后重新核对完整数据库逻辑摘要、文件字节、备份不变和清单不变。`validation_scope=baseline_business_database_assets_and_read_api`，只有整条实际链路通过后`business_data_validation`才为passed；`rollback_ready=false`仍明确不签署所有用户数据/完整部署可回滚的结论。

开始及预检查失败均写当前报告；完成检查后先确认进程退出、恢复环境并完成临时目录清理，再签署本项结果。任何一个进程清理失败也会尝试清理其他进程和恢复环境，并将本次记为失败。**当前真实Go二进制演练没有运行**；28项辅助函数/CLI/随机loopback夹具和51项PowerShell报告回归通过，不能替代真实Runtime迁移、完整资源/配置/部署版本回滚验收。原有回滚备份、用户数据库和服务未被执行修改。

## 6. 外部门禁

以下项目不能由本地结果替代：

- Live Gateway：Responses create/stream/function tool/compact、Skills API、remote MCP；
- OCI：路径逃逸、进程/资源限制、网络和凭据探测；
- 预发布：旧四流程、三种执行模式、多文件 Skill 编写/确认安装/调用/升级/下载、30+ 回合 compaction、并发、取消恢复、多租户、业务数据回滚和 UI 尺寸矩阵；最终全局 review 结论仍须独立完成。

当前禁止部署、上传、SSH、重启或修改测试机。只有用户明确授权后，才执行这些步骤。
