from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from dataclasses import asdict, dataclass
from importlib import metadata
import inspect
import os
from pathlib import Path
import sys
import tempfile
from time import perf_counter
from typing import Any
from urllib.parse import urlsplit, urlunsplit

from agents import AsyncOpenAI
from agents.memory import OpenAIResponsesCompactionSession
from agents.mcp import MCPServerStdio, MCPServerStreamableHttp
from openai import APIConnectionError, APIStatusError, APITimeoutError
from openai.resources.responses import AsyncResponses
from openai.resources.skills import AsyncSkills

from .config import Settings
from .session import native_compaction_output_valid
from .skill_metadata import map_local_skill, map_openai_hosted_skill


@dataclass(frozen=True)
class ProbeCheck:
    capability: str
    layer: str
    status: str
    reason: str = ""
    latency_ms: int | None = None
    http_status: int | None = None
    evidence: tuple[str, ...] = ()

    def public(self) -> dict[str, Any]:
        result = asdict(self)
        result["evidence"] = list(self.evidence)
        return {key: value for key, value in result.items() if value not in (None, "", [])}


def load_env_file(path: Path) -> None:
    if not path.is_file():
        return
    for raw_line in path.read_text(encoding="utf-8-sig").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        name, value = line.split("=", 1)
        name = name.strip()
        if not name or not name.replace("_", "a").isalnum() or name[0].isdigit():
            raise ValueError(f"invalid environment variable name in {path.name}")
        os.environ.setdefault(name, value.strip())


def public_endpoint(value: str) -> str:
    parsed = urlsplit(value.strip())
    if not parsed.scheme or not parsed.hostname:
        return "<invalid>"
    host = parsed.hostname
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    if parsed.port is not None:
        host = f"{host}:{parsed.port}"
    return urlunsplit((parsed.scheme, host, parsed.path.rstrip("/"), "", ""))


def static_checks() -> list[ProbeCheck]:
    return [
        ProbeCheck(
            capability="responses.create",
            layer="python_sdk",
            status="supported" if hasattr(AsyncResponses, "create") else "unsupported",
        ),
        ProbeCheck(
            capability="responses.stream",
            layer="python_sdk",
            status=(
                "supported"
                if "stream" in inspect.signature(AsyncResponses.create).parameters
                else "unsupported"
            ),
        ),
        ProbeCheck(
            capability="responses.compact",
            layer="python_sdk",
            status="supported" if hasattr(AsyncResponses, "compact") else "unsupported",
        ),
        ProbeCheck(
            capability="skills.list",
            layer="python_sdk",
            status="supported" if hasattr(AsyncSkills, "list") else "unsupported",
        ),
        ProbeCheck(
            capability="session.responses_compaction",
            layer="agents_sdk",
            status=(
                "supported"
                if OpenAIResponsesCompactionSession is not None
                else "unsupported"
            ),
        ),
        ProbeCheck(
            capability="mcp.streamable_http",
            layer="agents_sdk",
            status="supported" if MCPServerStreamableHttp is not None else "unsupported",
        ),
        ProbeCheck(
            capability="mcp.stdio",
            layer="agents_sdk",
            status="supported" if MCPServerStdio is not None else "unsupported",
        ),
        ProbeCheck(
            capability="skills.metadata_mapping",
            layer="application_adapter",
            status=(
                "supported"
                if callable(map_local_skill) and callable(map_openai_hosted_skill)
                else "unsupported"
            ),
        ),
    ]


def _failure_check(
    capability: str, started: float, exc: Exception, *, layer: str
) -> ProbeCheck:
    latency_ms = round((perf_counter() - started) * 1000)
    if isinstance(exc, APIStatusError):
        http_status = exc.status_code
        if http_status in {404, 405, 501}:
            status = "unsupported"
            reason = "endpoint_unavailable"
        elif http_status in {401, 403}:
            status = "blocked"
            reason = "authentication_or_policy"
        elif http_status == 429:
            status = "blocked"
            reason = "rate_limited"
        elif http_status >= 500:
            status = "blocked"
            reason = "gateway_or_provider_error"
        else:
            status = "blocked"
            reason = "request_rejected"
        return ProbeCheck(
            capability=capability,
            layer=layer,
            status=status,
            reason=reason,
            latency_ms=latency_ms,
            http_status=http_status,
            evidence=(type(exc).__name__,),
        )
    if isinstance(exc, (APITimeoutError, asyncio.TimeoutError)):
        reason = "timeout"
    elif isinstance(exc, APIConnectionError):
        reason = "connection_failed"
    else:
        reason = "client_error"
    return ProbeCheck(
        capability=capability,
        layer=layer,
        status="blocked",
        reason=reason,
        latency_ms=latency_ms,
        evidence=(type(exc).__name__,),
    )


async def _run_check(
    capability: str,
    operation: Callable[[], Awaitable[tuple[bool, tuple[str, ...]]]],
    *,
    layer: str = "gateway_live",
) -> ProbeCheck:
    started = perf_counter()
    try:
        passed, evidence = await operation()
    except Exception as exc:  # The report intentionally records only a safe category.
        return _failure_check(capability, started, exc, layer=layer)
    return ProbeCheck(
        capability=capability,
        layer=layer,
        status="supported" if passed else "blocked",
        reason="" if passed else "unexpected_response_shape",
        latency_ms=round((perf_counter() - started) * 1000),
        evidence=evidence,
    )


async def local_checks() -> list[ProbeCheck]:
    async def stdio_mcp_tool_call() -> tuple[bool, tuple[str, ...]]:
        sidecar_root = Path(__file__).resolve().parents[2]
        fixture = sidecar_root / "tests" / "fixtures" / "story_mcp_server.py"
        if not fixture.is_file():
            raise FileNotFoundError("MCP fixture is missing")
        with tempfile.TemporaryDirectory(prefix="content-agent-mcp-") as temp_dir:
            server = MCPServerStdio(
                {
                    "command": sys.executable,
                    "args": [str(fixture)],
                    "cwd": str(sidecar_root),
                    "env": {
                        "PYTHONUTF8": "1",
                        "STORY_MCP_STATE_FILE": str(Path(temp_dir) / "state.jsonl"),
                    },
                    "encoding": "utf-8",
                    "encoding_error_handler": "strict",
                },
                cache_tools_list=True,
                name="compatibility-story-fixture",
                client_session_timeout_seconds=10,
            )
            async with server:
                tools = await server.list_tools()
                result = await server.call_tool("lookup_story_fact", {"key": "hero"})
        text_items = [
            str(getattr(item, "text", ""))
            for item in getattr(result, "content", []) or []
        ]
        tool_names = {str(getattr(tool, "name", "")) for tool in tools}
        passed = "lookup_story_fact" in tool_names and any(
            "沈砚" in text for text in text_items
        )
        return passed, (f"tools={len(tool_names)}", "read_call_completed=true")

    return [
        await _run_check(
            "mcp.stdio_tool_call", stdio_mcp_tool_call, layer="local_sdk"
        )
    ]


async def live_checks(settings: Settings) -> list[ProbeCheck]:
    settings.validate_model()
    client = AsyncOpenAI(
        api_key=settings.model_api_key,
        base_url=settings.model_base_url,
        max_retries=0,
        timeout=min(settings.model_timeout_seconds, 90),
    )

    async def responses_create() -> tuple[bool, tuple[str, ...]]:
        response = await client.responses.create(
            model=settings.model_name,
            input="Return exactly the text OK.",
            max_output_tokens=128,
        )
        status = str(getattr(response, "status", "") or "")
        return bool(getattr(response, "id", "")), (f"response_status={status or 'unknown'}",)

    async def responses_stream() -> tuple[bool, tuple[str, ...]]:
        stream = await client.responses.create(
            model=settings.model_name,
            input="Return exactly the text OK.",
            max_output_tokens=128,
            stream=True,
        )
        event_types: set[str] = set()
        async for event in stream:
            event_type = str(getattr(event, "type", "") or "")
            if event_type:
                event_types.add(event_type)
        completed = "response.completed" in event_types
        evidence = tuple(f"event={name}" for name in sorted(event_types)[:12])
        return completed, evidence

    async def function_tool_call() -> tuple[bool, tuple[str, ...]]:
        response = await client.responses.create(
            model=settings.model_name,
            input="Call compatibility_echo once with text set to OK.",
            max_output_tokens=256,
            tools=[
                {
                    "type": "function",
                    "name": "compatibility_echo",
                    "description": "Echo a short compatibility probe value.",
                    "parameters": {
                        "type": "object",
                        "properties": {"text": {"type": "string"}},
                        "required": ["text"],
                        "additionalProperties": False,
                    },
                    "strict": True,
                }
            ],
            tool_choice={"type": "function", "name": "compatibility_echo"},
        )
        calls = [
            item
            for item in getattr(response, "output", [])
            if getattr(item, "type", "") == "function_call"
            and getattr(item, "name", "") == "compatibility_echo"
        ]
        return bool(calls), (f"function_calls={len(calls)}",)

    async def responses_compact() -> tuple[bool, tuple[str, ...]]:
        repeated_context = "Compatibility context sentence. " * 24
        compacted = await client.responses.compact(
            model=settings.model_name,
            input=[
                {"role": "user", "content": repeated_context},
                {"role": "assistant", "content": repeated_context},
                {"role": "user", "content": repeated_context},
                {"role": "assistant", "content": repeated_context},
            ],
        )
        return compaction_response_evidence(compacted)

    async def skills_list() -> tuple[bool, tuple[str, ...]]:
        page = await client.skills.list(limit=1)
        data = getattr(page, "data", None)
        return data is not None, ("read_only_list_completed=true",)

    try:
        checks = [
            await _run_check("responses.create", responses_create),
            await _run_check("responses.stream", responses_stream),
            await _run_check("responses.function_tool", function_tool_call),
            await _run_check("responses.compact", responses_compact),
            await _run_check("skills.list", skills_list),
        ]
    finally:
        await client.close()
    checks.append(
        ProbeCheck(
            capability="mcp.remote_tool_call",
            layer="gateway_live",
            status="not_tested",
            reason="no_local_mcp_fixture",
        )
    )
    return checks


def compaction_response_evidence(compacted: Any) -> tuple[bool, tuple[str, ...]]:
    object_type = str(getattr(compacted, "object", "") or "")
    output = [item if isinstance(item, dict) else item.model_dump(exclude_none=True) for item in (getattr(compacted, "output", []) or [])]
    native = native_compaction_output_valid(output)
    return object_type == "response.compaction" and native, (
        f"object={object_type or 'unknown'}", f"output_items={len(output)}", f"native_compaction={str(native).lower()}",
    )


async def build_report(settings: Settings, *, live: bool) -> dict[str, Any]:
    checks = static_checks()
    checks.extend(await local_checks())
    if live:
        checks.extend(await live_checks(settings))
    return {
        "schema_version": "agent_sdk_compatibility.v1",
        "endpoint": public_endpoint(settings.model_base_url),
        "model": settings.model_name or "<missing>",
        "versions": {
            "openai_agents": metadata.version("openai-agents"),
            "openai": metadata.version("openai"),
        },
        "live_requested": live,
        "checks": [check.public() for check in checks],
    }


def render_markdown(report: dict[str, Any]) -> str:
    lines = [
        "# OpenAI SDK 与网关兼容矩阵",
        "",
        f"- Endpoint: `{report['endpoint']}`",
        f"- Model: `{report['model']}`",
        f"- openai-agents: `{report['versions']['openai_agents']}`",
        f"- openai: `{report['versions']['openai']}`",
        f"- Live probe: `{'yes' if report['live_requested'] else 'no'}`",
        "",
        "| Capability | Layer | Status | HTTP | Latency | Reason / Evidence |",
        "|---|---|---|---:|---:|---|",
    ]
    for check in report["checks"]:
        evidence = ", ".join(check.get("evidence", []))
        detail = check.get("reason", "")
        if evidence:
            detail = f"{detail}; {evidence}" if detail else evidence
        lines.append(
            "| {capability} | {layer} | {status} | {http} | {latency} | {detail} |".format(
                capability=check["capability"],
                layer=check["layer"],
                status=check["status"],
                http=check.get("http_status", ""),
                latency=(
                    f"{check['latency_ms']} ms" if "latency_ms" in check else ""
                ),
                detail=detail,
            )
        )
    lines.extend([
        "",
        "`unsupported` 表示当前 SDK 或网关端点不提供该能力；`blocked` 表示端点存在但请求被配置、策略或上游错误阻断；`not_tested` 不等同于支持。",
        "",
    ])
    return "\n".join(lines)
