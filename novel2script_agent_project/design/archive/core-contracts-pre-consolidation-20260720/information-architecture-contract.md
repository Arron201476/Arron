# 信息架构合同

本文档定义 Novel2Script Agent 前端的信息架构。它约束左侧目录、中间内容区、右侧 Agent 面板、顶部栏、运行记录和窄屏策略。

## 页面定位

Novel2Script 前端是工作台，不是营销页，也不是流程按钮面板。

核心布局：

```text
Topbar：项目名、运行状态、模型状态
Left：项目目录
Center：当前内容 / artifact 编辑阅读区
Right：Agent 对话入口
```

## Topbar

承载：

- 产品标识。
- 当前项目标题。
- project status。
- runtime / provider 脱敏状态。

不承载：

- 生成按钮。
- 局部改写按钮。
- prompt 调试入口。

## Left：项目目录

职责：

- 展示当前 project 的主要产物。
- 展示当前链路。
- 展示 artifact 状态。
- 切换中间内容。

不负责：

- 启动生成。
- 继续 run。
- 局部修改。
- 审批。

### 未识别前目录

```text
剧本
输入材料
过程产物
运行记录
```

### 小说链目录

```text
剧本
输入材料
故事圣经
原文拆集
分集卡
剧本上下文
运行记录
```

### 非小说链目录

```text
剧本
输入材料
素材库
故事种子
剧集蓝图
分集卡
剧本上下文
运行记录
```

### 目录规则

1. 未识别前不同时展示小说和非小说两条完整链。
2. run 创建后，以 run.source_mode 为准。
3. 点击目录只切换中间内容，不触发生成。
4. 运行记录永远放最后。
5. 下游 invalidated 时必须显示已失效。

## Center：内容区

职责：

- 展示当前 artifact。
- 支持字段化阅读和编辑。
- 支持选区。
- 支持保存状态。
- 展示空态、错误态、失效态。

不负责：

- AI 指令入口。
- 审批按钮。
- 生成按钮。
- 重生成按钮。

AI 需求统一由右侧 Agent 承载。

### Artifact 展示形态

| artifact | 展示 |
|---|---|
| source_input | 原文 / 素材编辑器，文件预览 |
| story_bible | 结构化文档 |
| episode_split | 分集边界列表 |
| material_bank | 素材分组 |
| story_seed | 开发方向卡 |
| series_blueprint | 阶段地图 |
| episode_cards | 分集卡列表 |
| script_context | 角色状态、连续性、上下文摘要 |
| scripts | 剧本编辑器 |
| run_events | 事件列表 |

### 内容区状态

```text
empty
loading
ready
pending_approval
invalidated
failed
editing
```

## Right：Agent 面板

职责：

- 用户输入主入口。
- 普通聊天。
- 启动生成意图。
- 补充说明。
- 审批卡。
- 执行步骤摘要。
- 错误恢复。
- 局部修改请求。

不负责：

- 展示所有技术事件。
- 默认展示完整 prompt。
- 充当文件正文编辑器。

## Agent 消息类型

```text
UserMessage
AgentMessage
TaskStepList
ApprovalCard
ErrorCard
CompletionCard
AttachmentChip
ReferenceChip
```

## Composer

组成：

- attachment chips。
- reference chips。
- textarea。
- mode indicator。
- send button。

规则：

- Enter 发送。
- Shift Enter 换行。
- 文件名不插入 textarea。
- 发送按钮文案为“发送”。
- 等待确认时输入框用于补充要求，不创建新 run。

## 运行记录

运行记录是一个内容视图，不是右侧主聊天的替代品。

默认展示：

- 人话事件摘要。
- event type。
- status。
- time。

折叠展示：

- payload。
- prompt 摘要。
- artifact refs。

默认不展示：

- 完整 prompt。
- 完整 LLM raw output。
- API key。
- 本地 storage_path。

## 窄屏策略

工作区采用“中间内容为主、左右面板为工具”的自适应布局：

```text
Desktop（> 1280px）：左目录 + 中内容 + 右 Agent 三栏并排
Medium（761-1280px）：中内容常驻，可切换为“内容 + 目录”或“内容 + Agent”
Mobile（<= 760px）：目录 / 内容 / Agent 三个单区域视图切换
```

桌面交互规则：

1. 左右栏可以独立收起，恢复入口固定在各自对应的屏幕边缘。
2. 左右分隔线支持拖动调整宽度、键盘方向键调整和双击恢复默认宽度。
3. 用户栏宽偏好在浏览器中保存；作品当前视图和内容滚动位置按作品保存。
4. 产物生成、局部修改、状态刷新或出现审批请求时，只更新目录和 Agent 提醒，不自动切换中间内容。
5. 用户从左侧主动选择节点时，才切换中间内容。

窄屏下优先级：

1. Agent 输入和当前内容必须可达。
2. 左侧目录可折叠。
3. 运行记录可从菜单进入。
4. 不允许水平滚动导致内容不可读。

## 导航与 URL

建议 URL 结构：

```text
/projects/{project_id}
/projects/{project_id}/artifacts/{artifact_id}
/projects/{project_id}/runs/{run_id}/events
```

UI 可以保持单页应用，但关键视图需要可恢复。

## 信息架构验收

合格表现：

- 用户能看出当前在小说链还是非小说链。
- 用户能看出当前 run 是否在运行、暂停、等待确认、失败。
- 用户能知道下一步需要自己做什么。
- 用户能从右侧 Agent 发起生成、补充、确认、局部修改。
- 用户能从左侧切换查看过程产物。
- 用户能在中间阅读和编辑当前内容。

不合格表现：

- 左侧同时摊开两条链路。
- 中间出现一排 AI 操作按钮。
- 右侧审批卡脱离对话流。
- 文件名进入输入框正文。
- 内部 step done 用绿色确认勾。
- 运行记录默认刷满技术字段。

## 与 UI 状态稿关系

`frontend-ui-state-gallery.html` 是本信息架构的多状态视觉化表达。若 UI 稿和本文档冲突，以本文档和 `frontend-component-language.md` 为准。
