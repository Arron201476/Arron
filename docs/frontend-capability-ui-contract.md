# Frontend Capability UI Contract

状态：Stage 2 设计基线  
版本：v0.1  
适用范围：内容生产 Agent 第一版桌面网页端  
上游文档：

- `stage-1-product-definition.md`
- `stage-2-architecture-baseline.md`
- `capability-registry-contract.md`
- `runtime-domain-model.md`
- `artifact-dependency-contract.md`
- `capability-context-pack-contract.md`
- `asset-and-retention-contract.md`
- `api-event-contract.md`

## 1. 文档目标

本合同定义内容生产 Agent 第一版前端的信息架构、组件边界、状态来源和交互规则。

它负责回答：

1. 用户进入产品后先看到什么；
2. 作品入口页与作品工作台如何分工；
3. 同一个 Agent 如何承载多个 Skills；
4. Capability 和 Artifact 如何声明自己的领域 UI；
5. 哪些前端能力属于通用 Shell，不能在每个 Skill 中重复；
6. 小说、非小说和视频参考创作分别显示哪些视图；
7. 主要产物如何编辑、保存、要求 AI 修改、重新生成和确认；
8. 视频分批上传、集号排序、逐集状态和统一确认如何展示；
9. 运行中禁止普通聊天时，用户如何查看和控制任务；
10. SSE 断开、页面刷新、版本冲突和未保存草稿如何恢复；
11. 第一版如何保持轻量，不把内部生产工具做成复杂项目管理系统；
12. 现有 Novel2Script React 前端哪些组件保留、哪些需要拆分。

本文不产出视觉原型，也不直接修改 React 代码。

## 2. 用户与设计原则

### 2.1 目标用户

第一版用户：

- 公司内部内容生产人员；
- 需要从多种来源得到漫剧生产前剧本；
- 对传统聊天模型熟悉；
- 对 Agent、Run、Artifact、Dependency 等内部概念不熟悉；
- 更关心“现在处理到哪、我该确认什么、最后剧本在哪里”。

### 2.2 交互原则

```text
对话入口轻量
流程状态明确
当前任务聚焦
主要产物逐步确认
复杂技术概念后台化
异常可恢复
历史可追踪
```

### 2.3 行业对齐

本项目采用当前生产型 Agent 工作台常见结构：

```text
长期工作容器
-> Project

统一交互主体
-> General Agent

可发现领域能力
-> Capabilities / Skills

确定性任务执行
-> Run / Step / Task

持久化可编辑结果
-> Artifact / Version

实时状态
-> Snapshot + Event Stream
```

前端不把 Agent 简化成单个聊天框，也不把 Agent 做成传统多页流程表单。

### 2.4 轻量不等于隐藏状态

轻量意味着：

- 默认只显示当前需要处理的内容；
- 缺少配置时只询问必要字段；
- 普通用户不需要理解内部图和数据库；
- 高级历史、依赖和事件按需展开。

轻量不意味着：

- 隐藏失败；
- 隐藏版本；
- 自动跳过确认；
- 把所有步骤塞进聊天消息；
- 让用户猜是否仍在运行；
- 用模糊文案代替状态。

### 2.5 第一版桌面范围

第一版只支持桌面网页端。

验收视口：

```text
1280 x 720
1440 x 900
1920 x 1080
```

同时检查：

- 浏览器缩放 100%；
- 浏览器缩放 125%；
- Windows 常见中文字体渲染；
- 长中文作品名和文件名。

不建设：

- 移动端；
- 手机浏览器布局；
- 原生桌面端；
- 平板专用交互。

## 3. 顶层信息架构

### 3.1 页面

第一版只有两个主要页面：

```text
作品入口页
-> /projects

作品工作台
-> /projects/:project_id
```

根路径：

```text
/
-> /projects
```

不建设营销首页。

### 3.2 页面职责

作品入口页：

- 新建；
- 搜索；
- 打开；
- 重命名；
- 删除；
- 查看作品状态；
- 快速输入需求或上传材料创建作品。

作品工作台：

- 查看当前流程；
- 管理材料；
- 查看和编辑产物；
- 与统一 Agent 交互；
- 查看运行状态；
- 完成审批；
- 暂停、恢复和重试；
- 管理候选稿与最终稿；
- 导出。

### 3.3 Project 与 Conversation

UI 对用户呈现“一部作品一个连续工作台”。

底层仍保留：

```text
project_id
conversation_id
```

路由使用 Project ID，不把 Conversation ID 暴露为用户导航概念。

## 4. 应用路由

### 4.1 Canonical Routes

```text
/projects
/projects/:project_id
```

### 4.2 Workbench Query State

工作台可使用 URL Query 保存可分享的查看位置：

```text
?view=artifact
&artifact_id=art_...
&artifact_version_id=av_...
&scope_key=episode:3
```

规则：

- 使用稳定 ID；
- 不使用中文标题定位；
- 不使用数组索引定位；
- 不把未保存文本写入 URL；
- Project ID 与资源归属不一致时忽略并回到安全默认视图。

### 4.3 本地 UI 偏好

可以按 Project 保存：

- 左侧目录是否展开；
- Agent 面板是否展开；
- 面板宽度；
- 最近查看 Artifact；
- 最近查看 Episode；
- 编辑器非业务显示设置。

不能用本地偏好覆盖：

- 当前 Run；
- 当前 Approval；
- Artifact Current Version；
- Final Selection；
- Asset 状态；
- 可用动作。

## 5. 作品入口页

### 5.1 定位

作品入口页是内部工作入口，不是营销页，也不是复杂项目管理后台。

界面优先级：

```text
快速开始
最近作品
搜索与状态
作品管理
```

### 5.2 页面结构

```text
顶部栏
├─ 产品名称
├─ 共享工作区标记
└─ 新建作品

快速开始区
├─ Skill 引用
├─ 输入框
├─ 上传
└─ 发送

作品工具栏
├─ 搜索
└─ 状态筛选

作品列表
└─ 作品行
```

不使用大幅 Hero、营销口号或装饰插画。

### 5.3 快速开始

用户可以：

- 输入需求；
- 引用 Skill；
- 上传材料；
- 点击发送。

发送时：

1. 创建未命名 Project；
2. 创建主要 Conversation；
3. 上传 Asset；
4. 创建 Message；
5. 切换到工作台；
6. Agent 继续追问或准备 Run。

任何一步失败：

- 不重复创建 Project；
- 使用 Idempotency Key 恢复；
- 明确显示哪些内容已保存。

### 5.4 新建作品

“新建作品”打开小型 Dialog：

- 作品名称可选；
- 默认“未命名作品”；
- 不要求先选小说/非小说；
- 不要求先选 Skill；
- 创建后进入工作台。

### 5.5 作品列表

第一版使用紧凑列表或表格，不使用大面积卡片墙。

列：

| 列 | 内容 |
|---|---|
| 作品名称 | 可打开 |
| 当前状态 | 用户可理解状态 |
| 当前 Skill | 当前/最近 Run 的 Skill；无则为空 |
| 当前步骤 | 等待材料、等待确认、生成中等 |
| 更新时间 | 本地时间 |
| 最终稿 | 未确认/已确认 |
| 操作 | 重命名、删除 |

稳定排序：

```text
updated_at DESC, project_id ASC
```

### 5.6 作品状态

用户文案示例：

```text
等待材料
等待补充信息
等待确认
生成中
已暂停
生成失败
剧本已完成
```

状态文案通过统一映射生成，不能由组件判断中文字符串。

### 5.7 搜索

第一版：

- 按作品名称搜索；
- 客户端输入防抖；
- 服务端 Query；
- 空结果显示简洁 Empty State；
- 不提供高级筛选器构建器。

### 5.8 重命名

- 行内菜单触发；
- 小型 Dialog 或行内编辑；
- 保存携带 Expected Version；
- 冲突时加载最新名称；
- 空名称拒绝；
- 长名称截断显示但保留 Tooltip。

### 5.9 删除

删除：

```text
点击删除
-> 获取 Delete Preview
-> 显示文件、Run、候选稿、最终稿和版本影响
-> 用户再次确认
-> 执行 Delete Confirmation
-> 从列表移除
```

不能用浏览器原生 `confirm()` 代替影响预览。

## 6. 作品工作台 Shell

### 6.1 固定布局

```text
顶部：Project Header
左侧：Process and Artifact Navigation
中间：Active Workspace
右侧：General Agent Panel
底部/内容底部：Contextual Action Bar
```

### 6.2 桌面尺寸

建议初始值：

| 区域 | 初始宽度 | 范围 |
|---|---:|---:|
| 左侧目录 | 224px | 200-360px |
| 中间内容 | 自动 | 最小 640px |
| 右侧 Agent | 360px | 320-560px |
| 顶部栏 | 48-56px | 固定 |

左侧和右侧：

- 可折叠；
- 可拖动调整；
- 支持键盘调整；
- 保存本地偏好；
- 折叠后保留恢复图标；
- Pending Approval 时恢复图标显示状态点。

### 6.3 窄桌面

当中间区域不足 640px：

1. 优先折叠右侧 Agent；
2. 再允许折叠左侧目录；
3. 不压缩编辑器到文字重叠；
4. 不让按钮覆盖内容；
5. 不进入移动端抽屉模式。

### 6.4 Project Header

显示：

- 返回作品入口；
- 作品名称；
- 当前 Skill；
- 当前 Run 状态；
- 实时连接状态；
- 候选稿/最终稿入口；
- 导出入口；
- 更多操作。

不显示：

- `run_id`；
- `source_mode`；
- Provider 名称；
- Graph 节点；
- Token 数；
- 数据库状态。

### 6.5 Header 操作

熟悉操作使用 Lucide 图标：

- 返回；
- 暂停；
- 恢复；
- 重试；
- 下载；
- 更多；
- 折叠面板。

不熟悉或高风险命令使用图标加文字：

- 确认并进入下一步；
- 保留现有下游；
- 重新生成受影响内容；
- 结束本次运行；
- 删除作品。

图标按钮必须有 Tooltip 和 `aria-label`。

## 7. 左侧流程与产物目录

### 7.1 定位

左侧同时承担：

- 当前流程地图；
- 产物入口；
- 材料入口；
- 运行记录入口。

不承担文件树式任意层级管理。

### 7.2 导航分组

```text
材料
流程产物
剧本
版本与候选
运行记录
```

只有存在或当前 Capability 声明需要的分组才显示。

### 7.3 Nav Item

```json
{
  "nav_item_id": "artifact:story_bible",
  "label": "故事圣经",
  "icon_key": "book_open_text",
  "target": {
    "view_key": "artifact_editor",
    "artifact_type": "story_bible"
  },
  "status": "waiting_approval",
  "badge": "待确认",
  "enabled": true
}
```

### 7.4 状态

```text
未开始
等待材料
生成中
部分完成
待确认
已确认
已暂停
失败
已完成
来源已过期
```

状态颜色只辅助，不作为唯一表达。

### 7.5 生成来源

Nav Items 来自：

```text
Capability UI Manifest
+ Run Snapshot
+ Artifact Summary
+ Available Actions
```

不能通过：

```text
if source_mode == novel
else non_novel
```

硬编码整条目录。

### 7.6 内部 Artifact

`script_context`：

- 默认不出现在目录；
- 可在调试模式查看；
- 不显示确认；
- 不成为用户编辑目标。

`scripts`：

- 显示为完整阅读/导出视图；
- 不作为编辑真源；
- 编辑操作导航到对应 `script_unit`。

## 8. 中间 Active Workspace

### 8.1 职责

中间区域展示当前用户真正处理的对象：

- 材料；
- 结构化产物；
- 剧本；
- 视频批次；
- 剧本分析；
- Brief；
- 候选稿；
- 版本历史；
- 运行记录。

### 8.2 Workspace Header

包含：

- 当前产物名称；
- Scope，例如第 3 集；
- 当前版本；
- 保存状态；
- Approval 状态；
- 来源状态；
- 版本历史；
- 适用操作。

标题字号与工作台密度匹配，不使用 Hero 级字体。

### 8.3 Workspace Body

页面区段使用全宽、无外层浮动卡片结构。

卡片仅用于：

- 重复子项；
- 单个候选稿；
- Dialog；
- Approval 摘要；
- 错误详情。

不允许：

- 页面 Section 外套 Card；
- Card 内再套 Card；
- 每个字段一个装饰 Card。

### 8.4 Contextual Action Bar

主要产物处于待确认时，内容区底部显示稳定操作条：

- 保存；
- 要求 AI 修改；
- 重新生成；
- 暂停；
- 确认并进入下一步。

Action Bar：

- 来自 `available_actions`；
- 不遮挡编辑内容；
- Pending Approval 时即使 Agent 面板折叠也可见；
- 有未保存草稿时禁用确认；
- 有必需失败子项时禁用确认并说明原因。

## 9. General Agent Panel

### 9.1 一个 Agent

所有 Skills 共用同一个 Agent Panel。

Skill 切换不会：

- 创建第二个聊天面板；
- 清空主 Conversation；
- 更换 Agent 人设；
- 创建独立产品页面。

### 9.2 Panel 结构

```text
Agent Header
Conversation Timeline
Selection/Attachment Context
Composer
```

### 9.3 Agent Header

显示：

- Agent；
- Ready/Thinking/Waiting/Paused；
- 折叠。

不显示复杂模型参数。Pause/Resume/Retry 不在 Header 常驻，由 Composer 当前状态的右侧动作承担。

### 9.4 能力入口

位置：

```text
Composer 底部操作栏，位于附件与当前状态主动作之间
```

按钮显示“能力”。点击后显示三个 Skills：

1. 小说转剧本；
2. 非小说文本转剧本；
3. 视频参考创作。

菜单数据来自 Capability Discovery。

### 9.5 Skill 引用

用户通过“能力”入口选择 Skill：

- 在输入框中插入可见 Skill 引用；
- 前端同时保存结构化 `capability_ref`；
- 用户可以继续输入要求；
- 不立即创建 Run；
- 再次点击其他 Skill 时替换本条消息的引用；
- 发送后引用随 Message 持久化。

推荐表现：

```text
[小说转剧本] 根据这个文件生成 24 集剧本
```

UI 可以将引用渲染为不可编辑 Chip，但 Message Request 必须发送：

```json
{
  "capability_ref": {
    "capability_id": "novel_to_script",
    "selection_mode": "explicit"
  }
}
```

不能只靠解析输入框中的中文文本识别。

### 9.6 自动路由

用户不引用 Skill：

- 正常发送；
- Agent 进行意图识别；
- 意图清楚时选择 Capability；
- 不确定时追问；
- 不显示复杂路由过程。

### 9.7 零 Capability

`available_capabilities=[]`：

- “能力”入口显示空状态或禁用；
- Composer 仍可用；
- 上传仍可用；
- Agent 可普通对话；
- Agent 可解释通用材料；
- Agent 可说明当前缺少领域能力。

不能把整个 Agent Panel 禁用。

### 9.8 运行中

当前 Project 存在活动写 Run：

- Composer 切换到 `Running` 状态，不移除、不隐藏；
- 自由文本输入锁定，内容区显示当前步骤、进度和已完成内容保留说明；
- 右侧状态动作显示 Pause；
- “能力”入口默认禁用；仅当当前收集步骤的 `available_actions` 明确允许添加材料时可用；
- 附件入口同样由 `available_actions` 决定，不能仅按 Run 状态硬编码；
- 上传到当前执行集合只能通过明确的收集步骤操作；
- 不允许普通聊天；
- 允许切换其他 Project。

禁用文案必须说明：

```text
当前作品正在生成，暂停或等待完成后可继续发送。
```

### 9.9 Waiting Approval

`waiting_approval` 时：

- Agent Timeline 显示唯一业务主操作 `ApprovalCard`；
- Composer 不复制确认按钮，自由聊天仍受当前写 Run 限制；
- “要求 AI 修改”由 Approval Option、选区或子项上下文进入限定指令输入；
- 指令绑定当前 Approval Subject；
- 不能把普通闲聊误当修改。

### 9.10 Message Timeline

Timeline 展示：

- 用户消息；
- Agent 回复；
- Skill 引用；
- Asset 引用；
- Selection Snapshot 摘要；
- 生成配置确认；
- 主要状态摘要。

不把全部 Step Event 逐条刷成聊天气泡。

运行进度使用独立 Progress Summary，详细 Event 放运行记录。

## 10. 通用上传交互

### 10.1 入口

上传入口存在于：

- 作品入口快速开始；
- Agent Composer；
- 材料视图；
- 视频收集视图。

底层复用同一 Upload Session Client。

### 10.2 文件选择

支持：

```text
TXT MD DOCX PDF
JPG PNG WebP
MP4 MOV
```

Accept 只用于浏览器筛选，服务端白名单是权威。

### 10.3 上传状态

每个文件独立：

```text
等待上传
上传中
校验中
解析中
可用
失败
已过期
已删除
```

上传批次部分失败：

- 成功文件保留；
- 失败文件保留失败行；
- 只重试失败项；
- 不重传全部成功文件。

### 10.4 上传与 Run

上传成功后：

- Asset 出现在材料列表；
- Composer 显示引用；
- 不自动启动 Run；
- Agent 根据消息决定用途；
- 多份材料时明确本次选中集合。

### 10.5 图片

图片属于通用输入：

- 可直接询问图片内容；
- 可作为创作参考；
- 意图不明时 Agent 追问；
- 不自动出现第四个图片 Skill。

### 10.6 删除

未引用：

- 普通删除确认。

已引用：

- 获取影响预览；
- 展示已有产物仍保留；
- 展示失去的重新解析能力；
- 二次确认。

## 11. Capability UI Registry

### 11.1 定位

Capability UI Registry 让统一 Workbench 根据当前 Capability 和 Artifact 类型选择正确的领域组件。

它不允许 Skill 下发可执行前端代码。

### 11.2 两层 Registry

```text
Server UI Manifest
-> 声明 view_key、artifact_type、nav、action 和 schema_ref

Frontend Component Registry
-> 将受信任 view_key 映射到编译进产品的 React 组件
```

### 11.3 Compiled Server Manifest

该对象不是第二份手写 Capability 流程配置。服务启动时由源码 Manifest 的 UI Seed、Step Outputs、Artifact View Registry、Approval Type 和安全 Action Registry 确定性生成：

- `label/icon_key` 来自 Capability Manifest；
- `navigation` 来自可见 Step 和 Artifact 输出顺序；
- `artifact_views` 来自 Artifact Type 到受控 View Key 的注册；
- `task_views` 来自 Batch/Task Kind；
- `approval_views` 来自 Approval Type；
- `composer` 来自 `accepted_asset_kinds` 和 `entry_policy.input_collection_modes`。

编译结果只读并随 Capability Version 固定。若引用的 View Key 未注册，对应 Capability 标记为 `unavailable`，不能由模型自由生成页面。

```json
{
  "capability_id": "video_reference_creation",
  "capability_version": "1.0.0",
  "label": "视频参考创作",
  "icon_key": "clapperboard",
  "navigation": [],
  "artifact_views": {},
  "task_views": {},
  "approval_views": {},
  "composer": {
    "accepted_asset_kinds": ["video"],
    "allow_collecting_input": true
  }
}
```

### 11.4 Frontend Registry

```ts
type CapabilityUIRegistry = Record<
  string,
  {
    workbenchView?: React.ComponentType;
    artifactViews: Record<string, React.ComponentType>;
    taskViews: Record<string, React.ComponentType>;
    approvalViews: Record<string, React.ComponentType>;
  }
>;
```

实际实现使用懒加载模块，类型由生成的 API 类型约束。

### 11.5 安全边界

Server Manifest 只能引用允许的：

```text
view_key
icon_key
field_schema_ref
action_id
```

不能包含：

- JS URL；
- React Component Path；
- 任意 HTML；
- 任意脚本；
- 任意 API URL；
- CSS 字符串；
- 动态 import 地址。

### 11.6 Unknown Capability

前端不认识 Capability：

- 仍显示 Run 状态；
- 仍显示通用 Artifact Summary；
- 显示“当前前端版本不支持该能力视图”；
- 提供刷新/升级提示；
- 不用原始 JSON 编辑业务产物；
- 不影响其他 Project。

### 11.7 Unknown Artifact

允许只读通用 Inspector：

- Artifact Type；
- Version；
- Status；
- 来源；
- 创建时间；
- Payload Schema 校验状态。

不提供不受控通用 JSON 编辑器。

## 12. Artifact UI Registry

### 12.1 Artifact View Contract

```json
{
  "artifact_type": "episode_cards",
  "view_key": "episode_cards_editor",
  "editor_mode": "structured_collection",
  "scope": "episode",
  "confirmation_scope": "step",
  "supports": {
    "manual_edit": true,
    "ai_revision": true,
    "regenerate": true,
    "version_history": true
  }
}
```

### 12.2 通用 View Types

```text
source_material_view
structured_artifact_editor
collection_editor
script_unit_editor
scripts_reader
analysis_editor
brief_editor
asset_set_workspace
version_history
dependency_inspector
event_timeline
```

### 12.3 Current Artifact

前端通过：

- URL Target；
- Project Snapshot Current Focus；
- Pending Approval Subject；
- 最近本地查看位置；

决定当前 View。

优先级：

```text
Pending Approval explicit focus
-> URL explicit target
-> Server current focus
-> local last view
-> first available nav item
```

任何目标必须校验 Project 归属和精确版本。

## 13. 前端组件边界

### 13.1 Generic Shell Components

建议：

```text
AppRouter
ProjectIndexPage
ProjectQuickStart
ProjectList
WorkbenchPage
WorkbenchShell
ProjectHeader
ProcessNavigation
ActiveWorkspace
AgentPanel
Composer
CapabilityMenu
AttachmentTray
ContextualActionBar
AgentTimelineApprovalCard
RunStatusSummary
ComposerStateAction
ConnectionStatus
VersionHistoryDrawer
ImpactPreviewDialog
DeletePreviewDialog
ErrorBoundary
```

### 13.2 Domain Components

小说：

```text
StoryBibleEditor
EpisodeSplitEditor
EpisodeCardsEditor
ScriptUnitWorkbench
```

非小说：

```text
MaterialBankEditor
StorySeedEditor
SeriesBlueprintEditor
EpisodeCardsEditor
ScriptUnitWorkbench
```

视频：

```text
VideoAssetSetWorkspace
VideoEpisodeTable
ReferenceScriptWorkbench
ScriptAnalysisEditor
AdaptationOptionsView
AdaptationBriefEditor
```

### 13.3 共享 Domain Components

```text
EpisodeCardsEditor
ScriptUnitWorkbench
ScriptsReader
GenerationConfigForm
VersionBadge
SourceTraceView
```

视频 Skill 接入非小说后续流程时复用这些组件，不复制。

### 13.4 禁止中心组件继续膨胀

不继续在：

```text
App.tsx
ArtifactWorkspace.tsx
AgentPanel.tsx
```

加入所有 Capability 分支。

目标：

- App 只负责 Router 和 Provider；
- WorkbenchPage 负责页面组合；
- Server State 由 Query 层管理；
- Capability Registry 决定领域组件；
- Editor 自己管理草稿；
- Event Reducer 独立。

## 14. 前端状态分层

### 14.1 Server State

包括：

- Project；
- Conversation/Message；
- Asset；
- Asset Set；
- Run/Step/Task；
- Artifact Version；
- Approval；
- Candidate/Final；
- Event Cursor。

权威来源：

```text
API Snapshot + Query + SSE
```

### 14.2 Local UI State

包括：

- Panel 展开；
- Panel 宽度；
- 当前 Tab；
- Drawer/Dialog；
- 搜索输入；
- 当前 Episode；
- 编辑器光标；
- 未提交草稿。

### 14.3 不允许

- 将 Server State 全部复制进一个巨大 App `useState`；
- 组件分别维护相互冲突的 Run 副本；
- Event 直接修改多个独立状态数组；
- localStorage 作为当前 Run 真源；
- 根据聊天 Message 恢复 Artifact Current Version。

### 14.4 推荐技术

保持：

- React；
- TypeScript；
- Vite；
- Lexical；
- Lucide React；
- Vitest；
- Playwright。

建议增加：

```text
React Router
TanStack Query
OpenAPI TypeScript code generation
```

第一版不必增加全局 Zustand/Redux。

原因：

- Router 负责页面和深链接；
- Query 管理服务端缓存、失效和重试；
- 本地 UI 可以使用组件状态和小型 Context/Reducer；
- 避免第二套全局状态系统。

### 14.5 API 类型

HTTP 合同用 OpenAPI 表达。

前端：

- 从 OpenAPI 生成 Request/Response 类型；
- 不手写重复 `SourceMode`/Artifact/Event 枚举；
- Event Payload 使用版本化 JSON Schema；
- Capability/Artifact Schema 使用生成类型或受控校验器。

## 15. Snapshot 与 SSE

### 15.1 Bootstrap

```text
打开 /projects/:id
-> GET Project Snapshot
-> 写入 Query Cache
-> 保存 Cursor N
-> 渲染
-> 建立 Project SSE after N
```

### 15.2 Event Reducer

Event Reducer：

- 校验 Project ID；
- 检查 Seq；
- 去重 Event ID；
- 更新轻量进度；
- 使相关 Query 失效；
- 记录最新 Cursor。

不把 Event Payload 当完整资源。

### 15.3 Gap

收到非连续 Seq：

```text
停止应用新 Event
-> 标记“状态同步中”
-> 关闭当前 SSE
-> 重新 GET Snapshot
-> 从新 Cursor 连接
```

### 15.4 SSE 断开

- 不把 Run 标为失败；
- Header 显示“正在重新连接”；
- 保留已有内容；
- 按 API Contract 重连；
- 使用 Snapshot Poll 兜底；
- 恢复后隐藏状态。

### 15.5 Unknown Event

- 更新 Cursor；
- 安全忽略未知 Payload；
- 必要时失效 Project Snapshot；
- 不让页面崩溃；
- 不显示原始 JSON 错误给用户。

### 15.6 多标签页

两个标签页打开同一 Project：

- 各自订阅 Project SSE；
- 服务端版本保护写冲突；
- 一个标签页保存后另一个收到 Version Event；
- 有未保存草稿时不自动覆盖；
- 显示“服务端已有新版本”。

## 16. Loading、Empty 与 Error

### 16.1 Loading

使用稳定尺寸 Skeleton：

- Project 列表行；
- 左侧目录；
- Workspace Header；
- Editor Body；
- Agent Timeline。

Loading 文案、图标和状态变化不能导致布局跳动。

### 16.2 Empty

空状态只说明当前缺少的对象和一个主要动作：

```text
尚无材料
[上传材料]
```

不显示长篇产品使用说明。

### 16.3 Error 层级

```text
全局服务错误
-> 顶部状态条

Project Snapshot 错误
-> 页面级错误

单个 Artifact 错误
-> Workspace 内联错误

单个视频 Task 错误
-> 对应表格行

保存冲突
-> 冲突 Dialog
```

重要错误不能只用会自动消失的 Toast。

### 16.4 Error 内容

必须说明：

- 哪个对象失败；
- 已完成内容是否保留；
- 用户可执行动作；
- 是否会重新调用模型；
- 是否需要重新上传。

不展示内部异常。

## 17. 编辑协议

### 17.1 编辑真源

```text
结构化 Artifact
-> Artifact Version Payload

单集剧本
-> script_unit Artifact Version

完整剧本
-> scripts 只读聚合
```

### 17.2 编辑状态

```text
clean
dirty
saving
saved
conflict
save_failed
read_only
```

Workspace Header 必须显示。

### 17.3 保存

第一版采用：

- 本地草稿自动暂存；
- 服务端版本由用户显式保存；
- 保存创建 Artifact 新 Version；
- 不每输入一个字符创建服务端版本；
- 保存携带 Base Version；
- 保存成功更新 Current Pointer；
- 保存失败保留本地草稿。

### 17.4 本地草稿

建议使用 IndexedDB，键：

```text
project_id
+ artifact_id
+ base_version_id
+ scope_key
```

草稿：

- 只在当前浏览器保存；
- 不作为服务端版本；
- 定时写入；
- 保存成功后清理；
- 放弃修改后清理；
- Project 删除后清理；
- 不跨 Project 恢复。

### 17.5 恢复草稿

打开 Artifact：

```text
本地存在草稿
+ base_version_id 等于服务端 Current Version
-> 提示恢复

本地存在草稿
+ 服务端已有新版本
-> 进入冲突比较
```

不能静默覆盖服务端版本。

### 17.6 离开保护

Dirty 时：

- 切换 Artifact；
- 切换 Project；
- 关闭页面；
- 选择历史版本；

必须提示：

```text
保存并继续
放弃并继续
留在当前页面
```

### 17.7 空字符串

用户删除字段全部内容后：

- 保留 Dirty；
- 保存空字符串；
- 不当作“没有修改”；
- 不恢复旧文本。

### 17.8 版本冲突

冲突 Dialog 显示：

- 本地 Base Version；
- 服务端 Current Version；
- 本地草稿仍被保留；
- 重新加载；
- 查看差异；
- 基于最新版手动重做。

第一版不做自动文本三方合并。

## 18. 结构化 Artifact 编辑器

### 18.1 字段控件

Schema 驱动：

| 字段类型 | 控件 |
|---|---|
| boolean | Toggle/Checkbox |
| enum | Select/Menu |
| number | Number Input/Stepper |
| short text | Text Input |
| long text | Textarea |
| list | Repeated Rows |
| ordered list | Reorderable Rows |
| entity collection | Table/List + Detail Editor |

不根据 Payload 当前值临时猜全部业务控件。

### 18.2 Stable ID

人物、分集卡、场景、台词行使用 Stable ID。

编辑和 Selection Snapshot 不使用数组位置。

### 18.3 子项编辑

对于 Episode Cards：

- 左侧/顶部 Episode Selector；
- 中间单集详情；
- 子项独立保存；
- 当前步骤统一确认；
- 未保存集数显示 Dirty Badge。

### 18.4 来源标记

结构化产物支持显示：

```text
用户原始材料
材料整理
模型新增
推断
来源缺失
```

来源标记不使用只靠颜色的表达。

## 19. Script Unit Workbench

### 19.1 保留现有能力

保留现有：

- Lexical；
- Scene/Line Stable ID；
- Action/Dialogue/Transition Block；
- 文本选区；
- Selection Hash；
- 单集切换；
- 未保存拦截；
- TXT 导出基础。

### 19.2 页面结构

```text
Episode Rail
Script Editor
Editor Toolbar
Version/Save Status
Source/Continuity Inspector
Action Bar
```

### 19.3 Episode Rail

每集显示：

- 集号；
- 标题；
- 生成状态；
- 保存状态；
- 是否失败；
- 是否待确认；
- 是否有版本冲突。

固定尺寸，状态变化不能推动布局跳动。

### 19.4 单集编辑

- 每次只加载当前 `script_unit` Payload；
- 切集前处理 Dirty；
- 保存创建该集新 Version；
- 不保存整个 `scripts` 聚合；
- 当前集 AI 修改绑定 Selection Snapshot；
- 重生成只影响选中集及确定性连续性范围。

保存 `script_unit` 后如果服务端返回 `handoff_refresh_required=true`：

- Episode Rail 将对应集显示为“已保存，待更新生成依据”；
- 不显示可点击的统一确认；
- 用户可以继续编辑其他集并分别保存；
- Action Bar 显示唯一主操作“完成剧本编辑”；
- 点击后提交服务端返回的全部当前待刷新 Script Version；
- 刷新期间编辑器只读，各受影响集显示独立任务状态；
- 刷新失败保留已保存剧本，并在对应集提供重试；
- 全部刷新完成后恢复一个“确认全部单集剧本”操作。

### 19.5 全集确认

第一版采用：

```text
子项级编辑
+ 整个剧本步骤统一确认
```

确认前检查：

- 目标集数完整；
- 每个必需 `script_unit` 成功；
- 没有 Dirty；
- 没有 Save Failed；
- 没有未处理版本冲突；
- 没有 `stale` 或正在刷新的 `script_handoff`；
- 集号唯一且顺序正确。

不要求逐集点击确认。

### 19.6 长剧本

- 不一次渲染全部剧本编辑器；
- Episode Payload 按需获取；
- 完整阅读使用只读聚合；
- 超过阈值时分段加载；
- 导出由服务端使用精确 Version 生成。

## 20. Selection 与 AI 修改

### 20.1 创建 Selection

编辑器选中：

- 字段；
- 人物；
- 分集卡；
- 场景；
- 台词行；
- 文本范围。

前端创建不可变 Selection Snapshot。

### 20.2 Agent Panel 展示

Composer 上方显示 Selection Reference：

- Artifact；
- Version；
- Episode/Scene；
- 选中文字摘要；
- 移除按钮。

### 20.3 发送

Message/AI Revision 同时发送：

- 结构化 Selection Snapshot；
- 用户指令；
- Capability/Artifact Target；
- 当前 Client Context。

不能只发送引用文本。

### 20.4 Stale Selection

服务端返回 `SELECTION_SNAPSHOT_STALE`：

- 保留用户指令；
- 提示原版本已变化；
- 提供加载原版本或重新选择；
- 不自动把选区套到相似文本。

## 21. Approval 交互

### 21.1 可见位置

Pending Approval 同时出现在：

- 左侧 Nav Badge；
- Project Header 状态；
- Active Workspace 的只读状态提示；
- Agent Timeline 的 ApprovalCard。

业务确认主操作只放在 Agent Timeline 的 ApprovalCard。中间 Active Workspace 负责阅读、编辑、定位和版本状态，不复制第二套确认按钮。

### 21.2 Approval 内容

显示：

- 当前确认对象；
- 精确版本；
- 主要变更；
- 未完成/失败子项；
- 下游影响；
- 可选动作；
- 风险说明。

### 21.3 逐步确认

主要业务产物完成后：

- 用户查看；
- 可直接编辑；
- 可要求 AI 修改；
- 可重新生成；
- 可暂停；
- 明确确认后进入下一步骤。

Artifact 修改产生新 Version 后，旧 Approval 自动失效。

### 21.4 Action 来源

前端只渲染服务端返回的 Approval Options。

通用组件识别：

```text
approve
reject
revise_instruction
keep_downstream
regenerate_downstream
continue_incomplete
confirm_delete
select_final
```

Pause/Resume/Retry/Cancel 属于 Run/Task 控制，由 Run Snapshot 的 `available_actions` 投影到 Composer 或局部失败行，不属于 Approval Option。

### 21.5 禁用确认

以下情况禁用：

- Dirty；
- Saving；
- Save Failed；
- 子项失败；
- 版本冲突；
- Approval 已过期；
- Subject Snapshot 变化；
- 必需配置缺失；
- Asset Set 未 Seal。

显示具体原因，不能只显示灰按钮。

## 22. 下游影响交互

### 22.1 触发

用户保存已被下游使用的 Artifact 新版本后：

```text
创建 Impact Preview
-> 显示受影响对象和范围
-> 用户选择保留或重生成
```

### 22.2 Preview

展示：

- 修改内容；
- 直接受影响产物；
- 连续性传播范围；
- 已有候选稿；
- 预计重新执行步骤；
- 已完成内容是否保留。

### 22.3 保留下游

文案明确：

- 现有下游仍基于旧版本；
- 新上游不会静默混入；
- 旧 Candidate 保留；
- 以后可以从新版本生成另一套结果。

### 22.4 重新生成

文案明确：

- 从哪个步骤开始；
- 哪些集数重生成；
- 哪些未受影响内容保留；
- 是否会产生模型调用；
- 失败后旧版本仍可查看。

## 23. Pause、Resume、Retry 与 Cancel

### 23.1 Pause

- Running Composer 右侧提供 Pause 图标；
- 点击后显示“正在安全暂停”；
- 无活动 Attempt 时直接显示“已暂停”；
- 有活动 Attempt 时显示 `pausing`，并提供“撤销暂停”；
- 完成当前安全边界后 `paused`；
- 已完成内容保留。

在 `paused` 状态编辑会先创建新 Artifact Version 和 Impact Preview；如果修改影响当前 cursor，前端必须隐藏直接 Resume，要求用户先选择从受影响步骤重生成或放弃修改。不得按旧 cursor 继续并把新内容静默混入。

### 23.2 Resume

- 只在服务端 `available_actions` 包含时显示；
- 恢复同一 Cursor 和输入版本；
- Pending Approval 时不显示 Resume；
- 来源已过期时显示阻断原因。

### 23.3 Retry

- 失败行或 Step 显示 Retry；
- 只重试失败项；
- 说明是否重新调用模型；
- 成功项不显示可重复执行按钮；
- Retry 后保留旧 Attempt。

### 23.4 Cancel

Cancel 表示用户明确结束当前 Run，不再从当前 Cursor 恢复。

前端使用“结束本次运行”文案：

- 放在 More Menu，不与 Pause 并列为常用主按钮；
- 点击后显示二次确认；
- 明确说明已生成内容和历史记录继续保留；
- 明确说明结束后不能 Resume；
- 确认后调用 Cancel Command；
- Run 进入 Cancelled；
- 后续继续需要创建新 Run 或 Revision Run。

临时停止仍使用 Pause。

## 24. 视频 Asset Set Workspace

### 24.1 定位

视频参考创作的第一屏不是聊天说明，而是当前视频集合和逐集状态。

Agent Panel 继续存在，但主要操作在中间 Workspace。

### 24.2 Header

显示：

- 当前集合名称；
- 收集中/已完成上传；
- 视频数量；
- 已识别集数；
- 缺集/重复数量；
- 单集解析进度；
- 上传完成按钮。

### 24.3 操作

```text
添加视频
上传完成
检查顺序
批量重试失败项
统一确认解析结果
```

图标：

- Upload；
- Check；
- RefreshCw；
- GripVertical；
- AlertTriangle；
- Trash。

### 24.4 Video Episode Table

列：

| 列 | 内容 |
|---|---|
| 排序 | Drag Handle + `episode_order` |
| 集号 | `episode_no`，可编辑 |
| 文件 | 文件名 |
| 时长 | 原视频时长 |
| 上传 | 上传/校验状态 |
| 解析 | 单集 Task 状态 |
| 剧本 | `video_script_unit` 状态 |
| 原视频 | 到期时间/已过期 |
| 操作 | 查看、重试、重新生成、排除、删除 |

### 24.5 行尺寸

- 稳定行高；
- 长文件名省略并 Tooltip；
- 状态文案允许换行；
- Spinner 不改变列宽；
- 操作使用图标菜单；
- 错误详情在行下方展开，不嵌套 Card。

### 24.6 排序

默认：

- 高置信集号自然排序；
- 无集号按上传顺序暂排；
- 混合情况标记待确认。

人工：

- 拖动调整 `episode_order`；
- 直接编辑 `episode_no`；
- 保存创建新 Asset Set Version；
- 已启动 Run 固定读取启动时 Snapshot；
- Current Version Badge 更新。

### 24.7 分批上传

`collecting` 时：

- 上传按钮保持可用；
- 每批追加；
- 新成员立即显示；
- 只展示上传、排序和完整度状态，不提前解析；
- 不自动 Seal；
- 页面离开不代表上传完成。

### 24.8 上传完成

用户点击“上传完成”：

```text
Complete Check
-> 无问题：Seal
-> 有缺集：显示缺口
-> 有重复：要求处理
-> 有失败：要求重试或确认不完整继续
-> 有无集号：要求补齐并确认业务集号和顺序
```

### 24.9 缺集

显示：

```text
缺少：第 6 集、第 12 集
```

操作：

- 继续上传；
- 调整集号；
- 按不完整材料继续。

最后一项必须明确确认。

### 24.10 重复

同集号行高亮：

- 展示文件名、大小、时长；
- 用户改集号；
- 排除其中一个；
- 不自动覆盖。

### 24.11 逐集解析

状态：

```text
等待处理
处理中
已完成
失败
已排除
源视频已过期
源视频已删除
```

每集：

- 可查看高还原剧本；
- 可编辑；
- 可保存；
- 可重新生成；
- 失败可重试。

整个解析步骤只统一确认一次。

### 24.12 性能

第一版最多 100 个视频：

- 50 条以上采用窗口化或分页渲染；
- 不同时挂载 100 个剧本编辑器；
- 只加载展开行详情；
- Progress Event 合并刷新；
- 表格操作不因 SSE 高频更新丢失焦点。

## 25. 视频后续视图

### 25.1 Reference Scripts

- 按 `episode_order` 聚合；
- 可跳转单集；
- 显示材料完整度；
- 显示缺失集；
- 不作为单集编辑真源。

### 25.2 Script Analysis

独立 Artifact View，但不是独立 Skill。

支持：

- 查看；
- 编辑；
- AI 修改；
- 重新生成；
- 版本历史；
- 确认。

### 25.3 Adaptation Options

Agent 主动提出 2-3 套方案。

用户可以：

- 单选；
- 组合；
- 自定义；
- 要求 AI 修改。

UI 使用单选/Checkbox/编辑区，不把选项只放聊天文本。

用户提交改编方法前，同一确认面板补齐目标集数、单集时长和其他 Creation Config。提交成功后由服务端返回：

- Adaptation Decision Snapshot ID；
- Creation Config Snapshot ID；
- Approval Resolution；
- 下一可执行动作。

编辑器文字选区仍使用 Selection Snapshot，两者不能复用或混显。用户改变已确认方法时，UI 必须明确会生成新 Decision Snapshot 和 Brief 新版本。

### 25.4 Adaptation Brief

Brief 是进入新剧本创作前的强制确认点。

支持：

- 字段级编辑；
- AI 修改；
- 版本历史；
- 确认；
- 返回分析。

未确认：

- 不进入 Story Seed；
- 不询问后续无关配置；
- 不创建新剧本 Candidate。

### 25.5 后续共享流程

Brief 确认后显示：

```text
Story Seed
Series Blueprint
Episode Cards
Script Units
```

使用非小说共享组件，但导航仍归属：

```text
video_reference_creation
```

## 26. 小说 Skill UI

### 26.1 目录

```text
输入材料
故事圣经
原文拆集
分集卡
分集剧本
完整剧本
候选与最终稿
```

### 26.2 输入

- 选择 TXT/MD/DOCX/PDF；
- 标记为小说来源；
- 显示解析状态；
- 询问目标集数和单集时长；
- 已有明确分集标记时允许保留。

### 26.3 原文拆集

- 显示集号；
- 原文范围；
- 字数；
- 边界理由；
- 缺失/重叠检查；
- 子项编辑；
- 步骤统一确认。

### 26.4 剧本

复用 Script Unit Workbench。

## 27. 非小说 Skill UI

### 27.1 目录

```text
输入材料
素材库
故事种子
整剧蓝图
分集卡
分集剧本
完整剧本
候选与最终稿
```

### 27.2 输入

允许多份：

- 故事大纲；
- 集纲；
- 人物设定；
- 世界观；
- 台词；
- 其他文本。

UI 明确本次使用哪些 Asset。

### 27.3 材料完整度

Material Bank 显示：

- 用户明确事实；
- 整理结构；
- 缺失信息；
- 可能新增内容；
- 来源引用。

### 27.4 扩写策略

体量不足时：

- 不直接进入 Story Seed；
- Agent 提出扩写策略；
- 用户确认；
- 确认结果进入 Run Config/Approval。

## 28. Candidate 与 Final UI

### 28.1 Candidate 列表

每个 Candidate 显示：

- 名称；
- 来源 Skill；
- 来源 Run；
- 剧本集数；
- 创建时间；
- 当前版本；
- 是否曾为最终稿；
- 查看；
- 设为最终稿。

### 28.2 Final Selection

当前最终稿在 Header 和目录中明确标记。

首次选择和改选：

- 获取 Preview；
- 显示当前与目标 Candidate；
- 二次确认；
- 不删除旧 Final Selection；
- 不删除其他 Candidate。

### 28.3 完成后的修改

最终稿继续编辑：

- 原版本保留；
- 创建 Revision Run；
- 产生新 Candidate；
- 不自动替换最终稿；
- 用户再次确认改选。

## 29. Version History

### 29.1 入口

Workspace Header 的 History 图标打开 Drawer。

### 29.2 内容

显示：

- Version；
- 创建时间；
- 创建方式；
- 创建者类型；
- Base Version；
- 状态；
- Approval；
- 来源；
- 查看；
- 基于此版本继续。

### 29.3 历史查看

历史版本只读。

用户要修改历史版本：

- 显式“基于此版本创建修订”；
- 创建 Revision Run；
- 不移动 Current Pointer；
- 不覆盖当前版本。

## 30. 材料与来源追踪

### 30.1 Materials View

使用紧凑表格：

- 名称；
- 类型；
- 大小；
- 状态；
- 解析状态；
- 上传时间；
- 视频到期时间；
- 被哪些 Run 使用；
- 操作。

### 30.2 Source Trace

Artifact 可以打开来源侧栏：

- 上游 Artifact Version；
- Asset；
- Episode/Time Range；
- 来源状态；
- Dependency Path。

普通视图显示摘要，高级详情按需展开。

### 30.3 过期

视频过期显示：

```text
原视频已到期删除
已有剧本和分析仍可使用
重新解析需要重新上传
```

不把已有 Artifact 置灰为不可用。

## 31. Connection 与服务状态

### 31.1 状态

```text
connected
reconnecting
snapshot_syncing
offline
service_unavailable
```

### 31.2 展示

- Connected 不持续占据显眼空间；
- Reconnecting 在 Header 显示；
- Snapshot Syncing 禁止关键写操作；
- Offline 保留本地草稿；
- Service Unavailable 提供重试。

### 31.3 写操作

连接状态未知时：

- 不自动生成新 Idempotency Key 重发；
- 查询 Command；
- 刷新 Snapshot；
- 使用原 Key 恢复。

## 32. 可访问性

### 32.1 键盘

必须支持：

- Tab 顺序；
- Enter/Space 激活；
- Esc 关闭 Menu/Dialog；
- Arrow 导航 Menu/Tabs；
- 键盘调整面板宽度；
- 编辑器快捷键；
- 焦点返回触发按钮。

### 32.2 Focus

- 打开 Approval 时聚焦标题或首个主要动作；
- 保存错误聚焦错误摘要；
- Dialog 关闭后焦点返回；
- SSE 更新不能抢走编辑器焦点；
- 表格状态变化不能重置当前行焦点。

### 32.3 语义

- 正确 Heading 层级；
- Nav 使用 `nav`；
- 表格使用 Table 语义；
- 状态使用文本；
- Progress 使用 `aria-valuenow`；
- Error 使用合适 Live Region；
- 不把图标字符当唯一标签。

### 32.4 颜色

- 文本对比度满足 WCAG AA；
- 成功/警告/失败同时使用图标和文案；
- Focus Ring 清晰；
- Disabled 与 Read-only 可区分。

## 33. 视觉规范

### 33.1 风格

内部生产工具采用：

- 安静；
- 克制；
- 高信息密度但层级清楚；
- 中性色为主；
- 语义色点缀；
- 编辑器优先。

### 33.2 禁止

- 紫蓝渐变主色；
- 单一深蓝/Slate 大面积统治；
- Beige/Cream 页面；
- 装饰性光球；
- Bokeh；
- 大圆角营销卡片；
- 玻璃拟态；
- Hero；
- 过度动画；
- 每个 Section 浮动成卡片。

### 33.3 形状

- Card 圆角不超过 8px；
- Button、Input、Menu 使用一致半径；
- 工具图标按钮保持固定方形尺寸；
- 进度、徽标和状态不会改变容器尺寸。

### 33.4 字体

中文优先系统字体栈。

不按视口宽度缩放字体。

Letter Spacing：

```text
0
```

### 33.5 Motion

只使用：

- Panel 展开；
- Menu/Dialog；
- 状态过渡；
- Progress；
- 保存成功反馈。

遵循 `prefers-reduced-motion`。

## 34. 性能

### 34.1 首屏

作品入口：

- 不加载 Artifact 编辑器；
- 不加载 Lexical；
- 不订阅所有 Project Event；
- 只查询作品列表。

### 34.2 Workbench

- Capability 领域组件懒加载；
- Script Editor 只在需要时加载；
- Artifact Payload 按需 Query；
- Message 分页；
- Event 不保存在无限前端数组；
- 视频表格窗口化；
- 大型历史 Drawer 延迟加载。

### 34.3 Event 更新

- Progress Event 合并；
- Query Invalidation 去抖；
- 不让每个 Token 触发 React Render；
- 不把模型流式 Token 作为第一版业务 Event；
- 终态立即刷新。

### 34.4 内存

- 切换 Project 清理不再使用的编辑器实例；
- Query Cache 有合理回收；
- Blob URL 使用后释放；
- 不把视频文件读成 Data URL；
- 不在 React State 保存完整视频。

## 35. 前端数据安全

### 35.1 不保存

localStorage/IndexedDB 不保存：

- Provider Key；
- Authorization Header；
- 视频 Blob；
- 完整上传文件；
- 其他 Project 的混合缓存；
- 服务端绝对路径。

### 35.2 草稿

本地草稿包含业务内容，因此：

- 明确只用于当前浏览器恢复；
- Project 删除后清理；
- 保存后清理；
- 不写日志；
- 不发送第三方监控；
- 键包含 Project ID。

### 35.3 HTML

- 不渲染未经清洗的模型 HTML；
- Artifact 使用结构化组件；
- Markdown 输出使用安全白名单；
- Server Manifest 不允许任意 HTML。

## 36. 现有前端迁移

### 36.1 保留

现有可复用：

- React/Vite/TypeScript；
- Lexical Script Editor；
- Selection Snapshot；
- Script Stable IDs；
- Panel Resize；
- Collapse/Restore；
- Dirty Navigation Guard；
- Error Boundary；
- Vitest/Testing Library；
- Playwright；
- Lucide；
- TXT 下载基础；
- Agent Timeline 基础。

### 36.2 拆分

现有 `App.tsx` 拆为：

```text
AppRouter
ProjectIndexPage
WorkbenchPage
WorkbenchDataProvider
WorkbenchShell
ProjectEventProvider
DraftProvider
```

### 36.3 替换

| 现有 | 目标 |
|---|---|
| `projectPickerOpen` Modal | 独立 `/projects` 页面 |
| `SourceMode` Select | Skill Menu + Capability Ref |
| `source_mode` 导航 | Capability UI Manifest |
| `FileAttachment` Base64 | Asset Upload Session |
| Run SSE | Project SSE 主流 |
| App 内多个资源数组 | Query Cache |
| 手工 Merge Event 时间排序 | Seq Event Reducer |
| ArtifactWorkspace 分支 | Artifact UI Registry |
| 聊天卡片承载主要 Approval | Workspace Approval Bar |
| 完整 Run 响应带全部内容 | Snapshot + 精确 Query |

### 36.4 ArtifactWorkspace

当前通用展示与领域编辑混合。

迁移：

1. 提取 Workspace Header；
2. 提取 Artifact Status；
3. 提取 Generic Inspector；
4. 将每种 Artifact Editor 注册到 Registry；
5. Script Workbench 独立；
6. 删除 SourceMode 分支；
7. 用 Artifact Type/View Key 查组件。

### 36.5 AgentPanel

迁移：

1. 保留 Timeline、Selection Reference 和附件；
2. SourceMode Select 替换为 Composer 内“能力”入口和 Skill Menu；
3. Skill 引用结构化；
4. Composer 根据 Snapshot Action 切换 Empty/Active/Context/Running/Paused/Error；运行中保持自由文本入口，只读问答即时处理，修订请求排队到安全检查点，新 Skill 请求不启动第二条主链；
5. Approval 主操作统一进入 Agent Timeline ApprovalCard，中间工作区只展示状态；
6. Message 分页；
7. 普通聊天与结构化 AI Revision 区分。

## 37. 测试合同

### 37.1 Unit

- Capability Registry 正确映射组件；
- Unknown Capability 安全降级；
- Unknown Event 不崩溃；
- Event Seq 去重和 Gap；
- Action ID 映射；
- Status Label；
- URL Target；
- Panel Width Clamp；
- Draft Key Project 隔离；
- Empty String 保存；
- Dirty Navigation Guard；
- Natural Episode Ordering。

### 37.2 Component

- Project List；
- Quick Start；
- Capability Menu；
- Composer 六状态与 Action 投影；
- Asset Upload Tray；
- Process Navigation；
- Approval Bar；
- Impact Preview；
- Version Conflict；
- Script Unit Workbench；
- Video Episode Table；
- Adaptation Brief；
- Final Selection。

### 37.3 Integration

- Snapshot 后建立 SSE；
- SSE Gap 重新 Snapshot；
- 保存后 Artifact Query 更新；
- 版本冲突保留草稿；
- Approval 过期刷新；
- 上传不启动 Run；
- Skill 点击不启动 Run；
- 配置确认后启动；
- 活动 Run 锁定自由消息但保留状态控制；
- 不同 Project 可切换和并行。

### 37.4 Playwright

必须覆盖：

1. 新建作品；
2. 入口快速开始；
3. 打开/切换作品；
4. 重命名；
5. 删除影响确认；
6. 小说链主要确认点；
7. 非小说链主要确认点；
8. 视频多批上传；
9. 人工排序；
10. 缺集继续；
11. 单集失败重试；
12. Brief 确认；
13. Script Unit 编辑保存；
14. AI Selection 修改；
15. Pause/Resume；
16. SSE 断线恢复；
17. 刷新恢复 Pending Approval；
18. 最终稿选择和改选。

### 37.5 Visual QA

每个关键页面在：

```text
1280 x 720
1440 x 900
1920 x 1080
125% zoom
```

检查：

- 无文字重叠；
- 无按钮溢出；
- 无面板覆盖；
- 长文件名不撑开；
- 状态变化不跳动；
- Dialog 在视口内；
- Sticky Action Bar 不挡内容；
- Script Editor 可用；
- Agent 折叠/展开正常；
- 视频表格列稳定。

### 37.6 Accessibility

- Axe 基础扫描；
- Keyboard-only 关键路径；
- Focus Trap；
- Focus Return；
- Live Region；
- Contrast；
- Tooltip 可访问；
- Reduced Motion。

## 38. 三条链验收

### 38.1 小说

- 上传小说；
- Skill 显式/自动识别；
- 确认配置；
- 左侧目录正确；
- 主要产物逐步确认；
- Script Units 单集编辑；
- 全集统一确认；
- 生成 Candidate；
- 选择 Final。

### 38.2 非小说

- 多份材料可选择；
- Material Bank 来源清楚；
- 体量不足先确认扩写；
- Story Seed/Blueprint/Cards 可编辑；
- 运行中消息锁定；
- 暂停恢复；
- 完整剧本。

### 38.3 视频

- 多批上传不自动结束；
- 文件名自然排序；
- 可人工排序；
- 重复/缺集/失败可见；
- 每集 Task 状态；
- 单集剧本可编辑和重试；
- 解析步骤统一确认；
- Analysis 可编辑和确认；
- Options 可选择/组合；
- Brief 必须确认；
- 后续复用非小说组件；
- 保持一个 Capability Run；
- 原视频过期后已有产物仍可用。

## 39. 第一版不做

- 移动端；
- 营销首页；
- 多人协同编辑；
- 在线光标；
- 评论 @ 人；
- 复杂 RBAC；
- 自定义 Dashboard；
- 用户拖拽搭建 Workflow；
- Capability 下发任意 React 代码；
- 通用 JSON 编辑器编辑未知 Artifact；
- 每个 Skill 独立聊天页；
- 每个 Skill 独立上传组件；
- 每个 Skill 独立 Approval 系统；
- 所有 Event 变成聊天气泡；
- 运行中普通聊天；
- 自动三方合并版本冲突；
- 自动猜测视频上传完成；
- 自动选择重复集；
- 逐集确认按钮；
- 把 `scripts` 当编辑真源；
- 大面积 Card Wall；
- Hero 和装饰背景；
- 移动端抽屉适配。

## 40. 实施顺序

### 40.1 P0：通用 Shell

1. React Router；
2. Project Index Page；
3. Workbench Shell；
4. API Generated Types；
5. TanStack Query；
6. Project Snapshot；
7. Project SSE Provider；
8. Connection Recovery；
9. Skill Menu；
10. Server-driven Available Actions。

### 40.2 P1：编辑和审批

1. Artifact UI Registry；
2. Process Navigation；
3. Structured Artifact Editors；
4. Lexical Script Workbench 接入；
5. Local Draft；
6. Version Conflict；
7. Approval Bar；
8. Impact Preview；
9. Candidate/Final；
10. Export。

### 40.3 P2：三个 Skills

1. 小说目录和编辑器映射；
2. 非小说目录和编辑器映射；
3. Asset Set Workspace；
4. Video Episode Table；
5. Reference Scripts；
6. Script Analysis；
7. Adaptation Options；
8. Adaptation Brief；
9. 共享后续链 UI；
10. 三条链 E2E。

P0/P1/P2 是本协议内部实现优先级，不改变项目总阶段编号。

## 41. 完成门禁

进入三个 Skills 的输入、配置、步骤和 Artifact Schema 设计前，本合同必须满足：

- [x] 作品入口页已定义；
- [x] 工作台三栏 Shell 已定义；
- [x] Skill 菜单和自动路由已定义；
- [x] 零 Capability 交互已定义；
- [x] Capability/Artifact UI Registry 已定义；
- [x] Server State 与 Local State 已分离；
- [x] Snapshot/SSE 恢复已定义；
- [x] 编辑、草稿、版本冲突和 Selection 已定义；
- [x] 逐步 Approval 和下游影响已定义；
- [x] 视频分批、排序、缺集、逐集状态和统一确认已定义；
- [x] 三个 Skills 的目录和主要视图已定义；
- [x] Candidate、Final、Export 已定义；
- [x] 现有 React 前端迁移边界已定义；
- [x] 视觉、可访问性、性能和测试门禁已定义；
- [ ] 通过原型或实现后的桌面可用性评审；
- [ ] 通过真实几十集视频的工作台压力验收。

后两项属于实现和验收门禁，不阻止继续完成阶段 2 的三个 Skills Schema 设计。

## 42. 剧本检查 UI 补充

权威合同：

```text
content-semantics-and-quality-review-contract.md
```

三个 Capability 的左侧流程目录固定增加“剧本检查”，不增加第四个 Skill 菜单项。

触发和展示：

1. 整个剧本步骤统一确认后自动进入“检查中”，不询问是否审核；
2. 检查中剧本可读但只读，Composer 切换为 Running 状态并保留自由文本与运行控制；只读问答可即时处理，修改请求等待检查安全点；
3. `passed` 显示“已通过”并自动进入 Candidate；
4. `action_required` 显示“需处理”，按严重级别、集数和场次展示证据；
5. 页面只显示一个总体返工目标；
6. blocker 不显示“保留当前版本继续”；
7. high/medium 风险覆盖必须显示二次确认；
8. 检查失败显示失败批次、已生成内容保留和重试操作；
9. 刷新、切换作品和服务重启后从 Snapshot 恢复；
10. 用户不看到外部 Agent 编号、Prompt、模型思考或内部路由 ID。

UI 状态：

```text
待开始 | 检查中 | 需处理 | 已通过 | 检查失败
```
