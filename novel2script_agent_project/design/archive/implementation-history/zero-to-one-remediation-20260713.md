# 0-1 全量修复与回归记录

日期：2026-07-13  
适用项目：`novel2script_agent_project`  
原始问题清单：`zero-to-one-project-audit-20260713.md`

## 结论

原审计的 54 项问题已经完成一轮逐项处置，其中 2 项在本轮开始前已有核心修复，本轮对其补齐回归；用户要求继续处理的 52 项均已实现或完成工程化收口。当前没有已知 P0/P1 阻断问题。

“已关闭”表示对应的用户风险已有实现和自动化或 live 证据，不表示代码今后无需继续演进。覆盖率和大文件拆分仍作为持续质量指标，不再构成当前版本的正确性阻断项。

## P0 数据正确性

| ID | 状态 | 修复结果 | 回归证据 |
|---|---|---|---|
| P0-1 | 已关闭 | Project、Message、File 改用随机稳定 ID；runtime ID 从持久化计数恢复。 | workspace store ID/API 测试；服务重启恢复 8 个作品。 |
| P0-2 | 已关闭 | Project、Message、File 写入 `workspace.db`；Run、Event、Artifact、Approval 写入 `runtime.db`。 | SQLite roundtrip、迁移测试；新聊天重启后仍存在。 |
| P0-3 | 已关闭 | 启动时恢复 run，并将中断中的 job 收敛到可恢复状态。 | runtime restart/reconcile 测试。 |
| P0-4 | 已关闭 | `scripts` 聚合由最新 `script_unit` 生成，人工保存和 Agent 修改共用版本链。 | script aggregate、span patch、人工保存测试；live 编辑器验证。 |
| P0-5 | 已关闭 | 编辑缓存键包含 artifact/version；后台刷新不覆盖 dirty 编辑，409 时加载最新版本。 | ArtifactWorkspace/ScriptWorkbench 测试；live 后台更新保持当前视图。 |
| P0-6 | 已关闭 | PATCH 强制 `base_version`，锁内比较，冲突返回结构化 409 和最新 artifact。 | API conflict 测试。 |
| P0-7 | 已关闭 | 主控上下文只保留有限历史、摘要、选区和窗口；内容 worker 按集和 source offset 取上下文。 | 百万字输入预算测试；context assembler 测试。 |
| P0-8 | 已关闭 | trace 默认裁剪/脱敏/轮转；运行库不保存完整 prompt trace；上传内容留在本地 SQLite。 | LLM trace 测试、配置检查。 |

## P1 主流程与接口

| ID | 状态 | 修复结果 | 回归证据 |
|---|---|---|---|
| P1-1 | 已关闭 | 正式剧本编辑器切换为 Lexical，自定义 scene/line 稳定节点。 | 生产编辑器 gate；浏览器真实剧本 DOM。 |
| P1-2 | 已关闭 | editor state、结构化 scenes 和格式 mark 同步保存。 | ScriptWorkbench 保存/还原测试与 build。 |
| P1-3 | 已关闭 | 编辑 dirty 状态、离开确认、保存/放弃/留在当前页已实现。 | 组件测试；live 撤销后保存按钮恢复禁用。 |
| P1-4 | 已关闭 | 选区携带 artifact/episode/scene/line、field path 和上下文 hash。 | stable ID gate、revision context 测试。 |
| P1-5 | 已关闭 | 数组项按稳定 ID 编辑，支持增删、排序、对象和基础类型，不用数组下标跨版本定位。 | artifact 编辑矩阵与 schema 测试。 |
| P1-6 | 已关闭 | 9 类实际 artifact 均有可执行 schema、枚举、稳定 ID 和嵌套字段校验。 | data-driven schema valid/missing/type/enum/duplicate 测试。 |
| P1-7 | 已关闭 | 持久化错误保存在 runtime 并向 API 暴露，写失败不再静默继续。 | persistence error 测试。 |
| P1-8 | 已关闭 | approval resolve 失败返回 409，前端仅成功后移除确认卡。 | runtime decision 测试、结构化错误路径。 |
| P1-9 | 已关闭 | 业务错误按 400/404/409/413/500 返回统一错误对象。 | server API 测试。 |
| P1-10 | 已关闭 | 替换素材只更新输入版本，回复由实际动作生成，不再宣称已删除下游。 | source replacement 测试。 |
| P1-11 | 已关闭 | run job 使用 cancel context；暂停保留已完成 task，恢复从 cursor 继续。 | pause/resume 测试。 |
| P1-12 | 已关闭 | Claude 主控决策是意图权威；服务端 guard 只执行产品安全约束，不替换正常回复。 | 明确不生成、闲聊、明确生成 live/API 测试。 |
| P1-13 | 已关闭 | source mode 按显式选择、附件证据、模型决策的固定优先级收敛。 | source mode 测试。 |
| P1-14 | 已关闭 | 二次刷新失败显示同步提示，可手动重试；不静默吞掉。 | API client/工作区错误态实现检查。 |
| P1-15 | 已关闭 | 新增作品中心；每个作品拥有独立项目、聊天、文件和 run 工作区。 | 重启后 8 个作品可选择并恢复。 |
| P1-16 | 已关闭 | 前端使用连续 SSE，断线后按 last event 恢复并降级轮询。 | events 单测与 live run 事件恢复。 |
| P1-17 | 已关闭 | HTTP 增加超时、16MB 请求上限、本地 CORS、结构化错误和 request ID。 | CORS、413、错误结构测试。 |
| P1-18 | 已关闭 | message/upload 不再隐式创建未知 project。 | unknown project API 测试。 |
| P1-19 | 已关闭 | 删除 legacy start/continue 分支，正式路径统一走 async runtime。 | 全包编译、runtime 测试。 |
| P1-20 | 已关闭 | 默认 runtime 为 Eino 0.9.12 图执行器；direct worker 仅保留开发兼容。 | health `runtime=eino`、Eino worker 测试、live 模型链。 |

## P2 体验与工程质量

| ID | 状态 | 修复结果 | 回归证据 |
|---|---|---|---|
| P2-1 | 已关闭 | 三栏使用固定 grid 列位；桌面面板可独立关闭和恢复；窄屏改为目录/内容/Agent 分区切换。 | 1920 五种开合组合、1024 三分区、390 手机回归。 |
| P2-2 | 已关闭 | 长产物增加跳转条、分组、折叠、搜索、分集目录和版本比较。 | ArtifactWorkspace 测试与 live 阅读。 |
| P2-3 | 已关闭 | 编辑时按 section 折叠，数组项独立编辑，避免单张无限长表单。 | 组件视觉回归。 |
| P2-4 | 已关闭 | 字段 manifest 提供控件类型和中文 token fallback；未知字段显示原 key 且不丢字段。 | manifest 单测覆盖；真实 artifact 全字段渲染。 |
| P2-5 | 已关闭 | 根应用和关键区域加入 ErrorBoundary 与恢复入口。 | 前端组件测试/build。 |
| P2-6 | 已关闭 | tab、dialog、pressed、label、toolbar、status 等语义补齐。 | DOM snapshot 与角色定位回归。 |
| P2-7 | 已关闭 | Lexical 工作台 lazy load；纯业务 helper 从 App 抽离；React/Lexical 独立拆包。 | 构建最大包约 211KB；helper 单测。 |
| P2-8 | 已关闭 | runtime SQLite、workspace store、run/SSE/artifact handlers 已从入口拆分；旧执行路径删除。 | Go 全包测试；server/runtime 覆盖率。 |
| P2-9 | 已关闭 | API base URL 可配置；请求支持 timeout、AbortSignal、request ID 和有限重试。 | API client 实现与构建。 |
| P2-10 | 已关闭 | LLM 对 429/5xx/网络错误做退避重试，4xx 不重试。 | 503 三次、400 一次单测。 |
| P2-11 | 已关闭 | 建立 Vitest + Testing Library，覆盖事件、artifact、生产编辑器、Agent 摘要和工作区 helper。 | 5 files / 7 tests passed。 |
| P2-12 | 已关闭 | gate 直接运行生产 `ScriptWorkbench.test.tsx`，旧 Tiptap/CodeMirror spike gate 删除。 | `npm run gate:editor` passed。 |
| P2-13 | 已关闭 | 标准脚本和 CI 生成 Go coverage profile 并输出函数覆盖率。 | Go 总语句 56.0%，runtime 69.1%。 |
| P2-14 | 已关闭 | runtime 状态改为 SQLite 多表事务存储，替代持续膨胀的单 JSON。 | SQLite migration/roundtrip。 |
| P2-15 | 已关闭 | 事件 UI 按结构化 type/step/payload 映射，不依赖英文 message。 | events/label 单测。 |
| P2-16 | 已关闭 | 恢复和完成回复包含作品名、run 状态、产物数、版本和待办。 | helper 单测与恢复 live。 |
| P2-17 | 已关闭 | generation config 只以 run metadata 为权威，artifact 保存版本引用。 | runtime config authority 测试。 |
| P2-18 | 已关闭 | 所有主要 artifact 覆盖 field/entity/section/full regenerate；剧本覆盖 span/scene/episode。 | runtime revision matrix、worker schema matrix、live field patch。 |

## P3 项目治理

| ID | 状态 | 修复结果 | 回归证据 |
|---|---|---|---|
| P3-1 | 已关闭 | README 重写为当前 React + Go + Eino 项目说明。 | README 与脚本路径检查。 |
| P3-2 | 已关闭 | Python 原型移到 `design/archive/python-prototype`。 | 根目录扫描。 |
| P3-3 | 已关闭 | 当前项目初始化独立 Git 仓库。 | `git status` 可用。 |
| P3-4 | 已关闭 | cache、coverage、dist、run、exe、日志加入 `.gitignore`，非运行旧二进制已清理。 | 工作区生成物扫描。 |
| P3-5 | 已关闭 | Tiptap、CodeMirror 和 spike 源码/依赖删除，只保留 Lexical。 | package/build 检查。 |
| P3-6 | 已关闭 | `design/INDEX.md` 标记 Current 与 Archive/Reference，本记录覆盖旧审计状态。 | 统一 preview 重建。 |
| P3-7 | 已关闭 | 生产命名改为 `cmd/server`、`agent/runtime`、`run_server_hidden.ps1`。 | 路径扫描与 Go build。 |
| P3-8 | 已关闭 | 增加 `dev.ps1`、`test.ps1`、`build.ps1` 和 Windows CI。 | 本地逐项执行；workflow 检查。 |

## 全量回归基线

- 前端：5 个测试文件、7 项测试通过；生产编辑器 gate 通过；生产 build 通过。
- 前端覆盖率：statements 35.05%，branches 29.74%，functions 39.40%，lines 38.15%。
- Go：所有 package 测试通过；总 statements 56.0%，runtime 69.1%，worker 65.5%，mainagent 59.9%，LLM 58.5%。
- Live：闲聊不启动 run；明确生成先出配置；小说与非小说历史 run 可恢复；局部 field patch 只改目标字段；有下游时先询问保留或重生成；选择保留后下游不删除。
- 视觉：1920 桌面五种开合组合、1024 三分区、390 手机入口均通过；无水平溢出；控制台无 error/warn。

## 2026-07-13 展开收起纠偏

上一轮只检查了默认状态的横向溢出，没有覆盖桌面端连续点击开合后的全部组合，因此过早得出了“视觉回归完成”的结论。用户截图暴露后重新排查，确认并修复：

1. 隐藏 grid 子项后，浏览器自动把剩余面板补到前一列，导致内容进入 0px 列或 Agent 占据内容列。现在目录、内容、Agent 固定在第 1、2、3 列。
2. 顶栏和两个面板内部同时存在常驻开关，状态和作用区域不清楚。现在关闭入口只在面板内部；关闭后，左侧目录从左边缘恢复，右侧 Agent 从右边缘恢复，顶栏不再集中堆放恢复按钮。
3. 小于 1100px 时曾隐藏所有顶栏按钮，连作品中心也无法进入。现在保留作品入口，390px 时收为单个作品图标。
4. 普通 Agent 回复会重复挂载整套运行步骤，折叠摘要还会出现两个同名“生成剧本”。现在运行步骤固定在原流程消息；折叠态显示两个不同的最近阶段，默认仅保留一个“查看 N 步”入口。

本次新增 `AgentPanel.test.tsx`，并在真实浏览器中逐项点击全开、关目录、关 Agent、两侧都关、逐项恢复、展开 25 步、收起步骤以及窄屏三分区。

## 2026-07-14 侧栏恢复与产物编辑纠偏

1. 桌面侧栏关闭后保留 40px 边缘恢复条，目录固定在左边缘，Agent 固定在右边缘；两侧同时关闭时中间内容占用剩余空间。面板开合状态仍由浏览器记忆。
2. 小于 1100px 时不显示边缘恢复条，继续使用“目录 / 内容 / Agent”区域切换，避免重复入口。
3. 删除内容区顶部重复的 artifact 横向切换条，作品流程只由左侧目录管理。
4. 查看态和编辑态合并为同一个 `ArtifactPayloadView`：纵向产物大纲、章节 ID、展开状态和阅读顺序保持不变，编辑控件直接出现在原章节中。
5. 小于 900px 时产物大纲收为单个“大纲”按钮，打开纵向浮层；选择章节后自动关闭，不再把全部字段横向铺在正文上方。

回归证据：前端 5 个测试文件、7 项测试通过；1440 桌面验证单侧关闭、双侧关闭和逐项恢复；390 手机验证三分区、大纲浮层、原位编辑和零横向溢出；浏览器控制台无 error/warn。

## 后续阶段

本轮仍属于全局 12 步流程第 9 步“E2E 验证与缺陷修复”。关闭本清单后进入第 10 步“发布准备与可观测性”，重点是正式打包、数据备份/恢复入口、日志查看、升级迁移和发布门禁，不再继续扩大当前功能范围。
