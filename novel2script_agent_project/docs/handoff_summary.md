# Novel2Script Agent 版交接摘要

## 项目来源

- 旧工作目录：`C:\Users\egois\Desktop\novel2script_mainflow`
- 新项目目录：`C:\Users\egois\Desktop\novel2script_agent_project`
- 当前设计来源：原 Agent prompts/rules 设计资料
- 旧运行服务 8809/8810 不在本新项目内；不要为了新项目开发直接改旧运行代码。

## 已确定的核心设计原则

1. `Skills` 是用户可调用或由主 Agent 路由的完整能力。
2. `Rules` 是准则库，只包含：名称、用途描述、准则。
3. `Prompts` 是可执行任务说明，负责：角色、任务、引用 rules、输入、执行规则、输出、禁止事项。
4. Prompt 引用 rule 必须真实消费；不消费就不引用，部分消费要写清边界。
5. 小说链与非小说链前置流程不同，最终汇合到公共 `script_generate`。
6. 前端只是编辑、审批、展示；prompt/artifact 本身必须能让工程自动调度。
7. 需要 agent/workflow/前端协议的问题，不硬塞进 prompts/rules。

## 当前主流程

### 小说链

```text
source_text
-> step1_story_bible
-> step2_episode_split
-> step3_episode_cards
-> script_generate
```

核心 artifacts：

```text
story_bible -> episode_split -> episode_cards -> script_unit
```

### 非小说链

```text
user_material
-> step1_material_bank
-> step2_story_seed
-> step3_series_blueprint
-> step4_episode_cards
-> script_generate
```

核心 artifacts：

```text
material_bank -> story_seed -> series_blueprint -> episode_cards -> script_unit
```

## 当前 active prompts

- `design/小说-prompts/step1_story_bible.md`
- `design/小说-prompts/step2_episode_split.md`
- `design/小说-prompts/step3_episode_cards.md`
- `design/非小说-prompts/step1_material_bank.md`
- `design/非小说-prompts/step2_story_seed.md`
- `design/非小说-prompts/step3_series_blueprint.md`
- `design/非小说-prompts/step4_episode_cards.md`
- `design/视频-prompts/step1_video_script_extract.md`
- `design/prompts/script_generate.md`

## 当前 active rules

- `design/rules/shared/01_素材理解与故事圣经.md`
- `design/rules/shared/02_人物关系与声口.md`
- `design/rules/shared/03_结构规划_开头_冲突_爽点_尾钩.md`
- `design/rules/shared/04_剧本写作技法.md`
- `design/rules/shared/05_对白规则.md`
- `design/rules/shared/06_转场_闪回_连续性_格式.md`
- `design/rules/shared/07_小程序短剧适配.md`
- `design/rules/shared/08_示例库.md`
- `design/rules/shared/09_非小说素材与故事种子.md`
- `design/rules/video_to_script_extract/10_视频高还原剧本生成规则.md`

## 已处理的关键问题

- Step1/Step2 顺序已调整：小说先全文理解，再拆集。
- `Rule01` 已收窄为小说 Step1 使用；下游通过 `story_bible` 间接消费。
- 非小说链新增 `Rule09`，不再误用小说素材理解 rule。
- 公共 `script_generate` 已独立到 `design/prompts/script_generate.md`。
- 小说 Step3 和非小说 Step4 的 `next_action` 均统一为 `script_generate`。
- Rule 去重已做：规划归 `03`，对白归 `05`，转场/格式归 `06`，示例归 `08`，平台适配归 `07`。

## 仍未进入 prompts/rules 的能力

这些属于 agent/workflow/前端协议层，后续再做：

- 生成前配置 / 审批卡 / generation_profile。
- 局部修改、选区定位、artifact patch、局部重跑。
- 人物功能合并、新增功能角色。
- 手动全集切割入口。
- 自动改集数、合并拆分、重排剧情。
- run_state、checkpoint、dependency invalidation。

## 后续开发优先级

1. 建 `schemas/`，固定 artifact JSON schema。
2. 建最小 runner，按 `next_action` 自动调度 prompt。
3. 建 source_context 拼参器，支持 `source_mode: novel | non_novel`。
4. 建 run_state / checkpoint / artifacts 落盘规范。
5. 再接 Agent SDK 和前端壳子。
