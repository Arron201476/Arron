# 产品流程审计

## 技术路线更新

Agent 框架已定为 Go 原生的 CloudWeGo Eino。此前审计里提到的 `agent-worker/ TypeScript` 和 `Claude Agent SDK` 路线不再作为主方案。

新的下一阶段是：

```text
frontend/ TypeScript：三栏工作台、Agent chat、Run timeline、Approval card、Artifact editor
backend/ Go：project_state、run_state、artifact store、run events API
backend/internal/agent/mock：Go mock agent runtime
backend/internal/agent/eino：后续接真实 Eino runtime
```

先用 Go mock agent runtime 跑通：

```text
用户输入
-> Router
-> Main Agent plan
-> run events
-> artifact updates
-> approval pause
-> continue
-> scripts
```

这条闭环稳定后，再把 mock runtime 替换成 Eino。

审计对象：当前 `agent-startup-contract.md`、`artifact-schemas.md`、`workflow-state.md`、`main-agent-operations.md` 和 `product-review-checklist.md`。

审计口径：只看“用户输入到剧本输出”的产品主流程，不审分镜、镜头卡、计费、订阅和导出。

## 总结

当前主流程设计可以进入工程骨架阶段，但需要先补齐几处产品保护：

```text
入口低置信度时要问用户
不同意图的第一刀读取策略要明确
单集 script_unit 和最终 scripts 聚合产物要区分
局部选区来源要明确
剧本分批生成事件要能被前端消费
```

这些缺口已经同步补进相关设计文档。

## 8 点审计结果

### 1. 用户输入后，Agent 是否先判断入口

结论：基本通过，已补强。

已有设计：

```text
Router 支持 generate_novel / generate_non_novel / revise_artifact / revise_selection / chat / project_control / unknown。
Main Agent 有小说输入、非小说输入、局部修改三条主入口。
```

原缺口：

```text
低置信度或输入模糊时，没有明确“先问用户确认”的产品话术。
```

已补：

```text
main-agent-operations.md 增加“入口确认”。
```

### 2. Agent 是否先读现状，再动手

结论：通过，已补强。

已有设计：

```text
Main Agent 操作铁律包含“先读，不猜”。
```

原缺口：

```text
没有按不同 intent 明确第一刀读什么。
```

已补：

```text
main-agent-operations.md 增加“第一刀读取策略”。
```

### 3. 是否会保护用户确认过的内容

结论：通过。

已有设计：

```text
artifact status 包含 confirmed / superseded / invalidated。
approval_request 要说明 proposed_action、affected_artifacts、risk_notes。
重跑上游步骤时，下游 artifact 只标记失效，不自动覆盖。
```

仍需工程实现时注意：

```text
Go 后端必须强制拦截 confirmed artifact 的覆盖操作，不能只靠 prompt 自觉。
```

### 4. 是否支持暂停、继续、重跑

结论：通过。

已有设计：

```text
run_state.status 支持 paused / waiting_approval / failed。
run_state.next_action 用于继续。
重跑某一步必须创建新 step 和新 artifact version。
```

仍需工程实现时注意：

```text
前端需要在 Approval card 和失败卡里提供继续、暂停、重跑入口。
```

### 5. 剧本是否逐步生成，而不是突然刷满

结论：基本通过，已补强。

已有设计：

```text
main-agent-operations.md 定义分批剧本输出。
workflow-state.md 定义 run_event。
```

原缺口：

```text
分批写入事件没有明确 payload，前端不一定知道怎么显示增量。
```

已补：

```text
main-agent-operations.md 增加 script_batch_inserted 事件示例。
```

### 6. 局部修改是否足够克制

结论：基本通过，已补强。

已有设计：

```text
局部修改分 selection_only / node_level / episode_level / artifact_level / upstream_change。
只有 upstream_change 或 continuity_delta 改变时才触发下游失效。
```

原缺口：

```text
selection_ref 没有明确来源类型，容易把剧本选区、分集卡选区、故事圣经选区混用。
```

已补：

```text
main-agent-operations.md 增加 selection_ref.source 类型。
```

### 7. Agent 是否用人话回复

结论：通过。

已有设计：

```text
product-review-checklist.md 有合格/不合格例子。
main-agent-operations.md 有用户语言规则。
```

仍需工程实现时注意：

```text
内部 run_event 可以保留技术字段，但 Agent chat 展示给用户时必须转成人话。
```

### 8. 从用户输入到剧本输出是否闭环

结论：基本通过，已补强。

已有设计：

```text
小说链：source_input -> story_bible -> episode_split -> episode_cards -> script_context -> script_unit。
非小说链：source_input -> material_bank -> story_seed -> series_blueprint -> episode_cards -> script_context -> script_unit。
```

原缺口：

```text
只有 script_unit 单集产物，没有定义最终 scripts 聚合产物。
```

已补：

```text
artifact-schemas.md 增加 scripts artifact。
```

## 当前结论

主流程产品设计现在可以进入下一阶段：工程骨架。

下一阶段不需要马上接真实 Eino runtime，先做最小可运行壳：

```text
frontend/ TypeScript：三栏工作台、Agent chat、Run timeline、Approval card、Artifact editor
backend/ Go：project_state、run_state、artifact store、run events API
backend/internal/agent/mock：先做 Go mock agent runtime，按 next_action 返回模拟事件和 artifact
```

先用 mock worker 跑通：

```text
用户输入
-> Router
-> Main Agent plan
-> run events
-> artifact updates
-> approval pause
-> continue
-> scripts
```

这条闭环稳定后，再把 mock runtime 替换成 Eino runtime。
