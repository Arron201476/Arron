# Novel2Script Agent 当前核心文档

更新时间：2026-07-20

`design/preview.html` 是统一预览入口。本目录只保留与当前代码一致的现行规范；此前的合同、方案、审计和实施记录均已完整移入 `design/archive/`，不再作为开发依据。

## 事实优先级

1. 当前运行代码、SQLite 结构和自动化测试。
2. 本索引列出的七份现行规范。
3. `prompts/`、`小说-prompts/`、`非小说-prompts/`、`视频-prompts/`、`rules/` 中由后端实际读取的运行时内容。
4. `archive/` 只用于追溯历史决策。

## 当前规范

1. `current-product-workspace.md`：产品边界、作品工作区、附件和生成配置。
2. `current-agent-runtime.md`：Main Agent、Eino、模型分工、意图与上下文。
3. `current-workflows-revisions.md`：小说/非小说链路、审批、修改、刷新和失败恢复。
4. `current-api-data.md`：正式 API、状态枚举、Artifact、事件和持久化。
5. `current-frontend-interaction.md`：三栏工作台、过程产物、剧本编辑和选区交互。
6. `current-operations-release.md`：本地运行、安全、日志、备份和发布门禁。
7. `current-acceptance.md`：当前自动化与真实用户验收口径。

## 运行时 Prompt 与 Rule

- `prompts/`：公共剧本上下文和剧本生成 Prompt。
- `小说-prompts/`：故事圣经、原文拆集、小说分集卡 Prompt。
- `非小说-prompts/`：素材库、故事种子、剧集蓝图、非小说分集卡 Prompt。
- `rules/shared/`：多个 Skill 可复用、由内容模型按节点选择性注入的共享规则。
- `rules/video_to_script_extract/`：仅视频反推剧本 Skill 使用的私有规则。

这些文件会被后端直接读取。修改后必须同时运行后端测试和两条链路回归。

## 历史资料

- `archive/core-contracts-pre-consolidation-20260720/`：本次合并前的全部核心合同。
- `archive/product-discovery/`：产品研究和早期线框。
- `archive/frontend-design-history/`：旧视觉稿、Figma 资料和编辑器试验。
- `archive/implementation-history/`：旧技术路线、阶段审计和修复记录。
- `archive/python-prototype/`：已淘汰的 Python 原型。
