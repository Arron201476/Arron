# 第 10 步：发布准备与可观测性

## 1. 阶段结论

Novel2Script Agent 已完成全局 12 步流程第 10 步的工程实现与回归。当前交付形态仍是本地单用户工作台，不引入账号、云部署、计费或多人权限。

本阶段没有扩大生成链路功能，重点补齐：

1. 正式运行包；
2. 数据备份和恢复；
3. 脱敏运行日志查看；
4. SQLite 升级版本保护；
5. 安全启动、停止和发布门禁。

## 2. 正式运行包

执行：

```powershell
./scripts/package.ps1
```

输出目录：

```text
release/Novel2ScriptAgent/
  Novel2ScriptAgent.exe
  web/
  data/
  tools/
  .env.example
  Start-Novel2ScriptAgent.ps1
  Stop-Novel2ScriptAgent.ps1
  README.txt
```

正式包由同一个 Go 服务提供生产前端和 API，默认地址为 `http://127.0.0.1:8832/`。开发环境仍保持前端 8832、后端 8831，不受影响。

安全边界：

- 发布包不复制 `backend/.env`；
- 不包含 API Key；
- 不包含开发项目数据库、模型日志或测试数据；
- 前端使用相对 API 地址，不硬编码开发后端 8831；
- 未注册的 `/api/*` 不会被前端路由回退伪装成成功页面。

## 3. 启动与安全退出

`Start-Novel2ScriptAgent.ps1` 第一次运行时创建 `.env`，要求用户填写模型 API Key。正式服务隐藏启动后写入本地 PID 状态文件。

停止流程不是直接强杀：

1. 启动器为本次进程生成随机 shutdown token；
2. token 只保存在本地 PID 状态文件和后端进程环境中；
3. 停止脚本调用 `POST /api/system/shutdown` 并携带 token；
4. 后端停止接收 HTTP 请求，取消并等待当前 runtime job，再关闭数据库；
5. 15 秒仍未退出时停止脚本才执行兜底终止。

没有 token、token 错误或开发服务未配置 token 时，该接口统一返回 404。

## 4. 数据目录、备份与恢复

开发环境默认数据目录为项目根目录 `runs/`。正式包由启动器设置为包内 `data/`。也可以通过 `N2S_TRACE_DIR` 显式覆盖。

工具：

```powershell
./scripts/backup-data.ps1
./scripts/restore-data.ps1 -Backup <backup.zip> -ConfirmRestore
```

规则：

- 备份和恢复前必须先停止应用；
- 备份同时包含 `workspace.db` 和 `runtime.db`，以及存在时的 WAL 文件；
- 备份前校验 SQLite 文件头；
- 恢复前校验 manifest、格式版本和两个数据库；
- 恢复会先自动备份当前数据，再替换数据库；
- 备份不包含 `.env`、API Key 和 `llm_trace.jsonl`。

## 5. 数据库升级保护

`workspace.db` 当前 schema version 为 2，`runtime.db` 当前 schema version 为 1。

启动规则：

- 旧版本或未设置版本的数据库执行兼容迁移；
- 当前版本正常打开；
- 高于程序支持版本的数据库拒绝启动，避免旧程序误写新格式数据；
- 两个 SQLite 存储都使用单写者连接；
- 两个存储都启用 WAL 和 `busy_timeout=5000`，降低安全切换和短暂并发写入导致的 `SQLITE_BUSY`。

## 6. 日志查看

执行：

```powershell
./scripts/view-logs.ps1 -Limit 50
```

只展示：时间、耗时、模型、control/content、操作、project、run、artifact、episode 和错误状态码。

默认不展示：完整原文、完整 prompt、完整模型输出、API Key、provider endpoint 和本地绝对文件路径。

## 7. 发布门禁

执行：

```powershell
./scripts/release-gate.ps1
```

必须全部通过：

1. 前端单元/组件测试与覆盖率；
2. Go 全包测试；
3. `go vet ./...`；
4. 剧本编辑器专项门禁；
5. 前端生产构建；
6. 后端正式编译；
7. `design/preview.html` 存在。

Windows Application Control 偶发拦截临时 Go 工具时允许最多 3 次重试，但不能跳过测试或 vet。CI 继续保留 Go coverage 输出。

## 8. 本轮回归证据

- 前端：11 个测试文件、38 条测试通过；statements coverage 53.3%；编辑器 2 条专项测试通过；生产构建通过。
- 后端：所有 package 测试通过；`go vet ./...` 通过；正式编译通过。
- 正式包：8842 独立端口验证 health、首页、SPA 路由、API 404 和相对 API 地址。
- 浏览器：1440×900 和 1024×768 均无横向溢出、console error 或 response error。
- 生命周期：8843 通过启动脚本启动，停止脚本通过 token 安全退出，进程和 PID 文件均清理。
- 数据：备份、恢复前自动备份、恢复替换和脱敏日志查看均实际通过。
- 最终干净包约 38.15 MB；无 `.env`；`data/` 为空。

## 9. 阶段边界

第 10 步完成后，下一阶段是第 11 步“用户验收与发布候选观察”。该阶段以实际用户任务、长任务稳定性、模型成本/耗时和发布候选缺陷为主，不再默认增加新的主流程功能。
