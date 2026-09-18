from __future__ import annotations

import argparse
import asyncio
from datetime import datetime, timezone
import json
from pathlib import Path
import secrets
import sys

import httpx2 as httpx
from agents import Agent, ModelSettings, OpenAIResponsesModel, RunConfig, Runner, function_tool
from openai import AsyncOpenAI
from pydantic import BaseModel

PROJECT_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from content_agent_sidecar.compatibility import load_env_file
from content_agent_sidecar.config import Settings
from content_agent_sidecar.session import NativeCompactionProtocolError, create_sdk_session, prepare_sdk_session


class EmptyConversation:
    async def get_conversation_messages(self, conversation_id: str, limit: int = 0):
        return {"items": []}


class Recall(BaseModel):
    first: str
    last: str


async def run(output: Path, turns: int) -> dict:
    settings = Settings.from_env()
    report = {
        "started_at": datetime.now(timezone.utc).isoformat(),
        "status": "running", "model": settings.model_name, "turns_requested": turns,
        "completed_turns": 0, "compact_http_statuses": [], "compact_output_shapes": [], "tool_calls": [],
        "compaction_items_seen": 0, "input_tokens": 0, "output_tokens": 0,
        "scope": "Synthetic SDK Session + configured Responses gateway; not full platform E2E",
    }

    def persist():
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    async def observe_response(response: httpx.Response):
        if response.request.url.path.endswith("/responses/compact"):
            report["compact_http_statuses"].append(response.status_code)
            await response.aread()
            try:
                payload = response.json()
                report["compact_output_shapes"].append([
                    {"type": item.get("type"), "role": item.get("role"), "has_encrypted_content": bool(item.get("encrypted_content"))}
                    for item in payload.get("output", []) if isinstance(item, dict)
                ])
            except (ValueError, AttributeError):
                report["compact_output_shapes"].append("invalid_json")

    http = httpx.AsyncClient(event_hooks={"response": [observe_response]})
    client = AsyncOpenAI(
        api_key=settings.model_api_key, base_url=settings.model_base_url,
        timeout=min(settings.model_timeout_seconds, 180), max_retries=0, http_client=http,
    )
    session = None
    values = {index: "fixture-" + secrets.token_hex(5) for index in range(1, turns + 1)}

    @function_tool
    def lookup_fixture(index: int) -> str:
        """Return a synthetic ledger value for one index, only when the current user asks to store it."""
        report["tool_calls"].append(index)
        return values[index]

    model = OpenAIResponsesModel(model=settings.model_name, openai_client=client)
    config = RunConfig(tracing_disabled=True)
    agent = Agent(
        name="Session acceptance fixture", model=model, tools=[lookup_fixture],
        instructions="Maintain the fixture ledger in conversation memory. Call lookup_fixture exactly once per STORE request. Preserve all index/value associations for later recall. Respond STORED after each store.",
        model_settings=ModelSettings(max_tokens=512, store=False),
    )

    def new_session():
        return create_sdk_session(
            EmptyConversation(), "synthetic-acceptance", "ledger", client=client,
            model=settings.model_name, db_path=str(output.with_suffix(".sqlite")), current_user_content="",
        )

    try:
        session = new_session()
        if await session.get_items():
            raise RuntimeError("acceptance output must use a new session database")
        persist()
        for index in range(1, turns + 1):
            await prepare_sdk_session(session)
            result = await Runner.run(agent, f"STORE index {index}. Read its value using lookup_fixture and remember it.", session=session, max_turns=4, run_config=config)
            usage = result.context_wrapper.usage
            report["input_tokens"] += usage.input_tokens
            report["output_tokens"] += usage.output_tokens
            report["completed_turns"] = index
            report["compaction_items_seen"] += sum(item.get("type") == "compaction" for item in await session.get_items())
            persist()
            print(json.dumps({"turn": index, "compact_calls": len(report["compact_http_statuses"])}), flush=True)
        session.close()
        session = new_session()
        items = await session.get_items()
        report["restart_compaction_item_count"] = sum(item.get("type") == "compaction" for item in items)
        recall = Agent(
            name="Session recall acceptance", model=model, output_type=Recall,
            instructions="Recall the exact fixture values stored earlier. Do not invent or substitute values. Do not call any tools.",
            model_settings=ModelSettings(max_tokens=512, store=False),
        )
        result = await Runner.run(recall, f"Return first=the value stored at index 1, last=the value stored at index {turns}.", session=session, max_turns=2, run_config=config)
        report["input_tokens"] += result.context_wrapper.usage.input_tokens
        report["output_tokens"] += result.context_wrapper.usage.output_tokens
        report["recall_after_restart"] = result.final_output.first == values[1] and result.final_output.last == values[turns]
        report["one_tool_call_per_turn"] = report["tool_calls"] == list(range(1, turns + 1))
        report["status"] = "passed" if (
            report["recall_after_restart"] and report["one_tool_call_per_turn"]
            and report["restart_compaction_item_count"] > 0
            and report["compact_http_statuses"] and all(code == 200 for code in report["compact_http_statuses"])
        ) else "failed"
    except Exception as exc:
        report["status"] = "blocked" if isinstance(exc, NativeCompactionProtocolError) or getattr(exc, "status_code", None) in {401, 403, 404, 405, 429, 500, 502, 503} else "failed"
        report["error_type"] = type(exc).__name__
        report["error_status"] = getattr(exc, "status_code", None)
        report["error_code"] = getattr(exc, "code", None)
        if session is not None:
            report["preserved_item_count_after_error"] = len(await session.get_items())
    finally:
        if session is not None:
            session.close()
        await client.close()
        report["ended_at"] = datetime.now(timezone.utc).isoformat()
        persist()
    return report


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--env-file", type=Path, default=PROJECT_ROOT / "backend" / ".env.local")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--turns", type=int, default=32)
    args = parser.parse_args()
    if not 30 <= args.turns <= 40:
        parser.error("turns must be between 30 and 40")
    if args.output.exists() or args.output.with_suffix(".sqlite").exists():
        parser.error("output/session already exists; use a new acceptance path")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    load_env_file(args.env_file)
    report = asyncio.run(run(args.output, args.turns))
    print(json.dumps(report, ensure_ascii=False), flush=True)
    raise SystemExit(0 if report["status"] == "passed" else 1)


if __name__ == "__main__":
    main()
