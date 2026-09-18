from __future__ import annotations

import asyncio
import os
from pathlib import Path
from types import SimpleNamespace

import pytest

from content_agent_sidecar.compatibility import (
    ProbeCheck,
    compaction_response_evidence,
    load_env_file,
    local_checks,
    public_endpoint,
    render_markdown,
    static_checks,
)


def test_compaction_probe_does_not_accept_plaintext_summary_in_valid_envelope():
    passed, evidence = compaction_response_evidence(SimpleNamespace(object="response.compaction", output=[{"type": "message", "role": "user", "content": "ordinary summary"}]))
    assert passed is False
    assert "native_compaction=false" in evidence
    passed, evidence = compaction_response_evidence(SimpleNamespace(object="response.compaction", output=[{"type": "compaction", "id": "cmp_fixture", "encrypted_content": "opaque-fixture"}]))
    assert passed is True
    assert "native_compaction=true" in evidence
from content_agent_sidecar.skill_metadata import (
    UNIFIED_SKILL_SCHEMA,
    map_local_skill,
    map_openai_hosted_skill,
)


def test_public_endpoint_strips_credentials_query_and_fragment() -> None:
    assert (
        public_endpoint("https://user:secret@example.com:8443/v1?token=secret#part")
        == "https://example.com:8443/v1"
    )


def test_load_env_file_does_not_override_process_environment(
    tmp_path: Path, monkeypatch
) -> None:
    env_file = tmp_path / ".env.local"
    env_file.write_text("PROBE_KEEP=file\nPROBE_ADD=added\n", encoding="utf-8")
    monkeypatch.setenv("PROBE_KEEP", "process")
    monkeypatch.delenv("PROBE_ADD", raising=False)

    load_env_file(env_file)

    assert os.environ["PROBE_KEEP"] == "process"
    assert os.environ["PROBE_ADD"] == "added"


def test_static_checks_cover_required_sdk_surfaces() -> None:
    checks = {check.capability: check for check in static_checks()}

    assert checks["responses.create"].status == "supported"
    assert checks["responses.stream"].status == "supported"
    assert checks["responses.compact"].status == "supported"
    assert checks["skills.list"].status == "supported"
    assert checks["session.responses_compaction"].status == "supported"
    assert checks["mcp.streamable_http"].status == "supported"
    assert checks["mcp.stdio"].status == "supported"
    assert checks["skills.metadata_mapping"].status == "supported"


def test_local_compatibility_executes_real_stdio_mcp_tool() -> None:
    checks = {check.capability: check for check in asyncio.run(local_checks())}
    assert checks["mcp.stdio_tool_call"].status == "supported"


def test_local_and_openai_hosted_skills_share_one_metadata_contract() -> None:
    local = map_local_skill(
        {
            "capability_id": "outline-critic",
            "version": "1.0.0",
            "description": "Diagnose an outline.",
            "execution_mode": "inline",
            "entry_policy": {"auto_route": True},
            "skill": {
                "name": "outline-critic",
                "path": ".agents/skills/outline-critic/SKILL.md",
            },
        }
    )
    hosted = map_openai_hosted_skill(
        {
            "object": "skill",
            "id": "skill_123",
            "name": "outline-critic",
            "description": "Diagnose an outline.",
            "default_version": "3",
            "latest_version": "4",
        }
    )
    assert local.keys() == hosted.keys()
    assert local["schema_version"] == hosted["schema_version"] == UNIFIED_SKILL_SCHEMA
    assert local["source"] == "local_package"
    assert hosted["source"] == "openai_hosted"
    assert hosted["capability_id"] == "hosted:skill_123"
    assert hosted["allow_implicit_invocation"] is False


@pytest.mark.parametrize("policy,skill_policy,expected", [
    ({}, {}, True),
    ({"auto_route": True}, {}, True),
    ({}, {"allow_implicit_invocation": False}, False),
    ({"auto_route": False}, {"allow_implicit_invocation": True}, False),
    ({"auto_route": True}, {"allow_implicit_invocation": False}, False),
    ({"auto_route": "false"}, {}, False),
    ({}, {"allow_implicit_invocation": "true"}, False),
])
def test_local_skill_metadata_preserves_implicit_invocation_policy(policy, skill_policy, expected) -> None:
    mapped = map_local_skill({
        "capability_id": "custom_skill", "version": "1.0.0", "description": "Review material.",
        "entry_policy": policy,
        "skill": {"name": "custom-skill", "path": "skills/custom-skill/SKILL.md", **skill_policy},
    })
    assert mapped["allow_implicit_invocation"] is expected


def test_probe_check_omits_empty_fields() -> None:
    assert ProbeCheck(
        capability="test",
        layer="static",
        status="supported",
    ).public() == {
        "capability": "test",
        "layer": "static",
        "status": "supported",
    }


def test_markdown_distinguishes_unsupported_blocked_and_not_tested() -> None:
    report = {
        "endpoint": "https://example.com/v1",
        "model": "gpt-test",
        "versions": {"openai_agents": "1", "openai": "2"},
        "live_requested": True,
        "checks": [
            ProbeCheck("a", "gateway_live", "unsupported").public(),
            ProbeCheck("b", "gateway_live", "blocked").public(),
            ProbeCheck("c", "gateway_live", "not_tested").public(),
        ],
    }

    rendered = render_markdown(report)

    assert "| a | gateway_live | unsupported |" in rendered
    assert "| b | gateway_live | blocked |" in rendered
    assert "| c | gateway_live | not_tested |" in rendered
