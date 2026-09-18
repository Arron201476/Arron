import asyncio
from copy import deepcopy

import pytest

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.backend import BackendError, backend_memory_activity
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.runtime import AgentContext
from test_native_capabilities import native_backend
from test_native_workspace_transport import Reply, response, wire


def catalog():
    payload = deepcopy(native_backend().catalog)
    payload["tools"] = [tool for tool in payload["tools"] if tool["id"] != "runtime:view_image"]
    return payload


def test_memory_catalog_real_http_prepares_only_native_tools():
    async def run():
        with wire(response(catalog())) as (backend, requests):
            context = AgentContext("project", "conversation", backend, memory_generation_id="generation", memory_generation_attempt=1, attempt_token="token")
            with backend_memory_activity("project", "generation", 1, "token"):
                prepared = await AgentToolProvider(backend, [], guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
            assert not prepared.mcp_servers and not prepared.selected_ids
            assert set(prepared.catalog.descriptors) == {"runtime:apply_patch", "runtime:exec_command"}
            method, path, headers, body = requests[0]
            assert (method, path, body) == ("GET", "/internal/v1/agent-tools/catalog", b"")
            assert headers["X-Agent-Memory-Generation-ID"] == "generation"
    asyncio.run(run())


@pytest.mark.parametrize("mutation", ["mcp", "hosted", "business", "approval", "duplicate"])
def test_memory_catalog_rejects_nonprivate_response(mutation):
    payload = catalog()
    if mutation in {"mcp", "hosted"}:
        payload["mcp_servers" if mutation == "mcp" else "hosted_tools"] = [{"private": "must-not-use"}]
    elif mutation == "business":
        payload["tools"][0]["id"] = "runtime:publish_workspace_files"
    elif mutation == "approval":
        payload["tools"][0]["approval"] = "never"
    else:
        payload["tools"].append(deepcopy(payload["tools"][0]))
    async def run():
        with wire(response(payload)) as (backend, _):
            with backend_memory_activity("project", "generation", 1, "token"):
                with pytest.raises(BackendError):
                    await backend.get_agent_tool_catalog()
    asyncio.run(run())


def test_memory_catalog_does_not_follow_redirect():
    async def run():
        with wire(response(catalog())) as (target, target_requests):
            with wire(Reply(b"", status=302, headers=[("Location", target._base_url + "/catalog")])) as (backend, _):
                with backend_memory_activity("project", "generation", 1, "token"):
                    with pytest.raises(BackendError):
                        await backend.get_agent_tool_catalog()
            assert not target_requests
    asyncio.run(run())


@pytest.mark.parametrize("invalid", [None, "prepare-approval", "publish-approval", "prepare-access", "publish-access"])
def test_memory_catalog_private_publication_policies(invalid):
    payload = catalog()
    for name, approval, access in [("prepare_agent_memory_publication", "never", "read"),
                                   ("publish_agent_memory", "always", "write")]:
        tool = deepcopy(payload["tools"][0])
        tool.update(id="runtime:" + name, name=name, approval=approval, access=access)
        payload["tools"].append(tool)
    if invalid:
        target, field = invalid.split("-")
        payload["tools"][-2 if target == "prepare" else -1][field] = "always" if target == "prepare" and field == "approval" else "never" if field == "approval" else "sensitive"

    async def run():
        with wire(response(payload)) as (backend, _):
            with backend_memory_activity("project", "generation", 1, "token"):
                if invalid:
                    with pytest.raises(BackendError):
                        await backend.get_agent_tool_catalog()
                else:
                    assert await backend.get_agent_tool_catalog() == payload
    asyncio.run(run())
