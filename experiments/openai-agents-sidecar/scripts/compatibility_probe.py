from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path
import sys


PROJECT_ROOT = Path(__file__).resolve().parents[3]
SIDECAR_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SIDECAR_ROOT / "src"))

from content_agent_sidecar.compatibility import (  # noqa: E402
    build_report,
    load_env_file,
    render_markdown,
)
from content_agent_sidecar.config import Settings  # noqa: E402


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--env-file",
        type=Path,
        default=PROJECT_ROOT / "backend" / ".env.local",
    )
    parser.add_argument("--live", action="store_true")
    parser.add_argument("--json-output", type=Path)
    parser.add_argument("--markdown-output", type=Path)
    args = parser.parse_args()

    load_env_file(args.env_file)
    report = asyncio.run(build_report(Settings.from_env(), live=args.live))
    serialized = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    print(serialized, end="")

    if args.json_output:
        args.json_output.parent.mkdir(parents=True, exist_ok=True)
        args.json_output.write_text(serialized, encoding="utf-8")
    if args.markdown_output:
        args.markdown_output.parent.mkdir(parents=True, exist_ok=True)
        args.markdown_output.write_text(render_markdown(report), encoding="utf-8")


if __name__ == "__main__":
    main()
