# 阶段 9 E2E 验收

状态：内部 Demo 门禁通过，等待产品负责人确认是否进入阶段 10  
日期：2026-08-07

## 验收结论

阶段 9 约定范围已完成：通用 Agent 基础流程、小说转剧本、非小说转剧本、视频参考创作三条真实 Provider 链路以及关键前后端回归均通过。机器可读总证据见 `../acceptance/evidence/stage9-overall-regression.json`。

本结论只针对内部 Demo，不代表正式生产版本已经具备上线条件。

## 三条真实链路

| Capability | Run | 耗时 | 结果 | 主要产物 |
|---|---|---:|---|---|
| 非小说转剧本 | `run_8db56b02ddd712cb3faae7c2cdec4ed2` | 13 分 36 秒 | 完成 | 10 类 Artifact、1 个 Candidate |
| 小说转剧本 | `run_b4b499b50fb5833030bd9cace357444c` | 16 分 16 秒 | 完成 | 9 类 Artifact、1 个 Candidate |
| 视频参考创作 | `run_c333a34019ae5c59cb4e19783a974a2c` | 26 分 33 秒 | 完成 | 13 类 Artifact、1 个 Candidate |

视频链使用 60.53 秒真实样本。`build_story_seed` 首次因 Prompt 与严格 Schema 冲突失败，修正专用 Prompt 后在同一 Run 内重试并完成，验证了失败定位和恢复路径。

## 整体回归

- Capability 合同：3 个 Manifest、34 个步骤、19 个 Response Adapter，通过。
- 阶段 2 主矩阵：96/96，通过；覆盖 10 个 Fixture、17 个领域。
- 阶段 8 套件计划：96/96 唯一归类，5 个套件、3 类环境，通过。
- 通用 Agent 与核心 API：7/7，通过；覆盖聊天、Registry、作品创建/读取/重命名和删除影响预览。
- 前端：Vitest 16/16、生产构建、真实后端浏览器 Smoke，全部通过。
- 过程产物工作台：17 类 Artifact 已注册；真实视频作品 13/13 产物完成可读性、流程顺序、状态和编辑入口回归，证据见 `../acceptance/evidence/stage9-artifact-workspace.json`。
- 后端：13/13 含测试包通过。Windows WDAC 阻止本机新生成测试 EXE，因此采用 Linux 交叉编译测试二进制并在受信服务器执行。

真实三链执行覆盖了审批、暂停、恢复、失败步骤重试、Artifact 聚合、Candidate 和质量审核。编辑、版本、影响分析等细分行为由 Runtime、HTTP API 自动化和 96 项矩阵回归覆盖，不冒充为逐项人工操作。

## 本轮修复

1. Runtime 通过声明的依赖读取不可变配置快照，修正质量审核、剧本聚合和体量判断的配置读取。
2. 三份 Manifest 的 `aggregate_scripts` 显式声明 `creation` 配置依赖。
3. 视频分析步骤同时读取 `reference_scripts` 与逐集 `video_script_unit`，避免只看到索引而生成占位分析。
4. 视频链增加专用 Story Seed Prompt，使输出严格匹配 Artifact Schema。
5. 更新过期的依赖血缘断言与迁移测试 Fixture。
6. 将中间过程产物从 JSON 调试视图升级为结构化文档工作台，补齐中文字段、流程排序、状态、结构化编辑和历史版本预览；交互基线见 `artifact-workspace-interaction-baseline.md`。

## 未关闭门禁

以下事项不属于本次内部 Demo 通过范围，仍保持未完成：

- 40 集真实视频的负载、并发、长时 SSE 和故障恢复；
- 完整多集参考剧的人工内容抽查；
- 正式发布包、迁移 Dry Run、灰度与回滚；
- 生产监控、告警和多进程高可用。

因此，阶段 9 可以按内部 Demo 标准收口，但进入阶段 10 仍需产品负责人明确确认。
