from __future__ import annotations

import asyncio
from dataclasses import replace
import json
from types import SimpleNamespace

import pytest
from agents import ModelSettings, Runner, SQLiteSession
from agents.tool_context import ToolContext
from agents.items import ModelResponse
from agents.models.interface import ModelTracing
from agents.usage import Usage
from openai.types.responses import (
    ResponseFunctionToolCall,
    ResponseOutputMessage,
    ResponseOutputText,
)

from content_agent_sidecar.config import Settings
from content_agent_sidecar.contracts import (
    AgentExecutionRequest,
    ControlDecision,
    SkillRoutingDecision,
    ArtifactSourceRoutingDecision,
)
from content_agent_sidecar.runtime import (
    OpenAIAgentsRuntime,
    AgentContext,
    RequiredCommitModelAdapter,
    RuntimeCompatibilityError,
    _build_commit_payload,
    _explicit_artifact_versions,
    _explicit_revision_request,
    _has_attachment_sources,
    _inherit_recent_material_context,
    _load_skill_instructions,
    _previous_turn_consulted_capabilities,
    _routing_skill_catalog,
    commit_agent_action,
)
from test_stateful_execution import StreamingSequence, final_item, tool_item
from test_run_state_approval import SequenceModel


def test_skill_confirmation_inherits_only_adjacent_material_context() -> None:
    request = {
        "content": "使用视频参考创作 Skill。",
        "attachment_refs": [],
        "client_context": {},
    }
    _inherit_recent_material_context(
        request,
        {
            "items": [
                {
                    "role": "user",
                    "content": "这个视频讲了什么？",
                    "message_context": {
                        "attachment_refs": [
                            {"asset_id": "ast_video", "asset_snapshot_id": "ass_video"}
                        ],
                        "client_context": {
                            "current_asset_set_version_id": "asv_video"
                        },
                    },
                },
                {
                    "role": "assistant",
                    "content": "这个需求适合使用「视频参考创作」Skill。你希望使用该 Skill 的完整生产流程，还是由通用 Agent 直接生成普通产物？",
                },
            ]
        },
    )
    assert request["attachment_refs"] == [
        {"asset_id": "ast_video", "asset_snapshot_id": "ass_video"}
    ]
    assert request["client_context"]["current_asset_set_version_id"] == "asv_video"

    unrelated = {"content": "聊聊人物动机", "attachment_refs": [], "client_context": {}}
    _inherit_recent_material_context(unrelated, {"items": []})
    assert unrelated["attachment_refs"] == []


def test_uploaded_attachment_is_a_source_without_artifact_resolution() -> None:
    assert _has_attachment_sources(
        {
            "attachment_refs": [
                {
                    "asset_id": "ast_video",
                    "asset_snapshot_id": "ass_video",
                    "display_name": "shot_01.mp4",
                }
            ]
        }
    )


def test_continue_plan_is_not_an_implicit_artifact_revision() -> None:
    assert _explicit_revision_request(
        {
            "content": "你按照这个流程继续吧",
            "selection_snapshot": None,
            "client_context": {"current_artifact_id": "art_working_draft"},
        }
    ) is False
    assert _explicit_revision_request({"content": "继续写这个工作稿"}) is True
    assert _explicit_revision_request(
        {
            "content": "改得更紧凑",
            "selection_snapshot": {"selection": {"target_scope": "selection"}},
        }
    ) is True


def test_continue_plan_revision_decision_is_rejected() -> None:
    context = AgentContext(
        "project",
        "conversation",
        FakeBackend(),  # type: ignore[arg-type]
        raw_request={
            "content": "你按照这个流程继续吧",
            "selection_snapshot": None,
            "client_context": {"current_artifact_id": "art_working_draft"},
        },
    )
    decision = ControlDecision(
        reply="正在准备修改稿。",
        intent="revise",
        confidence=0.9,
        target_ref={
            "target_id": "art_working_draft",
            "artifact_version_id": "av_working_draft",
            "field_path": "payload.content",
        },
    )

    with pytest.raises(ValueError, match="执行既定方案应继续生成"):
        asyncio.run(_build_commit_payload(context, decision))
    assert not _has_attachment_sources({"attachment_refs": []})
    assert not _has_attachment_sources(
        {"attachment_refs": [{"asset_id": "ast_video"}]}
    )


from instruction_fixtures import EmptyInstructionsBackend


class FakeBackend(EmptyInstructionsBackend):
    def __init__(self) -> None:
        self.commit_calls = 0
        self.commit_decisions: list[dict[str, object]] = []
        self.capability_detail_requests: list[tuple[str, str, str]] = []

    async def get_conversation_messages(
        self, _conversation_id: str, limit: int = 20
    ) -> dict[str, object]:
        return {"items": [], "count": 0, "limit": limit}

    async def search_artifacts(
        self, _project_id: str, query: str = "", artifact_type: str = "", limit: int = 20
    ) -> dict[str, object]:
        return {"items": [], "count": 0, "limit": limit}

    async def commit_agent_turn(self, *_args, **_kwargs):  # type: ignore[no-untyped-def]
        self.commit_calls += 1
        self.commit_decisions.append(_args[3])
        return {"data": {}}

    async def get_capabilities(self, _project_id: str = "") -> dict[str, object]:
        return {
            "data": {
                "items": [
                    {
                        "capability_id": "novel_to_script",
                        "version": "1.4.0",
                        "label": "小说转剧本",
                        "description": "将小说原文转换为分集剧本。",
                        "execution_mode": "stateful_workflow",
                        "status": "available",
                        "entry_policy": {
                            "auto_route": True,
                            "requires_user_confirmation": True,
                        },
                        "routing": {
                            "explicit_aliases": ["小说转剧本"],
                            "intent_examples": ["把小说改成短剧剧本"],
                        },
                    },
                    {
                        "capability_id": "video_reference_creation",
                        "version": "1.2.0",
                        "label": "视频参考创作",
                        "description": "解析视频并参考创作。",
                        "execution_mode": "stateful_workflow",
                        "status": "available",
                        "entry_policy": {
                            "auto_route": True,
                            "requires_user_confirmation": True,
                        },
                        "routing": {
                            "explicit_aliases": ["视频参考创作"],
                            "intent_examples": ["解析视频并参考创作"],
                        },
                    },
                    {
                        "capability_id": "outline_critic",
                        "version": "1.0.0",
                        "label": "大纲诊断",
                        "description": "诊断大纲问题，不用于转剧本。",
                        "execution_mode": "inline",
                        "status": "available",
                        "entry_policy": {
                            "auto_route": True,
                            "requires_user_confirmation": False,
                        },
                        "routing": {
                            "explicit_aliases": ["大纲诊断", "诊断大纲"],
                            "intent_examples": ["诊断这份大纲的问题"],
                        },
                        "skill": {"name": "outline-critic"},
                    },
                ]
            }
        }

    async def get_capability(
        self, capability_id: str, project_id: str = "", version: str = ""
    ) -> dict[str, object]:
        self.capability_detail_requests.append((capability_id, project_id, version))
        items = (await self.get_capabilities(project_id))["data"]["items"]  # type: ignore[index]
        item = next(  # type: ignore[arg-type]
            candidate
            for candidate in items
            if candidate["capability_id"] == capability_id
        )
        detail = dict(item)
        if capability_id == "outline_critic":
            detail["skill"] = {
                "name": "outline-critic",
                "instructions": "Diagnose the outline before suggesting changes.",
            }
            detail["default_config_ref"] = "review"
            detail["config_options"] = ["review"]
            detail["commands"] = ["inspect", "invoke"]
            detail["input_schema"] = {
                "type": "object",
                "properties": {"outline": {"type": "string"}},
            }
            detail["config_schemas"] = {
                "review": {
                    "type": "object",
                    "properties": {
                        "depth": {"enum": ["concise", "deep"]}
                    },
                }
            }
        return {"data": detail}

    async def get_image_data_url(
        self, _asset_id: str, _asset_snapshot_id: str
    ) -> str | None:
        return None

    async def get_video_frame_data_urls(
        self, _asset_id: str, _asset_snapshot_id: str
    ) -> list[str]:
        return []


def make_settings(base_url: str, retries: int) -> Settings:
    return Settings(
        backend_base_url="http://127.0.0.1:8860",
        model_base_url=base_url,
        model_api_key="test-key",
        model_name="gpt-test-model",
        model_timeout_seconds=3,
        model_max_output_tokens=1024,
        model_max_retries=retries,
        # Leave enough room for the SDK's transient-error backoff. Dedicated
        # timeout tests cover the production timeout behavior separately.
        run_timeout_seconds=30,
        tracing_enabled=False,
        internal_token="internal-test-token",
    )


def test_compaction_protocol_failure_closes_session_and_reports_specific_stage(monkeypatch):
    from content_agent_sidecar.session import NativeCompactionProtocolError

    class FailedSession:
        closed = False

        async def get_items(self):
            raise NativeCompactionProtocolError()

        def close(self):
            self.closed = True

    session = FailedSession()
    runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", 0), FakeBackend())
    monkeypatch.setattr(runtime, "_session", lambda *_args: session)

    async def exercise():
        stream = runtime.start_execution_stream(AgentExecutionRequest(project_id="p", conversation_id="c", request={"content": "Read"}, idempotency_key="id"))
        with pytest.raises(RuntimeCompatibilityError) as error:
            async for _ in stream.events():
                pytest.fail("failed preparation emitted a successful event")
        assert error.value.failure_stage == "session_compaction"
        assert "compaction" in str(error.value)

    asyncio.run(exercise())
    assert session.closed


def execution_fixture(tmp_path, backend, model, repair=None):
    runtime = OpenAIAgentsRuntime(
        replace(make_settings("http://127.0.0.1:9/v1", 0), session_db_path=str(tmp_path / "provider-session.db")), backend
    )
    runtime._execution_agent.model = model
    # A provider may ignore tool_choice. Exercise the postcondition independently
    # of the repair adapter, through the real SDK Runner and terminal tool.
    runtime._commit_repair_agent.model = repair or StreamingSequence([])

    async def no_skill_route(_request, _items):
        return set()

    runtime._route_skills = no_skill_route
    return runtime


async def execute_and_close(runtime, request):
    try:
        return await runtime.execute_turn(request)
    finally:
        await runtime._model_client.close()


def test_execution_provider_failure_cannot_commit_or_fake_success(tmp_path) -> None:
    backend = FakeBackend()
    failure = RuntimeError("provider failed")
    failure.status_code = 500
    model = StreamingSequence([failure])
    runtime = execution_fixture(tmp_path, backend, model)
    with pytest.raises(RuntimeCompatibilityError) as error:
        asyncio.run(
            execute_and_close(runtime,
                AgentExecutionRequest(
                    project_id="prj_test",
                    conversation_id="conv_test",
                    request={"content": "生成大纲"},
                    idempotency_key="11111111-1111-4111-8111-111111111111",
                )
            )
        )

    assert backend.commit_calls == 0
    assert len(model.inputs) == 1
    assert str(error.value) == (
        "model provider request failed (HTTP 500, RuntimeError)"
    )


def test_execution_agent_requires_exactly_one_terminal_commit() -> None:
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend()
    )

    assert runtime._execution_agent.model_settings.tool_choice == "required"
    assert runtime._execution_agent.reset_tool_choice is False
    assert callable(runtime._execution_agent.tool_use_behavior)
    assert len(runtime._commit_repair_agent.tools) == 1
    assert runtime._commit_repair_agent.tools[0].name == "commit_agent_action"
    assert runtime._commit_repair_agent.model_settings.tool_choice == "required"


def test_execution_agent_distinguishes_sdk_approval_from_terminal_commit() -> None:
    from content_agent_sidecar.agent_tools import TOOL_APPROVAL_INSTRUCTIONS

    runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", 0), FakeBackend())
    assert TOOL_APPROVAL_INSTRUCTIONS in str(runtime._execution_agent.instructions)


def test_terminal_commit_cannot_bypass_pending_sdk_tool_approval() -> None:
    backend = FakeBackend()
    context = AgentContext("project", "conversation", backend)
    context.active_tool_calls["pending-write"] = {"status": "pending_approval"}
    result = asyncio.run(commit_agent_action.on_invoke_tool(
        ToolContext(context, tool_name="commit_agent_action", tool_call_id="blocked-commit", tool_arguments="{}"),
        json.dumps({"decision": {"intent": "chat", "reply": "Waiting for approval", "confidence": 1}}),
    ))
    assert json.loads(result)["committed"] is False
    assert "awaiting approval" in json.loads(result)["validation_error"]
    assert backend.commit_calls == 0
    assert context.commit_result is None


def test_execution_agent_requires_authoritative_history_search_for_missing_memory() -> None:
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend()
    )

    assert "search_conversation_history" in str(runtime._execution_agent.instructions)
    assert "禁止未搜索就回答无法确认" in str(runtime._execution_agent.instructions)
    assert "必须优先回答，不得声称没有记忆" in str(runtime._execution_agent.instructions)


def test_execution_agent_uses_one_generic_skill_loader() -> None:
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend()
    )
    tool_names = {tool.name for tool in runtime._execution_agent.tools}
    assert "load_skill_instructions" in tool_names
    assert not any(name.startswith("consult_") for name in tool_names)
    assert not hasattr(runtime, "_skill_agents")


def test_runner_input_includes_validated_image_attachment() -> None:
    backend = FakeBackend()

    async def image_data_url(
        asset_id: str, snapshot_id: str
    ) -> str | None:
        assert (asset_id, snapshot_id) == ("image-1", "snapshot-1")
        return "data:image/jpeg;base64,dGVzdA=="

    backend.get_image_data_url = image_data_url  # type: ignore[method-assign]
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), backend
    )

    result = asyncio.run(
        runtime._runner_input(
            {
                "content": "评价这张图",
                "attachment_refs": [
                    {
                        "asset_id": "image-1",
                        "asset_snapshot_id": "snapshot-1",
                    }
                ],
            },
            "prompt",
        )
    )

    assert result == [
        {
            "role": "user",
            "content": [
                {"type": "input_text", "text": "prompt"},
                {
                    "type": "input_image",
                    "image_url": "data:image/jpeg;base64,dGVzdA==",
                    "detail": "auto",
                },
            ],
        }
    ]


def test_runner_input_includes_validated_video_frames() -> None:
    backend = FakeBackend()

    async def video_frame_data_urls(
        asset_id: str, snapshot_id: str
    ) -> list[str]:
        assert (asset_id, snapshot_id) == ("video-1", "snapshot-1")
        return [
            "data:image/jpeg;base64,ZnJhbWUx",
            "data:image/jpeg;base64,ZnJhbWUy",
        ]

    backend.get_video_frame_data_urls = video_frame_data_urls  # type: ignore[method-assign]
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), backend
    )

    result = asyncio.run(
        runtime._runner_input(
            {
                "content": "这个视频讲了什么",
                "attachment_refs": [
                    {
                        "asset_id": "video-1",
                        "asset_snapshot_id": "snapshot-1",
                    }
                ],
            },
            "prompt",
        )
    )

    assert isinstance(result, list)
    content = result[0]["content"]
    assert [item["type"] for item in content] == [
        "input_text",
        "input_text",
        "input_image",
        "input_image",
    ]
    assert content[2]["image_url"].endswith("ZnJhbWUx")


def test_runner_input_defers_video_frames_from_skill_input_binding() -> None:
    backend = FakeBackend()
    calls = 0

    async def video_frame_data_urls(
        _asset_id: str, _snapshot_id: str
    ) -> list[str]:
        nonlocal calls
        calls += 1
        return ["data:image/jpeg;base64,ZnJhbWU="]

    backend.get_video_frame_data_urls = video_frame_data_urls  # type: ignore[method-assign]
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), backend
    )

    result = asyncio.run(
        runtime._runner_input(
            {
                "capability_ref": {
                    "capability_id": "custom_asset_set_skill",
                    "version": "7.3.0",
                },
                "attachment_refs": [
                    {
                        "asset_id": "video-1",
                        "asset_snapshot_id": "snapshot-1",
                    }
                ],
            },
            "prompt",
            [
                {
                    "capability_id": "custom_asset_set_skill",
                    "input_binding": {
                        "source_type": "custom_video_source",
                        "asset_role": "primary_source",
                        "asset_set_purpose": "custom_video_collection",
                    },
                }
            ],
            {"custom_asset_set_skill"},
        )
    )

    assert result == "prompt"
    assert calls == 0


def test_generic_skill_loader_progressively_discloses_instructions() -> None:
    context = AgentContext(
        "project",
        "conversation",
        FakeBackend(),  # type: ignore[arg-type]
        routed_capabilities={"outline_critic"},
        capability_versions={"outline_critic": "1.0.0"},
    )

    output = json.loads(
        asyncio.run(_load_skill_instructions(context, "outline_critic"))
    )

    assert output["execution_mode"] == "inline"
    assert output["skill"]["instructions"].startswith("Diagnose the outline")
    assert output["default_config_ref"] == "review"
    assert output["config_options"] == ["review"]
    assert output["commands"] == ["inspect", "invoke"]
    assert output["input_schema"]["properties"]["outline"]["type"] == "string"
    assert output["config_schemas"]["review"]["properties"]["depth"]["enum"] == [
        "concise",
        "deep",
    ]
    assert context.consulted_capabilities == {"outline_critic"}
    assert set(context.loaded_capabilities) == {"outline_critic"}
    assert context.backend.capability_detail_requests == [
        ("outline_critic", "project", "1.0.0")
    ]


def test_generic_skill_loader_rejects_unrouted_id() -> None:
    context = AgentContext("project", "conversation", FakeBackend())  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="不在本轮"):
        asyncio.run(_load_skill_instructions(context, "outline_critic"))


def test_archived_selected_skill_version_controls_script_permissions() -> None:
    class VersionedBackend(FakeBackend):
        async def get_capabilities(self, _project_id: str = "") -> dict[str, object]:
            return {
                "data": {
                    "items": [
                        {
                            "capability_id": "versioned_skill",
                            "version": "2.0.0",
                            "label": "Versioned Skill",
                            "description": "Active version",
                            "execution_mode": "inline",
                            "status": "available",
                            "entry_policy": {"auto_route": False},
                            "routing": {},
                            "skill": {
                                "name": "versioned-skill",
                                "scripts": [{"id": "v2-script"}],
                            },
                        }
                    ]
                }
            }

        async def get_capability(
            self, capability_id: str, project_id: str = "", version: str = ""
        ) -> dict[str, object]:
            self.capability_detail_requests.append(
                (capability_id, project_id, version)
            )
            return {
                "data": {
                    "capability_id": capability_id,
                    "version": "1.0.0",
                    "label": "Versioned Skill",
                    "description": "Archived selected version",
                    "execution_mode": "inline",
                    "status": "available",
                    "entry_policy": {"auto_route": False},
                    "routing": {},
                    "skill": {
                        "name": "versioned-skill",
                        "scripts": [{"id": "v1-script"}],
                    },
                }
            }

    backend = VersionedBackend()
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), backend
    )
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        capability_versions={"versioned_skill": "1.0.0"},
    )
    active_items = asyncio.run(backend.get_capabilities("project"))["data"]["items"]  # type: ignore[index]
    resolved = asyncio.run(
        runtime._resolve_selected_capability_versions(
            context,
            active_items,  # type: ignore[arg-type]
            {"versioned_skill"},
        )
    )
    prepared = asyncio.run(
        runtime._tool_provider.prepare(
            context,
            resolved,
            {"versioned_skill"},
        )
    )

    assert resolved[0]["version"] == "1.0.0"
    assert context.allowed_skill_scripts == {("versioned_skill", "v1-script")}
    assert ("versioned_skill", "v2-script") not in context.allowed_skill_scripts
    assert backend.capability_detail_requests == [
        ("versioned_skill", "project", "1.0.0")
    ]
    assert context.consulted_capabilities == set()
    assert context.loaded_capabilities == {}
    assert prepared.mcp_servers == []


def test_routing_catalog_uses_registry_metadata_without_fixed_ids() -> None:
    items = asyncio.run(FakeBackend().get_capabilities())["data"]["items"]  # type: ignore[index]

    catalog = _routing_skill_catalog(items, auto_route_only=True)  # type: ignore[arg-type]

    assert {item["capability_id"] for item in catalog} == {
        "novel_to_script",
        "video_reference_creation",
        "outline_critic",
    }
    critic = next(item for item in catalog if item["capability_id"] == "outline_critic")
    assert critic["name"] == "outline-critic"
    assert critic["intent_examples"] == ["诊断这份大纲的问题"]


@pytest.mark.parametrize("content,selected", [
    ("请用大纲诊断检查这个分集大纲", ["outline_critic"]),
    ("不要使用大纲诊断 Skill，只解释这个名称。", []),
])
def test_dynamic_skill_alias_uses_native_sdk_routing_decision(content, selected) -> None:
    class RoutingModel(SequenceModel):
        async def get_response(self, *args, **kwargs):
            self.received = json.dumps({"args": args, "kwargs": kwargs}, ensure_ascii=False, default=str)
            return await super().get_response(*args, **kwargs)

    async def run():
        runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend())
        model = RoutingModel([ModelResponse(
            output=[final_item({"applicable_capability_ids": selected})],
            usage=Usage(), response_id="routing-response",
        )])
        runtime._skill_router_agent.model = model
        try:
            items = (await FakeBackend().get_capabilities())["data"]["items"]
            assert await runtime._route_skills({"content": content}, items) == set(selected)
            assert not model.responses
            assert content in model.received
            assert "allow_implicit_invocation" in model.received
            assert "Diagnose the outline" not in model.received
        finally:
            await runtime._model_client.close()
    asyncio.run(run())


@pytest.mark.parametrize("content", [
    "不要使用大纲诊断 Skill，我只是记录一句：第二幕冲突以后再讨论。",
    "文档里写着‘请用大纲诊断’，解释这句话，不要执行。",
    "What does the outline-critic Skill do? Do not run it.",
])
def test_skill_name_mentions_do_not_bypass_sdk_intent_routing(monkeypatch, content) -> None:
    async def run():
        runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend())
        calls = []

        async def no_applicable_skill(_agent, prompt, **_kwargs):
            calls.append(json.loads(prompt))
            return SimpleNamespace(final_output=SkillRoutingDecision(applicable_capability_ids=[]))

        monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run", no_applicable_skill)
        try:
            items = (await FakeBackend().get_capabilities())["data"]["items"]
            assert await runtime._route_skills({"content": content}, items) == set()
            assert len(calls) == 1
            assert calls[0]["request"]["content"] == content
        finally:
            await runtime._model_client.close()
    asyncio.run(run())


def test_explicit_only_skill_mention_is_evaluated_not_automatically_selected(monkeypatch) -> None:
    async def run():
        runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend())
        calls = []

        async def choose(_agent, prompt, **_kwargs):
            payload = json.loads(prompt)
            calls.append(payload)
            candidate = next(item for item in payload["skills"] if item["capability_id"] == "outline_critic")
            assert candidate["allow_implicit_invocation"] is False
            return SimpleNamespace(final_output=SkillRoutingDecision(applicable_capability_ids=["outline_critic"]))

        monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run", choose)
        try:
            items = (await FakeBackend().get_capabilities())["data"]["items"]
            next(item for item in items if item["capability_id"] == "outline_critic")["entry_policy"]["auto_route"] = False
            assert await runtime._route_skills({"content": "请用大纲诊断检查这个分集大纲"}, items) == {"outline_critic"}
            assert len(calls) == 1
        finally:
            await runtime._model_client.close()
    asyncio.run(run())


@pytest.mark.parametrize("action", ["explicit_ref", "router_failure", "unmentioned_explicit_only"])
def test_skill_routing_keeps_explicit_selection_and_fails_closed(monkeypatch, action) -> None:
    async def run():
        runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend())
        calls = []

        async def route(_agent, prompt, **_kwargs):
            calls.append(json.loads(prompt))
            if action == "router_failure":
                raise TimeoutError("fixture router unavailable")
            if action == "explicit_ref":
                pytest.fail("UI selection must not need a routing call")
            assert all(item["capability_id"] != "outline_critic" for item in calls[-1]["skills"])
            return SimpleNamespace(final_output=SkillRoutingDecision(applicable_capability_ids=["outline_critic"]))

        monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run", route)
        try:
            items = (await FakeBackend().get_capabilities())["data"]["items"]
            request = {"content": "请用大纲诊断检查内容"}
            if action == "explicit_ref":
                request["capability_ref"] = {"capability_id": "outline_critic", "version": "1.0.0"}
            elif action == "unmentioned_explicit_only":
                request = {"content": "Review this material."}
                next(item for item in items if item["capability_id"] == "outline_critic")["entry_policy"]["auto_route"] = False
            assert await runtime._route_skills(request, items) == set()
            assert len(calls) == (0 if action == "explicit_ref" else 1)
        finally:
            await runtime._model_client.close()
    asyncio.run(run())


def test_named_skill_after_catalog_budget_is_still_a_routing_candidate(monkeypatch) -> None:
    async def run():
        runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend())
        items = [{"capability_id": f"skill_{i}", "status": "available", "description": "x" * 1024,
                  "execution_mode": "inline", "entry_policy": {"auto_route": True}}
                 for i in range(70)]
        items.append({"capability_id": "z_last", "status": "available", "description": "Check a story.",
                      "execution_mode": "inline", "entry_policy": {"auto_route": False},
                      "skill": {"name": "last-skill", "path": "skills/last-skill/SKILL.md", "instructions": "private full instructions"}})

        async def choose(_agent, prompt, **_kwargs):
            payload = json.loads(prompt)
            assert payload["skills"][0]["capability_id"] == "z_last"
            assert payload["skills"][0]["path"] == "skills/last-skill/SKILL.md"
            assert "private full instructions" not in prompt
            assert len(json.dumps(payload["skills"], ensure_ascii=False, separators=(",", ":"))) <= 8000
            return SimpleNamespace(final_output=SkillRoutingDecision(applicable_capability_ids=["z_last"]))

        monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run", choose)
        try:
            assert await runtime._route_skills({"content": "Use $last-skill to check this story."}, items) == {"z_last"}
        finally:
            await runtime._model_client.close()
    asyncio.run(run())


def test_model_router_cannot_invent_unregistered_skill(monkeypatch) -> None:
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend()
    )
    items = asyncio.run(FakeBackend().get_capabilities())["data"]["items"]  # type: ignore[index]

    async def model_route(*_args, **_kwargs):  # type: ignore[no-untyped-def]
        return SimpleNamespace(
            final_output=SkillRoutingDecision(
                applicable_capability_ids=["outline_critic", "invented_skill"]
            )
        )

    monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run", model_route)
    routed = asyncio.run(
        runtime._route_skills(  # noqa: SLF001
            {"content": "帮我评审这个内容"}, items  # type: ignore[arg-type]
        )
    )

    assert routed == {"outline_critic"}


def test_applicable_skill_blocks_unconfirmed_generic_artifact_creation() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={"content": "基于这三章正文生成剧本"},
    )
    context.consulted_capabilities.add("novel_to_script")
    decision = ControlDecision(
        reply="已生成剧本。",
        intent="create_artifact",
        confidence=0.9,
        artifact_draft={
            "artifact_type": "generic_document",
            "title": "剧本",
            "payload": {"content": "正文"},
        },
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "clarify"
    assert payload["artifact_draft"] is None
    assert payload["clarification"]["options"] == ["使用 Skill", "由通用 Agent 直接生成"]


def test_inline_skill_executes_without_workflow_confirmation() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={"content": "诊断这份大纲"},
    )
    context.consulted_capabilities.add("outline_critic")
    decision = ControlDecision(
        reply="诊断完成。",
        intent="create_artifact",
        confidence=0.9,
        artifact_draft={
            "artifact_type": "generic_document",
            "title": "大纲诊断",
            "payload": {"content": "核心问题：冲突升级不足。"},
        },
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "create_artifact"
    assert payload["artifact_draft"]["title"] == "大纲诊断"
    assert payload["capability_ref"] == {
        "capability_id": "outline_critic",
        "version": "1.0.0",
        "selection_mode": "inferred",
    }


def test_background_skill_uses_generic_task_action_from_registry_mode() -> None:
    class BackgroundBackend(FakeBackend):
        async def get_capabilities(self, project_id: str = "") -> dict[str, object]:
            payload = await super().get_capabilities(project_id)
            items = payload["data"]["items"]  # type: ignore[index]
            items.append(  # type: ignore[union-attr]
                {
                    "capability_id": "story_research_digest",
                    "version": "1.0.0",
                    "label": "创作调研摘要",
                    "description": "后台整理创作资料。",
                    "execution_mode": "background_task",
                    "status": "available",
                    "entry_policy": {
                        "auto_route": True,
                        "requires_user_confirmation": False,
                    },
                    "routing": {
                        "explicit_aliases": ["后台调研"],
                        "intent_examples": ["后台整理创作资料"],
                    },
                    "skill": {"name": "story-research-digest"},
                }
            )
            return payload

    backend = BackgroundBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={
            "content": "后台调研这些资料",
            "capability_ref": {
                "capability_id": "story_research_digest",
                "version": "1.0.0",
                "selection_mode": "explicit",
            },
        },
    )
    decision = ControlDecision(
        reply="开始后台调研。",
        intent="propose_capability",
        confidence=1,
        capability_id="story_research_digest",
        capability_config={"depth": "focused"},
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["proposed_action"] == {
        "action_type": "start_background_task",
        "capability_ref": payload["capability_ref"],
        "input": {},
        "config": {"depth": "focused"},
        "requires_confirmation": False,
    }
    assert payload["reply"] == "已将「创作调研摘要」加入后台任务。"


def test_installed_stateful_skill_uses_registry_config_and_attachment_input() -> None:
    class StatefulSkillBackend(FakeBackend):
        async def get_capabilities(self, project_id: str = "") -> dict[str, object]:
            payload = await super().get_capabilities(project_id)
            items = payload["data"]["items"]  # type: ignore[index]
            items.append(  # type: ignore[union-attr]
                {
                    "capability_id": "story_review_workflow",
                    "version": "1.0.0",
                    "label": "故事评审流程",
                    "description": "按持久工作流评审故事。",
                    "execution_mode": "stateful_workflow",
                    "default_config_ref": "review",
                    "status": "available",
                    "entry_policy": {
                        "auto_route": False,
                        "requires_user_confirmation": True,
                    },
                    "routing": {
                        "explicit_aliases": ["故事评审流程"],
                        "intent_examples": ["评审故事并生成报告"],
                    },
                    "skill": {"name": "story-review-workflow"},
                }
            )
            return payload

    backend = StatefulSkillBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={
            "content": "使用故事评审流程",
            "capability_ref": {
                "capability_id": "story_review_workflow",
                "version": "1.0.0",
                "selection_mode": "explicit",
            },
            "attachment_refs": [
                {"asset_id": "ast_story", "asset_snapshot_id": "ass_story"}
            ],
        },
    )
    decision = ControlDecision(
        reply="请确认评审配置。",
        intent="propose_capability",
        confidence=1,
        capability_id="story_review_workflow",
        capability_config={"review_depth": "deep"},
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["proposed_action"] == {
        "action_type": "collect_run_configuration",
        "capability_ref": payload["capability_ref"],
        "input": {},
        "config": {
            "config_ref": "review",
            "payload": {"review_depth": "deep"},
        },
        "requires_confirmation": False,
    }


def test_applicable_skill_blocks_unconfirmed_chat_plan() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={"content": "写一集都市悬疑短剧，先告诉我准备怎么处理，不要启动"},
    )
    context.consulted_capabilities.add("novel_to_script")
    decision = ControlDecision(
        reply="我会先梳理人物和冲突。",
        intent="chat",
        confidence=0.9,
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "clarify"
    assert payload["proposed_action"] is None
    assert payload["clarification"]["options"] == ["使用 Skill", "由通用 Agent 直接生成"]


def test_explicit_generic_agent_choice_bypasses_skill_suggestion() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={"content": "不用 Skill，直接由通用 Agent 生成一集故事大纲"},
    )
    context.consulted_capabilities.add("novel_to_script")
    decision = ControlDecision(
        reply="已生成故事大纲。",
        intent="create_artifact",
        confidence=0.9,
        artifact_draft={
            "artifact_type": "generic_document",
            "title": "故事大纲",
            "payload": {"content": "正文"},
        },
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "create_artifact"
    assert payload["artifact_draft"]["title"] == "故事大纲"


def test_unconsulted_skill_clarification_is_rejected() -> None:
    context = AgentContext(
        "project",
        "conversation",
        FakeBackend(),  # type: ignore[arg-type]
        raw_request={"content": "只修改第1集最后一行"},
    )
    decision = ControlDecision(
        reply="请选择处理方式。",
        intent="clarify",
        confidence=0.9,
        clarification={
            "question": "请选择处理方式",
            "options": ["使用 Skill", "由通用 Agent 修改"],
        },
    )

    with pytest.raises(ValueError, match="不得提供 Skill 选项"):
        asyncio.run(_build_commit_payload(context, decision))


def test_propose_capability_without_skill_agent_consultation_is_blocked() -> None:
    backend = FakeBackend()
    context = AgentContext("project", "conversation", backend)  # type: ignore[arg-type]
    decision = ControlDecision(
        reply="使用小说转剧本。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="novel_to_script",
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "clarify"
    assert payload["proposed_action"] is None
    assert backend.commit_calls == 0


def test_explicit_skill_selection_does_not_require_redundant_consultation() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={
            "content": "请解析我提供的参考视频。",
            "capability_ref": {
                "capability_id": "video_reference_creation",
                "version": "1.2.0",
                "selection_mode": "explicit",
            },
            "attachment_refs": [
                {
                    "asset_id": "ast_video",
                    "asset_snapshot_id": "ass_video",
                    "display_name": "episode.mp4",
                }
            ],
        },
    )
    decision = ControlDecision(
        reply="请确认本轮配置。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="video_reference_creation",
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "propose_capability"
    assert payload["capability_ref"] == {
        "capability_id": "video_reference_creation",
        "version": "1.2.0",
        "selection_mode": "explicit",
    }
    assert payload["proposed_action"]["capability_ref"] == payload["capability_ref"]


def test_explicit_skill_selection_does_not_authorize_a_different_skill() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        raw_request={
            "content": "请解析我提供的参考视频。",
            "capability_ref": {
                "capability_id": "video_reference_creation",
                "version": "1.2.0",
                "selection_mode": "explicit",
            },
        },
    )
    decision = ControlDecision(
        reply="使用小说转剧本。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="novel_to_script",
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "clarify"
    assert payload["proposed_action"] is None


def test_inferred_skill_requires_user_confirmation_before_proposal() -> None:
    backend = FakeBackend()
    context = AgentContext("project", "conversation", backend)  # type: ignore[arg-type]
    context.consulted_capabilities.add("novel_to_script")
    decision = ControlDecision(
        reply="请确认本轮配置。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="novel_to_script",
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "clarify"
    assert payload["proposed_action"] is None
    assert payload["clarification"]["options"] == ["使用 Skill", "由通用 Agent 直接生成"]


def test_confirmed_inferred_skill_creates_current_proposal() -> None:
    backend = FakeBackend()
    context = AgentContext("project", "conversation", backend)  # type: ignore[arg-type]
    context.consulted_capabilities.add("novel_to_script")
    decision = ControlDecision(
        reply="请确认本轮配置。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="novel_to_script",
        skill_confirmed=True,
        capability_config={
            "target_episode_count": 3,
            "episode_duration_minutes": 2,
            "user_requirements": ["节奏紧凑"],
        },
        source_artifact_version_ids=["av_outline"],
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "propose_capability"
    assert payload["source_artifact_version_ids"] == ["av_outline"]
    assert payload["reply"] == "已为「小说转剧本」Skill 准备配置卡，请确认配置后再启动任务。"
    assert payload["proposed_action"]["capability_ref"]["capability_id"] == "novel_to_script"
    assert payload["proposed_action"]["config"] == {
        "config_ref": "creation",
        "payload": {
            "target_episode_count": 3,
            "episode_duration_minutes": 2,
            "user_requirements": ["节奏紧凑"],
        },
    }
    assert payload["proposed_action"]["input"] == {
        "artifact_versions": [
            {
                "artifact_version_id": "av_outline",
                "role": "primary_source",
                "order": 1,
            }
        ]
    }


def test_confirmed_skill_accepts_previous_sdk_consultation() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        previous_turn_consulted_capabilities={"novel_to_script"},
        raw_request={"content": "确认使用 Skill"},
    )
    decision = ControlDecision(
        reply="请确认本轮配置。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="novel_to_script",
        skill_confirmed=True,
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "propose_capability"
    assert payload["proposed_action"]["capability_ref"]["capability_id"] == "novel_to_script"


def test_ambiguous_follow_up_cannot_confirm_previous_skill_suggestion() -> None:
    backend = FakeBackend()
    context = AgentContext(
        "project",
        "conversation",
        backend,  # type: ignore[arg-type]
        previous_turn_consulted_capabilities={"novel_to_script"},
        raw_request={"content": "按照刚才你说的流程"},
    )
    decision = ControlDecision(
        reply="请确认本轮配置。",
        intent="propose_capability",
        confidence=0.9,
        capability_id="novel_to_script",
        skill_confirmed=True,
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["intent"] == "clarify"
    assert payload["proposed_action"] is None


def test_previous_turn_consultation_only_reads_latest_sdk_turn() -> None:
    items = [
        {"role": "user", "content": "旧请求"},
        {"type": "function_call", "name": "consult_novel_to_script_skill"},
        {"role": "user", "content": "当前上一轮请求"},
        {
            "type": "function_call",
            "name": "load_skill_instructions",
            "arguments": '{"capability_id":"outline_critic"}',
        },
        {"type": "function_call", "name": "commit_agent_action"},
    ]

    assert _previous_turn_consulted_capabilities(items) == {"outline_critic"}


def test_sdk_resolved_source_overrides_mismatched_main_agent_source() -> None:
    backend = FakeBackend()
    context = AgentContext(
        project_id="prj_test",
        conversation_id="conv_test",
        backend=backend,  # type: ignore[arg-type]
        consulted_capabilities={"novel_to_script"},
        resolved_source_artifact_version_ids=["av_outline"],
        source_resolution_attempted=True,
    )
    decision = ControlDecision(
        reply="已确认。",
        intent="propose_capability",
        confidence=1,
        capability_id="novel_to_script",
        skill_confirmed=True,
        source_artifact_version_ids=["av_wrong_script"],
    )

    payload = asyncio.run(_build_commit_payload(context, decision))

    assert payload["proposed_action"]["input"]["artifact_versions"] == [
        {
            "artifact_version_id": "av_outline",
            "role": "primary_source",
            "order": 1,
        }
    ]


@pytest.mark.parametrize(("decision", "expected_versions", "requires_binding"), [
    (ArtifactSourceRoutingDecision(), [], False),
    (ArtifactSourceRoutingDecision(needs_clarification=True), [], True),
    (ArtifactSourceRoutingDecision(artifact_titles=["Stored outline"]), ["av_outline"], True),
    (ArtifactSourceRoutingDecision(artifact_titles=["Missing outline"]), [], True),
    (ArtifactSourceRoutingDecision(artifact_titles=["Stored outline", "Missing outline"]), [], True),
    (ArtifactSourceRoutingDecision(artifact_titles=["Stored outline"], needs_clarification=True), [], True),
])
def test_source_resolution_distinguishes_request_text_from_unresolved_artifacts(monkeypatch, decision, expected_versions, requires_binding):
    runtime = OpenAIAgentsRuntime(make_settings("http://127.0.0.1:9/v1", 0), FakeBackend())

    async def resolve(*_args, **_kwargs):
        return SimpleNamespace(final_output=decision)

    monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run", resolve)
    result = asyncio.run(runtime._resolve_artifact_sources(
        {"content": "Use the supplied material"},
        {"items": [{"title": "Stored outline", "artifact_type": "generic_document", "current_version_id": "av_outline"}]},
        {"document"},
    ))
    assert result.version_ids == expected_versions
    assert result.requires_binding is requires_binding


def test_background_skill_can_start_from_text_in_the_user_request():
    backend = FakeBackend()

    async def capabilities(_project=""):
        return {"data": {"items": [{"capability_id": "background_fixture", "version": "1.0.0", "label": "Background", "execution_mode": "background_task", "status": "available", "entry_policy": {"requires_user_confirmation": False}}]}}

    backend.get_capabilities = capabilities
    context = AgentContext("project", "conversation", backend, raw_request={
        "content": "Summarize these supplied facts: the archive opens in June.",
        "capability_ref": {"capability_id": "background_fixture", "version": "1.0.0"},
    }, source_resolution_attempted=True, source_resolution_requires_binding=False)
    payload = asyncio.run(_build_commit_payload(context, ControlDecision(
        intent="propose_capability", capability_id="background_fixture", confidence=1,
        source_artifact_version_ids=["unrequested-artifact"],
    )))
    assert payload["proposed_action"]["action_type"] == "start_background_task"
    assert payload["proposed_action"]["input"] == {}
    assert not payload["proposed_action"]["requires_confirmation"]


def test_explicit_artifact_title_resolves_exact_current_version() -> None:
    catalog = {
        "items": [
            {
                "title": "《青铜钥匙》完整3集短剧剧本",
                "current_version_id": "av_script",
            },
            {
                "title": "《青铜钥匙》3集都市悬疑短剧故事大纲",
                "current_version_id": "av_outline",
            },
        ]
    }

    resolved = _explicit_artifact_versions(
        {
            "content": (
                "只以《青铜钥匙》3集都市悬疑短剧故事大纲为来源，"
                "生成3集剧本。"
            )
        },
        catalog,
    )

    assert resolved == ["av_outline"]


def test_focused_artifact_version_wins_for_document_skills() -> None:
    catalog = {
        "items": [
            {
                "title": "原创短剧本及续写方向",
                "current_version_id": "av_options",
                "artifact_type": "generic_document",
            },
            {
                "title": "完整续写剧本",
                "current_version_id": "av_script",
                "artifact_type": "generic_document",
            },
        ]
    }

    resolved = _explicit_artifact_versions(
        {
            "content": "请使用剧本续写 Skill。",
            "client_context": {"current_artifact_version_id": "av_script"},
        },
        catalog,
        {"text", "document"},
    )

    assert resolved == ["av_script"]


def test_focused_document_is_not_used_by_video_skill() -> None:
    catalog = {
        "items": [
            {
                "title": "当前文档",
                "current_version_id": "av_document",
                "artifact_type": "generic_document",
            }
        ]
    }

    resolved = _explicit_artifact_versions(
        {
            "content": "使用视频参考创作 Skill。",
            "client_context": {"current_artifact_version_id": "av_document"},
        },
        catalog,
        {"video"},
    )

    assert resolved == []


def test_stale_focused_version_is_not_used_as_skill_source() -> None:
    catalog = {
        "items": [
            {
                "title": "当前文档",
                "current_version_id": "av_current",
                "artifact_type": "generic_document",
            }
        ]
    }

    resolved = _explicit_artifact_versions(
        {
            "content": "使用非小说文本转剧本 Skill。",
            "client_context": {"current_artifact_version_id": "av_stale"},
        },
        catalog,
        {"text", "document"},
    )

    assert resolved == []


def test_execution_retries_terminal_commit_once(tmp_path) -> None:
    backend = FakeBackend()
    model = StreamingSequence([[final_item({"reply": "Uncommitted candidate"})]])
    repair = StreamingSequence([[tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "Done", "confidence": 1}}, "commit")]])
    runtime = execution_fixture(tmp_path, backend, model, repair)
    result = asyncio.run(
        execute_and_close(runtime,
            AgentExecutionRequest(
                project_id="prj_test",
                conversation_id="conv_test",
                request={"content": "我想写一本剧本"},
                idempotency_key="22222222-2222-4222-8222-222222222222",
            )
        )
    )

    assert len(model.inputs) == len(repair.inputs) == 1
    assert backend.commit_calls == 1
    assert backend.commit_decisions[0]["reply"] == "Done"
    assert result == {"data": {}}


def test_execution_does_not_preload_large_artifact_catalog(tmp_path) -> None:
    class LargeCatalogBackend(FakeBackend):
        async def search_artifacts(
            self,
            _project_id: str,
            query: str = "",
            artifact_type: str = "",
            limit: int = 20,
        ) -> dict[str, object]:
            return {
                "items": [
                    {
                        "artifact_id": f"artifact_{index}",
                        "current_version_id": f"version_{index}",
                        "title": "single episode script " + ("x" * 500),
                        "artifact_type": "script_unit",
                        "scope_key": f"episode:{index + 1}",
                    }
                    for index in range(50)
                ],
                "count": 50,
            }

    backend = LargeCatalogBackend()
    model = StreamingSequence([[tool_item("commit_agent_action", {"decision": {"intent": "chat", "reply": "Done", "confidence": 1}}, "commit")]])
    runtime = execution_fixture(tmp_path, backend, model)

    result = asyncio.run(
        execute_and_close(runtime,
            AgentExecutionRequest(
                project_id="prj_large",
                conversation_id="conv_large",
                request={"content": "How many episodes are in the complete script?"},
                idempotency_key="55555555-5555-4555-8555-555555555555",
            )
        )
    )

    assert result == {"data": {}} and backend.commit_calls == 1
    assert len(model.inputs) == 1
    assert "artifact_49" not in str(model.inputs[0])
    assert len(str(model.inputs[0])) < 2000


def test_streamed_execution_uses_runner_stream_and_filters_raw_control_delta(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), FakeBackend()
    )

    async def no_skill_route(_request, _items):  # type: ignore[no-untyped-def]
        return set()

    class StreamResult:
        interruptions: list[object] = []
        final_output = ""
        is_complete = False

        def __init__(self, context) -> None:  # type: ignore[no-untyped-def]
            self.context = context
            self.cancelled = False

        async def stream_events(self):  # type: ignore[no-untyped-def]
            yield SimpleNamespace(
                type="raw_response_event",
                data=SimpleNamespace(delta='{"reply":"private control"}'),
            )
            yield SimpleNamespace(
                type="run_item_stream_event",
                name="tool_called",
                item=SimpleNamespace(
                    raw_item=SimpleNamespace(name="inspect_project")
                ),
            )
            self.context.commit_result = {
                "data": {"agent_message": {"message_id": "msg_agent"}}
            }
            self.is_complete = True

        def cancel(self) -> None:
            self.cancelled = True

    def run_streamed(_agent, _input, **kwargs):  # type: ignore[no-untyped-def]
        return StreamResult(kwargs["context"])

    monkeypatch.setattr(runtime, "_route_skills", no_skill_route)
    monkeypatch.setattr("content_agent_sidecar.runtime.Runner.run_streamed", run_streamed)

    async def collect():  # type: ignore[no-untyped-def]
        stream = runtime.start_execution_stream(
            AgentExecutionRequest(
                project_id="prj_stream",
                conversation_id="conv_stream",
                request={"content": "inspect"},
                idempotency_key="77777777-7777-4777-8777-777777777777",
                agent_turn_id="turn_stream",
            )
        )
        return [event async for event in stream.events()]

    events = asyncio.run(collect())

    assert [event["event"] for event in events] == [
        "agent.tool.started",
        "agent.turn.committed",
    ]
    assert events[0]["data"] == {"tool_name": "inspect_project"}
    assert "private control" not in json.dumps(events)


def test_execution_without_terminal_commit_fails_without_out_of_runner_write(tmp_path) -> None:
    backend = FakeBackend()
    model = StreamingSequence([[final_item({"reply": "Uncommitted candidate"})]])
    repair = StreamingSequence([[final_item({"reply": "Still uncommitted"})] for _ in range(2)])
    runtime = execution_fixture(tmp_path, backend, model, repair)
    with pytest.raises(RuntimeCompatibilityError) as error:
        asyncio.run(
            execute_and_close(runtime,
                AgentExecutionRequest(
                    project_id="prj_test",
                    conversation_id="conv_test",
                    request={"content": "我想写一本剧本"},
                    idempotency_key="33333333-3333-4333-8333-333333333333",
                )
            )
        )

    assert len(model.inputs) == 1 and len(repair.inputs) == 2
    assert backend.commit_calls == 0
    assert str(error.value) == "Agents SDK run completed without commit_agent_action"


def test_execution_without_terminal_commit_rolls_session_back(tmp_path) -> None:
    backend = FakeBackend()
    model = StreamingSequence([[final_item({"reply": "Uncommitted candidate"})]])
    repair = StreamingSequence([[final_item({"reply": "Still uncommitted"})] for _ in range(2)])
    runtime = execution_fixture(tmp_path, backend, model, repair)

    async def run():
        session = SQLiteSession("prj_test:conv_test", tmp_path / "provider-session.db")
        previous = [{"role": "assistant", "content": "earlier turn"}]
        try:
            await session.add_items(previous)
            with pytest.raises(RuntimeCompatibilityError, match="without commit_agent_action"):
                await execute_and_close(runtime,
                AgentExecutionRequest(
                    project_id="prj_test",
                    conversation_id="conv_test",
                    request={"content": "我想写一本剧本"},
                    idempotency_key="44444444-4444-4444-8444-444444444444",
                )
            )
            assert await session.get_items() == previous
            assert len(model.inputs) == 1 and len(repair.inputs) == 2
            assert previous[0] in model.inputs[0] and backend.commit_calls == 0
        finally:
            session.close()

    asyncio.run(run())


@pytest.mark.parametrize("after_tool", [False, True])
def test_nonstream_model_recovery_is_not_reported_as_tool_approval(tmp_path, after_tool):
    from test_model_failure_production import transient_error

    backend = FakeBackend()
    steps = [[tool_item("inspect_recent_conversation", {"limit": 8}, "read")]] if after_tool else []
    model = StreamingSequence([*steps, transient_error()])
    runtime = execution_fixture(tmp_path, backend, model)

    async def run():
        session = SQLiteSession("p:c", tmp_path / "provider-session.db")
        previous = [{"role": "assistant", "content": "Earlier committed turn"}]
        try:
            await session.add_items(previous)
            with pytest.raises(RuntimeCompatibilityError, match="model recovery requires the streamed execution protocol") as error:
                await execute_and_close(runtime, AgentExecutionRequest(project_id="p", conversation_id="c", idempotency_key="i", request={"content": "Read"}))
            assert error.value.failure_stage == "run_state_checkpoint"
            assert backend.commit_calls == 0 and len(model.inputs) == 1 + int(after_tool)
            assert await session.get_items() == previous
        finally:
            session.close()

    asyncio.run(run())


class StaticModel:
    def __init__(self, response: ModelResponse) -> None:
        self.response = response

    async def get_response(self, *_args, **_kwargs):  # type: ignore[no-untyped-def]
        return self.response

    def stream_response(self, *_args, **_kwargs):  # type: ignore[no-untyped-def]
        raise AssertionError("streaming is not used by this adapter test")

    async def close(self) -> None:
        return None


def text_response(text: str) -> ModelResponse:
    return ModelResponse(
        output=[
            ResponseOutputMessage(
                id="msg_test",
                content=[
                    ResponseOutputText(
                        annotations=[], logprobs=[], text=text, type="output_text"
                    )
                ],
                role="assistant",
                status="completed",
                type="message",
            )
        ],
        usage=Usage(requests=1),
        response_id=None,
    )


def test_required_commit_adapter_converts_valid_decision_json_to_sdk_tool_call() -> None:
    decision = {
        "reply": "可以开始创作。",
        "intent": "chat",
        "confidence": 0.9,
        "capability_id": None,
        "clarification": None,
        "artifact_draft": None,
        "target_ref": None,
        "goal_update": {
            "action": "set",
            "title": "完成三集短剧",
            "success_criteria": ["三集均已生成"],
        },
    }
    adapter = RequiredCommitModelAdapter(
        StaticModel(text_response(json.dumps(decision, ensure_ascii=False)))  # type: ignore[arg-type]
    )
    response = asyncio.run(
        adapter.get_response(
            "instructions",
            "input",
            ModelSettings(tool_choice="required"),
            [],
            None,
            [],
            ModelTracing.DISABLED,
            previous_response_id=None,
            conversation_id=None,
            prompt=None,
        )
    )

    assert len(response.output) == 1
    assert isinstance(response.output[0], ResponseFunctionToolCall)
    assert response.output[0].name == "commit_agent_action"
    expected_decision = {
        **decision,
        "artifact_drafts": [],
        "capability_config": {},
        "source_artifact_version_ids": [],
    }
    assert json.loads(response.output[0].arguments) == {"decision": expected_decision}


def test_required_commit_adapter_does_not_invent_tool_call_for_invalid_text() -> None:
    original = text_response("这不是可校验的决策 JSON")
    adapter = RequiredCommitModelAdapter(StaticModel(original))  # type: ignore[arg-type]
    response = asyncio.run(
        adapter.get_response(
            "instructions",
            "input",
            ModelSettings(tool_choice="required"),
            [],
            None,
            [],
            ModelTracing.DISABLED,
            previous_response_id=None,
            conversation_id=None,
            prompt=None,
        )
    )

    assert response is original


def test_sdk_runner_executes_adapted_terminal_commit_tool() -> None:
    backend = FakeBackend()
    runtime = OpenAIAgentsRuntime(
        make_settings("http://127.0.0.1:9/v1", retries=0), backend
    )
    decision = {
        "reply": "已理解你的请求。",
        "intent": "chat",
        "confidence": 0.95,
        "capability_id": None,
        "clarification": None,
        "artifact_draft": None,
        "target_ref": None,
    }
    runtime._commit_repair_agent.model = RequiredCommitModelAdapter(
        StaticModel(text_response(json.dumps(decision, ensure_ascii=False)))  # type: ignore[arg-type]
    )
    context = AgentContext(
        project_id="prj_test",
        conversation_id="conv_test",
        backend=backend,  # type: ignore[arg-type]
        raw_request={"content": "你好"},
        idempotency_key="44444444-4444-4444-8444-444444444444",
    )

    asyncio.run(
        Runner.run(
            runtime._commit_repair_agent,
            "提交本轮终态",
            context=context,
            max_turns=2,
        )
    )

    assert backend.commit_calls == 1
    assert backend.commit_decisions == [
        {
            "reply": "已理解你的请求。",
            "intent": "chat",
                "confidence": 0.95,
                "source_artifact_version_ids": [],
                "capability_ref": None,
            "target_ref": None,
            "clarification": None,
                "proposed_action": None,
                "artifact_draft": None,
                "artifact_drafts": [],
                "goal_update": None,
            }
        ]
    assert context.commit_result == {"data": {}}
