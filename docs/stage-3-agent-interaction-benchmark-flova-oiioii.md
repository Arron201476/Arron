# 阶段 3 Agent 交互竞品调研：Flova 与 OiiOii

日期：2026-07-30

## 1. 调研目的与证据边界

本调研用于阶段 3 收口和阶段 4 视觉、组件与交互设计，不改动当前低保真原型代码。

证据范围：

- Flova：已登录实际产品、已有项目只读体验、公开首页和官方 Agent 使用说明；未发起生成、未重新生成、未消耗积分。
- OiiOii：公开首页和 2026 年 7 月 14 日更新的正式版使用手册；登录后工作区未实操，相关判断均标记为“官方手册观察”。
- 当前项目：`stage-3-information-architecture.md`、阶段 3 低保真原型和验收矩阵。

主要来源：

- [Flova Agent 官方说明](https://www.flova.ai/docs/en/features/agent)
- [Flova 产品](https://flova.tv/zh-CN/)
- [OiiOii 产品](https://www.oiioii.tv/)
- [OiiOii 正式版使用手册](https://ecncw7du1qtr.feishu.cn/wiki/Ds4Ow4yguinSHaktxeXcWls5nNh)

## 2. Flova 实际体验结论

### 2.1 首页入口

Flova 首页使用一个统一自然语言输入框，输入框内提供：

- 文件；
- 模型；
- Skill；
- 资产库；
- 语音输入；
- 发送。

Skill 是可显式选择的生产能力，但不是用户开始任务的必选项。用户也可以直接描述目标，由 Agent 判断如何推进。

证据：

- [已登录首页](../design/research/flova-oiioii-agent/13-flova-logged-home-project-click.png)
- [未登录 Skill 点击后的登录门禁](../design/research/flova-oiioii-agent/12-flova-skill-click.png)

### 2.2 项目工作区

Flova 登录后的实际项目工作区不是纯聊天界面，而是三栏生产工作台：

```text
左侧：故事板、关键元素、镜头、音频层、未归类素材
中间：当前素材或镜头预览、局部操作和局部生成
右侧：Agent 对话与执行时间线
```

顶部还提供媒体空间、时间线、文档和导出等项目级视图。

证据：

- [真实项目工作区](../design/research/flova-oiioii-agent/15-flova-live-project-workspace.png)

### 2.3 Agent 对话不是普通消息流

实际项目中的右侧对话由多种生产消息组成：

- 用户指令，可携带镜头、元素或素材引用；
- Agent 执行状态，例如 `Media Assets 已完成` 或生成暂停；
- 可展开的执行过程；
- 分步骤状态，例如分析、资产配置、提示词编写、素材生成；
- 生成结果缩略图和可点击产物；
- 失败位置和停止原因；
- 媒体或时间线同步事件；
- 每条关键 AI 消息后的回退入口；
- 结果说明和下一步建议。

这说明聊天区承担的是“生产控制台”，不是单纯问答区。

### 2.4 Agent、Skill 与项目规格的分工

Flova 官方说明明确区分：

- Agent：读取项目上下文并调度后续任务；
- Skill：定义该类型内容如何制作，包括流程、参考使用、模型和提示规则；
- Final Video Spec：保存时长、画幅、语言和风格等全局规格。

该结构与本项目已经确定的 `General Agent + Domain Capability + Run Config/Artifact + Runtime` 边界一致。

证据：

- [Agent 官方说明](../design/research/flova-oiioii-agent/09-flova-agent-docs.png)
- [可见执行过程说明](../design/research/flova-oiioii-agent/10-flova-visible-execution.png)

### 2.5 Agent 与手动编辑并存

Flova 同时提供：

- 右侧 General Agent 对话；
- 中间当前元素或素材的局部调整输入；
- 明确的提示词、重新生成、下载、裁剪、超清等确定性操作。

局部输入只作用于当前选中对象，不是第二条独立项目对话。官方说明还提到 Agent 执行与手动编辑发生冲突时，应通过冲突面板让用户选择保留版本。

## 3. OiiOii 官方手册观察

### 3.1 首页与能力入口

OiiOii 首页使用左侧全局导航：

- 发现；
- 新建；
- 项目；
- 资产；
- 技能。

首页把“进入创作”作为主入口，同时展示剧情故事创作、剧本智能分集、爆款复刻和 Skill 技能制造机等能力。

证据：

- [OiiOii 首页](../design/research/flova-oiioii-agent/01-oiioii-home.png)

### 3.2 统一输入区

正式版使用手册展示的输入区包含：

- 自然语言故事或剧本输入；
- 添加文件；
- 上传剧本；
- 角色、场景和风格参考；
- 我的资产；
- 创作模式选择。

模式以输入框下方的选项呈现，附件和参考信息以输入框内的上下文附件呈现。

证据：

- [故事动画入口](../design/research/flova-oiioii-agent/04-oiioii-seedance-flow.png)
- [故事、剧本和参考输入](../design/research/flova-oiioii-agent/05-oiioii-input-story.png)

### 3.3 两栏式 Agent 工作方式

OiiOii 手册中的主要工作方式是：

```text
左侧：Agent 对话、配置追问、流程推进
右侧：画布、分镜、素材和编辑结果
```

用户发送创意后，Agent 不直接隐藏执行，而是在对话中继续收集：

- 影片时长；
- 画面比例；
- 对白语言；
- 情绪关键词；
- 其他自由要求。

结构化选择卡嵌入对话过程，右侧画布负责承载产物。

证据：

- [启动生成与结构化追问](../design/research/flova-oiioii-agent/06-oiioii-start-generation.png)

### 3.4 分镜产物可直接编辑

OiiOii 使用四宫格或九宫格展示分镜，支持：

- 选中单个分镜修改；
- 调整分镜顺序；
- 生成或重新生成；
- 进入其他分镜编辑动作。

证据：

- [分镜流程](../design/research/flova-oiioii-agent/07-oiioii-two-column-storyboard.png)
- [分镜编辑细节](../design/research/flova-oiioii-agent/08-oiioii-storyboard-grid-detail.png)

### 3.5 不应照搬的限制

OiiOii FAQ 建议一集使用一个项目，原因是项目上下文过长可能导致效果下降或画布异常。

本项目不能照搬这一点。本项目已经确定“一部作品对应一个连续工作台和一个主对话”，且需要整剧分析、跨集一致性、批量视频解析和候选剧本管理。应由 Context Pack、Artifact 依赖和 Worker 最小上下文解决，而不是拆成每集一个项目。

## 4. 与当前项目的对照

| 维度 | Flova | OiiOii | 当前项目 | 结论 |
|---|---|---|---|---|
| 工作区 | 三栏 | 两栏 | 三栏 | 保留三栏，不改成固定两栏 |
| Agent 定位 | 项目操作员 | 创作流程主入口 | General Agent | 方向一致 |
| Skill | 可显式选择，也可自动路由 | 能力入口和创作模式 | 三个 Domain Capabilities | 方向一致 |
| 产物承载 | 独立专业面板 | 右侧画布 | 中间 Active Workspace | 方向一致 |
| 对话消息 | 执行步骤、产物、同步、失败、回退 | 追问卡和流程引导 | 普通气泡加少量进度卡 | 需要重点增强 |
| 局部 AI 修改 | 中间局部输入 | 画布内编辑 | 只从右侧 Agent 发起 | 应增加严格限定范围的快捷入口 |
| 运行状态 | 会话内可见，可停止、回退、分支 | 会话内逐步推进 | 独立进度卡 | 需融合到消息时间线 |
| 手动编辑 | 与 Agent 并行，冲突时处理 | 画布直接编辑 | 中间编辑并保存版本 | 方向一致 |
| 长内容 | 项目状态和专业面板承载 | 官方建议每集一个项目 | 一部作品持续工作台 | 不采用 OiiOii 的每集项目限制 |

## 5. 阶段 4 必须落实的交互决策

### 5.1 保留三栏，但允许收起和扩展

正式工作状态继续使用：

```text
流程与产物目录 + Active Workspace + General Agent
```

同时增加：

- 左侧目录可收起；
- Agent 面板可在默认宽度和扩展宽度之间切换；
- 空作品和意图澄清状态优先扩大 Agent 区；
- 打开具体产物后恢复标准三栏。

不新增完全独立的两栏产品页面。

### 5.2 Agent 面板不能只用普通聊天气泡

阶段 4 至少设计以下消息组件：

- `user_instruction`：用户指令和引用对象；
- `clarification_card`：最少必要追问和结构化选项；
- `plan_card`：准备执行的步骤和约束；
- `run_progress_card`：当前步骤、批次进度、暂停和取消；
- `approval_card`：待确认产物和确认动作；
- `artifact_result_card`：产物摘要、版本和定位入口；
- `failure_card`：失败步骤、原因和重试；
- `sync_event`：版本、依赖和下游同步事件；
- `context_chip`：文件、选区、Artifact 和 Skill 引用。

点击产物引用必须定位到中间工作区对应对象。

### 5.3 局部 AI 修改采用“快捷发起”，不建设第二个聊天

中间工作区可以提供：

```text
选中子项或文本
-> 点击“让 Agent 修改”
-> 自动把目标对象和选区加入右侧 Agent 上下文
-> 用户补充要求并发送
```

可以使用紧凑的局部命令条，但不能形成第二套自由对话历史。项目仍只有一个主对话。

### 5.4 第一版不开放并发写入

Flova 支持 Agent 与手动编辑并行，并通过冲突面板解决冲突。该能力依赖成熟的分支、合并和冲突处理，第一版不照搬。

当前项目继续保持同一作品只有一个活动写 Run，但运行中不应只显示一个“死掉的禁用输入框”，应替换为明确的运行控制区：

- 查看当前步骤；
- 暂停并修改；
- 取消；
- 查看已完成产物；
- 失败时重试。

普通新生成任务仍被阻止。

### 5.5 不暴露模型和内部执行术语

Flova 和 OiiOii 面向更广泛的生成平台，因此公开模型选择和大量创作模式。本项目第一版目标用户不熟悉 Agent，且最终产物固定为剧本，因此：

- 不在主界面暴露模型名；
- 不显示内部 Step ID、Prompt、Rule 或 Worker；
- Skill 菜单只显示三个已确认的用户能力；
- Agent 自动路由时显示用户可读的能力名称和理由；
- Rule 继续由 Skill 内部按需加载。

### 5.6 确认点保持业务级，而不是每个模型动作都确认

借鉴两者的“过程可见”，但不把每个内部动作都做成审批。

用户只确认：

- 启动配置；
- 主要用户可见 Artifact；
- 缺集继续和扩写策略；
- 整批剧本；
- Adaptation Brief；
- 最终稿选择。

模型分析、提示词编写和单次工具调用只显示状态，不要求用户逐项确认。

## 6. 阶段 4 设计任务

进入阶段 4 后，优先完成：

1. 三栏尺寸、收起和 Agent 扩展状态；
2. Agent 消息组件和对话时间线；
3. Composer 的文件、Skill、上下文引用和运行中状态；
4. 结构化追问、审批、失败、暂停和恢复卡片；
5. 中间工作区到 Agent 的“让 Agent 修改”路径；
6. 产物引用定位和版本同步事件；
7. 小说、非小说和视频三条主链的高保真关键页面；
8. 1280、1440 和 1920 宽度下的可读性与长内容验证。

## 7. 收口判断

本次调研不要求推翻阶段 3 信息架构。当前“统一作品工作台 + 三栏 + 单一 General Agent + Domain Skills + 独立 Artifact”的方向与主流内容生产 Agent 一致。

阶段 4 的重点不是重新讨论页面数量，而是把右侧 Agent 从“附属聊天栏”升级为真正的生产控制台，并让它与中间产物编辑、版本、确认和运行状态形成清晰联动。
