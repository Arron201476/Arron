# 阶段 5 T5 通用生产能力验收

日期：2026-08-03  
状态：通过  
下一步：T6 视频参考创作

## 1. 结论

通用生产能力已独立于三个 Domain Skill 建立。上传、Asset Set、解析、版本、编辑、导出、Candidate/Final、删除与保留均由 Runtime 管理，Domain Skill 只读取精确快照。

## 2. 已完成能力

| 能力 | 验收结果 |
|---|---|
| TXT/MD/DOCX/PDF 上传边界 | 类型、大小、快照和解析状态可追踪；DOCX 内置解析，PDF 通过配置化命令接入 |
| 图片通用输入 | 走 General Agent Image Tool，不创建 Domain Run；意图不明确由 Agent 追问 |
| Asset Set | 不可变版本、自然集号识别、人工排序、缺集/重复/失败检查、Seal/Reopen 已实现 |
| Parse Retry | 当前资产快照重试，状态和事件可追踪 |
| Artifact Version | 子项编辑形成新版本，旧版本和依赖保留 |
| Candidate/Final | 一个作品只有一个最终稿，未确认候选历史保留 |
| TXT/DOCX 导出 | 精确绑定 Candidate 与 scripts 版本，24 小时过期 |
| 删除保护 | Asset 和 Project 均先预览影响再确认；活动 Run 阻止删除 |
| 视频保留 | 原视频 7 天后进入删除任务；剧本、分析、Brief 和来源元数据随作品保留 |
| 数据迁移 | Schema v17，升级前自动备份并记录 migration history |

## 3. 自动化证据

- Project 删除预览、确认、幂等和活动 Run 阻断：通过；
- Asset 删除影响、7 天保留和失败重试：通过；
- DOCX 导出结构与精确版本保护：通过；
- Parse Retry HTTP 与 Runtime：通过；
- HTTP API 全回归：通过；
- Runtime、Shell、Provider 和 Capability 回归：通过；
- `go vet ./...`：通过。

PDF 正式解析器命令和内网对象存储属于部署配置，不在本地合成测试中伪造通过。
