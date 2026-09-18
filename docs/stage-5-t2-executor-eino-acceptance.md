# 阶段 5 T2 Executor / Eino 验收

日期：2026-08-03  
状态：通过  
下一步：T3 小说链迁移

## 1. 实施边界

T2 只建立通用执行基础，不把业务 Run 状态迁入 Eino：

```text
Capability Manifest
-> Executor Registry / Provider Requirement
-> Business Runtime Task Claim
-> Eino Worker Execution Graph
-> Provider
-> Parse / One Repair / Structural Validate
-> Runtime Adapter / Full Schema Validate / Commit
```

Runtime 继续是 Run、Task、Artifact、Approval、Event 和版本状态的唯一权威。

## 2. 已完成

1. 新增统一 Executor Registry，区分 Runtime、Worker 和 Workflow Executor；
2. Worker 默认订阅从 Registry 生成，不再维护独立硬编码清单；
3. `workflow.shared_script_quality_review` 已可发现并进入默认订阅；
4. 视频解析和四个视频 Runtime Executor 未实现前不注册，视频能力启动投影为不可用；
5. Capability 启动可用性同时检查所有 Executor 和 Provider Requirement；
6. OpenAI-compatible 配置拆分为 Control、Content、Multimodal Video 和 Image Understanding 角色；
7. Control/Content 支持旧 `CONTENT_AGENT_LLM_*` 兼容回退，视频/图片不错误继承文本配置；
8. Worker 改为 Eino Graph：`build_request -> invoke_model -> parse_or_repair -> validate_provider_result`；
9. 非法 JSON、内部 checkpoint 和明确 Provider Result Contract 失败时只修复一次；
10. Trace 回调只记录节点、Runtime ID、错误码和修复次数，不记录 Prompt、来源正文或 Key；
11. Context Pack 支持可选 `provider_result_contract`，并纳入 Context Hash 和 Provenance；
12. 旧 Response Adapter 的原始包装输出不在 Worker 被最终产物 Schema 误杀，Runtime 适配后仍执行完整合同校验。

## 3. 验收证据

- `internal/executor`：通过；
- `internal/modelprovider`：通过；
- `internal/capability`：通过；
- `internal/runtime`：通过；
- `internal/worker`：通过，包含原小说 Worker E2E；
- `cmd/worker`：通过；
- Server 和 Worker 生产入口：编译通过；
- `go vet ./...`：通过；
- Capability 合同校验：3 个 Manifest、34 个 Step、14 个 Response Adapter，全通过。

Windows Application Control 仍会延迟随机临时 `.test.exe`；本轮继续使用固定测试二进制并逐包执行，不属于代码缺陷。

## 4. 未包含

- 小说业务链完整迁移与固定小说回归：T3；
- 非小说完整回归：T4；
- DOCX/PDF 解析、通用图片、导出与迁移：T5；
- 视频媒体处理和真实多集验收：T6；
- 前后端联调交接：T7。
