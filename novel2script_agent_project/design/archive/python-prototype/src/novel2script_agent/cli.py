from __future__ import annotations

import argparse
import json
from dataclasses import asdict
from pathlib import Path

from .config import DESIGN_ROOT, PROMPT_PATHS, REQUIRED_SKILLS, SKILLS_ROOT
from .router import detect_intent
from .runner import AgentRunner


def validate_design() -> int:
    missing: list[Path] = []
    for paths in PROMPT_PATHS.values():
        missing.extend(path for path in paths if not path.exists())
    missing.extend(SKILLS_ROOT / name for name in REQUIRED_SKILLS if not (SKILLS_ROOT / name).exists())
    if not DESIGN_ROOT.exists():
        missing.append(DESIGN_ROOT)
    if missing:
        print("missing:")
        for path in missing:
            print(f"- {path}")
        return 1
    print("design ok")
    print(f"prompt chains: {', '.join(PROMPT_PATHS)}")
    print(f"skills: {len(REQUIRED_SKILLS)}")
    return 0


def show_plan(mode: str) -> int:
    plan = AgentRunner().build_plan(mode)  # type: ignore[arg-type]
    print(json.dumps(asdict(plan), ensure_ascii=False, indent=2))
    return 0


def route_text(text: str) -> int:
    intent = detect_intent(text)
    print(json.dumps(asdict(intent), ensure_ascii=False, indent=2))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(prog="novel2script-agent")
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("validate-design")
    plan_parser = subparsers.add_parser("plan")
    plan_parser.add_argument("--mode", choices=["novel", "non_novel"], required=True)
    route_parser = subparsers.add_parser("route")
    route_parser.add_argument("text")
    args = parser.parse_args()
    if args.command == "validate-design":
        return validate_design()
    if args.command == "plan":
        return show_plan(args.mode)
    if args.command == "route":
        return route_text(args.text)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
