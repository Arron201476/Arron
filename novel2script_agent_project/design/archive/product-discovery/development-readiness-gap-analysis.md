# Novel2Script 开发准入包盘点

本文档按《AI 项目前后端开发前置文档清单（通用）》的 12 类标准，盘点当前 Novel2Script Agent 设计资料。它回答三个问题：

1. 现有 preview 里哪些内容可以进入开发准入包。
2. 哪些内容是历史参考或重复资料。
3. 哪些关键文档还缺，不能直接进入完整前后端开发。

## 12 类文档覆盖情况

| 序号 | 类别 | 当前状态 | 现有支撑文档 | 缺口 |
|---|---|---|---|---|
| 1 | 产品目标与边界 | 初版具备 | `product-scope-contract.md`、`product-flow-audit.md`、`product-review-checklist.md`、`screenwriter-feedback-analysis.html` | 需要在真实开发中持续同步范围变更、产品指标和版本取舍 |
| 2 | 用户流程与状态机 | 初版具备 | `workflow-state-transition-contract.md`、`workflow-state.md`、`frontend-interaction-audit.md`、`main-agent-operations.md` | 需要落成后端 state reducer、事件映射测试和异常恢复测试 |
| 3 | 信息架构 | 初版具备 | `information-architecture-contract.md`、`frontend-interaction-audit.md`、`frontend-component-language.md` | 需要落成 React 组件映射、响应式实现和视觉终稿校验 |
| 4 | 静态 UI 稿 / 高保真交互稿 | 初版具备 | `frontend-design-system.md`、`frontend-ui-state-spec.md`、`frontend-ui-state-gallery.html` | 需要落成 React 组件实现、Playwright 视觉回归和真实数据接入后复核 |
| 5 | 组件语义与文案规范 | 初版具备 | `frontend-component-library-plan.md`、`frontend-component-spec.md`、`frontend-component-language.md`、`script-editor-spike-plan.md`、`script-editor-spike-result.md`、`fixtures/script-editor-spike-fixture.json` | 需要完成浏览器交互验证、长剧本性能测试、真实 suggestion patch、React 组件状态页、组件单测和可访问性测试 |
| 6 | 数据对象 / Artifact Schema | 初版具备 | `shared-schema-contract.md`、`artifact-schemas.md`、`local-file-attachments.md` | 需要落成 Go / TypeScript 实体类型、fixture 和校验 |
| 7 | Agent 行为协议 | 初版具备 | `agent-behavior-contract.md`、`main-agent-operations.md`、`agent-startup-contract.md` | 需要落成 router / runtime 规则和行为测试 |
| 8 | Prompt / Tool / Skill 设计 | 初版具备 | `runner-prompt-assembly-contract.md`、小说 prompts、非小说 prompts、公共 `script_generate.md`、9 个 skills | 需要落成 runner、输出校验器和 Eino 节点 |
| 9 | API / 事件协议 | 初版具备 | `api-event-contract.md`、`workflow-state.md`、`go-mock-runtime.md`、`local-file-attachments.md` | 需要和 Go / TypeScript 类型实现对齐，并补可执行 API 测试 |
| 10 | 模型与运行时策略 | 初版具备 | `model-runtime-eino-contract.md`、`main-agent-model-runtime.md`、`agent-startup-contract.md`、`go-mock-runtime.md` | 需要接入 Eino 前按官方 SDK 做实现细化 |
| 11 | 权限、数据与安全 | 初版具备 | `data-security-contract.md`、`local-file-attachments.md` | 需要落成后端校验、日志脱敏和删除实现 |
| 12 | 验收用例与测试清单 | 初版具备 | `acceptance-test-contract.md`、`fixtures/script-editor-spike-fixture.json`、`product-review-checklist.md`、`frontend-interaction-audit.md` | 需要落成自动化 E2E、API 测试和 UI 回归 |

## 当前明显冗余或历史参考

这些文件不应作为开发准入包的主合同，但可以保留在 preview 的“历史参考”区：

| 文件 | 判断 | 原因 |
|---|---|---|
| `frontend-visual-mock.html` | 偏历史 | 单一等待确认状态草稿，已被多状态 gallery 替代 |
| `frontend-mock-shell.md` | 偏历史 | 内容仍写“无依赖 TypeScript 壳”，与当前 React + Vite 方向不一致 |
| `agent-shell-wireframe.html` | 参考 | 早期线框稿，可帮助理解演进，但不应作为最终 UI 稿 |
| `agent-framework.html` | 参考 | 架构可视化与 `main-agent-operations.md`、`agent-startup-contract.md` 有重叠 |
| `editor-research-notes.md` | 参考 | 编辑器调研材料，不是开发合同 |
| `screenwriter-feedback-analysis.html` | 参考输入 | 是编剧反馈归类，不是产品边界最终稿 |

Archive 目录内的三份 prompt/skill 审计文件已经不进入 preview 主目录，保持当前处理方式即可。

## 当前最关键缺口

如果目标是“preview 完成后基本可以进入前后端开发”，下一步最应该补：

1. **前端组件实现与视觉回归**
   - `frontend-design-system.md`、`frontend-component-library-plan.md`、`frontend-component-spec.md`、`script-editor-spike-plan.md`、`script-editor-spike-result.md` 和 `frontend-ui-state-gallery.html` 已给出 UI 方向、组件库落地方案和编辑器 spike 初稿；下一步要完成浏览器交互验证、长剧本性能测试、真实 suggestion patch，落成 React 组件状态页、Playwright 视觉回归和真实数据复核。

2. **状态机代码化**
   - `workflow-state-transition-contract.md` 已补齐状态合同；下一步要落成后端 state reducer、事件映射测试、暂停/继续/确认/失败恢复测试。

3. **共享 schema 落地实现**
   - `shared-schema-contract.md` 已有第一版，还需要落成 Go struct、TypeScript types、fixture 和校验。

4. **API 可执行测试**
   - `api-event-contract.md` 已有第一版，但还需要落成 Go handler、TS client、SSE/轮询事件流和 API 测试。

5. **验收用例自动化**
   - `acceptance-test-contract.md` 已有第一版，还需要落成 Playwright E2E、API 测试、mock runtime 测试和 UI 状态回归。

## Preview 目录处理原则

新的 `preview.html` 按 12 类准入文档组织：

- 每类先显示该类的状态、支撑文档和缺口。
- 同一个文件如果支撑多个类别，只在第一次出现时渲染正文，后续类别只链接引用。
- 历史参考文件保留，但不混入 12 类主合同。
- archive 目录继续不进入主 preview。
