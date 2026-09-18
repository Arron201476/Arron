# 阶段 1-6 代码复盘

日期：2026-08-05  
范围：新项目顶层 `backend/`、`frontend/`、`capabilities/v1/`、`schemas/v1/` 和权威合同；`novel2script_agent_project/` 仅作为迁移来源，不参与运行。  
结论：**两个 P0 声明/实现漂移已经关闭，阶段 6 已达到产品确认条件；在产品负责人明确确认前，当前主阶段仍保持阶段 6。**

## 1. P0 关闭结果

### P0-1 质量审核处理闭环

已实现：

1. `ai_revise`：按 QualityReview 唯一语义路由映射真实 Capability Step，生成 ImpactReview 和 RegenerationPlan，携带审核证据与用户要求返工。
2. `manual_edit`：前端定位受影响 `script_unit`；保存新版本时原子解决质量处理请求、使旧 Review 失效、恢复剧本步骤并刷新对应 `script_handoff`。
3. `confirm_change`：只在 `recommended_route=user` 时展示；记录用户确认内容，并从受影响剧本步骤创建返工计划。
4. `accept_with_risk`：只允许 high/medium，blocker 在 Runtime 和前端同时禁止。
5. 前端按 QualityReview 显示严重级别、受影响集、证据、影响、修改目标和唯一总体处理目标。
6. 返工不直接调用模型；Run 先暂停，仍由用户恢复后进入 Worker 执行和后续统一剧本确认。

验证覆盖：AI 返修、用户确认变更、手动编辑与交接刷新、blocker 操作矩阵、幂等回放。

### P0-2 普通检查点四类操作闭环

已实现：

1. `approve` 继续走通用 Approval Resolution。
2. `edit_artifact` 只定位并打开当前确认对应的产物编辑器，保存后创建新 Artifact Version。
3. `request_ai_revision` 先收集修改要求，再走专用 regeneration command。
4. `regenerate_artifact` 显示影响说明并二次确认，再走专用 regeneration command。
5. 目标产物组成第一重生成组，下游依赖随后；批次任务按 Step + Task Keys 分组，避免不同集错误合并。
6. 原版本与历史记录保留，返工计划、审核记录和事件均持久化且支持幂等重试。

## 2. 本轮其他修复

1. 最终稿确认改为专用 Final Selection Command。
2. 补齐 MOV/WebP 等空 MIME 文件分类；混合上传只把视频加入视频 Asset Set。
3. SSE 补齐 Asset Set、质量审核等实际 Runtime 事件。
4. Server ProviderSet 补注册 `subtitle_ocr_provider`。
5. Asset Set 创建、改版、封存、重开补齐事务内幂等记录。
6. 剧本导出补齐幂等回放。
7. 前端不确定网络错误使用同一 Idempotency-Key 重试。
8. 剧本手动保存后接通 `script-edit-completions`，不再停在交接刷新之前。

## 3. 回归结果

| 项目 | 结果 |
|---|---|
| Capability 合同校验 | 通过：3 Manifests、34 Steps、14 Adapters |
| 阶段 2 验收矩阵 | 通过：96 Cases、10 Fixtures、17 Areas |
| Runtime 全包 | 通过，含新增普通返修与质量处理测试 |
| HTTP API 包 | 通过 |
| 前端协议测试 | 通过：10/10 |
| TypeScript + Vite 生产构建 | 通过 |
| Playwright Route Mock | 通过：入口、菜单、删除、运行、视频、最终稿、导出、质量审核 |
| 1280/1440/1920 布局 | 通过，无横向溢出；Composer 贴底；发送按钮 32x32 |
| 50 项产物压力视图 | 通过 |
| blocker 质量审核视图 | 通过；仅展示手动修改与确认故事变更 |
| Go 全仓执行 | `runtime`、`httpapi` 等通过；3 个包被 Windows Application Control 拒绝启动测试 EXE |

环境说明：`cmd/worker`、`internal/worker`、`internal/mediaprocessor` 已完成编译，但测试 EXE 启动被系统策略拒绝；这三项不能记录为运行通过。真实 Worker/Provider 和 40 集视频内容验收仍属于阶段 7/9，不计入阶段 6 前端完成结论。

## 4. 阶段 7 准入判断

阶段 6 技术门禁已满足，等待产品负责人确认：

- 三个 Capability 的入口、配置、运行、确认和结果视图可操作；
- 普通确认点不存在展示不可执行按钮；
- 质量审核四种操作具备确定性 Runtime 路径和专用前端投影；
- 编辑、AI 修改、重新生成和确认不会混用同一命令；
- 桌面三档布局与关键交互自动化通过；
- 仍未完成的刷新聚合查询、真实 Server/Worker 联调和 Provider 压测已明确归入后续阶段。

产品负责人明确确认后，才把 `project-lifecycle-stage-gates.md` 的当前主阶段改为阶段 7。
