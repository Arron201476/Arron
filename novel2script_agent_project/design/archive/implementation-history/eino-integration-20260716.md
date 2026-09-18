# Eino 正式接入与回归记录（2026-07-16）

状态：Current。范围仅限 `novel2script_agent_project`。

## 接入结果

项目默认 Runtime 已从 native 切换为 Eino，`N2S_RUNTIME=native` 保留为回滚开关。

Eino 当前负责：

1. Main Agent 决策图：`interpret_intent -> apply_business_guards`。
2. 规划产物图：`validate_input -> invoke_model -> validate_output`。
3. 剧本产物图：`validate_input -> invoke_model -> validate_output`。
4. 局部修改图：`validate_input -> invoke_model -> validate_output`。
5. 原文拆集阶段图：`validate_split_stage_input -> invoke_split_stage_model -> validate_split_stage_output`。

现有 Go Runtime 与 SQLite 继续负责 Project、Run、Artifact、Approval、Event、版本、失败 task cursor 和重启恢复。Eino 节点不直接写业务状态，也不与业务审批并行维护第二套 Checkpoint 状态。

## 行为兼容

- HTTP API、前端数据结构和 SSE 不变。
- 控制模型失败时仍安全停止，不执行关键词语义兜底。
- Artifact Schema 与生成配置继续由确定性校验器约束。
- `script_context` 继续作为内部产物，不显示确认卡。
- 修改上游后，下游继续先标记 stale，再由用户选择保留或重生成。
- 暂停、恢复、失败 task 重跑和服务重启恢复继续使用稳定 Run/Task cursor。

## 自动化证据

- Main Agent Eino/native 决策一致性测试通过。
- Main Agent控制模型失败安全兜底测试通过。
- Eino规划、剧本、局部修改、输入拦截测试通过。
- Runtime运行时标记、递进长度纠偏及原有回归测试通过。
- Server路由、默认Runtime与并行监听测试通过。
- `go vet ./...` 和Go构建通过。

## 原文拆集稳定性重构

- 原文由 Go 建立稳定索引；全局模型输入最多 300 个代表单元，避免长篇输入无上限增长。
- 先生成覆盖全部目标集数的全局骨架，再按每批 5 集串行选择边界。
- 候选窗口按平均原文体量自适应限制为 300–1200 字符，相邻集候选区间互不交叉。
- 每批 checkpoint 写入 Run metadata；失败批次重试不会重做已完成批次。
- 后端最终校验起点、终点、集数、连续性、遗漏、重复与顺序，通过后才创建 `episode_split`。
- 30 集隔离实跑已验证全局阶段加 6 个批次可完成且最终 30 集连续覆盖；该次实跑还暴露并修复了控制模型附件压缩误改原附件的问题，Server 端到端测试确认新 Run 保留完整原文。

## 真实链路证据

### 小说链

- Eino隔离服务：`127.0.0.1:8841`
- run：`run_000001`
- 状态：completed
- 结果：7类目标Artifact齐全，无失败事件，只有1个 `run_completed`。

### 非小说链

- run：`run_000049`
- 状态：completed
- 结果：8类目标Artifact齐全，只有1个active `script_unit`和1个 `run_completed`。
- 首次真实回归暴露模型长度纠偏重复同一提示的问题；修复为最多两次、携带实际长度与明确缩减量的递进纠偏后，从原失败task cursor恢复成功。
- 最终剧本长度825字，满足既定275-825字验收范围，没有放宽产品规则。

## 运行约束

- 默认：`N2S_RUNTIME=eino`（环境变量留空时同样使用Eino）。
- 回滚：`N2S_RUNTIME=native`。
- 并行验证：可用 `N2S_ADDR` 指定独立监听地址。
- 切换Runtime不得改变SQLite数据库、对象ID、Artifact版本或前端合同。
