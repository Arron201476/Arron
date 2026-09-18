# 阶段 5 T1 通用 Agent Shell 验收

状态：已完成  
日期：2026-08-03

## 1. 实现范围

T1 只实现通用 Agent 控制面，不迁入 Domain Worker：

```text
Message Request
-> Eino interpret_intent
-> deterministic Guard
-> AgentDecision / Proposed Action
-> Runtime 原子持久化
```

Runtime 继续是 Project、Message、Run、Artifact、Approval 和 Event 的唯一状态权威。Main Agent 不直接创建 Run，也不生成 Runtime 资源 ID。

## 2. 已完成能力

- 控制模型支持普通对话、追问、能力发现、缺口说明和语义路由；
- 用户显式 `capability_ref` 优先于任何模型判断；
- 自然语言精确命中 Skill Label/Alias 时可确定性路由；
- 模型自动路由必须命中当前 Registry、Capability 可用、允许 `auto_route` 且置信度不低于 0.72；
- 自动路由使用当前注册版本和 `selection_mode=inferred`，模型不能编造版本；
- Guard 只允许初次生成 `collect_run_configuration`，普通 Message 不创建 Run；
- 配置卡通过独立 Runtime Command 升级为 `start_run` version 2，重新计算 Hash 并要求显式确认；
- 活动 Run、非法 Skill 和非法附件在 Provider 调用前预检，事务提交时再次权威校验；
- 低置信度返回结构化追问，虚构 Skill 和模型失败均不产生动作；
- 零 Domain Capability 时 Project、Message 和通用 Agent Shell 仍成立；
- 未配置控制模型时保留显式 Skill、Alias、能力发现和结构化追问降级。

## 3. 服务配置

Server 在以下任一变量出现时启用 OpenAI-compatible 控制模型，并要求三项完整：

```text
CONTENT_AGENT_LLM_ENDPOINT
CONTENT_AGENT_LLM_API_KEY
CONTENT_AGENT_LLM_MODEL
```

可选项继续使用现有 Provider 配置：

```text
CONTENT_AGENT_LLM_TIMEOUT_SECONDS
CONTENT_AGENT_LLM_MAX_OUTPUT_TOKENS
CONTENT_AGENT_LLM_JSON_MODE
```

Key 仅由服务端环境变量读取，不写入 Capability、Message、Decision 或日志。

## 4. 验收结果

2026-08-03 本地回归：

- `go test ./internal/shell ./internal/httpapi ./cmd/server` 通过；
- `go test ./...` 中除 Worker 测试二进制被 Windows Application Control 临时拦截外，其余包通过；
- `go test ./internal/worker -count=1` 单独重跑通过；
- `node acceptance/validate-capability-contracts.mjs` 通过；
- 当前机器统计为 3 个 Capability、34 个 Step、14 个 Response Adapter。

新增验收覆盖：

- 零 Capability 真实模型普通对话；
- 零 Capability 无模型结构化追问；
- 显式 Skill 不被模型改路由；
- 精确 Alias 无模型路由；
- 高置信度自动路由；
- 低置信度追问；
- 虚构 Skill 拒绝；
- 模型失败无动作；
- HTTP 自动路由只保存 Proposed Action，不创建活动 Run；
- HTTP 配置卡可确定性升级为启动确认，并在用户确认后创建 Run；
- HTTP 零 Capability 仍保存完整 Message Exchange。

## 5. T2 交接

T1 不包含以下工作：

- Executor Registry 与 Capability 启动可用性汇总；
- Worker Eino Graph、Schema Repair 和 Callback/Trace；
- Control/Content/Video/Image 等 Provider Role 分离；
- Generic Image Tool 与授权 Asset Resolver；
- 小说、非小说和视频业务链迁移。

这些项目从 T2 开始处理。通用图片仍是第一版要求，但图片上传本身不会自动启动 Domain Run。
