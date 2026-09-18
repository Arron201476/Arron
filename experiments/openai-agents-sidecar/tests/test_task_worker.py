import asyncio
import json
from types import SimpleNamespace

import pytest

from agents import Agent as SDKAgent, ModelBehaviorError

from content_agent_sidecar.task_worker import RuntimeJSONOutputSchema, SDKTaskWorker
import content_agent_sidecar.task_worker as task_worker_module


def test_task_worker_slots_have_distinct_claim_identities() -> None:
    from test_app import settings

    first = SDKTaskWorker(settings(), slot=1)
    second = SDKTaskWorker(settings(), slot=2)

    assert first._worker_id.endswith("-slot-1")
    assert second._worker_id.endswith("-slot-2")
    assert first._worker_id != second._worker_id


def test_task_worker_uses_content_model_without_changing_control_model() -> None:
    from test_app import settings

    configured = settings()
    configured = type(configured)(
        **{
            **configured.__dict__,
            "content_model_base_url": "https://content.example/v1",
            "content_model_api_key": "content-key",
            "content_model_name": "gemini-3p5-flash-routerhub",
            "content_model_max_output_tokens": 8192,
        }
    )

    worker = SDKTaskWorker(configured)

    assert configured.model_name == "gpt-test-model"
    assert worker._content_model_name == "gemini-3p5-flash-routerhub"
    assert type(worker._model).__name__ == "RouterHubGeminiChatCompletionsModel"
    assert worker._max_tokens == 8192


def test_parse_and_validate_accepts_matching_json_object() -> None:
    pack = {
        "output_contract": {
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
                "additionalProperties": False,
            }
        }
    }

    assert SDKTaskWorker._parse_and_validate('{"title":"故事圣经"}', pack) == {
        "title": "故事圣经"
    }


def test_runtime_json_output_schema_returns_validated_object() -> None:
    schema = RuntimeJSONOutputSchema(
        {
            "type": "object",
            "required": ["title"],
            "properties": {"title": {"type": "string"}},
        }
    )

    assert schema.validate_json('{"title":"分集卡"}') == {"title": "分集卡"}


def test_runtime_json_output_schema_preserves_partial_object_for_runtime_repair() -> None:
    schema = RuntimeJSONOutputSchema(
        {
            "type": "object",
            "required": ["title"],
            "properties": {"title": {"type": "string"}},
        }
    )

    assert schema.validate_json('{"wrong":"value"}') == {"wrong": "value"}


def test_runtime_json_output_schema_rejects_invalid_json() -> None:
    schema = RuntimeJSONOutputSchema({"type": "object"})

    with pytest.raises(ModelBehaviorError, match="valid JSON"):
        schema.validate_json('{"title":')


def test_execute_claim_repairs_invalid_structured_output(monkeypatch) -> None:
    class FakeRun:
        final_output = {"wrong": "value"}
        context_wrapper = SimpleNamespace(usage=None)
        raw_responses = []

        async def stream_events(self):
            if False:
                yield None

    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(run_timeout_seconds=1)
    worker._model = object()
    worker._max_tokens = 100
    repaired = {"title": "已修复"}
    repair_calls = []
    captured_run_config = None

    async def render_input(pack):
        return "instructions", [{"role": "user", "content": "input"}]

    async def repair(pack, output, error):
        repair_calls.append((output, str(error)))
        return repaired, FakeRun()

    worker._render_input = render_input
    worker._repair = repair
    monkeypatch.setattr(task_worker_module, "Agent", lambda **kwargs: SDKAgent(**{**kwargs, "model": None}))
    def run_streamed(*args, **kwargs):
        nonlocal captured_run_config
        assert args[0].input_guardrails and args[0].output_guardrails
        captured_run_config = kwargs["run_config"]
        return FakeRun()

    monkeypatch.setattr(task_worker_module.Runner, "run_streamed", run_streamed)
    pack = {
        "provider_result_contract": {
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
            }
        }
    }

    payload, _, _ = asyncio.run(worker._execute_generation({"context_pack": pack}))

    assert payload == repaired
    assert repair_calls == [('{"wrong": "value"}', "'title' is a required property")]
    assert captured_run_config.tracing_disabled is True
    assert captured_run_config.trace_include_sensitive_data is False


def test_execute_claim_retries_when_sdk_rejects_structured_transport(monkeypatch) -> None:
    class RejectedRun:
        def cancel(self):
            pass

        async def stream_events(self):
            raise ModelBehaviorError("'script_analysis' is a required property")
            yield None

    class CompatibleRun:
        final_output = '{"script_analysis":{"summary":"已分析"}}'
        context_wrapper = SimpleNamespace(usage=None)
        raw_responses = []

        async def stream_events(self):
            if False:
                yield None

    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(run_timeout_seconds=1)
    worker._model = object()
    worker._max_tokens = 100

    async def render_input(pack):
        return "instructions", [{"role": "user", "content": "input"}]

    worker._render_input = render_input
    created_agents = []

    def create_agent(**kwargs):
        created_agents.append(kwargs)
        return SDKAgent(**{**kwargs, "model": None})

    runs = iter([RejectedRun(), CompatibleRun()])
    used_agents = []

    def stream(agent, *args, **kwargs):
        used_agents.append(agent)
        return next(runs)

    monkeypatch.setattr(task_worker_module, "Agent", create_agent)
    monkeypatch.setattr(
        task_worker_module.Runner, "run_streamed", stream
    )
    pack = {
        "provider_result_contract": {
            "artifact_type": "provider_result",
            "schema": {
                "type": "object",
                "required": ["script_analysis"],
                "properties": {
                    "script_analysis": {
                        "type": "object",
                        "required": ["summary"],
                        "properties": {"summary": {"type": "string"}},
                    }
                },
            },
        }
    }

    payload, _, _ = asyncio.run(worker._execute_generation({"context_pack": pack}))

    assert payload == {"script_analysis": {"summary": "已分析"}}
    assert len(used_agents) == 2
    assert used_agents[0].output_type is not None and used_agents[1].output_type is None
    assert used_agents[0].tools == used_agents[1].tools


def test_routerhub_execution_uses_plain_json_transport_from_first_request(
    monkeypatch,
) -> None:
    class CompatibleRun:
        final_output = '{"title":"已生成"}'
        context_wrapper = SimpleNamespace(usage=None)
        raw_responses = []

        async def stream_events(self):
            if False:
                yield None

    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(run_timeout_seconds=1)
    worker._model = object()
    worker._max_tokens = 100
    worker._content_model_name = "gemini-3p5-flash-routerhub"

    async def render_input(pack):
        return "instructions", [{"role": "user", "content": "input"}]

    worker._render_input = render_input
    used_agents = []

    def stream(agent, *args, **kwargs):
        used_agents.append(agent)
        return CompatibleRun()

    monkeypatch.setattr(
        task_worker_module, "Agent", lambda **kwargs: SDKAgent(**{**kwargs, "model": None})
    )
    monkeypatch.setattr(task_worker_module.Runner, "run_streamed", stream)
    pack = {
        "output_contract": {
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
            }
        },
        "upstream_context": [
            {
                "artifact_version_id": "av_context_1",
                "artifact_type": "story_bible",
                "content": {"title": "权威版本"},
            }
        ],
    }

    payload, _, _ = asyncio.run(worker._execute_generation({"context_pack": pack}))

    assert payload == {"artifact": {"title": "已生成"}}
    assert len(used_agents) == 1
    assert used_agents[0].output_type is None
    assert used_agents[0].model_settings.tool_choice == "required"
    assert len(used_agents[0].tools) == 1


def test_execute_claim_retries_plain_json_when_gateway_rejects_schema(monkeypatch) -> None:
    class FakeBadRequestError(Exception):
        pass

    class RejectedRun:
        def cancel(self):
            pass

        async def stream_events(self):
            raise FakeBadRequestError("schema rejected")
            yield None

    class CompatibleRun:
        final_output = '{"title":"已生成"}'
        context_wrapper = SimpleNamespace(usage=None)
        raw_responses = []

        async def stream_events(self):
            if False:
                yield None

    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(run_timeout_seconds=1)
    worker._model = object()
    worker._max_tokens = 100

    async def render_input(pack):
        return "instructions", [{"role": "user", "content": "input"}]

    worker._render_input = render_input
    created_agents = []

    def create_agent(**kwargs):
        created_agents.append(kwargs)
        return SDKAgent(**{**kwargs, "model": None})

    runs = iter([RejectedRun(), CompatibleRun()])
    used_agents = []

    def stream(agent, *args, **kwargs):
        used_agents.append(agent)
        return next(runs)

    monkeypatch.setattr(task_worker_module, "BadRequestError", FakeBadRequestError)
    monkeypatch.setattr(task_worker_module, "Agent", create_agent)
    monkeypatch.setattr(
        task_worker_module.Runner, "run_streamed", stream
    )
    pack = {
        "output_contract": {
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
            }
        }
    }

    payload, _, _ = asyncio.run(worker._execute_generation({"context_pack": pack}))

    assert payload == {"artifact": {"title": "已生成"}}
    assert len(used_agents) == 2
    assert used_agents[0].output_type is not None and used_agents[1].output_type is None
    assert used_agents[0].mcp_servers == used_agents[1].mcp_servers


def test_repair_compatibility_retry_uses_plain_json_text_transport(monkeypatch) -> None:
    class CompatibleRun:
        final_output = '{"title":"已修复"}'

        async def stream_events(self):
            if False:
                yield None

    class RejectedRun:
        async def stream_events(self):
            raise ModelBehaviorError("'title' is a required property")
            yield None

        def cancel(self):
            pass

    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(run_timeout_seconds=1)
    worker._model = object()
    worker._max_tokens = 100
    created_agents = []

    def create_agent(**kwargs):
        created_agents.append(kwargs)
        return SDKAgent(**{**kwargs, "model": None})

    calls = 0

    def run(*args, **kwargs):
        nonlocal calls
        calls += 1
        if calls == 1:
            return RejectedRun()
        return CompatibleRun()

    monkeypatch.setattr(task_worker_module, "Agent", create_agent)
    monkeypatch.setattr(task_worker_module.Runner, "run_streamed", run)
    pack = {
        "output_contract": {
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
            }
        }
    }

    payload, _ = asyncio.run(worker._repair(pack, '{"wrong":true}', ValueError("bad")))

    assert payload == {"title": "已修复"}
    assert len(created_agents) == 2
    assert "output_type" not in created_agents[1]


def test_execute_video_claim_retries_fresh_result_before_runner(monkeypatch) -> None:
    class FakeRun:
        final_output = {}
        context_wrapper = SimpleNamespace(usage=None)
        raw_responses = []

        async def stream_events(self):
            if False:
                yield None

    class FakeVideoTool:
        def __init__(self) -> None:
            self.last_result = ""
            self.usage = {"total_tokens": 10}
            self.trace_ref = "video-trace"
            self.calls = 0
            self.instructions = []
            self.discarded = []

        async def analyze(self, _pack, instructions):
            self.calls += 1
            self.instructions.append(instructions)
            if self.calls == 1:
                self.last_result = json.dumps(
                    {
                        "artifact_type": "video_script_unit",
                        "schema_ref": "video_script_unit.v1",
                        "schema_version": "1.0.0",
                        "content": {"episode_no": 1, "scenes": []},
                    },
                    ensure_ascii=False,
                )
                return self.last_result
            self.last_result = json.dumps(
                {
                    "artifact_type": "video_script_unit",
                    "schema_ref": "video_script_unit.v1",
                    "schema_version": "1.0.0",
                    "content": {
                        "episode_no": 1,
                        "scenes": [
                            {
                                "heading": "场1-1 面馆 日 内",
                                "characters": ["孙长贵"],
                                "blocks": [
                                    {
                                        "block_type": "dialogue",
                                        "speaker": "孙长贵",
                                        "text": "今天牛肉面十四元。",
                                    }
                                ],
                            }
                        ],
                    }
                },
                ensure_ascii=False,
            )
            return self.last_result

        def discard_cached(self, context_hash):
            self.discarded.append(context_hash)

    worker = object.__new__(SDKTaskWorker)
    worker._settings = SimpleNamespace(
        run_timeout_seconds=1,
        video_task_timeout_seconds=1,
    )
    worker._model = object()
    worker._max_tokens = 100
    worker._video_tool = FakeVideoTool()

    async def render_input(_pack):
        return "instructions", [{"role": "user", "content": "input"}]

    worker._render_input = render_input
    created_agents = []

    def create_agent(**kwargs):
        created_agents.append(kwargs)
        return object()

    monkeypatch.setattr(task_worker_module, "Agent", create_agent)
    monkeypatch.setattr(
        task_worker_module.Runner, "run_streamed", lambda *args, **kwargs: FakeRun()
    )
    schema = {
        "type": "object",
        "required": ["episode_no", "script_text", "scenes"],
        "properties": {
            "episode_no": {"type": "integer"},
            "script_text": {"type": "string", "minLength": 1},
            "scenes": {"type": "array", "minItems": 1},
        },
    }
    pack = {
        "output_contracts": [
            {"artifact_type": "video_script_unit", "schema": schema}
        ],
        "task_cursor": {"video": {"episode_order": 1}},
        "context_hash": "video-context-1",
    }

    payload, usage, trace_ref = asyncio.run(
        worker._execute_generation(
            {
                "executor_id": "workflow.video_script_extract",
                "context_pack": pack,
            }
        )
    )

    assert worker._video_tool.calls == 2
    assert worker._video_tool.discarded == ["video-context-1"]
    assert "final video_script_unit JSON object" in worker._video_tool.instructions[0]
    assert "Simplified Chinese" in worker._video_tool.instructions[0]
    assert "never translate or transliterate" in worker._video_tool.instructions[0]
    assert created_agents == []
    unit = SDKTaskWorker._video_script_unit(payload)
    assert unit is not None
    assert "孙长贵：今天牛肉面十四元。" in unit["script_text"]
    assert usage["video_provider"] == {"total_tokens": 10}
    assert trace_ref == "video-trace"


def test_video_output_quality_rejects_english_labels_and_sparse_coverage() -> None:
    with pytest.raises(ValueError, match="labels must be Chinese"):
        SDKTaskWorker._assert_video_output_quality(
            {
                "video_script_unit": {
                    "scenes": [
                        {
                            "characters": ["Shen"],
                            "blocks": [{"block_type": "dialogue", "speaker": "Shen"}],
                            "source_refs": [],
                        }
                    ]
                }
            },
            0,
        )


def test_assemble_video_evidence_preserves_authoritative_subtitles_and_actions() -> None:
    pack = {
        "output_contracts": [
            {"artifact_type": "video_script_unit", "schema": {"type": "object"}}
        ],
        "task_cursor": {
            "video": {
                "episode_no": 1,
                "episode_order": 1,
                "file_name": "第一集.mp4",
                "asset_id": "ast_video_1",
                "asset_snapshot_id": "ass_video_1",
            }
        },
    }
    subtitles = [
        {
            "subtitle_id": "subtitle_0001",
            "start_ms": 1_000,
            "end_ms": 2_000,
            "text": "你终于来了。",
        },
        {
            "subtitle_id": "subtitle_0002",
            "start_ms": 4_000,
            "end_ms": 5_000,
            "text": "我一直在等你。",
        },
    ]
    evidence = {
        "video_evidence": {
            "title": "重逢",
            "plot_summary": "两人在门口重逢。",
            "scenes": [{
                "scene_id": "scene_1_1",
                "heading": "场1-1 门口 日 外",
                "location": "门口",
                "interior_exterior": "外",
                "time_of_day": "日",
                "time_range": {"start_ms": 0, "end_ms": 10_000},
                "characters": ["林夏", "陈知"],
                "actions": [{
                    "text": "△林夏停下脚步。",
                    "time_range": {"start_ms": 500, "end_ms": 900},
                }],
            }],
            "dialogue_annotations": [
                {"subtitle_id": "subtitle_0001", "speaker": "林夏", "delivery": "低声"},
                {"subtitle_id": "subtitle_0002", "speaker": "陈知", "delivery": ""},
            ],
        }
    }

    assembled = SDKTaskWorker._assemble_video_evidence_payload(
        evidence, pack, subtitles, 10_000
    )
    normalized = SDKTaskWorker._normalize_video_script_payload(assembled, pack)
    unit = normalized["video_script_unit"]

    assert [block["text"] for block in unit["scenes"][0]["blocks"]] == [
        "林夏停下脚步。",
        "你终于来了。",
        "我一直在等你。",
    ]
    assert "△林夏停下脚步。" in unit["script_text"]
    assert "林夏（低声）：你终于来了。" in unit["script_text"]
    SDKTaskWorker._assert_video_output_quality(
        normalized, 10_000, require_speaker_resolution=True
    )


def test_assemble_video_evidence_rejects_missing_subtitle_annotation() -> None:
    with pytest.raises(ValueError, match="subtitle annotations do not match"):
        SDKTaskWorker._assemble_video_evidence_payload(
            {
                "video_evidence": {
                    "scenes": [{
                        "heading": "场1-1 门口 日 外",
                        "time_range": {"start_ms": 0, "end_ms": 10_000},
                        "actions": [{"text": "林夏停下脚步。"}],
                    }],
                    "dialogue_annotations": [],
                }
            },
            {"task_cursor": {"video": {"episode_order": 1}}},
            [{
                "subtitle_id": "subtitle_0001",
                "start_ms": 1_000,
                "end_ms": 2_000,
                "text": "你终于来了。",
            }],
            10_000,
        )


def test_assemble_video_evidence_accepts_explicit_schema_wrapper() -> None:
    payload = {
        "schema": {
            "video_evidence": {
                "scenes": [{
                    "heading": "场1-1 门口 日 外",
                    "time_range": {"start_ms": 0, "end_ms": 10_000},
                    "actions": [{"text": "林夏停下脚步。"}],
                }],
                "dialogue_annotations": [{
                    "subtitle_id": "subtitle_0001",
                    "speaker": "林夏",
                    "delivery": "低声",
                }],
            }
        }
    }
    result = SDKTaskWorker._assemble_video_evidence_payload(
        payload,
        {"task_cursor": {"video": {"episode_order": 1}}},
        [{
            "subtitle_id": "subtitle_0001",
            "start_ms": 1_000,
            "end_ms": 2_000,
            "text": "你终于来了。",
        }],
        10_000,
    )

    assert result["video_script_unit"]["scenes"][0]["blocks"][1]["text"] == "你终于来了。"


def test_video_output_quality_rejects_scene_without_action() -> None:
    with pytest.raises(ValueError, match="action blocks"):
        SDKTaskWorker._assert_video_output_quality(
            {
                "video_script_unit": {
                    "source_refs": [{
                        "source_type": "video_time_range",
                        "time_range": {"start_ms": 0, "end_ms": 10_000},
                    }],
                    "scenes": [{
                        "scene_id": "scene_1_1",
                        "characters": ["林夏"],
                        "blocks": [{
                            "block_type": "dialogue",
                            "speaker": "林夏",
                            "text": "你终于来了。",
                        }],
                    }],
                }
            },
            10_000,
        )


def test_video_output_quality_rejects_mostly_unknown_speakers_after_attribution() -> None:
    payload = {
        "video_script_unit": {
            "scenes": [{
                "characters": ["食客甲", "食客乙"],
                "blocks": [
                    {"block_type": "dialogue", "speaker": "未知人物", "text": "第一句"},
                    {"block_type": "dialogue", "speaker": "未知人物", "text": "第二句"},
                    {"block_type": "dialogue", "speaker": "未知人物", "text": "第三句"},
                ],
            }]
        }
    }

    SDKTaskWorker._assert_video_output_quality(payload, 0)
    with pytest.raises(ValueError, match="mostly unresolved"):
        SDKTaskWorker._assert_video_output_quality(
            payload, 0, require_speaker_resolution=True
        )
    with pytest.raises(ValueError, match="coverage incomplete"):
        SDKTaskWorker._assert_video_output_quality(
            {
                "video_script_unit": {
                    "scenes": [
                        {
                            "characters": ["沈师傅"],
                            "blocks": [{"block_type": "dialogue", "speaker": "沈师傅"}],
                            "source_refs": [
                                {
                                    "source_type": "video_time_range",
                                    "time_range": {"start_ms": 0, "end_ms": 60_000},
                                }
                            ],
                        }
                    ]
                }
            },
            1_000_000,
        )


@pytest.mark.parametrize(
    "payload",
    [
        {"video_script_unit": [{"scenes": []}]},
        {"artifact": [{"scenes": []}]},
        {"video_script_unit": '{"scenes": []}'},
        {"artifact": {"video_script_unit": {"scenes": []}}},
    ],
)
def test_video_script_unit_unwraps_supported_single_object_envelopes(
    payload: dict[str, object],
) -> None:
    assert SDKTaskWorker._video_script_unit(payload) == {"scenes": []}


@pytest.mark.parametrize(
    "payload",
    [
        {"video_script_unit": []},
        {"video_script_unit": [{"scenes": []}, {"scenes": []}]},
        {"video_script_unit": "not-json"},
        {"result": {"scenes": []}},
    ],
)
def test_video_script_unit_rejects_ambiguous_or_unknown_envelopes(
    payload: dict[str, object],
) -> None:
    assert SDKTaskWorker._video_script_unit(payload) is None


def test_video_output_quality_rejects_evidence_beyond_source_duration() -> None:
    with pytest.raises(ValueError, match="exceeds source duration"):
        SDKTaskWorker._assert_video_output_quality(
            {
                "video_script_unit": {
                    "source_refs": [{
                        "source_type": "video_time_range",
                        "time_range": {"start_ms": 0, "end_ms": 155_000},
                    }],
                    "scenes": [{
                        "characters": ["宋师傅"],
                        "blocks": [{
                            "block_type": "dialogue",
                            "speaker": "宋师傅",
                            "source_refs": [{
                                "source_type": "video_time_range",
                                "time_range": {"start_ms": 230_000, "end_ms": 235_000},
                            }],
                        }],
                    }],
                }
            },
            154_861,
        )


def test_provider_result_runtime_schema_uses_exact_transport_contract() -> None:
    schema = {
        "type": "object",
        "required": ["video_script_unit"],
        "properties": {"video_script_unit": {"type": "object"}},
        "additionalProperties": False,
    }
    pack = {
        "provider_result_contract": {
            "artifact_type": "provider_result",
            "schema": schema,
        }
    }

    runtime_schema = SDKTaskWorker._runtime_output_schema(pack)

    assert runtime_schema.json_schema() is schema
    assert runtime_schema.validate_json(
        '{"video_script_unit":{"episode_no":1}}'
    ) == {"video_script_unit": {"episode_no": 1}}


def test_provider_result_runtime_schema_preserves_existing_transport_wrapper() -> None:
    wrapped = {
        "type": "object",
        "required": ["video_script_unit"],
        "properties": {"video_script_unit": {"type": "object"}},
        "additionalProperties": False,
    }
    pack = {
        "provider_result_contract": {
            "artifact_type": "video_script_unit",
            "schema": wrapped,
        }
    }

    assert SDKTaskWorker._runtime_output_schema(pack).json_schema() is wrapped


@pytest.mark.parametrize("field", ["title", "artifact", "provider_result"])
def test_direct_skill_result_uses_exact_provider_schema(field: str) -> None:
    schema = {
        "type": "object",
        "required": [field, "content"],
        "properties": {field: {"type": "string"}, "content": {"type": "string"}},
        "additionalProperties": False,
    }
    pack = {
        "provider_result_contract": {"artifact_type": "provider_result", "schema": schema},
        "output_contract": {"artifact_type": "generic_document", "schema": schema},
    }
    payload = {field: "Review", "content": "Completed"}
    assert SDKTaskWorker._runtime_output_schema(pack).json_schema() is schema
    assert SDKTaskWorker._parse_and_validate(json.dumps(payload), pack) == payload
    assert SDKTaskWorker._provider_envelope(payload, pack) is payload
    with pytest.raises(ValueError):
        SDKTaskWorker._parse_and_validate(json.dumps({"artifact": payload}), pack)


def test_provider_result_runtime_schema_adds_object_type_to_all_of_contract() -> None:
    schema = {
        "allOf": [
            {
                "type": "object",
                "required": ["status"],
                "properties": {"status": {"type": "string"}},
            },
            {"$defs": {"issue": {"type": "object"}}},
        ]
    }
    pack = {"provider_result_contract": {"schema": schema}}

    runtime_schema = SDKTaskWorker._runtime_output_schema(pack).json_schema()

    assert runtime_schema is not schema
    assert runtime_schema["type"] == "object"
    assert "allOf" not in runtime_schema
    assert runtime_schema["required"] == ["status"]
    assert runtime_schema["$defs"] == {"issue": {"type": "object"}}
    assert "type" not in schema


def test_single_artifact_runtime_schema_adds_transport_wrapper() -> None:
    artifact_schema = {
        "type": "object",
        "required": ["episode_no"],
        "properties": {"episode_no": {"type": "integer"}},
        "additionalProperties": False,
    }
    pack = {
        "output_contracts": [
            {"artifact_type": "video_script_unit", "schema": artifact_schema}
        ]
    }

    runtime_schema = SDKTaskWorker._runtime_output_schema(pack)

    assert runtime_schema.validate_json(
        '{"video_script_unit":{"episode_no":1}}'
    ) == {"video_script_unit": {"episode_no": 1}}


def test_task_checkpoint_runtime_schema_remains_bare() -> None:
    checkpoint_schema = {
        "type": "object",
        "required": ["episodes"],
        "properties": {"episodes": {"type": "array"}},
    }
    pack = {
        "provider_result_contract": None,
        "output_contract": {
            "artifact_type": "task_checkpoint:episode_split",
            "schema": checkpoint_schema,
        },
    }

    assert SDKTaskWorker._runtime_output_schema(pack).json_schema() is checkpoint_schema


def test_parse_and_validate_rejects_schema_mismatch() -> None:
    pack = {
        "output_contract": {
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
            }
        }
    }

    with pytest.raises(ValueError, match="required property"):
        SDKTaskWorker._parse_and_validate("{}", pack)


def test_parse_and_validate_accepts_json_code_fence() -> None:
    pack = {"output_contract": {"schema": {"type": "object"}}}

    assert SDKTaskWorker._parse_and_validate("```json\n{\"ok\":true}\n```", pack) == {
        "ok": True
    }


def test_output_rejection_is_detected_from_runtime_error() -> None:
    from content_agent_sidecar.backend import BackendError

    assert SDKTaskWorker._is_output_rejection(
        BackendError("backend returned HTTP 422: OUTPUT_SCHEMA_VALIDATION_FAILED")
    )
    assert SDKTaskWorker._is_output_rejection(
        BackendError("backend returned HTTP 409: BATCH_COVERAGE_INVALID")
    )
    assert SDKTaskWorker._is_output_rejection(
        BackendError("backend returned HTTP 422: OUTPUT_LENGTH_INSUFFICIENT")
    )


def test_safe_error_detail_redacts_api_keys() -> None:
    detail = SDKTaskWorker._safe_error_detail(
        RuntimeError("provider used " + "sk-" + "secretvalue1234567890")
    )

    assert "sk-secret" not in detail
    assert "[REDACTED]" in detail


def test_provider_envelope_wraps_bare_single_artifact() -> None:
    pack = {"output_contract": {"artifact_type": "story_bible", "schema": {}}}

    assert SDKTaskWorker._provider_envelope({"title": "x"}, pack) == {
        "artifact": {"title": "x"}
    }


def test_provider_envelope_preserves_existing_wrapper() -> None:
    pack = {"output_contract": {"artifact_type": "story_bible", "schema": {}}}
    payload = {"story_bible": {"title": "x"}}

    assert SDKTaskWorker._provider_envelope(payload, pack) is payload


def test_provider_envelope_keeps_internal_task_checkpoint_bare() -> None:
    pack = {
        "intent": {"operation": "generate_task_checkpoint"},
        "provider_result_contract": {
            "artifact_type": "task_checkpoint:global_plan",
            "schema": {},
        },
    }
    payload = {"episode_skeletons": [], "global_risks": []}

    assert SDKTaskWorker._provider_envelope(payload, pack) is payload


def test_provider_envelope_detects_checkpoint_from_artifact_type() -> None:
    pack = {
        "provider_result_contract": {
            "artifact_type": "task_checkpoint:episode_split",
            "schema": {},
        }
    }
    payload = {"episodes": []}

    assert SDKTaskWorker._provider_envelope(payload, pack) is payload


def test_provider_envelope_preserves_provider_result_contract_wrapper() -> None:
    pack = {
        "intent": {"operation": "generate_artifact"},
        "provider_result_contract": {
            "artifact_type": "provider_result",
            "schema": {
                "type": "object",
                "required": ["episode_cards"],
            },
        },
    }
    payload = {"episode_cards": {"episodes": [], "continuity_delta": {}}}

    assert SDKTaskWorker._provider_envelope(payload, pack) is payload


def test_multiple_output_contracts_do_not_use_single_artifact_envelope() -> None:
    pack = {
        "output_contracts": [
            {"artifact_type": "script_unit", "schema": {"type": "object"}},
            {"artifact_type": "script_handoff", "schema": {"type": "object"}},
        ]
    }

    assert SDKTaskWorker._requires_artifact_envelope(pack) is False
    payload = {"script_unit": {}, "script_handoff": {}}
    assert SDKTaskWorker._provider_envelope(payload, pack) is payload


def test_context_catalog_hides_content_and_preserves_pinned_identity() -> None:
    pack = {
        "upstream_context": [
            {
                "artifact_id": "art_1",
                "artifact_version_id": "av_1",
                "artifact_type": "script_handoff",
                "scope_key": "episode:1",
                "selection_policy": "sdk_context_candidate",
                "content": {"continuity_delta": {"new_facts": ["事实"]}},
            }
        ]
    }

    assert SDKTaskWorker._context_catalog(pack) == [
        {
            "artifact_id": "art_1",
            "artifact_version_id": "av_1",
            "artifact_type": "script_handoff",
            "scope_key": "episode:1",
            "selection_policy": "sdk_context_candidate",
        }
    ]


def test_render_input_preserves_previous_batch_results_for_sdk() -> None:
    from test_app import settings

    worker = SDKTaskWorker(settings())
    previous = {
        "task_result_checkpoint_id": "trc_1",
        "item_key": "episode:1-5",
        "artifact_type": "episode_cards",
        "payload": {
            "episode_cards": {
                "episodes": [{"episode_no": 5, "ending_hook": {"hook_text": "门外传来敲门声"}}],
                "continuity_delta": {"hooks_opened": ["门外来客身份"]},
            }
        },
        "payload_hash": "hash_1",
    }
    pack = {
        "project_id": "prj_1",
        "intent": {"operation": "generate_artifact"},
        "target": {"scope_key": "episode:6-10"},
        "prompt": {"content": "生成分集规划"},
        "rules": [],
        "output_contracts": [{"artifact_type": "episode_cards", "schema": {"type": "object"}}],
        "task_cursor": {
            "batch": {
                "episode_start": 6,
                "episode_end": 10,
                "previous_batch_results": [previous],
            }
        },
    }

    _, items = asyncio.run(worker._render_input(pack))
    payload = json.loads(items[0]["content"][0]["text"])

    assert payload["task_cursor"]["batch"]["previous_batch_results"] == [previous]


def test_render_input_includes_structured_state_and_adjacent_output() -> None:
    from test_app import settings

    worker = SDKTaskWorker(settings())
    state = {
        "artifact_version_id": "av_state_3",
        "version": 3,
        "content": {"state_version": 3, "state": {"facts": ["前情事实"]}},
    }
    adjacent = {
        "artifact_version_id": "av_episode_2",
        "artifact_type": "video_script_unit",
        "scope_key": "episode:2",
        "selection_policy": "state_adjacent",
        "content": {"episode_no": 2, "plot_summary": "上一集完整输出"},
    }
    pack = {
        "project_id": "prj_1",
        "intent": {"operation": "generate_artifact"},
        "target": {"scope_key": "episode:3"},
        "prompt": {"content": "生成下一集"},
        "rules": [],
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "structured_run_state": state,
        "upstream_context": [adjacent],
        "task_cursor": {"video": {"episode_order": 3}},
    }

    _, items = asyncio.run(worker._render_input(pack))
    payload = json.loads(items[0]["content"][0]["text"])

    assert payload["structured_run_state"] == state
    assert payload["state_adjacent_context"] == [adjacent]


def test_render_input_includes_pinned_skill_and_run_input() -> None:
    from test_app import settings

    worker = SDKTaskWorker(settings())
    pack = {
        "project_id": "prj_1",
        "intent": {"operation": "generate_artifact"},
        "target": {"scope_key": "step:create_review"},
        "run_input_snapshot": {"source_asset_ids": ["ast_story"]},
        "skill_instructions": {
            "id": "skill:story-review-workflow",
            "content": "Follow the installed review policy.",
        },
        "prompt": {"content": "Create the review."},
        "rules": [],
        "output_contracts": [
            {"artifact_type": "generic_document", "schema": {"type": "object"}}
        ],
        "upstream_context": [],
    }

    instructions, items = asyncio.run(worker._render_input(pack))
    payload = json.loads(items[0]["content"][0]["text"])

    assert instructions.startswith("Follow the installed review policy.")
    assert "# Workflow Prompt\nCreate the review." in instructions
    assert payload["run_input"] == {"source_asset_ids": ["ast_story"]}


def test_context_tools_expose_only_go_pinned_versions() -> None:
    pack = {
        "upstream_context": [
            {
                "artifact_version_id": "av_1",
                "artifact_type": "script_unit",
                "content": {"episode_no": 1},
            }
        ]
    }

    tools = SDKTaskWorker._context_tools(pack)

    assert len(tools) == 1
    assert tools[0].name == "read_context_artifacts"


def test_context_tool_records_versions_actually_read_by_sdk() -> None:
    reads: set[str] = set()
    tools = SDKTaskWorker._context_tools(
        {
            "upstream_context": [
                {"artifact_version_id": "av_1", "content": {"episode_no": 1}},
                {"artifact_version_id": "av_2", "content": {"episode_no": 2}},
            ]
        },
        reads,
    )
    context = SimpleNamespace(
        tool_name=tools[0].name,
        run_config=None,
        context=None,
        usage=None,
        tool_call_id="call_1",
        tool_arguments=None,
    )

    result = asyncio.run(
        tools[0].on_invoke_tool(
            context,
            '{"artifact_version_ids":["av_2"]}',
        )
    )

    assert '"av_2"' in result
    assert reads == {"av_2"}


def test_unwrap_provider_result_envelope_requires_matching_artifact_type() -> None:
    pack = {
        "provider_result_contract": {
            "artifact_type": "adaptation_options",
            "schema": {"type": "object"},
        }
    }
    payload = {
        "artifact_type": "adaptation_options",
        "schema_ref": "adaptation_options",
        "schema_version": "1.0.0",
        "content": {"adaptation_options": []},
    }

    assert SDKTaskWorker._unwrap_provider_result_envelope(payload, pack) == {
        "adaptation_options": []
    }
    mismatched = dict(payload, artifact_type="other")
    assert SDKTaskWorker._unwrap_provider_result_envelope(mismatched, pack) is mismatched


def test_normalize_video_script_payload_rebuilds_text_and_canonicalizes_alias() -> None:
    pack = {
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "task_cursor": {
            "video": {
                "episode_no": 2,
                "episode_order": 2,
                "asset_id": "ast_2",
                "asset_snapshot_id": "ass_2",
                "character_registry": [
                    {
                        "canonical_name": "李虹燕",
                        "aliases": ["李虹燕", "虹燕"],
                        "first_seen_episode": 1,
                        "last_seen_episode": 1,
                        "mention_count": 2,
                    }
                ],
            }
        },
    }
    payload = {
        "video_script_unit": {
            "episode_no": 2,
            "episode_order": 2,
            "script_text": "第2集",
            "scenes": [
                {
                    "heading": "场2-1 村口 日 外",
                    "characters": ["虹燕"],
                    "blocks": [
                        {
                            "block_type": "dialogue",
                            "speaker": "虹燕",
                            "text": "开始采收。",
                        }
                    ],
                }
            ],
        }
    }
    got = SDKTaskWorker._normalize_video_script_payload(payload, pack)
    unit = got["video_script_unit"]
    assert unit["scenes"][0]["characters"] == ["李虹燕"]
    assert unit["scenes"][0]["blocks"][0]["speaker"] == "李虹燕"
    assert "李虹燕：开始采收。" in unit["script_text"]


def test_normalize_video_script_payload_uses_authoritative_task_episode_identity() -> None:
    pack = {
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "task_cursor": {
            "video": {
                "episode_no": 1,
                "episode_order": 1,
                "file_name": "胡辣汤1.mp4",
                "asset_id": "ast_hula_1",
                "asset_snapshot_id": "ass_hula_1",
            }
        },
    }
    payload = {
        "video_script_unit": {
            "episode_no": 7,
            "episode_order": 7,
            "source_file_name": "模型猜测.mp4",
            "source_refs": [{
                "source_type": "video_time_range",
                "time_range": {"start_ms": 0, "end_ms": 5000},
            }],
            "scenes": [{
                "heading": "场1-1 汤店 日 内",
                "characters": ["食客甲", "食客乙"],
                "blocks": [
                    {"block_type": "dialogue", "speaker": "食客甲", "text": "来一碗。"},
                    {"block_type": "dialogue", "speaker": "食客乙", "text": "我也要。"},
                ],
            }],
        }
    }

    got = SDKTaskWorker._normalize_video_script_payload(payload, pack)["video_script_unit"]

    assert got["episode_no"] == 1
    assert got["episode_order"] == 1
    assert got["source_file_name"] == "胡辣汤1.mp4"
    assert got["script_text"].startswith("第1集")
    assert got["source_refs"][0]["asset_id"] == "ast_hula_1"
    assert got["source_refs"][0]["asset_snapshot_id"] == "ass_hula_1"


def test_apply_video_speaker_assignments_splits_unknown_multiline_dialogue() -> None:
    payload = {
        "video_script_unit": {
            "episode_no": 1,
            "scenes": [
                {
                    "scene_id": "scene_1_1",
                    "heading": "场1-1 摊位 日 外",
                    "characters": ["沈师傅", "工人"],
                    "blocks": [
                        {
                            "block_type": "dialogue",
                            "line_id": "line_1_1_1",
                            "speaker": "未知人物",
                            "text": "猪脚饭还是16块吧\n从今天起18\n怎么涨价了",
                            "uncertainty": "speaker_unknown",
                        }
                    ],
                }
            ],
        }
    }
    candidates = [
        {"key": "scene_1_1:0:0", "allowed_speakers": ["沈师傅", "工人", "未知人物"]},
        {"key": "scene_1_1:0:1", "allowed_speakers": ["沈师傅", "工人", "未知人物"]},
        {"key": "scene_1_1:0:2", "allowed_speakers": ["沈师傅", "工人", "未知人物"]},
    ]
    assignments = [
        {"key": "scene_1_1:0:0", "speaker": "工人"},
        {"key": "scene_1_1:0:1", "speaker": "沈师傅"},
        {"key": "scene_1_1:0:2", "speaker": "工人"},
    ]

    got = SDKTaskWorker._apply_video_speaker_assignments(payload, candidates, assignments)
    blocks = got["video_script_unit"]["scenes"][0]["blocks"]

    assert [block["speaker"] for block in blocks] == ["工人", "沈师傅", "工人"]
    assert "\n".join(block["text"] for block in blocks) == (
        "猪脚饭还是16块吧\n从今天起18\n怎么涨价了"
    )
    assert all("uncertainty" not in block for block in blocks)


def test_apply_video_speaker_assignments_downgrades_out_of_scene_name_only() -> None:
    payload = {
        "video_script_unit": {
            "scenes": [
                {
                    "scene_id": "scene_1_1",
                    "blocks": [
                        {
                            "block_type": "dialogue",
                            "speaker": "未知人物",
                            "text": "第一句\n第二句",
                        }
                    ],
                }
            ]
        }
    }
    candidates = [
        {"key": "scene_1_1:0:0", "allowed_speakers": ["沈师傅", "未知人物"]},
        {"key": "scene_1_1:0:1", "allowed_speakers": ["沈师傅", "未知人物"]},
    ]

    got = SDKTaskWorker._apply_video_speaker_assignments(
        payload,
        candidates,
        [
            {"key": "scene_1_1:0:0", "speaker": "赵老板"},
            {"key": "scene_1_1:0:1", "speaker": "沈师傅"},
        ],
    )
    blocks = got["video_script_unit"]["scenes"][0]["blocks"]

    assert [block["speaker"] for block in blocks] == ["未知人物", "沈师傅"]


def test_split_video_dialogue_lines_keeps_same_speaker_lines_independent() -> None:
    unit = {
        "scenes": [
            {
                "blocks": [
                    {
                        "block_type": "dialogue",
                        "line_id": "line_1_1_1",
                        "speaker": "食客甲",
                        "text": "第一句\n第二句\n第三句",
                    }
                ]
            }
        ]
    }

    SDKTaskWorker._split_video_dialogue_lines(unit)
    blocks = unit["scenes"][0]["blocks"]

    assert [block["text"] for block in blocks] == ["第一句", "第二句", "第三句"]
    assert [block["speaker"] for block in blocks] == ["食客甲", "食客甲", "食客甲"]
    assert [block["line_id"] for block in blocks] == [
        "line_1_1_1_1",
        "line_1_1_1_2",
        "line_1_1_1_3",
    ]


def test_video_speaker_batches_respect_scene_and_line_limits() -> None:
    candidates = [
        {"scene_id": f"scene_{scene}", "key": f"scene_{scene}:0:{line}"}
        for scene in range(1, 8)
        for line in range(12)
    ]

    batches = SDKTaskWorker._video_speaker_batches(candidates)

    assert sum(len(batch) for batch in batches) == len(candidates)
    assert all(len(batch) <= 60 for batch in batches)
    assert all(len({item["scene_id"] for item in batch}) <= 5 for batch in batches)


def test_video_speaker_candidates_include_single_line_narration_options() -> None:
    candidates = SDKTaskWorker._video_speaker_candidates({
        "scenes": [{
            "scene_id": "scene_1",
            "heading": "早餐铺 日 内",
            "characters": ["宋师傅", "未知人物"],
            "blocks": [{
                "block_type": "dialogue",
                "speaker": "未知人物",
                "text": "那一锅汤还没卖完，吵声已经传到了街尾。",
            }],
        }],
    })

    assert len(candidates) == 1
    assert candidates[0]["allowed_speakers"] == [
        "宋师傅", "旁白", "画外音", "未知人物"
    ]


def test_render_video_delivery_uses_chinese_labels() -> None:
    assert SDKTaskWorker._render_video_delivery("voiceover") == "画外音"
    assert SDKTaskWorker._render_video_delivery("internal monologue") == "内心独白"
    assert SDKTaskWorker._render_video_delivery("低声") == "低声"
    assert SDKTaskWorker._render_video_delivery("unexpected_english_label") == ""


def test_video_tool_payload_is_normalized_before_final_schema_validation() -> None:
    schema = {
        "type": "object",
        "required": ["episode_no", "script_text", "scenes"],
        "properties": {
            "episode_no": {"type": "integer"},
            "script_text": {"type": "string", "minLength": 1},
            "scenes": {"type": "array", "minItems": 1},
        },
    }
    pack = {
        "output_contracts": [
            {"artifact_type": "video_script_unit", "schema": schema}
        ],
        "task_cursor": {"video": {"episode_order": 1}},
    }
    raw = json.dumps(
        {
            "video_script_unit": {
                "episode_no": 1,
                "scenes": [
                    {
                        "heading": "场1-1 面馆 日 内",
                        "characters": ["孙长贵"],
                        "blocks": [
                            {
                                "block_type": "dialogue",
                                "speaker": "孙长贵",
                                "text": "今天牛肉面十四元。",
                            }
                        ],
                    }
                ],
            }
        },
        ensure_ascii=False,
    )

    parsed = SDKTaskWorker._parse_json_object(raw)
    normalized = SDKTaskWorker._normalize_video_script_payload(parsed, pack)
    validated = SDKTaskWorker._parse_and_validate(
        json.dumps(normalized, ensure_ascii=False), pack
    )

    assert "孙长贵：今天牛肉面十四元。" in validated["video_script_unit"]["script_text"]


def test_normalize_video_script_payload_rejects_empty_shell() -> None:
    pack = {
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "task_cursor": {"video": {"episode_order": 7}},
    }
    payload = {
        "video_script_unit": {
            "episode_no": 7,
            "episode_order": 7,
            "script_text": "第7集",
            "scenes": [{"heading": "场7-1 村口 日 外", "characters": [], "blocks": []}],
        }
    }
    with pytest.raises(ValueError, match="no content blocks"):
        SDKTaskWorker._normalize_video_script_payload(payload, pack)


def test_normalize_video_script_payload_rejects_ungrounded_video_fallback() -> None:
    pack = {
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "task_cursor": {"video": {"episode_order": 1}},
    }
    payload = {
        "video_script_unit": {
            "episode_no": 1,
            "episode_order": 1,
            "source_file_name": "第1集.mp4",
            "source_refs": [{"source_type": "user_message", "message_id": "msg_1"}],
            "scenes": [{
                "heading": "场1-1 视频内容未取得 时间未确认 内外",
                "characters": [],
                "blocks": [{"block_type": "action", "text": "无法读取视频。"}],
            }],
        }
    }
    with pytest.raises(ValueError, match="no video timeline evidence"):
        SDKTaskWorker._normalize_video_script_payload(payload, pack)


def test_normalize_video_script_payload_rejects_fake_one_millisecond_evidence() -> None:
    pack = {
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "task_cursor": {"video": {"episode_order": 1}},
    }
    payload = {
        "video_script_unit": {
            "episode_no": 1,
            "episode_order": 1,
            "source_file_name": "第1集.mp4",
            "scenes": [{
                "heading": "场1-1 视频内容待确认 未知 内外",
                "characters": [],
                "blocks": [{
                    "block_type": "action",
                    "text": "完整视频分析接口未返回结果。",
                    "source_refs": [{
                        "source_type": "video_time_range",
                        "time_range": {"start_ms": 0, "end_ms": 1},
                    }],
                }],
            }],
        }
    }
    with pytest.raises(ValueError, match="no video timeline evidence"):
        SDKTaskWorker._normalize_video_script_payload(payload, pack)


def test_normalize_video_script_payload_allows_new_similar_character_name() -> None:
    pack = {
        "output_contracts": [{"artifact_type": "video_script_unit", "schema": {"type": "object"}}],
        "task_cursor": {
            "video": {
                "episode_no": 2,
                "episode_order": 2,
                "character_registry": [
                    {
                        "canonical_name": "李虹燕",
                        "aliases": ["李虹燕"],
                        "first_seen_episode": 1,
                        "last_seen_episode": 1,
                        "mention_count": 1,
                    }
                ],
            }
        },
    }
    payload = {
        "video_script_unit": {
            "episode_no": 2,
            "episode_order": 2,
            "script_text": "旧镜像",
            "scenes": [
                {
                    "heading": "场2-1 村口 日 外",
                    "characters": ["李红燕"],
                    "blocks": [
                        {"block_type": "dialogue", "speaker": "李红燕", "text": "开始采收。"}
                    ],
                }
            ],
        }
    }
    got = SDKTaskWorker._normalize_video_script_payload(payload, pack)
    assert got["video_script_unit"]["scenes"][0]["characters"] == ["李红燕"]


def test_enumerated_background_roles_are_not_treated_as_name_typos() -> None:
    assert not SDKTaskWorker._likely_video_character_typo("工人甲", "工人乙")
    assert not SDKTaskWorker._likely_video_character_typo("村民一", "村民二")
    assert SDKTaskWorker._likely_video_character_typo("李虹燕", "李红燕")


def test_parse_and_validate_validates_wrapped_artifact_payload() -> None:
    pack = {
        "output_contract": {
            "artifact_type": "story_bible",
            "schema": {
                "type": "object",
                "required": ["title"],
                "properties": {"title": {"type": "string"}},
            },
        }
    }

    assert SDKTaskWorker._parse_and_validate(
        '{"story_bible":{"title":"x"}}', pack
    ) == {"story_bible": {"title": "x"}}


def test_parse_json_object_unwraps_singleton_object_array() -> None:
    assert SDKTaskWorker._parse_json_object('[{"title":"x"}]') == {"title": "x"}


@pytest.mark.parametrize("payload", ["[]", '[{"title":"x"},{"title":"y"}]', '["x"]'])
def test_parse_json_object_rejects_non_singleton_object_arrays(payload: str) -> None:
    with pytest.raises(ValueError, match="not a JSON object"):
        SDKTaskWorker._parse_json_object(payload)


def test_parse_and_validate_strips_matching_provider_transport_metadata() -> None:
    pack = {
        "provider_result_contract": {
            "artifact_type": "provider_result",
            "schema": {
                "type": "object",
                "required": ["video_script_unit"],
                "properties": {"video_script_unit": {"type": "object"}},
                "additionalProperties": False,
            },
        }
    }

    assert SDKTaskWorker._parse_and_validate(
        json.dumps({
            "artifact_type": "provider_result",
            "schema_ref": "video-script-unit.schema.json",
            "schema_version": "1.0.0",
            "video_script_unit": {"episode_no": 1},
        }),
        pack,
    ) == {"video_script_unit": {"episode_no": 1}}


def test_parse_and_validate_projects_complete_provider_transport_to_contract() -> None:
    pack = {
        "provider_result_contract": {
            "artifact_type": "provider_result",
            "schema": {
                "type": "object",
                "required": ["video_script_unit"],
                "properties": {"video_script_unit": {"type": "object"}},
                "additionalProperties": False,
            },
        }
    }

    assert SDKTaskWorker._parse_and_validate(
        json.dumps({
            "video_script_unit": {"episode_no": 2},
            "characters": ["宋师傅", "小林"],
        }, ensure_ascii=False),
        pack,
    ) == {"video_script_unit": {"episode_no": 2}}


def test_parse_and_validate_does_not_hide_missing_provider_result() -> None:
    pack = {
        "provider_result_contract": {
            "artifact_type": "provider_result",
            "schema": {
                "type": "object",
                "required": ["video_script_unit"],
                "properties": {"video_script_unit": {"type": "object"}},
                "additionalProperties": False,
            },
        }
    }

    with pytest.raises(ValueError, match="required property"):
        SDKTaskWorker._parse_and_validate(
            json.dumps({"characters": ["宋师傅"]}, ensure_ascii=False),
            pack,
        )


def test_story_bible_coverage_is_completed_from_validated_source_analysis() -> None:
    pack = {
        "task_cursor": {
            "batch": {
                "phase": "story_bible_aggregate",
                "source_analysis": {
                    "units": [
                        {
                            "source_unit_id": "SRC-1",
                            "summary": "第一段",
                            "key_events": ["事件"],
                            "character_changes": [],
                            "conflict_stage": "建立",
                            "hook_or_suspense_potential": "high",
                            "source_refs": [{"source_unit_id": "SRC-1"}],
                        },
                        {
                            "source_unit_id": "SRC-2",
                            "summary": "第二段",
                            "key_events": [],
                            "character_changes": [],
                            "conflict_stage": "升级",
                            "hook_or_suspense_potential": "medium",
                            "source_refs": [{"source_unit_id": "SRC-2"}],
                        },
                    ]
                },
            }
        }
    }
    payload = {
        "source_structure": [
            {"source_unit_id": "SRC-2", "summary": "模型生成的第二段"}
        ]
    }

    normalized = SDKTaskWorker._normalize_story_bible_coverage(payload, pack)

    assert [
        item["source_unit_id"] for item in normalized["source_structure"]
    ] == ["SRC-1", "SRC-2"]
    assert normalized["source_structure"][0]["summary"] == "第一段"
    assert normalized["source_structure"][1]["summary"] == "模型生成的第二段"


def test_source_analysis_chunk_is_reordered_and_pinned_to_runtime_sources() -> None:
    expected = [
        {
            "source_unit_id": "SRC-1",
            "asset_id": "ast-1",
            "asset_snapshot_id": "ass-1",
        },
        {
            "source_unit_id": "SRC-2",
            "asset_id": "ast-1",
            "asset_snapshot_id": "ass-1",
        },
    ]
    payload = {
        "source_kind": "novel",
        "units": [
            {
                "source_unit_id": "SRC-2",
                "summary": "第二段",
                "source_refs": [{"evidence_excerpt": "证据二"}],
            },
            {
                "source_unit_id": "SRC-1",
                "summary": "第一段",
                "source_refs": [],
            },
        ],
        "coverage_check": {},
        "source_trace": {"grounded": [], "inferred": [], "claims": []},
    }

    got = SDKTaskWorker._normalize_source_analysis_chunk(payload, expected)

    assert [item["source_unit_id"] for item in got["units"]] == ["SRC-1", "SRC-2"]
    assert got["units"][1]["source_refs"] == [
        {
            "source_type": "asset_text_range",
            "asset_id": "ast-1",
            "asset_snapshot_id": "ass-1",
            "source_unit_id": "SRC-2",
            "range_label": "SRC-2",
            "evidence_excerpt": "证据二",
        }
    ]
    assert got["coverage_check"]["covered_source_unit_ids"] == ["SRC-1", "SRC-2"]
