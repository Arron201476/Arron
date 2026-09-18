# 阶段 2 验收测试矩阵

更新时间：2026-07-29

状态：阶段 2 设计合同，不代表业务代码已实现或真实样本已验收。

机器可读真源：

- `../acceptance/stage-2-matrix.json`
- `../acceptance/stage-2-matrix.schema.json`
- `../acceptance/validate-stage-2-matrix.ps1`

重复执行设计门禁：

```powershell
./acceptance/validate-stage-2-matrix.ps1
```

## 1. 目的

本文档把阶段 1 成功标准和阶段 2 的全部架构、数据、API、前端及迁移合同转换为可执行验收项，解决三个问题：

1. 设计阶段能否证明没有关键协议遗漏；
2. 实现阶段能否用确定性测试证明状态、版本、依赖和恢复正确；
3. 发布阶段能否用真实小说、非小说和几十集视频证明产品流程与内容质量可用。

本矩阵不把“文档已写完”当成“能力已运行”，也不把“一次模型输出看起来不错”当成底层流程正确。

## 2. 测试对象边界

### 2.1 General Agent Shell

即使 Registry 中没有 Domain Capability，Shell 仍必须成立，至少支持：

- 普通对话与澄清；
- Project 管理；
- 通用文本、文件和图片输入；
- Asset 查看；
- 能力发现与能力缺口说明；
- 不创建无法解释的 Domain Run 或 Artifact。

### 2.2 用户可见 Skills

第一版只验收三个用户 Skills：

- `novel_to_script`
- `non_novel_to_script`
- `video_reference_creation`

`script_analysis` 是视频 Skill 的内部步骤，不是第四个 Skill。视频 Skill 后半段复用非小说共享 Workflow，但 Run 的 Capability ID 不改变。

`review_script_set` 是三个 Skill 共享的内部质量审核步骤，也不是第四个 Skill。Manifest、Schema、Compiler 和 QualityReview Runtime/API 已在阶段 3 第 11 步 11A 实现；真实审核子图和自动返工复审在 11F 验收。

### 2.3 通用生产 Runtime

重点验证：

- Project、Run、Step、Task、Attempt；
- Artifact、Artifact Version、Dependency；
- Approval、Candidate、Final Selection；
- QualityReview、QualityOverride；
- Asset、Asset Set、Retention Job；
- Command、Event、Snapshot、Cursor；
- Pause、Resume、Retry、Cancel、Restart；
- 幂等、乐观并发、迟到结果和数据迁移。

## 3. 三道门禁

| 门禁 | 通过条件 | 本阶段是否执行 |
|---|---|---|
| `stage2_design` | 所有合同被用例追踪；机器矩阵可解析、ID 唯一、引用存在、覆盖无缺口；没有 P0 设计矛盾 | 是 |
| `implementation` | P0 自动化用例全部通过；三条链使用 Mock/确定性 Provider 跑通；状态、并发、重启和恢复无阻断缺陷 | 否，进入编码后执行 |
| `release` | 真实固定样本完成业务抽查；几十集视频压力、迁移 Dry Run、桌面浏览器 QA、部署与恢复全部通过 | 否，发布前执行 |

规则：

- `required_by` 表示最迟必须通过的门禁；
- P0 失败阻止对应门禁；
- P1 可在明确风险、负责人和修复期限后有条件放行，但不能破坏数据正确性；
- P2 是增强项，不得被伪装成第一版 P0；
- 同一用例在更早门禁通过后，影响代码发生变化时仍需回归。

## 4. 测试分层

| 层级 | 验证重点 | Provider |
|---|---|---|
| Contract | JSON Schema、Manifest、API/Event、路径和跨文档不变量 | 不调用 |
| Unit | 状态转换、路由、Scope、依赖计算、Context 预算、排序 | Stub |
| Component | Skill Menu、编辑器、审批条、视频表格、冲突界面 | Mock API |
| Integration | Runtime + DB + API + SSE + Blob + Adapter | Fake/Mock Provider |
| E2E | 浏览器内完整用户路径、刷新、断线和重启 | 先 Mock，发布门禁用真实 Provider |
| Manual | 内容质量、视觉、可访问性、迁移 Go/No-Go | 真实环境 |

确定性状态测试与模型质量测试必须分离。模型超时、结构错误、迟到结果和重复回调由可控 Fixture 注入，不能依赖真实服务偶然出错。

## 5. Fixture 合同

| Fixture | 所有者 | 可用时间 | 用途 |
|---|---|---|---|
| `fixture_zero_capability` | 开发 | 实现期生成 | 空 Registry 下的 Agent Shell |
| `fixture_contract_assets` | 开发 | 当前已有 | 文档、Schema、Manifest、Prompt 和 Rules 静态检查 |
| `fixture_runtime_synthetic` | 开发 | 实现期生成 | 两作品、状态机、版本、审批、事件、并发和重启 |
| `fixture_asset_edges` | 开发 | 实现期生成 | 格式伪装、超限、缺集、重复、无集号、过期和删除失败 |
| `fixture_novel_regression` | 产品 | 验收时提供 | 一本真实小说反复回归 |
| `fixture_non_novel_synthetic_v1` | 开发 | 实现期编写 | 同一原创故事的完整大纲、简略梗概和零散设定 |
| `fixture_video_series_real` | 产品 | 验收时提供 | 一部几十集漫剧，至少分两批上传 |
| `fixture_legacy_snapshot` | 开发 | 迁移实现期生成 | 旧系统一致性脱敏快照和对账基线 |
| `fixture_provider_failures` | 开发 | 实现期生成 | 超时、限流、结构错误、迟到和重复响应 |

真实小说和真实视频不得提交到源码仓库。Fixture 清单只记录受控位置、哈希、题材、集数、时长和验收授权。

## 6. 完整用例矩阵

下表给出所有 96 个用例的最小可读索引。每项的 Fixture、合同引用和完整断言以机器矩阵为准。

### 6.1 设计与 Agent Shell

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| DES-001 | P0 | 设计 | 每份阶段 2 合同至少被一项测试引用 |
| DES-002 | P0 | 设计 | JSON、ID、枚举、Fixture 和文档引用有效 |
| DES-003 | P0 | 设计 | 设计完成、实现通过和发布通过不混报 |
| DES-004 | P1 | 设计 | 范围外能力不进入第一版强制实现 |
| DES-005 | P0 | 设计 | Manifest、Step 图、引用、Adapter 与权威文档语义一致 |
| SHELL-001 | P0 | 实现 | 零 Capability 可普通对话且不创建 Domain Run |
| SHELL-002 | P0 | 实现 | Project 与 Asset 通用操作不依赖 Skill |
| SHELL-003 | P0 | 实现 | 能力缺失时明确说明，不自由执行 |
| SHELL-004 | P1 | 发布 | 图片按通用意图处理，不确定时追问 |
| SHELL-005 | P0 | 实现 | 显式 Skill、自动路由和模糊追问均正确 |
| SHELL-006 | P0 | 实现 | Skill Invocation 与 Business Run 分离，只有确认后的有状态工作流创建 Run |

### 6.2 Registry 与 Runtime

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| REG-001 | P0 | 实现 | 三个 Manifest 合法，ID 唯一 |
| REG-002 | P0 | 实现 | Prompt、Rules、Schema 引用可解析 |
| REG-003 | P0 | 实现 | Rules 只由具体 Step 按需加载 |
| REG-004 | P0 | 实现 | Public Manifest 不下发可执行前端内容 |
| REG-005 | P0 | 实现 | Run 固定 Definition Version，旧 Run 可恢复 |
| RUN-001 | P0 | 实现 | 两作品数据隔离且可并行 |
| RUN-002 | P0 | 实现 | 同作品最多一个活动写 Run |
| RUN-003 | P0 | 实现 | 状态机拒绝非法转换 |
| RUN-004 | P0 | 实现 | Pause/Resume/Retry 保持原输入快照 |
| RUN-005 | P0 | 实现 | Cancel 保留成功产物且不可恢复 |
| RUN-006 | P0 | 实现 | 重启恢复且不重复成功任务 |
| RUN-007 | P0 | 实现 | 迟到或重复 Provider 结果不污染状态 |

### 6.3 Artifact、Dependency 与 Context

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| ART-001 | P0 | 实现 | 编辑、AI 修改和重生成都创建不可变新版本 |
| ART-002 | P0 | 实现 | 旧 base version 返回 409，不覆盖新版本 |
| ART-003 | P0 | 实现 | 依赖绑定精确 Version，图不跨作品且无环 |
| ART-004 | P0 | 实现 | 上游修改先生成影响预览 |
| ART-005 | P0 | 实现 | 单集修改不误伤无关集，连续性传播可停止 |
| ART-006 | P0 | 实现 | `scripts` 是精确引用聚合，不是编辑真源 |
| ART-007 | P0 | 实现 | 多 Candidate 共存且只有一个 Active Final |
| CTX-001 | P0 | 实现 | Routing/Capability/Execution Context 分层 |
| CTX-002 | P0 | 实现 | Context、Summary、Cache 强制 Project 隔离 |
| CTX-003 | P0 | 实现 | Selection Snapshot 不可变，空删除合法 |
| CTX-004 | P0 | 实现 | 目标优先级确定，歧义时追问 |
| CTX-005 | P0 | 实现 | 预算裁剪不丢必需输入，裁剪可追踪 |
| CTX-006 | P0 | 实现 | Active Run、Viewed Run 与历史 Run 上下文隔离 |

### 6.4 Asset、API 与 SSE

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| ASSET-001 | P0 | 实现 | 白名单、真实类型和配置化上限有效 |
| ASSET-002 | P0 | 实现 | 上传或点击 Skill 不自动启动 Run |
| ASSET-003 | P0 | 实现 | Blob 与 SQLite 业务状态分离 |
| ASSET-004 | P0 | 实现 | 原视频与 Provider 处理副本分离 |
| ASSET-005 | P0 | 实现 | 视频七天到期，派生产物和 Tombstone 保留 |
| ASSET-006 | P0 | 实现 | 已引用文件删除前预览影响并确认 |
| ASSET-007 | P1 | 实现 | 中断上传和删除任务可重入 |
| API-001 | P0 | 实现 | REST v1 Envelope 和安全错误统一 |
| API-002 | P0 | 实现 | Command 幂等键与请求哈希正确 |
| API-003 | P0 | 实现 | Snapshot 是状态真源 |
| API-004 | P0 | 实现 | Available Actions 由服务端状态决定 |
| API-005 | P1 | 发布 | 旧 API 只做新真源兼容投影 |
| SSE-001 | P0 | 实现 | Event 只按 Seq 排序，不按时间戳 |
| SSE-002 | P0 | 实现 | 断线、重复、Gap 和过期 Cursor 可恢复 |
| SSE-003 | P0 | 实现 | 状态写入与 Outbox 在同一业务事务 |
| SSE-004 | P1 | 发布 | 慢客户端不阻塞 Worker，终态不丢 |

### 6.5 前端

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| UI-001 | P0 | 实现 | 作品入口页和统一三栏工作台可用 |
| UI-002 | P0 | 实现 | Skill 菜单来自 Manifest，点击只插入引用 |
| UI-003 | P0 | 实现 | 运行中 Composer 保持输入；只读问答、排队修改、新 Skill 冲突正确分流，写操作保持单写者 |
| UI-004 | P0 | 实现 | 子项编辑、草稿、空删除和冲突恢复正确 |
| UI-005 | P0 | 实现 | 主要步骤逐步确认，批量步骤统一确认 |
| UI-006 | P1 | 发布 | 四个桌面视口和可访问性门禁通过 |
| UI-007 | P0 | 实现 | 多 Run 作品保持一条连续主对话，切换记录不替换对话 |

### 6.6 小说与非小说

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| NOVEL-001 | P0 | 发布 | 小说完整主链、逐步编辑确认和 Candidate |
| NOVEL-002 | P0 | 发布 | 体量不足先确认扩写且不新增关键主线 |
| NOVEL-003 | P0 | 发布 | 超过五集的拆集、批次失败与恢复 |
| NOVEL-004 | P1 | 发布 | 旧 Prompt/Rules 行为兼容且旧字段不外溢 |
| NON-001 | P0 | 发布 | 非小说完整主链、逐步编辑确认和 Candidate |
| NON-002 | P0 | 发布 | 三种材料完整度和体量判断正确 |
| NON-003 | P0 | 实现 | 用户事实与模型推断可区分 |
| NON-004 | P0 | 实现 | 多集任务、失败重试和引用聚合正确 |

### 6.7 视频参考创作

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| VIDEO-001 | P0 | 发布 | 一部剧可分两批以上上传且不自动结束 |
| VIDEO-002 | P0 | 发布 | 自然排序、人工排序、重复和缺集均可处理 |
| VIDEO-003 | P0 | 实现 | Seal 后单视频单 Task，绑定精确 Run 输入和 Asset Snapshot |
| VIDEO-004 | P0 | 实现 | 部分失败只重试失败集 |
| VIDEO-005 | P0 | 实现 | 用户明确完成后才 Seal，整剧分析只读 Sealed |
| VIDEO-006 | P0 | 实现 | 缺集继续需显式确认并标记完整度 |
| VIDEO-007 | P0 | 发布 | 单集剧本可编辑、重生成，批次统一确认 |
| VIDEO-008 | P0 | 发布 | 整剧分析基于已确认聚合并引用证据 |
| VIDEO-009 | P0 | 发布 | Options、Brief 可改且 Brief 必须先确认 |
| VIDEO-010 | P0 | 实现 | 复用非小说共享后续链但不切换 Capability |

### 6.8 剧本质量审核

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| QREV-001 | P0 | 设计 | 审核是内部共享 Step，当前未实施状态如实标记 |
| QREV-002 | P0 | 实现 | 整个剧本统一确认后自动固定快照并审核 |
| QREV-003 | P0 | 实现 | 通过后自动聚合、创建 Candidate，不自动 Final |
| QREV-004 | P0 | 实现 | action_required 操作矩阵、blocker 和风险覆盖正确 |
| QREV-005 | P0 | 实现 | Guard 只映射一个最上游返工 Step |
| QREV-006 | P0 | 实现 | 修改后旧 Review 失效并按影响范围自动重审 |
| QREV-007 | P0 | 实现 | API、Event、幂等、批次失败和重启恢复正确 |
| QREV-008 | P0 | 实现 | 前端五态、只读、问题卡、自动 Candidate 和失败恢复正确 |

### 6.9 迁移、安全和非功能

| ID | P | 最迟门禁 | 核心断言 |
|---|---|---|---|
| MIG-001 | P0 | 发布 | 一致性快照且不原地修改旧库 |
| MIG-002 | P0 | 发布 | 重复导入幂等，ID 映射唯一 |
| MIG-003 | P0 | 发布 | 旧字段规范化，失败 Payload 降级只读 |
| MIG-004 | P0 | 发布 | 未完成旧 Run 创建 Continuation，不续旧 cursor |
| MIG-005 | P0 | 发布 | 数量、顺序、关系、哈希和状态对账 |
| MIG-006 | P0 | 发布 | 不自动推断历史 Final Selection |
| MIG-007 | P0 | 发布 | Cutover 前后回滚边界正确 |
| SEC-001 | P0 | 实现 | 凭据仅服务端持有，日志与指标脱敏 |
| SEC-002 | P0 | 实现 | 防路径穿越，不保存客户端绝对路径 |
| SEC-003 | P1 | 发布 | 共享工作区边界如实呈现 |
| SEC-004 | P0 | 发布 | 来源标记、禁止照搬 Rule 和用户确认有效 |
| NFR-001 | P0 | 发布 | 几十集并发、SSE、恢复和工作台压力通过 |
| NFR-002 | P0 | 发布 | TXT/DOCX 与工作台剧本同源一致 |
| NFR-003 | P0 | 发布 | 人工内容抽查不替代确定性流程门禁 |

## 7. 自动化实施要求

### 7.1 必须自动化

以下内容不能只靠人工点击：

- JSON Schema、Manifest 和引用解析；
- 状态机合法性；
- Project 隔离；
- 幂等、乐观并发和数据库互斥；
- Artifact Version 和 Dependency；
- Context Target、Selection、Budget；
- Asset 过期和删除 Worker；
- Snapshot、SSE Seq、Replay、Gap、Outbox；
- 批次排序、失败项重试和聚合；
- QualityReview 触发、Gate、唯一返工、Override 和自动重审；
- 迁移幂等、ID、数量、哈希和关系对账；
- 日志脱敏和路径穿越。

### 7.2 Hybrid

模型相关用例分两段：

1. 使用 Mock Provider 验证输入快照、调用次数、Schema、状态和错误恢复；
2. 使用真实 Provider 验证内容可用性、字幕/对白还原、事实一致性和改编效果。

真实 Provider 结果变化不能让确定性流程测试变成随机失败。内容阈值应由人工评分表或独立评审结果记录。

### 7.3 浏览器 E2E

Playwright 最少覆盖：

- 新建、打开、切换、重命名、删除作品；
- Skill 菜单、自动路由和模糊追问；
- 小说、非小说主要确认点；
- 视频多批上传、排序、缺集继续、失败重试；
- 单集编辑、AI Selection 修改、版本冲突；
- Pause、Resume、Cancel；
- SSE 断线、Gap 和刷新恢复；
- Candidate 首选与改选 Final；
- 剧本统一确认后自动审核、问题处理、风险覆盖和返工重审；
- TXT/DOCX 导出。

## 8. 内容质量验收

### 8.1 共同评审顺序

1. 输出结构和必填字段；
2. 上下游事实一致；
3. 集数、人物、关系和事件无明显自相矛盾；
4. 剧本格式、场次、对白和来源引用；
5. 用户已确认要求是否执行；
6. 自动质量审核是否绑定当前精确剧本版本；
7. 内容表现和主观质量。

### 8.2 视频高还原

按以下顺序评审：

```text
剧情与人物关系
-> 反转和尾钩
-> 主要对白
-> 全部逐句对白
-> 精确时间码
```

第一版接受高还原初稿边界，不预先增加独立 OCR/ASR。真实样本必须记录缺失类型，例如：

- 画面信息遗漏；
- 字幕遗漏；
- 音频台词遗漏；
- 说话人归属错误；
- 时间码偏差；
- 无依据新增。

只有真实样本证明当前管线无法满足使用，才重新评估 OCR/ASR，不在验收报告中声称已经“逐字无遗漏”。

### 8.3 改编内容

必须检查：

- `script_analysis` 的事实层有来源证据；
- 判断层与建议层合理但不要求唯一答案；
- `adaptation_options` 不直接复制原剧情表达；
- `adaptation_brief` 已由用户确认；
- 新剧本遵守 Brief、来源标记和禁止照搬 Rule；
- 第一版不声称具备自动查重或版权判定。

## 9. 缺陷与报告规则

每次执行记录：

- Build/Commit；
- 环境和数据库 Schema Version；
- Capability Definition Version；
- Provider/Model Variant；
- Fixture ID 与内容哈希；
- Case ID；
- 结果：`pass`、`fail`、`blocked`、`not_run`；
- 实际结果、证据和缺陷 ID；
- 是否影响 P0 门禁。

`blocked` 不是 `pass`。缺少真实视频、正式网关或迁移快照时，对应用例保持 `not_run/blocked`，不得用文档评审代替。

## 10. 合同追踪

| 阶段 2 合同 | 主要用例 |
|---|---|
| `stage-2-architecture-baseline.md` | DES-001、DES-003、DES-004 |
| `capability-registry-contract.md` | SHELL-001~003、REG-001~005、VIDEO-010 |
| `runtime-domain-model.md` | RUN-001~007、ART-001、ART-007 |
| `artifact-dependency-contract.md` | ART-003~006 |
| `capability-context-pack-contract.md` | CTX-001~005、VIDEO-003 |
| `asset-and-retention-contract.md` | ASSET-001~007、VIDEO-001~007 |
| `api-event-contract.md` | API-001~005、SSE-001~004 |
| `frontend-capability-ui-contract.md` | UI-001~006、VIDEO-002、VIDEO-007~009 |
| `capability-workflow-schema-contract.md` | REG-002、ART-006、NOVEL-004、VIDEO-010 |
| `content-semantics-and-quality-review-contract.md` | QREV-001~008、NON-003 |
| `data-migration-compatibility-plan.md` | MIG-001~007 |
| `schemas/v1/*.schema.json` | REG-001~002、ART-006、NON-003、VIDEO-006 |
| `capabilities/v1/*.json` | REG-001~005、NOVEL-001~003、NON-001/004、VIDEO-003~010 |
| 视频 Rules/Prompts | VIDEO-008~009、SEC-004 |

## 11. 阶段 2 设计收口检查

阶段 2 可以进入总体一致性评审，必须满足：

- [x] 三道门禁已分离；
- [x] Fixture 所有者和可用时间已定义；
- [x] 96 个用例覆盖 Shell、三个 Skills、内容语义、质量审核、Runtime、前后端、迁移和安全；
- [x] 每项用例有优先级、层级、自动化方式、最迟门禁和合同来源；
- [x] 所有阶段 2 合同进入追踪表；
- [x] 真实模型内容验收与确定性流程验收分离；
- [x] 没有把未运行的实现/发布用例标记为通过；
- [x] 机器矩阵通过结构和跨引用校验；
- [x] 阶段 2 全体合同完成最终一致性评审；
- [ ] 产品负责人确认阶段 2 收口并进入实现。

最后一项确认前，不自动进入业务代码实现。
