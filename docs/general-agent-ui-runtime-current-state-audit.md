# 通用 Agent UI Runtime 现状盘点与迁移基线

状态：现状审计完成，作为改造设计输入  
日期：2026-08-21  
范围：作品工作台左侧流程与产物、中间 Artifact 工作区、右侧 Agent 交互及其后端投影协议

## 1. 结论

当前系统已经具备通用 Agent 的业务底座，但尚未形成通用 Agent UI Runtime。

已成立的部分：

- Project、Conversation、Skill Invocation、Run、Step Run、Artifact、Version、Approval、Event 已分层；
- Runtime 是运行状态、产物版本和审批的权威；
- 一个 Project 可以保存多个独立 Run，并保持一条主对话；
- Agent 每轮可以读取 Run Index、Active Run、Viewed Run、Artifact Set 和最小聚焦产物；
- Capability Manifest 已声明步骤、输出 Artifact、审批、输入类型和入口 UI Seed；
- 前端已经有通用三栏 Shell、Artifact 版本、选区定位、修改请求和 Renderer Registry 雏形。

尚未成立的部分：

- 后端没有输出可执行的 Workspace View Projection；
- Capability Public Definition 只公开入口图标和步骤摘要，没有导航、Artifact View、任务 View、审批 View 和 Composer 约束；
- 前端自行维护 Artifact 标签、顺序、隐藏规则、目录层级和 Renderer 选择；
- 视频剧本、普通剧本、合集、质量审核、配置卡和上传配置仍在 `App.tsx` 中按具体类型或 Capability ID 分支；
- 未引用 Skill 的普通生产请求没有通用 Artifact 创建和展示闭环；
- 新 Skill 仍需要修改前端主文件，不能只注册 Manifest、Schema 和受信任 View。

因此当前实现应定义为：

```text
通用业务 Runtime
+ 通用 Agent/上下文 Harness
+ 半通用工作台 Shell
+ 三个领域 Skill 的前端硬编码适配
```

不能定义为已经完成“Agent 根据任意 Skill 或输入实时构建工作台”。

## 2. 实时构建的准确含义

本项目的“实时构建”是运行时组合受信任组件，不是模型生成 React、HTML、CSS 或组件路径。

```text
Capability Definition / Artifact Schema
                 +
Runtime Snapshot / Available Actions
                 +
Agent Decision / Current Intent
                 +
Client Focus / Selection
                 ↓
      Workspace Projection Compiler
                 ↓
        Workspace View Model
                 ↓
 Frontend Registry 组合已编译组件
```

四类输入的权威边界：

| 输入 | 负责内容 | 权威性 |
|---|---|---|
| Capability Definition | 稳定步骤、产物类型、Schema、审批和展示提示 | 定义真源 |
| Runtime Snapshot | 当前状态、版本、可用操作、活动 Run 和审批 | 运行真源 |
| Agent Decision | 当前意图、目标、追问、临时交互建议 | 建议，必须经 Guard |
| Client Focus | 当前查看项、选区和本地展开状态 | 视图提示，不得改业务状态 |

模型可以选择注册过的 `view_key` 或提出临时 UI block 意图，但不能：

- 下发任意前端代码；
- 下发组件路径或动态 import 地址；
- 自定义 API URL；
- 绕过 Runtime 的 `available_actions`；
- 用聊天文本决定 Run、Approval 或 Artifact 状态。

## 3. 当前真实数据链

### 3.1 请求与执行

```text
Composer
-> POST Conversation Message
-> Main Agent Decision
-> Guard / Proposed Action
-> 用户确认并补齐输入配置
-> Runtime StartRun
-> Step / Task / Worker
-> Artifact Version / Approval / Event
-> Project Snapshot + SSE
-> React Workbench
```

### 3.2 Project Snapshot 当前返回

当前 `/api/v1/projects/{project_id}/snapshot` 返回：

- Project；
- Messages；
- Artifacts；
- Pending Approvals；
- Public Capabilities；
- Script Candidates 和 Final Selection；
- Active Run、Steps、Tasks、Available Actions；
- Proposed Actions；
- Activities；
- Revision Requests；
- Target Resolutions。

它没有返回：

- Workspace View Model；
- 左侧导航节点；
- Artifact View Descriptor；
- 聚合产物与子产物关系的展示策略；
- 当前页面可使用的受信任组件和插槽；
- Unknown Artifact 的安全降级策略结果。

前端只能根据原始对象二次推断，因此产生了当前耦合。

## 4. 当前三个 Skill 的产物结构

### 4.1 小说转剧本

```text
source_input
-> source_manifest (internal)
-> story_bible
-> episode_split
-> episode_cards
-> script_context[] (internal)
-> script_unit[] + script_handoff[] (handoff internal)
-> quality review
-> scripts
```

### 4.2 非小说文本转剧本

```text
source_input
-> source_manifest (internal)
-> material_bank
-> story_seed
-> series_blueprint
-> episode_cards
-> script_context[] (internal)
-> script_unit[] + script_handoff[] (handoff internal)
-> quality review
-> scripts
```

### 4.3 视频参考创作

```text
source_input
-> video_script_unit[]
-> reference_scripts
-> script_analysis
-> adaptation_options
-> adaptation_brief
-> story_seed
-> series_blueprint
-> episode_cards
-> script_context[] (internal)
-> script_unit[] + script_handoff[] (handoff internal)
-> quality review
-> scripts
```

这三条链路的步骤和产物已经由 Capability Manifest 声明。左侧目录不应再复制一份固定顺序。

## 5. 已经通用化的能力

| 能力 | 当前实现判断 | 目标归属 |
|---|---|---|
| Project 与一条主 Conversation | 已实现 | Agent Shell |
| Skill Invocation 与 Business Run 分离 | 已实现 | Agent Shell / Runtime |
| Active Run 与 Viewed Run 分离 | 已实现 | Runtime / View Context |
| Run/Step/Task 生命周期 | 已实现 | Runtime |
| Artifact 与版本历史 | 已实现 | Runtime + Generic Workspace |
| Approval 与 Available Actions | 已实现 | Runtime + Generic Interaction |
| SSE + Snapshot 回补 | 已实现 | Shell Transport |
| 用户选区与文字定位 | 已有可复用实现 | Agent Context Harness |
| 通用修改请求与候选版本 | 已有主体实现 | Agent Revision Harness |
| Asset、ZIP、Asset Set | 已有主体实现 | Generic Input Harness |
| Artifact 通用结构化阅读/编辑 | 部分实现 | Generic Artifact Renderer |
| 三栏工作台框架 | 已实现骨架 | Generic Workbench Shell |

这些能力必须保留，改造不能把它们重新塞回 Skill。

## 6. 当前前端耦合清单

### P0：阻止新 Skill 插件式接入

1. `App.tsx` 维护第二份 Artifact 标签表。
2. `App.tsx` 维护第二份跨三个 Skill 的 Artifact 顺序表。
3. `App.tsx` 硬编码内部 Artifact 类型集合。
4. 左侧只有 `script_unit` 和 `video_script_unit` 自动形成分集目录。
5. 中间工作区直接分支 `VideoScriptArtifactEditor`、`ScriptArtifactEditor` 和 `ScriptCollectionView`。
6. `ScriptCollectionView` 写死 `scripts -> script_unit`、`reference_scripts -> video_script_unit`。
7. 发送消息后补齐输入时按三个 Capability ID 推导 `source_type` 和视频配置。
8. 配置卡按 `video_reference_creation` 单独进入另一套逻辑。
9. Approval Card 在主文件内按 `quality_review`、`adaptation`、`volume_fit` 分支。

后果：第四个 Skill 即使后端 Manifest、Schema 和 Worker 全部存在，也不能自动得到正确目录、工作区、配置和确认交互。

### P1：展示元数据存在多真源

目前标签和字段展示至少来自：

- Capability Manifest 的 `label/ui`；
- `App.tsx` 的 `artifactLabels/artifactFlowOrder/internalArtifactTypes`；
- `artifactPresentation.ts`；
- `presentation.ts` 的 Capability/Step 标签；
- 各具体组件内部的标题和文案。

多真源会造成：

- Manifest 改步骤但左侧顺序不变；
- Artifact 新增后降级成技术名称；
- 相同产物在左侧、中间和 Agent 卡片名称不一致；
- 内部产物是否外显由前端版本决定，而不是定义决定。

### P1：Renderer Registry 只是选择器，不是 UI Runtime

当前 Registry 只有四种 Kind：

```text
document
script
script_collection
video_script
```

缺少：

- 导航策略；
- 子项/Scope 策略；
- View 插槽；
- 编辑模式；
- 支持操作；
- Schema 与 View 的兼容校验；
- Approval/Task/Composer Registry；
- Unknown View 的可观测降级。

### P1：`App.tsx` 承担过多编排

当前文件约 1200 行，同时负责：

- 页面路由；
- Project 列表；
- Snapshot/SSE；
- 当前 Artifact Focus；
- 消息发送和输入绑定；
- 左侧目录；
- Artifact Workspace；
- Agent Timeline；
- Run 配置；
- Revision；
- Approval；
- Composer；
- 视频批次。

这会让任何新领域 View 都自然地继续添加主文件分支。

### P2：通用文档 Renderer 仍有领域判断

`ArtifactDocument` 已能递归展示和编辑结构化数据，但仍直接识别：

- `episode_cards`；
- `script_unit`；
- `video_script_unit`；
- 剧本字段与场次结构。

这些可保留为注册组件，但不应继续存在于通用 Document Renderer 的主分支里。

## 7. 后端协议缺口

### 7.1 Capability Public Definition 不足

当前公开字段主要是：

```text
label / description / execution_mode / accepted_asset_kinds
commands / icon_key / menu_order
steps: id / kind / artifact_types / approval summary
```

需要增加由服务端确定性编译的 `ui_manifest`：

```json
{
  "capability_id": "video_reference_creation",
  "capability_version": "1.2.0",
  "navigation": [],
  "artifact_views": {},
  "task_views": {},
  "approval_views": {},
  "composer": {}
}
```

它不能成为第二份手写流程。应由以下真源编译：

- Capability Manifest 的 UI Seed、Step、Output、Visibility；
- Artifact View Registry；
- Approval View Registry；
- Task View Registry；
- 安全 Action Registry。

### 7.2 Artifact 缺少展示合同

Artifact 当前有稳定身份、类型、Scope、版本和依赖，但没有标准展示描述：

```text
view_key
label_key
visibility
navigation_group
child_scope
editor_mode
supported_actions
slot bindings
collection member relation
```

这些字段不应全部写进每个 Artifact 数据行，而应来自版本化 Artifact View Definition，并在 Workspace Projection 中解析。

### 7.3 Project Snapshot 缺少视图投影

需要新增服务端投影对象，而不是让 Agent 直接控制整个页面：

```json
{
  "workspace": {
    "navigation": { "runs": [], "active_node_id": "..." },
    "canvas": { "view_key": "...", "artifact_ref": {}, "slots": [] },
    "interaction": { "blocks": [], "actions": [] },
    "focus": { "source": "approval|url|server|local" }
  }
}
```

投影应由 Runtime 状态和注册定义确定性生成。Agent 只可以补充临时交互 block，不能覆盖业务状态。

## 8. 目标组件结构

### 8.1 Generic Workbench Shell

```text
WorkbenchPage
├─ ProjectHeader
├─ ProcessNavigation
├─ ArtifactCanvas
└─ AgentPanel
   ├─ ConversationTimeline
   ├─ InteractionBlockHost
   └─ Composer
```

### 8.2 Registry

```text
WorkspaceViewRegistry
ArtifactViewRegistry
NavigationRendererRegistry
InteractionBlockRegistry
ApprovalViewRegistry
TaskViewRegistry
ComposerExtensionRegistry
```

Registry 映射到编译进前端的受信任 React 组件，不接收任意模块路径。

### 8.3 首批受信任 View

通用 View：

- `document_view`；
- `structured_editor`；
- `collection_view`；
- `table_view`；
- `media_view`；
- `version_compare_view`；
- `unknown_artifact_inspector`。

领域 View：

- `episode_plan_editor`；
- `script_editor`；
- `script_collection_reader`；
- `media_script_review`；
- `adaptation_option_selector`；
- `quality_review_panel`。

领域 View 是可注册插件，不是 Skill 自己拥有的页面。多个 Skill 可以引用同一个 View。

## 9. 左侧导航目标

左侧不再按 Artifact Type 临时分组后套固定顺序，而是消费 `navigation.nodes`：

```json
{
  "node_id": "run:123/artifact:script_unit",
  "kind": "artifact_group",
  "label": "单集剧本",
  "order": 70,
  "visibility": "user",
  "scope_mode": "episode",
  "children": [],
  "status_summary": {}
}
```

应支持：

- Run 切换；
- Step/Artifact 分组；
- episode、scene、asset、range 等通用 Scope；
- 单项和集合；
- 内部节点隐藏但可供 Agent/依赖系统读取；
- 状态、失败、待确认和 stale 聚合；
- 未知 Artifact 的安全只读入口。

## 10. 中间工作区目标

`ArtifactCanvas` 只处理通用生命周期：

- 加载当前版本；
- 未保存修改保护；
- 保存新版本；
- 版本历史；
- 选区快照；
- Agent 修改稿预览；
- View 加载失败；
- Unknown View 降级。

具体内容由 `view_key` 对应组件处理。

视频剧本示例：

```json
{
  "view_key": "media_script_review",
  "artifact_ref": {
    "artifact_id": "...",
    "artifact_version_id": "..."
  },
  "slots": [
    { "slot": "source_media", "binding": "source_asset" },
    { "slot": "scene_locator", "binding": "payload.scenes" },
    { "slot": "editor", "binding": "payload.script_text" }
  ]
}
```

## 11. Agent 与普通输入如何驱动 UI

### 11.1 引用 Skill

1. Agent/用户选中 Capability；
2. Capability UI Manifest 决定允许的输入和配置 block；
3. Runtime 创建 Invocation；
4. 满足门禁后创建 Run；
5. Workspace Projection 根据新 Run 和 Artifact 实时增加目录与 View。

### 11.2 不引用 Skill

普通聊天仍可使用 Agent。若用户提出可落盘的生产请求：

1. Agent 判断是聊天回复、inline 执行、background task 或需要新增 Artifact；
2. Guard 校验是否有对应 Tool/Artifact Contract；
3. 有注册合同时创建通用 Artifact/Task，并投影到左侧和中间；
4. 没有注册能力时说明缺少什么，不伪造页面或业务状态。

第一版尚缺“通用 Artifact 创建 Tool”和 inline/background task 的完整执行器。因此 UI Runtime 改造不能虚假宣称任意输入都能产生产物，但协议必须预留此路径。

## 12. 文档冲突

`frontend-capability-ui-contract.md` 和 `stage-2-architecture-baseline.md` 已描述 Registry 方向，与本次目标一致。

需要在实施时修订的历史表述：

- `stage-5-t7-stage-6-7-handoff.md` 仍写“Run 活跃时禁止普通聊天”，与后续通用 Agent 架构基线冲突；当前权威规则是运行中允许普通聊天和只读操作，只阻止冲突写 Run。
- 同一交接文档声称“只通过 Capability UI Registry 注册领域视图”，但当前代码没有达到这一完成状态，应改为目标要求而非已实现结论。
- 旧文档中的固定三 Skill 页面清单只能作为第一版验收样本，不能继续作为 Shell 组件边界。

## 13. 迁移顺序

### M0：冻结现状与补合同测试

- 为三个 Skill 生成当前目录和 View 的 Golden Fixture；
- 覆盖多个 Run、内部 Artifact、未知 Artifact 和无 Skill 项目；
- 不改业务链路和现有 Artifact Payload。

### M1：建立后端 UI Definition Registry

- 定义受信任 `view_key`、navigation policy、scope mode、editor mode；
- 建立 Artifact/Approval/Task View Definition；
- Capability Compiler 校验所有引用；
- 未注册 View 使 Capability 不可用或进入明确降级，禁止静默猜测。

### M2：建立 Workspace Projection

- 基于 Project Snapshot、Run、Step、Artifact、Approval 编译导航和 Canvas Descriptor；
- 明确 Active Run、Viewed Run、Active Artifact 和 Pending Approval Focus；
- API 返回版本化 Workspace View Model；
- SSE 事件只触发刷新，不让前端重新推导业务结构。

### M3：拆分前端 Shell

- 从 `App.tsx` 提取 Workbench Controller、Process Navigation、Artifact Canvas、Agent Panel；
- 建立各类 Registry 和 Unknown View；
- 将版本、编辑、选区、Revision 等公共生命周期留在 Canvas Host。

### M4：迁移三个现有 Skill

- 小说和非小说共用 `script_editor`、`script_collection_reader`；
- 视频使用 `media_script_review`，但仍复用通用编辑、版本、定位和修改协议；
- episode cards 使用注册的 `episode_plan_editor`；
- adaptation/quality/volume-fit 迁入 Interaction/Approval Registry；
- 删除 `App.tsx` 中 Capability ID 和 Artifact Type 分支。

### M5：补通用输入路径

- 增加 inline/background task UI Projection；
- 增加通用 Artifact 创建 Tool/Contract；
- 验证不引用 Skill 时聊天始终可用，注册过的普通产物可以进入工作台。

### M6：回归与删除旧逻辑

- 三条完整链路回归；
- 多 Run、修改、审批、暂停恢复、失败重试、ZIP 和视频批次回归；
- 删除旧标签表、固定顺序表、内部类型表和主文件领域分支；
- 浏览器验证 1280x720、1440x900、1920x1080。

## 14. 文件级迁移地图

| 当前文件 | 处理 |
|---|---|
| `backend/internal/capability/types.go` | 扩展编译后的只读 UI Manifest 类型 |
| `backend/internal/capability/compiler.go` | 校验 View/Action/Scope 引用 |
| `backend/internal/capability/registry.go` | 输出 Public UI Manifest |
| `backend/internal/httpapi/runtime_handlers.go` | Snapshot 增加 Workspace Projection |
| `backend/internal/runtime/*` | 保持业务真源；新增只读投影服务，不迁入前端展示逻辑 |
| `frontend/src/App.tsx` | 拆分并删除领域特判 |
| `frontend/src/artifacts/artifactRendererRegistry.ts` | 升级为类型安全 Artifact View Registry |
| `frontend/src/artifacts/artifactPresentation.ts` | 元数据迁到生成类型/Registry，保留字段格式化工具 |
| `frontend/src/artifacts/ArtifactDocument.tsx` | 只保留通用文档能力，领域视图拆出注册 |
| `frontend/src/artifacts/ScriptArtifactEditor.tsx` | 注册为可复用领域组件 |
| `frontend/src/artifacts/VideoScriptArtifactEditor.tsx` | 注册为组合式媒体校对组件，不绑定 Skill |
| `frontend/src/artifacts/ScriptCollectionView.tsx` | 通过 relation descriptor 获取成员，不写死类型映射 |
| `frontend/src/types.ts` | 增加生成的 Workspace/View Contract 类型 |

## 15. 验收门禁

改造完成必须满足：

1. 增加一个 Fixture Skill，只新增 Manifest/Schema/Registry 注册，不修改 `App.tsx`，即可展示目录、产物和审批。
2. 同一 Artifact View 可被两个以上 Skill 复用。
3. 一个 Skill 可以组合多个 View，不拥有整套页面。
4. 无 Skill 项目仍能聊天、上传并查看通用 Asset。
5. 未知 Capability 不破坏工作台；未知 Artifact 有明确只读 Inspector。
6. 内部 Artifact 是否外显来自定义，不来自前端集合常量。
7. 左侧顺序来自编译投影，不来自前端固定数组。
8. 集、场、素材等子目录由 Scope Policy 生成，不按 Artifact Type 判断。
9. Agent 不能下发任意组件、代码、CSS 或 API 地址。
10. 所有写操作仍由 Runtime Guard、版本和 Available Actions 校验。
11. 三条现有 Skill 的步骤、审批、版本、修改和最终剧本行为不回归。
12. 前端没有按三个现有 Capability ID 决定核心工作台结构的代码。

## 16. 实施决策

本次盘点后的建议不是继续修补 `artifactRendererRegistry.ts`，而是先完成 M0-M2 的协议和投影，再迁移前端。

原因：如果先拆 React 组件而后端仍只返回原始 Artifact 列表，新的组件层仍会复制当前的标签、排序、隐藏和关系推断，最终只是把硬编码从 `App.tsx` 移到更多文件中。

下一开发入口应为：

```text
Workspace View Model v1
-> Artifact/Interaction View Registry v1
-> Projection Compiler
-> 前端 Shell 迁移
```
