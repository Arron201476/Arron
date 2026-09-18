# 接 Eino 前全局自查与回归报告（2026-07-15）

状态：Historical baseline。范围仅限 `novel2script_agent_project`。Eino 接入后的当前结论以 `eino-integration-20260716.md` 为准。

## 结论

当前 native 基线已经达到“可以开始正式接入 Eino”的准入状态：前后端构建、Go 全包测试、`go vet`、前端单元/组件测试、真实双链路、重启恢复、失败 task 恢复、附件按需读取和浏览器视觉巡检均通过。未发现仍会阻断 Eino 接入的明显产品或数据一致性缺陷。

“意图识别准确无误”无法由任何模型系统做绝对保证。本项目采用主流的有限动作合同：控制模型理解开放表达，确定性 guard 校验状态和高影响动作，runtime 执行有限 action，内容 Worker 不参与流程决策。当前测试覆盖了高风险反例和真实路径，剩余模型误判风险被限制为安全回复或追问，不允许直接越过确认点。

## 本轮关闭的问题

### 数据与恢复

1. 生成配置二次确认会丢失首次附件 ID。已改为配置卡持有原 `file_ids` 并随确认请求提交。
2. 后端在没有明确附件时会读取作品全部文件。已删除该兜底，只读取本次附件或 Main Agent 明确返回的 `target_file_ids`。
3. 多个历史附件被模糊引用时可能混入错误文件。现在必须追问；只有一个且用户明确提到附件时才自动定位。
4. 未启动 run 的生成配置卡刷新后消失。现在把恢复字段写入 agent message 的 `decision_context`，前端可重建同一张卡和附件引用。
5. workspace 指向已不存在的 active run 时无法稳定恢复。启动和项目列表恢复会清理失效引用，保留作品、聊天和附件。
6. 恢复/重跑后旧剧本选区可能被重新消费。前后端都把 `resume_run`、`rerun_step` 记为选区消费动作。
7. SSE 快照和实时事件到达顺序不同会导致过程记录乱序。事件现在去重后按时间和稳定 event ID 排序。

### Main Agent 与流程

1. 运行中暂停、用户暂停后恢复、待确认状态继续的 guard 已分开，不能用 approve 绕过用户暂停，也不能用 resume 绕过 approval。
2. 失败重跑只接受 runtime 记录的失败 step，并从失败 task cursor 继续。
3. 明确修改直接执行；目标、范围或修改方向不明确时先追问。Agent 原回复不再被前端替换为固定文案。
4. 局部修改按字段、实体、区块、剧本选区、多行、场景和单集粒度执行；sparse patch 不重写无关内容。
5. 上游修改不会直接删除下游。已有下游先保持可见并标记 stale，用户选择后才保留或重新生成；没有下游时不弹额外配置。
6. `script_context` 使用独立内部 Prompt，不写剧本、不展示确认卡。
7. 控制模型失败不再使用关键词语义兜底。只允许显式配置的备用控制模型；仍失败时安全停止执行。
8. 接入前默认 runtime 为 `native`，`eino` 仅在显式配置时启用薄适配器；该状态已由 `eino-integration-20260716.md` 关闭。

### Artifact、Prompt 与 Skill

1. 所有可执行 artifact 增加统一 Schema 校验；手动保存和模型输出使用同一结构合同。
2. `episode_split`、`series_blueprint`、`episode_cards` 必须服从 run 级目标集数，episode ID 完整覆盖 `1..N`。
3. 产物局部修改和手动保存都会创建递增版本；旧版本 superseded，新版本不会错误回到 v1。
4. Prompt/Skill 路径在服务启动时校验；两条生产链的 Prompt 与关键 Skill 映射增加显式测试。
5. 小说拆集和非小说剧集蓝图 Prompt 已统一为“启动前配置是权威值”，删除“未指定集数时自然决定”的旧逻辑。
6. 剧本首次输出仍按目标字数 60%–150%严格纠偏；纠偏稿允许 55%–165%的小幅格式容差，避免极少量场记或标点让整条 run 失败。

### API 与前端

1. 公开路由集中在 `routes.go`，18 条当前路由有注册矩阵测试；旧 `/api/agent/messages`、直接 start/continue 和旧 file GET 不再公开。
2. 已删除未使用的整链 Worker 接口、旧前端 API 和 `script_suggestions` 脚手架。
3. GET 只重试网络错误和 5xx，不重复请求确定性 404。
4. 输入材料、过程产物、剧本、运行记录共用稳定内容宽度；过程目录固定、内容独立滚动。
5. 浏览器回归确认空作品不出现过程产物，输入材料不暴露生成配置和内部滚动条，剧本 U/S 格式生效，桌面和紧凑视口无横向溢出。

## 自动化证据

- Go：`go test ./...` 全部通过。
- Go 静态检查：`go vet ./...` 通过。
- 前端：10 个测试文件、26 项测试通过。
- TypeScript：`tsc -b` 通过。
- 生产构建：Vite build 和 Go build 通过。仅有 Lexical 第三方 PURE 注释位置警告，不影响产物。
- 浏览器审计：1920×1000、1024×768 通过；结果与截图位于 `artifacts/visual-regression/`。

## 真实链路证据

### 小说链

- run：`run_000807`
- 结果：completed
- active artifacts：source_input、story_bible、episode_split、episode_cards、script_context、script_unit、scripts
- 验证：主要节点逐项确认，script_context 无确认卡，最终只有一个 active script_unit 和一个 run_completed。
- 真实触发过一次剧本长度失败，按失败 task cursor 重试后完成；此前产物没有丢失或重复。

### 非小说链

- run：`run_000859`
- 结果：completed
- active artifacts：source_input、material_bank、story_seed、series_blueprint、episode_cards、script_context、script_unit、scripts
- 验证：后端重启后恢复 failed run 和 6 个既有产物，从原 script task 继续，完成确认和 scripts 聚合；最终 8 个 active artifacts、1 个 script_unit、1 个 run_completed。

### 配置与附件恢复

- project：`project_136ae019daf3808be15b7234`
- 上传本身未创建 run。
- 缺生成配置时未创建 run。
- 持久化 agent message 保存 `requires_generation_config=true` 和 source_mode。
- 持久化 user message 保留原 file_id，前端恢复测试验证可重建配置卡。

## 仍保留但不阻断接入的技术债

1. `runtime.go`、`cmd/server/main.go`、`App.tsx`、`ArtifactWorkspace.tsx` 仍偏大。当前边界和测试足以接入，但 Eino 阶段应按状态机、上下文装配、版本传播和 UI orchestration 分模块拆分，避免继续在大文件追加条件分支。
2. 前端现有覆盖率约 40%，关键选区算法较高，但 API client 与 App 编排覆盖仍低。真实 API 和浏览器回归已补风险，Eino 接入时应为新 adapter、interrupt/resume 和 checkpoint hydration 增加契约测试。
3. `backend/` 根目录仍有 3 个历史可执行文件、一个 `$coverage` 文件和本机辅助启动脚本。它们不被当前构建或运行引用，但属于仓库清洁债；删除二进制属于破坏性清理，应在单独确认后执行。
4. 接入前 Eino 只是 Worker 单节点薄适配器，并非完整 Main Agent Graph；该技术债已由 `eino-integration-20260716.md` 关闭。

## Eino 接入门槛

正式接入必须保持以下不变量：

- HTTP API、对象 ID、Artifact 版本和 SQLite 数据兼容。
- Main Agent 的有限 action、确定性 guard 和附件按需读取规则不变。
- 每个主要 artifact 的 interrupt/approval 语义不变；script_context 继续隐藏。
- 修改后下游先 stale、用户选择后再 invalidated/regenerate。
- failed task cursor、暂停 cursor、SSE 重放和重启恢复行为不变。
- native 与 Eino 必须共享同一套契约测试和真实双链路 smoke。
