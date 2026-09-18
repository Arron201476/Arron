from agents import FunctionTool

from .project_skills import install_workspace_skill, validate_workspace_skill
from .runtime import (
    execute_skill_script, get_artifact_downloads, inspect_artifact_version,
    inspect_current_artifact, inspect_project, inspect_project_goal,
    inspect_recent_conversation, inspect_text_asset, list_project_assets,
    list_skill_resources, load_skill_instructions, read_skill_resource,
    search_artifacts, search_conversation_history,
)
from .workspace_files import apply_workspace_patch, list_workspace_files, read_workspace_file
from .native_publication import prepare_workspace_publication, publish_workspace_files
from .memory_publication import prepare_agent_memory_publication, publish_agent_memory
from .instruction_tools import get_saved_instructions, update_saved_instructions


def skill_execution_tools() -> list[FunctionTool]:
    return [
        get_saved_instructions, update_saved_instructions,
        inspect_project, inspect_project_goal, list_project_assets, inspect_text_asset,
        search_artifacts, inspect_current_artifact, inspect_artifact_version, get_artifact_downloads,
        inspect_recent_conversation, search_conversation_history,
        list_skill_resources, read_skill_resource, execute_skill_script,
        list_workspace_files, read_workspace_file, apply_workspace_patch,
        prepare_workspace_publication, publish_workspace_files,
        prepare_agent_memory_publication, publish_agent_memory,
        validate_workspace_skill, install_workspace_skill, load_skill_instructions,
    ]
