from __future__ import annotations

from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[2]
DESIGN_ROOT = PROJECT_ROOT / "design"
RUNS_ROOT = PROJECT_ROOT / "runs"
ARTIFACTS_ROOT = PROJECT_ROOT / "artifacts"

PROMPT_PATHS = {
    "novel": [
        DESIGN_ROOT / "小说-prompts" / "step1_story_bible.md",
        DESIGN_ROOT / "小说-prompts" / "step2_episode_split.md",
        DESIGN_ROOT / "小说-prompts" / "step3_episode_cards.md",
        DESIGN_ROOT / "prompts" / "script_generate.md",
    ],
    "non_novel": [
        DESIGN_ROOT / "非小说-prompts" / "step1_material_bank.md",
        DESIGN_ROOT / "非小说-prompts" / "step2_story_seed.md",
        DESIGN_ROOT / "非小说-prompts" / "step3_series_blueprint.md",
        DESIGN_ROOT / "非小说-prompts" / "step4_episode_cards.md",
        DESIGN_ROOT / "prompts" / "script_generate.md",
    ],
}

SKILLS_ROOT = DESIGN_ROOT / "skills"
REQUIRED_SKILLS = [
    "01_素材理解与故事圣经.md",
    "02_人物关系与声口.md",
    "03_结构规划_开头_冲突_爽点_尾钩.md",
    "04_剧本写作技法.md",
    "05_对白规则.md",
    "06_转场_闪回_连续性_格式.md",
    "07_小程序短剧适配.md",
    "08_示例库.md",
    "09_非小说素材与故事种子.md",
]
