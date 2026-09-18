from __future__ import annotations

import json
import os
from pathlib import Path

from mcp.server.mcpserver import MCPServer


server = MCPServer(
    name="story-fixture",
    description="Local protocol fixture for the content Agent tool plane.",
)


@server.tool()
def lookup_story_fact(key: str) -> dict[str, str]:
    """Read one deterministic story fact by key."""

    facts = {
        "hero": "沈砚",
        "injury": "林舟左手受伤",
    }
    return {"key": key, "value": facts.get(key, "unknown")}


@server.tool()
def save_story_fact(key: str, value: str) -> dict[str, str]:
    """Persist one story fact for approval-gate verification."""

    state_file = Path(os.environ["STORY_MCP_STATE_FILE"])
    with state_file.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps({"key": key, "value": value}, ensure_ascii=False))
        handle.write("\n")
    return {"status": "saved", "key": key}


if __name__ == "__main__":
    server.run("stdio")
