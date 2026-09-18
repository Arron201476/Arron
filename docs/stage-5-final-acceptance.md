# 阶段 5 技术方案最终验收

状态：已完成，产品负责人已确认进入阶段 6  
版本：v1.0  
日期：2026-08-05

## 1. 结论

阶段 5 的 T0-T7 已完成。技术方案已经足够支撑阶段 6 前端实现与阶段 7 后端收口，不应继续被外部视频网关稳定性阻塞。

阶段 5 通过不代表产品、后端或发布完成：

- 阶段 6 尚未创建正式前端；
- 阶段 7 仍需关闭豆包 504、正式媒体运行环境、部署、迁移和保留任务；
- 阶段 9 仍需执行真实 40 集、浏览器 E2E、SSE、性能和内容抽查；
- 阶段 10 前必须通过所有 `release` 门禁。

## 2. 阶段职责纠偏

机器验收矩阵一直将以下内容标为 `release`：

- 真实固定样本业务抽查；
- 几十集视频并发、SSE 和恢复压力；
- 浏览器 QA；
- 迁移、部署和恢复。

此前 T6 文档误将 40 集真实验收设为阶段 5 完成条件，导致技术方案阶段被公司网关 504 阻塞。现已按 12 阶段职责修正：

```text
阶段 5：冻结技术路径、状态语义、协议和交接
阶段 7：完成正式 Provider 与媒体运行环境
阶段 9：执行真实 40 集和完整 E2E
```

发布门禁没有删除或降低，只是回到正确阶段。

## 3. T0-T7 状态

| 任务 | 状态 | 主要证据 |
|---|---|---|
| T0 合同收口 | 通过 | 最终交互基线、API/Manifest/Runtime 状态对齐 |
| T1 通用 Agent Shell | 通过 | 零 Capability、普通聊天、路由、Guard 和确认启动 |
| T2 Executor Eino | 通过 | Executor Registry、Worker Graph、Schema Repair 和 Trace |
| T3 小说链 | 通过 | 固定小说迁移回归、Artifact/Approval/编辑影响 |
| T4 非小说链 | 通过 | 0-1 固定样本、扩写策略和共享质量审核 |
| T5 通用生产能力 | 通过 | 文件、图片、Asset Set、导出、Candidate/Final、保留与迁移方案 |
| T6 视频参考创作 | 技术方案通过 | 单集成功、真实多集故障、逐集保存、失败隔离和失败项重试 |
| T7 阶段 6/7 交接 | 通过 | `stage-5-t7-stage-6-7-handoff.md` |

## 4. 视频证据与风险

已经验证：

- MediaKit + 豆包是唯一视频转剧本路径；
- 真实单集 154.09 秒到达 `waiting_approval`；
- 三集真实任务中成功集可保存，失败集不冲掉兄弟任务；
- 批任务全部收敛后才失败；
- 用户重试只重置失败项；
- MediaKit 第 3 集可正常得到 16 段字幕；
- 媒体与字幕错误可进入 Runtime 错误协议；
- 当前部署默认视频并发为 1，处理副本默认目标为 8 MiB。

仍未通过：

- 豆包网关对第 3 集重复 HTTP 504；
- 当前机器策略阻止 FFmpeg 执行 7 MiB 对照转码；
- 40 集真实内容、性能、SSE 和浏览器交互。

归属：前两项是阶段 7 P0，40 集是阶段 9/发布 P0。

## 5. 最终验证

2026-08-05：

```text
PASS  Stage 2 acceptance matrix: 96 cases
PASS  Capability contracts: 3 manifests / 34 steps / 14 adapters
PASS  internal/runtime full regression
PASS  internal/worker fixed-path full regression
PASS  internal/mediaprocessor
PASS  internal/capability fixed-path regression
PASS  internal/shell
PASS  internal/modelprovider
PASS  cmd/video-worker
BLOCK internal/httpapi current rerun: Windows Application Control
```

HTTP API 已有 2026-08-03 全量通过证据；本轮未修改 HTTP 包。2026-08-05 重新编译成功，但测试 EXE 被系统策略阻止启动，因此不声称本轮 HTTP 回归通过。

## 6. 文档一致性

- 阶段 5 T0-T7 状态唯一；
- 40 集验收统一归入阶段 9/发布；
- 豆包 504 统一归入阶段 7 Provider 接入；
- Runtime 是业务状态唯一真源；
- Eino 只负责编排；
- 前端只读取 API、SSE、Snapshot、Approval 和 Available Actions；
- Skill 是用户可见能力，Rule 是能力内部规则；
- 旧项目只作迁移参考，不参与新项目运行。

## 7. 产品确认

阶段 5 的技术工作和交接工作已经完成。产品负责人于 2026-08-05 明确确认：

```text
阶段 5 通过，同意进入阶段 6 前端实现。
```

结论：阶段 5 已关闭，阶段 6 已启动。
