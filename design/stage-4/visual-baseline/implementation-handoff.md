# 阶段 4 UI / UE 最终实现交接

状态：阶段 4 已通过  
Figma File Key：`0W4vkvzJKrosx2XyYSVTbS`  
机读真源：`design/stage-4/figma-final-state.json`

## 1. 权威顺序

阶段 5、6 实施时按以下顺序解释交互和视觉：

1. 最终 Figma 节点和 `figma-final-state.json`；
2. 本交接文件；
3. `docs/stage-4-ui-qa-checklist.md`；
4. `design/stage-4/visual-baseline/DESIGN.md`；
5. 阶段 3 原型和更早设计稿只用于追溯，不得覆盖最终基线。

`design/stage-4/figma-state.json`、旧 v2.4 试验板、历史 Refined Frame ID 和“方向 A/B 候选”均不是实现输入。

## 2. 最终组件源

| 组件 | Node ID | 合同 |
|---|---|---|
| Button | `239:108` | 3 种语义 x 6 状态 |
| IconButton | `239:184` | 3 种语义 x 5 状态；必须有 Tooltip 与 `aria-label` |
| Composer Action | `302:135` | Attach / Capability / Send / Pause / Resume / Retry x 5 状态 |
| Agent Composer | `238:117` | Empty / Active / Context / Running / Paused / Error |
| ApprovalCard | `240:51` | 业务级确认、版本与影响说明 |
| Delete Dialog | `240:52` | 阻断性二次确认 |
| Agent Event | `240:111` | 运行与同步事件 |
| Failure Card | `247:223` | 失败证据与局部重试 |

组件质量展示板为 `286:229`。正式 Composer 实例证据为 `312:20`、`312:48`、`312:75`、`312:95`、`312:124`。

## 3. 页面与压力基线

最终 Figma 只保留 12 个产品页面：Cover、Foundations、Components、Project Entry、Workbench Core、Novel Skill、Non-novel Skill、Video Reference Skill、Script Workspace、Shared States、Responsive Stress、Prototype。

关键证据：

- 剧本已保存：`256:2`；
- 剧本未保存：`258:35`；
- 版本历史：`259:69`；
- 候选对比：`260:102`；
- 最终稿：`261:154`；
- 生成失败：`263:2`；
- 删除来源：`263:2554`；
- 不完整材料：`263:2652`；
- 检查点恢复：`263:2765`；
- 1280 / 1440 / 1920：`265:2` / `265:58` / `265:114`；
- 50 集顶部 / 底部：`266:47` / `266:147`；
- 最终原型入口：`267:2`。

## 4. Agent Composer 状态合同

Composer 固定在右侧 Agent 面板底部，统一高度 `160px`，内部采用 `32 / 80 / 48` 三段结构，宽度必须支持 `348 / 392 / 420px`。

| 状态 | 内容区 | 右侧动作 | 普通输入 |
|---|---|---|---|
| Empty | 通用占位说明 | Send Disabled | 可输入 |
| Active | 当前上下文与用户指令 | Send | 可输入 |
| Context | 选区、附件或产物引用 | Send | 可输入 |
| Running | 当前步骤、进度和保留说明 | Pause | 锁定自由文本 |
| Paused | 暂停位置与恢复说明 | Resume | 只允许当前状态支持的结构化操作 |
| Error | 失败信息和已保留内容 | Retry | 保留原指令，禁止静默覆盖 |

“运行中锁定”只表示禁止自由聊天或启动第二个写 Run，不表示整个 Composer 组件消失或不可操作。附件、能力入口和右侧状态动作是否可用，必须由服务端 `available_actions` 与当前收集步骤共同决定。

## 5. 审批与编辑合同

- 业务确认主操作只出现在右侧 Agent 时间线的 `ApprovalCard`，不得在中间工作区复制第二套确认按钮。
- 中间工作区负责 Artifact 阅读、编辑、版本和定位；顶部只保留当前步骤必要的非确认操作。
- 请求 Agent 修改只通过选区、子项上下文或 Composer 发起，不在每个步骤常驻“让 Agent 修改”。
- 手动编辑、Agent 修改和重新生成必须形成不同版本原因，旧版本保留。
- Approval 绑定精确 Subject Version；编辑后旧 Approval 失效，新版本重新进入确认。
- 暂停、恢复、重试是 Run/Task 控制，不得混入 Approval Option 伪装成业务确认。
- 结束本次运行放在 More Menu，必须二次确认，不能与暂停并列为主操作。

## 6. 用户侧命名

- Composer 工具栏入口显示“能力”。
- 菜单项显示具体 Skill 名称：小说转剧本、非小说文本转剧本、视频参考创作。
- `Capability`、`Run`、`Rule`、`Worker`、内部 Step ID 只用于技术合同，不直接暴露给用户。
- 请求仍使用结构化 `capability_ref`，不能从显示文案反向猜测协议字段。

## 7. 实现硬门禁

1. 复用正式组件源的尺寸、状态和语义，不按旧 Frame 重新描摹控件。
2. 三栏连续铺满，不做浮动页面卡片；Composer 必须与 Agent 面板底部贴合，`bottomGap=0`。
3. 运行、暂停、失败、审批和空输入状态不能共用一个模糊发送按钮。
4. 所有状态动作由服务端状态和受控 Action ID 投影，前端不得凭文案猜测。
5. 1440x900、1280x800、1920x1080 和 50 集目录必须进入浏览器回归。
6. DOM 阶段继续验证键盘顺序、Tooltip、`aria-label`、`focus-visible`、200% 缩放、屏幕阅读器和真实对比度；Figma 通过不替代这些验证。
7. 任何与本交接冲突的阶段 2/3 历史文档，按“最终 Figma优先”处理，并在阶段 5 合同收口时修正。
