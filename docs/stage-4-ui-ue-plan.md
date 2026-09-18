# 阶段 4 UI / UE 规范执行计划

状态：已通过  
版本：v1.7  
日期：2026-07-31

## 1. 阶段目标

把阶段 3 已确认的信息架构和交互基线，转成可供前端实现的设计系统、组件库、高保真关键状态与视觉验收基准。

本阶段不实现 React 页面，不连接真实后端，不验证模型内容质量。

## 2. 设计基线

1. 使用“统一作品工作台 + 三栏 + 单一 General Agent + Domain Skills + 独立 Artifact”。
2. 左侧是流程与产物目录，中间是当前 Artifact 工作区，右侧是唯一 Agent 对话与生产控制台。
3. 请求 Agent 修改是选区、子项或 Composer 中的上下文动作，不在每个步骤顶部或底部常驻重复入口。
4. 第一版每个作品只有一个活动写 Run。
5. Skill 菜单只展示小说转剧本、非小说文本转剧本、视频参考创作。
6. 不暴露模型、Prompt、Rule、Worker、内部 Step ID。
7. 用户只确认业务级 Artifact 和高影响决策，不确认每次模型或工具调用。
8. 风格定位为安静、专业、适合长时间阅读的内容生产工作台，不做营销页或装饰性后台。

## 3. 交付结构

```text
00 Cover
01 Foundations
02 Base Components
03 Workbench Components
04 Agent Components
05 Artifact Components
06 Script Editor Components
07 Project Entry States
08 Workbench Core States
09 Novel Skill States
10 Non-novel Skill States
11 Video Reference Skill States
12 Shared Error and Recovery States
13 Responsive Frames
14 Prototype Flows
```

## 4. 组件范围

### 4.1 基础组件

- Button
- IconButton
- Input
- Textarea
- Select
- Checkbox
- SegmentedControl
- Tabs
- Menu
- Dialog
- Tooltip
- StatusBadge
- Progress
- Chip
- Toast
- EmptyState
- Skeleton

### 4.2 工作台组件

- AppHeader
- ProjectRow
- ProjectMenu
- ProjectSidebar
- FlowSection
- ArtifactNavItem
- WorkbenchShell
- ArtifactHeader
- VersionMenu
- EpisodeOutline
- ArtifactFieldGroup
- BatchTaskList

### 4.3 Agent 组件

- UserInstruction
- ClarificationCard
- PlanCard
- RunProgressCard
- ApprovalCard
- ArtifactResultCard
- FailureCard
- SyncEvent
- ContextChip
- AttachmentChip
- SkillMenu
- AgentComposer
- RunningControlBar

### 4.4 Artifact 与编辑器组件

- SourcePreview
- StructuredArtifactView
- EpisodeCard
- VideoEpisodeRow
- AnalysisView
- AdaptationBriefEditor
- CandidateList
- ScriptEditorShell
- ScriptToolbar
- ScriptDocument
- SelectionActionBar
- SuggestionPatchCard
- RevisionHistory

## 5. 必须覆盖的高保真状态

### 5.1 项目入口

1. 作品列表默认态；
2. 搜索和状态筛选；
3. 新建作品；
4. 行内更多菜单；
5. 重命名；
6. 删除影响确认；
7. 空作品列表。

### 5.2 通用工作台

1. 空作品与普通聊天；
2. 文件已添加但未发送；
3. Skill 菜单；
4. 意图不确定的结构化追问；
5. 执行计划确认；
6. 运行中控制；
7. 等待业务确认；
8. 失败与重试；
9. 暂停后恢复；
10. Artifact 结果与定位；
11. 局部 AI 修改；
12. 手动编辑与未保存；
13. 版本历史；
14. 最终稿选择。

### 5.3 小说 Skill

1. 小说原文输入；
2. 故事圣经；
3. 原文拆集；
4. 分集卡确认；
5. 剧本生成与编辑。

### 5.4 非小说 Skill

1. 非小说材料输入；
2. 素材库；
3. 故事种子；
4. 剧集蓝图；
5. 分集卡确认；
6. 剧本生成与编辑。

### 5.5 视频参考创作 Skill

1. 分批上传与批次归属；
2. 按文件名排序和人工调整；
3. 单集解析任务列表；
4. 缺集继续确认；
5. 单集剧本查看、编辑和重新生成；
6. 完整剧本汇总；
7. 剧本分析；
8. 改编方案建议；
9. Adaptation Brief 编辑与确认；
10. 接入非小说后续链；
11. 来源标记与禁止照搬提示。

## 6. 响应式范围

第一版仅设计桌面网页：

| 宽度 | 左侧 | 中间 | 右侧 |
|---:|---:|---:|---:|
| 1280 | 默认收窄，可收起 | 不低于 560 | 默认 340，可扩展 |
| 1440 | 默认完整 | 弹性 | 默认 380，可扩展 |
| 1920 | 默认完整 | 限制正文最大阅读宽度 | 默认 420，可扩展 |

页面不得横向滚动。三栏独立滚动，Agent Composer 固定在面板底部。

## 7. 视觉验收矩阵

每个关键组件至少检查：

- default
- hover
- pressed
- focus-visible
- disabled
- loading
- error
- long-content

每个关键页面至少检查：

- 1280 x 800
- 1440 x 900
- 1920 x 1080
- 最长中文作品名和文件名
- 50 集目录
- 长剧本正文
- 多个 Context Chip 和 Attachment Chip
- Agent 运行、审批、失败卡连续出现
- 浏览器缩放 100%

## 8. 执行顺序

1. 冻结视觉方向与 Foundations；
2. 建基础组件及全部交互状态；
3. 建工作台、Agent、Artifact 和编辑器复杂组件；
4. 先完成项目入口与通用工作台状态；
5. 复用组件完成三条 Skill 状态；
6. 完成响应式 Frame 和关键 Prototype Flow；
7. 对组件源头执行视觉自测；
8. 对状态页执行多视口、长内容和交互走查；
9. 修正后形成阶段 4 定稿候选；
10. 产品负责人确认后进入阶段 5。

## 9. 当前进度

- [x] 阶段 3 交互基线确认；
- [x] 阶段 4 范围和交付结构建立；
- [x] Foundations、色彩、排版、间距与表面规范完成；
- [x] Button、IconButton、Agent Composer、ApprovalCard、Dialog、Event、FailureCard 正式组件源完成；
- [x] 正式组件通过 100% 局部截图、几何、状态、长中文和业务上下文硬门禁；
- [x] Composer 六状态与 348 / 392 / 420 三宽压力验证；
- [x] 项目入口默认、锚定菜单、删除确认和空状态；
- [x] 通用聊天、运行、审批、失败、暂停和恢复状态；
- [x] 小说、非小说文本和视频参考创作三条 Skill 主链；
- [x] 视频分批上传、逐集任务、缺集确认、剧本分析和改编 Brief；
- [x] 剧本已保存、未保存、版本历史、候选对比和唯一最终稿闭环；
- [x] 1280 x 800 / 1440 x 900 / 1920 x 1080 响应式页面；
- [x] 真实 50 行目录、纵向滚动和第 50 集末尾可达状态；
- [x] 三条主链原型入口和最终稿确认跳转；
- [x] 重复“让 Agent 修改 / 自己编辑”入口清理；
- [x] 审批、失败和运行控制统一进入 Agent 时间线；
- [x] 40 个产品/原型 Frame 完成截图、几何、文本、组件和 Agent Panel 全量复核；
- [x] 关闭全部 P0 / P1 和未接受的 P2；
- [x] 更新证据清单、审计结果、验收记录和机读状态；
- [x] 删除全部历史 Figma 页面和过时组件源，只保留 12 个最终页面；
- [x] 产品负责人确认。

当前结论：阶段 4 已正式通过，Figma 只保留 12 个最终页面，项目已进入阶段 5。

完整证据见 `docs/stage-4-ui-evidence-manifest.md`、`docs/stage-4-visual-audit-results.md`、`docs/stage-4-acceptance-review.md` 和 `docs/stage-4-ui-qa-checklist.md`。
