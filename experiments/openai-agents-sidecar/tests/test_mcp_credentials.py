import asyncio
import json
import os
from pathlib import Path

import pytest
from agents import Agent, Runner, RunConfig
from agents.items import ModelResponse
from agents.usage import Usage
from openai.types.responses import ResponseFunctionToolCall

from content_agent_sidecar.agent_tools import AgentToolProvider, AgentToolConfigurationError, _json_safe
from content_agent_sidecar.runtime import AgentContext, _serialize_paused_run
from test_agent_tools import AuditBackend, StaticModel, _context, _mcp_catalog


class CredentialBackend(AuditBackend):
    def __init__(self, transport="stdio", value="Bearer fixture-private", binding="binding-A"):
        catalog = _mcp_catalog(Path(__file__).parent / "fixtures" / "mcp_credential_server.py")
        catalog["tools"] = catalog["tools"][:1]
        server = catalog["mcp_servers"][0]
        server.update(transport=transport, environment={}, header_environment={}, credential_binding=binding, defer_loading=False)
        server["allowed_tools"] = server["allowed_tools"][:1]
        if transport == "stdio": server["credential_environment"] = {"MCP_TEST_API_TOKEN": "api-token"}
        else: server.update(url="https://example.com/mcp", credential_headers={"Authorization": "api-token"})
        super().__init__(catalog)
        self.resolutions = []
        self.receipt = {"server_id": server["id"], "credential_binding": binding, "values": {"api-token": value}}

    async def resolve_mcp_credentials(self, server_id, binding):
        self.resolutions.append((server_id, binding))
        return self.receipt


@pytest.mark.parametrize("transport", ["stdio", "sse", "streamable_http"])
def test_credentials_are_connection_local_and_absent_from_metadata(transport):
    before = dict(os.environ)
    backend = CredentialBackend(transport)
    prepared = asyncio.run(AgentToolProvider(backend, []).prepare(_context(), [], set()))
    server = prepared.mcp_servers[0]
    observed = server.params.env if transport == "stdio" else server.params["headers"]
    assert observed["MCP_TEST_API_TOKEN" if transport == "stdio" else "Authorization"] == "Bearer fixture-private"
    assert backend.resolutions == [("story-fixture", "binding-A")]
    assert os.environ == before
    assert "fixture-private" not in repr(prepared.catalog)
    assert "fixture-private" not in repr(server._agent_tool_server)
    assert not backend.begun


@pytest.mark.parametrize("case", ["server", "binding", "missing", "extra", "crlf", "nul", "empty", "size"])
def test_invalid_secret_receipt_never_constructs_a_connection(case):
    backend = CredentialBackend()
    if case in {"server", "binding"}: backend.receipt["server_id" if case == "server" else "credential_binding"] = "foreign"
    if case == "missing": backend.receipt["values"] = {}
    if case == "extra": backend.receipt["values"]["PATH"] = "bad"
    if case in {"crlf", "nul", "empty", "size"}: backend.receipt["values"]["api-token"] = {"crlf":"a\r\nb", "nul":"a\0b", "empty":" ", "size":"a" * 4097}[case]
    with pytest.raises(AgentToolConfigurationError):
        asyncio.run(AgentToolProvider(backend, []).prepare(_context(), [], set()))
    assert not backend.begun


@pytest.mark.parametrize("mode", ["conversation", "background", "stateful"])
def test_native_sdk_stdio_consumes_exact_execution_local_credential(mode):
    backend = CredentialBackend(value="Bearer " + mode)
    context = AgentContext("prj-tools", "conv-tools", backend, agent_turn_id="turn-tools", skill_invocation_id="sinv-tools")
    if mode != "conversation":
        context.agent_turn_id = ""
        context.attempt_token = "fixture-attempt-token"
        if mode == "background": context.agent_task_attempt_id = "task-attempt"
        else: context.execution_attempt_id = "execution-attempt"

    async def run():
        prepared = await AgentToolProvider(backend, []).prepare(context, [], set())
        async with prepared:
            response = ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="credential-check",
                name="mcp_story-fixture__lookup_story_fact", arguments=json.dumps({"key":mode}))], usage=Usage(requests=1), response_id="credential-response")
            agent = Agent(name="Credential fixture", model=StaticModel(response), mcp_servers=prepared.mcp_servers,
                          mcp_config={"include_server_in_tool_names":True}, tool_use_behavior="stop_on_first_tool")
            result = await Runner.run(agent, "Verify fixture", context=context, max_turns=1, run_config=RunConfig(tracing_disabled=True))
            assert "credential-verified" in json.dumps(_json_safe([item.raw_item for item in result.new_items]))
            state, _, _ = _serialize_paused_run(result, allow_no_approvals=True)
            assert "Bearer " + mode not in json.dumps(state)
        assert len(backend.completed) == 1 and not backend.failed
        assert "Bearer " + mode not in json.dumps(backend.completed + backend.begun)
    asyncio.run(run())
    assert "MCP_TEST_API_TOKEN" not in os.environ


@pytest.mark.parametrize("transport", ["streamable_http", "sse"])
def test_native_sdk_http_transports_receive_only_their_scoped_header(transport):
    import socket
    import uvicorn
    from mcp.server.mcpserver import MCPServer

    async def run():
        mcp = MCPServer(name="header-credential-fixture")

        @mcp.tool()
        def lookup_story_fact(key: str) -> dict[str, str]:
            """Return only a credential verification receipt."""
            return {"status": "credential-verified"}

        app = mcp.streamable_http_app() if transport == "streamable_http" else mcp.sse_app()
        expected = ""
        checked = []

        async def authenticated(scope, receive, send):
            if scope["type"] == "http":
                actual = dict(scope["headers"]).get(b"authorization", b"").decode()
                checked.append(actual == expected)
                if actual != expected:
                    await send({"type":"http.response.start", "status":401, "headers":[]})
                    await send({"type":"http.response.body", "body":b"Unauthorized"})
                    return
            await app(scope, receive, send)

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
        assert port not in {8860, 8880}
        listener.listen(32)
        server = uvicorn.Server(uvicorn.Config(authenticated, log_level="error", access_log=False))
        task = asyncio.create_task(server.serve(sockets=[listener]))
        try:
            async with asyncio.timeout(10):
                while not server.started:
                    if task.done(): await task; raise AssertionError("HTTP fixture did not start")
                    await asyncio.sleep(.01)
            for user in ("user-A", "user-B"):
                checked_before = len(checked)
                expected = "Bearer " + user
                backend = CredentialBackend(transport, value=expected, binding="binding-" + user)
                backend.catalog["mcp_servers"][0]["url"] = f"http://127.0.0.1:{port}/" + ("mcp" if transport == "streamable_http" else "sse")
                context = AgentContext("prj-tools", "conv-tools", backend, agent_turn_id="turn-tools")
                prepared = await AgentToolProvider(backend, []).prepare(context, [], set())
                async with prepared:
                    response = ModelResponse(output=[ResponseFunctionToolCall(type="function_call", call_id="check-" + user,
                        name="mcp_story-fixture__lookup_story_fact", arguments='{"key":"check"}')], usage=Usage(requests=1), response_id="credential-response")
                    agent = Agent(name="Credential HTTP fixture", model=StaticModel(response), mcp_servers=prepared.mcp_servers,
                                  mcp_config={"include_server_in_tool_names":True}, tool_use_behavior="stop_on_first_tool")
                    result = await Runner.run(agent, "Verify fixture", context=context, max_turns=1, run_config=RunConfig(tracing_disabled=True))
                    state, _, _ = _serialize_paused_run(result, allow_no_approvals=True)
                    assert expected not in json.dumps(state)
                    assert len(backend.completed) == 1 and not backend.failed
                    assert "credential-verified" in json.dumps(_json_safe([item.raw_item for item in result.new_items]))
                assert len(checked) > checked_before
            assert checked and all(checked)
        finally:
            server.should_exit = True
            try: await asyncio.wait_for(task, 10)
            finally: listener.close()
    asyncio.run(run())
