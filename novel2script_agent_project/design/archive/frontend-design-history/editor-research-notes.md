# Editor Research Notes

本页记录前端编辑器的产品参考结论。

## 调研来源

- Sudowrite：面向小说作者，核心包括 Chat、Story Bible、Write、Rewrite、Describe、Canvas。Story Bible 从 idea 到 outline、chapter beats、正文生成是分阶段的。
- Scrivener：典型写作工具结构是 Binder / Corkboard / Outliner / Editor。Corkboard 把章节或段落挂到虚拟卡片上，Outliner 展示章节结构、摘要、字数、metadata。
- Novelcrafter：强调 Codex、Planner、Chat；用户可以在 planner 移动 scenes，AI 会从 Codex 拉取人物和情节信息。

## 对 Novel2Script 的结论

```text
左侧不是输入框，而是项目 binder / artifact 目录
中间不是 JSON，而是结构化编辑器
右侧不是日志面板，而是 Agent 对话和当前 run 摘要
技术 run events 可以保留，但放到“运行记录”里
```

## 中间编辑器规则

不同 artifact 要有不同展示：

```text
story_bible：一句话故事 / 核心冲突 / 角色 / 必须保留事实 / 风险
material_bank：用户素材 / 可用元素 / 缺失信息
story_seed：一句话故事 / 核心钩子 / 情绪引擎
series_blueprint：目标集数 / 阶段规划
episode_split：集数 / 原文范围 / 尾钩强度 / 用户注意项
episode_cards：开场压力 / 主要冲突 / 场景计划 / 本集尾钩 / 风险提示
script_context：必须遵守 / 允许补充 / 禁止改动
script_unit：场景标题 / 动作 / 对白 / 尾钩
scripts：最终剧本正文
```

## 状态规则

左侧目录状态只使用产品语义：

```text
待生成
生成中
已生成
待确认
```

不能把系统内部 `confirmed` 直接翻译成“已确认”。没有用户确认动作时，只能显示“已生成”。
