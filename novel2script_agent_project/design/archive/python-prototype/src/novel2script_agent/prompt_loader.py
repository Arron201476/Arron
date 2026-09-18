from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path


@dataclass(slots=True)
class PromptDocument:
    path: Path
    content: str


def load_markdown(path: Path) -> PromptDocument:
    if not path.exists():
        raise FileNotFoundError(f"Prompt not found: {path}")
    return PromptDocument(path=path, content=path.read_text(encoding="utf-8"))


def load_prompt_chain(paths: list[Path]) -> list[PromptDocument]:
    return [load_markdown(path) for path in paths]
