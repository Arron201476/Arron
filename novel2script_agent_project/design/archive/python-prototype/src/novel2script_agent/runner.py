from __future__ import annotations

from uuid import uuid4

from .config import PROMPT_PATHS
from .models import AgentPlan, AgentStep, SourceMode

OUTPUT_ARTIFACTS = {
    "novel": ["story_bible", "source_chunks", "episode_cards", "scripts"],
    "non_novel": ["material_bank", "story_seed", "series_blueprint", "episode_cards", "scripts"],
}


class AgentRunner:
    def build_plan(self, source_mode: SourceMode) -> AgentPlan:
        prompt_paths = PROMPT_PATHS[source_mode]
        outputs = OUTPUT_ARTIFACTS[source_mode]
        steps = [
            AgentStep(
                step_id=f"step{index + 1}",
                prompt_path=str(prompt_path),
                output_artifact=outputs[index],
                needs_approval=index < len(outputs) - 1,
            )
            for index, prompt_path in enumerate(prompt_paths)
        ]
        return AgentPlan(run_id=str(uuid4()), source_mode=source_mode, steps=steps)
