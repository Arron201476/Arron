# 阶段 7 后端收口进度

状态：已完成（内部 Demo 代码门禁）  
日期：2026-08-06

## 当前交付定位

当前阶段 7 的近期目标收缩为“内部可交互功能 Demo”：跑通小说转剧本、非小说文本转剧本、视频参考创作三条真实生成主链，以及中间产物编辑、逐步确认、失败重试、刷新恢复和最终剧本确认。

Demo 不承担生产部署、企业签名、多进程高可用、压力指标、监控告警、备份迁移和 40 集规模验收。上述事项仍保留在正式版门禁中，不因 Demo 收缩而删除。

为适配当前 Windows WDAC 环境，Demo 允许 Server 托管 Content/Video Worker 轮询循环。该模式只改变部署拓扑：Worker 仍通过 Runtime HTTP API 执行 `claim -> submit -> commit`，不新增第二套业务状态或生成逻辑；正式部署必须关闭该模式并运行独立 Worker。

## 当前门禁

| 工作包 | 状态 | 当前证据 |
|---|---|---|
| B7-0 API 收口 | 已完成 | 真实 Server/SQLite/Vite 代理已接通；项目聚合快照与待处理配置卡查询已实现；真实 Edge 自动化通过，产品负责人于 2026-08-06 确认聊天、三个 Skill 入口、小说配置卡、刷新恢复和作品 CRUD 无明显问题 |
| B7-1 存储与媒体 | 本机开发环境通过 | 二进制 Asset 使用文件存储；FFmpeg/FFprobe 8.1.2 可执行；压缩、超时长拒绝和 MOV 处理集成测试通过 |
| B7-2 Provider 接入 | 代码完成，实跑转阶段 9 | 本地密钥配置可加载，三个 Capability 均为 available；Control Prompt 与 `/messages` 已有联调证据；已增加可关闭的 Server 内托管 Worker 模式，受信环境中的真实 Provider 执行归阶段 9 |
| B7-3 Demo 任务闭环 | 代码完成，实跑转阶段 9 | 复用现有 Runtime、Worker、租约、提交和失败重试；已补视频链改编方案、文本链体量不足扩写策略的结构化确认，以及不可变 Config/Decision Snapshot 和 Context Pack |
| B7-4 数据与保留 | 正式版延后 | 已有 7 天保留、Tombstone、备份和迁移基础；Demo 保留现有行为，Dry Run 进入正式版门禁 |
| B7-5 安全与可观测性 | 正式版延后 | Demo 继续遵守凭据不入库、不回传前端和日志不记录正文；完整指标、告警和资源保护进入正式版门禁 |
| B7-6 正式构建 | 外部环境阻断，Demo 不阻塞 | 企业 WDAC 要求 Enterprise signing，本机无代码签名证书、WSL 或 Docker；不再以独立 Worker 构建阻塞内部 Demo，但正式版仍必须解决 |

## 阶段 7 代码完成门禁

1. Server 可通过显式开关启动单进程 Demo 执行模式，默认关闭；
2. 不选择 Skill 时，General Agent 仍可完成普通对话、意图澄清和能力说明；
3. 小说和非小说两条链具备真实 Provider 的逐步 Artifact、编辑、重新生成和确认执行路径；
4. 视频链具备 MediaKit 全量字幕与豆包视频理解、逐集保存并按用户确认顺序进入后续非小说链的执行路径；
5. 三条链均使用同一 Runtime 状态、版本、审批和事件协议，刷新后可以恢复；
6. 关键失败可见且可重试，不以伪造产物或前端本地状态宣称成功；
7. 三条真实链的浏览器与内容验收纳入阶段 9，不以阶段 7 编译证据替代。

## 本轮代码证据

- `go build -buildvcs=false ./...` 通过；
- Runtime 全测试包已通过 `go test -c` 完成测试编译；
- `pnpm test -- --run`：12 项通过；
- `pnpm run build` 通过；
- `node acceptance/validate-capability-contracts.mjs` 通过，3 个 Manifest、34 个步骤、14 个 Response Adapter 均有效。
- schema v18 已通过内存 SQLite 建库校验，共 48 张表；旧 Run 缺失的配置快照会在迁移事务中回填，完成后才提升数据库版本。

受 WDAC 限制，新生成的 Go 测试和 Server 可执行文件仍无法在本机启动，因此上述证据不等同于真实三链 Demo 已通过。产品负责人于 2026-08-06 确认阶段 7 按代码门禁完成，并将真实三链运行统一转入阶段 9。

## 已验证命令

- `go test ./internal/runtime ./internal/httpapi -count=1`
- `go test ./internal/mediaprocessor -run TestFFmpegPrepareIntegration -count=1 -v`
- `node scripts/real-backend-smoke.mjs`
- `pnpm run build`

## 产品补充验收

阶段 6 产品交互验收迁移到 B7-0。当前项目入口与空工作台已可使用真实后端；三个 Capability 的完整交互必须在 B7-2 Provider 配置完成后一起验收。补充验收未通过前不得关闭 B7-0。

## 当前阻断

Windows Code Integrity 事件 `3033/3077` 明确记录：策略 ID `{0283ac0f-fff1-49ae-ada1-8a933130cad6}` 要求 Enterprise signing，新生成的 Go Server、Worker、Test 以及项目内 Go/FFmpeg 工具因未签名被拒绝。换目录和换文件名无效。本机证书库没有代码签名证书，也没有 WSL 或 Docker。当前仅能复用项目内昨日已成功运行的 Server 构建完成 Control 联调，不能据此宣称正式 Worker 环境通过。

受支持的解除方式只能选择其一：

1. 公司 IT 为开发构建提供企业代码签名证书、签名流水线或明确的 WDAC 允许规则；
2. 公司批准并提供 WSL/Linux 或容器开发环境，在 Linux 中运行 Go Server/Worker；
3. 使用公司现有受信 Linux 开发/测试机完成阶段 7 Worker 联调。

不允许关闭 WDAC、复制改名绕过策略或长期依赖昨日临时 Server 二进制。后端还必须具备外网访问权限，否则 Control、Content、Video 和 MediaKit 会进入安全失败路径。

## B7-0 产品检查点

产品负责人已于 2026-08-06 在 `http://127.0.0.1:8860/` 验证：

1. 新建、打开、返回、重命名和删除作品；
2. 不选 Skill 时发送普通聊天；
3. “能力”菜单展示三个 Skill；
4. 选择“小说转剧本”并发送请求后出现“设置剧本体量”配置卡；
5. 刷新工作台后配置卡仍然存在。

结论：以上 6 项无明显问题，B7-0 正式关闭。该检查点不宣称剧本 Worker 已生成产物；三条主链的真实生成属于 B7-2/B7-3，完整样本 E2E 属于阶段 9。
