# Eino 接入后全局审计与回归（2026-07-16）

## 结论

本轮已完成前后端、Main Agent、两条生成链路、交互状态和 Eino 兼容性的全局审计。发现的功能问题均已修复并回归；当前正式服务使用 Eino 编排与 SQLite 持久化，历史作品恢复正常。

## 本轮发现并修复

1. 选中内容后说“先不要修改，只分析”或“你觉得怎么改”，仍可能被后置保护逻辑强制改写。
   - 已在快速路径和三个后置强制改写入口统一增加否定修改、只求建议保护。
   - 真实复测中剧本始终保持 v1，没有生成新版本。
2. `inspect_artifact` 的回复可能只说“我来看看，稍等”，但该动作没有后续异步执行器。
   - 主 prompt 现要求本轮直接给出完整分析或精确追问。
   - 主控若仍返回空承诺，会立即进行一次同轮纠正；纠正仍失败则安全降级，不再承诺后台处理。
3. 通用 500 错误可能向前端暴露数据库路径、SQLite 或模型授权细节。
   - 5xx 现统一输出用户安全信息；模型超时和限流分别显示明确提示。
4. 前端删除附件失败时曾静默移除本地展示，造成界面与后端状态短暂不一致。
   - 现改为后端删除成功后再移除；失败时保留附件并在聊天区提示。
5. Agent 行为合同仍把内部 `script_context` 列为用户确认点。
   - 已与产品合同、Runtime 和前端统一：`script_context` 保留为内部归一化产物，不展示确认卡。
6. 非小说缺少生成配置时的示例固定写“20 集”，容易被理解为默认值。
   - 已改为中性 2 集示例；系统仍不静默默认任何集数。
7. 页面没有 favicon，桌面浏览器产生一次 404 资源告警。
   - 已补充与现有 N 品牌一致的 favicon；两种视口回归均无资源错误。

## 架构审计

- 前端只负责展示、输入、选区与用户按钮，不拥有生成流程权威。
- Main Agent 负责意图、目标范围、是否执行和下一动作；内容模型负责 artifact 与剧本内容。
- Runtime 是 run、task、approval、artifact 版本和下游影响的唯一业务状态权威。
- SQLite 分别持久化作品/聊天/附件和运行态；刷新、重启后可恢复。
- Eino 包装主控和内容 worker 的输入校验、模型调用与输出校验，未复制 Runtime 的状态机。
- 原生模式仍可通过 `N2S_RUNTIME=native` 回滚；默认运行时为 `eino`。
- 现有兼容迁移代码集中在持久化恢复边界，没有发现第二套执行权威或绕过正式 API 的遗留入口。

## 两条真实链路

### 小说链

`source_input -> story_bible -> episode_split -> episode_cards -> script_context -> script_unit -> scripts`

- 状态：completed
- 用户确认点：4
- 重试：0
- 有效 artifact：7 类，各 1 份；`script_unit` 1 份
- `script_context` 确认卡：0

### 非小说链

`source_input -> material_bank -> story_seed -> series_blueprint -> episode_cards -> script_context -> script_unit -> scripts`

- 状态：completed
- 用户确认点：5
- 重试：0
- 有效 artifact：8 类，各 1 份；`script_unit` 1 份
- `script_context` 确认卡：0

两条链均由 Claude 完成主控判断，由 Gemini 完成内容调用；Run 元数据记录 `runtime=eino`。

## 自动化与页面回归

- 后端：`go vet ./...` 通过。
- 后端：agent、llm、mainagent、worker、server 测试通过。
- Runtime：拆集、5 集分批、断点恢复、版本、所有主要 artifact 局部/整体修改、下游保留/重生、剧本选区与手动编辑测试通过。
- API：18 条正式路由注册矩阵、错误结构、CORS、请求体限制、恢复与持久化测试通过。
- 前端：11 个测试文件、38 项测试通过。
- 前端：TypeScript 与 Vite 生产构建通过。
- 页面：1440x900、1024x768 两种视口加载成功，无横向溢出、无控制台错误、无 4xx/5xx 资源请求。

## Eino 结论

当前 Eino 与项目兼容，没有状态冲突。它适合继续承担编排、节点校验和后续可观测性扩展，但不应接管作品持久化或 Runtime 业务状态机。后续可选优化是把 Eino 节点耗时与失败标签接入统一运行记录；这不是当前上线阻断项。

## 正式服务

- 后端：`http://127.0.0.1:8831`，Eino + SQLite
- 前端：`http://127.0.0.1:8832`
- 统一文档入口：`design/preview.html`
