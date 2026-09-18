# Stage 5 T7 阶段 6/7 交接

状态：阶段 5 交接已完成；阶段 6 已进入并完成工程自测，等待产品负责人验收  
版本：v1.0  
日期：2026-08-05

## 1. 交接结论

阶段 5 已冻结第一版产品的技术边界、状态真源、能力协议、前端状态投影和三条主链。后续按 12 阶段执行：

```text
阶段 6：实现桌面 Web 前端
阶段 7：收口后端、API、数据库、Provider 和部署适配
阶段 8：按最终实现编制测试计划
阶段 9：执行浏览器 E2E、真实样本和几十集压力验收
```

阶段 6 和阶段 7 共享 API、Schema、SSE 和 Capability Manifest，不允许各自维护第二套业务步骤或状态判断。

## 2. 冻结输入

### 2.1 产品与交互

- `stage-2-prd-user-stories.md`
- `stage-3-information-architecture.md`
- `stage-4-acceptance-review.md`
- `stage-4-ui-evidence-manifest.md`
- `stage-5-interaction-baseline-audit.md`

Figma 最终设计只使用阶段 4 保留的 12 个页面和正式组件源。正式 `Composer Action` 为 `302:135`，正式 `Agent Composer` 为 `238:117`。历史页面、旧组件和已删除 Frame 不得作为实现输入。

### 2.2 机器与运行合同

- `capabilities/v1/*.json`：能力和步骤声明真源；
- `schemas/v1/*.json`：输入、配置和 Artifact Payload 真源；
- `api-event-contract.md`：API、错误、幂等、SSE 和状态投影；
- `runtime-domain-model.md`：业务状态唯一真源；
- `frontend-capability-ui-contract.md`：前端渲染和交互边界；
- `asset-and-retention-contract.md`：上传、媒体、保留和删除；
- `acceptance/stage-2-matrix.json`：实现与发布验收归属。

## 3. 阶段 6 前端工作包

| 工作包 | 范围 | 完成证据 |
|---|---|---|
| F6-0 工程基线 | 创建正式 `frontend/`，固定框架、路由、数据请求、图标、测试和构建配置 | 本地可启动、构建通过、无旧项目前端运行依赖 |
| F6-1 作品入口 | 新建、打开、重命名、删除作品；状态和更新时间 | 作品列表真实连接 Project API，删除有影响确认 |
| F6-2 通用工作台 | 左侧作品/步骤，中间 Artifact，右侧 Agent Timeline 与底部 Composer | 三栏尺寸、滚动、空态和桌面响应式符合最终设计 |
| F6-3 Agent 与能力入口 | 普通聊天、显式 Skill 菜单、自动路由追问、配置卡和启动确认 | 零 Capability 可聊天；能力引用传 `capability_ref`，不传显示文字 |
| F6-4 Timeline 与审批 | Message、运行事件、错误、ApprovalCard、暂停、恢复、重试和取消 | 确认主操作只在 Timeline ApprovalCard；运行控制不伪装成审批 |
| F6-5 产物与版本 | 来源、故事圣经、分集规划、剧本、分析、Brief、版本历史和修订记录 | Artifact 由类型 Registry 渲染；保存使用版本保护 |
| F6-6 视频批次 | 分批上传、封存、自然排序、人工排序、缺集确认、逐集状态和失败集重试 | 成功集保留；失败集独立重试；未封存前不启动 Run |
| F6-7 候选与最终稿 | 候选对比、唯一最终稿确认、TXT/DOCX 导出 | 历史候选保留；最终选择使用独立确认请求 |
| F6-8 恢复与质量 | SSE 重连、Snapshot 回补、冲突、未保存草稿、键盘和可访问性 | 关键状态浏览器自动化与 1440x900 实图验收通过 |

阶段 6 不实现：新 Capability、后端状态机、Provider 直连、40 集真实压力测试、移动端和登录权限。

## 4. 阶段 6 状态规则

1. 前端只根据 Snapshot、Event、Approval 和 `available_actions` 渲染，不根据聊天文字猜状态。
2. Composer 固定为 Empty、Active、Context、Running、Paused、Error 六态。
3. Run 活跃时禁止普通聊天，但保留允许的运行控制动作。
4. 业务确认只出现在 Agent Timeline 的 ApprovalCard；Artifact 区只负责阅读、编辑和选择上下文。
5. 三个 Skills 共用工作台 Shell，只通过 Capability UI Registry 注册领域视图。
6. 所有按钮必须使用正式组件状态；不得把 Figma 中的静态文字当成交互控件。
7. 请求失败必须显示 Runtime 错误和可用动作，不允许静默重试、静默降级或删除已完成产物。

## 5. 阶段 7 后端工作包

| 工作包 | 范围 | 完成证据 |
|---|---|---|
| B7-0 API 收口 | 对照阶段 6 页面补齐并验证 Query、Command、SSE 和错误 Envelope | 前端不需要读取数据库或拼接内部状态 |
| B7-1 存储与媒体 | 二进制 Asset 存储、处理副本、FFmpeg/FFprobe 受支持运行环境 | 不使用 SQLite Base64 保存几十集视频；原文件不被覆盖 |
| B7-2 Provider 接入 | Control、Content、Video、MediaKit 角色配置、超时、限流和调用追踪 | 凭据仅服务端；豆包 504 有请求级诊断和稳定接口结论 |
| B7-3 任务可靠性 | Worker 部署、租约、失败隔离、失败项重试和关停恢复 | `preserve_success_retry_failed` 在服务进程和持久数据库中通过 |
| B7-4 数据与保留 | 7 天视频删除、Tombstone、备份、迁移和回滚 Dry Run | 删除不影响业务 Artifact；迁移可对账和回滚 |
| B7-5 安全与可观测性 | 配置校验、日志脱敏、指标、Trace、并发和资源保护 | 不记录 Key/视频正文；Provider 错误可定位 |
| B7-6 正式构建 | Server、Content Worker、Video Worker 的启动、关闭和部署配置 | 新项目是唯一运行边界，不依赖旧项目进程或数据目录 |

## 6. 视频网关风险归属

当前真实证据：

- 单集完整链路成功；
- 三集运行中第 1、2 集可成功，第 3 集重复 HTTP 504；
- 1/2/3 Worker 均观察到网关 504；
- Runtime 已验证成功集保留、批次收敛和失败项重试；
- MediaKit 对第 3 集可正常提取 16 段字幕。

因此：

- 技术方案和 T6 批任务语义已验证，不阻塞阶段 6；
- 豆包 504 是 B7-2 的 P0 Provider 接入风险，阻止阶段 7 完成；
- 真实 40 集并发、SSE、恢复、性能和内容抽查属于阶段 9/发布门禁；
- 第一版不增加备用 OCR/视频理解链路，不静默切 Provider；
- 当前部署默认视频 Worker 并发为 1，提高并发必须重新验收。

## 7. 阶段 8/9 保留门禁

阶段 8 必须把以下内容写入最终测试计划：

- 三条主链浏览器 E2E；
- 真实固定小说、非小说和视频样本；
- 40 集至少分两批上传；
- 缺集、人工排序、暂停恢复和失败项重试；
- SSE 断开回补、刷新恢复、迟到结果和幂等；
- 编辑、版本、统一确认、质量审核、候选和最终稿；
- 视频 7 天保留、删除任务和来源 Tombstone；
- Provider 504、超时、限流和结构错误。

阶段 9 必须实际执行以上测试。40 集真实样本未通过时不得进入阶段 10 发布。

## 8. 禁止事项

- 前端复制 Capability 步骤或审批规则；
- 后端根据页面名称分支业务状态；
- 为绕过 504 增加未确认的备用视频链路；
- 把 Eino Checkpoint 当作 Run、Artifact 或 Approval 真源；
- 从旧项目直接运行服务、读取数据库或长期双写；
- 因阶段 6 开发需要而修改已确认 Figma，不先走设计变更评审；
- 把 Mock/单集测试描述为 40 集或发布验收通过。

## 9. T7 完成判定

- 阶段 6 和阶段 7 的输入、工作包、边界和完成证据明确；
- 视频网关风险有唯一阶段归属，不再阻塞技术方案阶段；
- 40 集验收保留在阶段 9/发布门禁；
- 前后端以同一 API、Schema、SSE 和 Manifest 开发；
- 产品负责人确认阶段 5 并同意进入阶段 6。

除最后一项外，T7 交接已完成。
