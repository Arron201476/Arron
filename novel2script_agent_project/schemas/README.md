# Artifact Schemas

当前先固定 artifact 边界，具体 JSON Schema 后续再补：

- `material_bank`: 非小说素材库。
- `story_seed`: 非小说故事种子。
- `story_bible`: 小说全文理解 / 故事圣经。
- `series_blueprint`: 非小说剧集体量与结构蓝图。
- `source_chunks`: 小说分集原文块。
- `episode_cards`: 分集卡 / 规划。
- `scripts`: 剧本正文。

原则：prompt 负责输入输出字段；rule 只提供准则，不定义参数或 schema。Skill 负责完整能力、工作流和按步骤选择所需 rules。
