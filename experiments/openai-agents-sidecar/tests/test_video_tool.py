from __future__ import annotations

import asyncio
import subprocess
from types import SimpleNamespace
from agents import Agent as SDKAgent

import content_agent_sidecar.video_tool as video_tool_module
from content_agent_sidecar.video_tool import (
    _MAX_VIDEO_ANALYSIS_OUTPUT_TOKENS,
    VideoAnalysisTool,
)


def test_video_analysis_output_budget_is_bounded_for_gateway_latency() -> None:
    assert _MAX_VIDEO_ANALYSIS_OUTPUT_TOKENS == 65_535


def test_video_analysis_prompt_exposes_authoritative_source_duration() -> None:
    prompt = VideoAnalysisTool._timeline_bounds_prompt(154_861)

    assert "SOURCE_VIDEO_DURATION_MS:\n154861" in prompt
    assert "Every source time range must stay between 0" in prompt


def test_subtitle_mode_removes_conflicting_runtime_output_contract() -> None:
    instructions = (
        "Return compact video_evidence only."
        "\n\nRUNTIME_OUTPUT_CONTRACT:\n"
        '{"schema":{"type":"object"}}'
    )

    assert VideoAnalysisTool._effective_video_instructions(
        instructions, has_subtitles=True
    ) == "Return compact video_evidence only."
    assert VideoAnalysisTool._effective_video_instructions(
        instructions, has_subtitles=False
    ) == instructions


def test_transcode_video_sync_targets_complete_compact_mp4(monkeypatch) -> None:
    calls: list[list[str]] = []

    def run(command, **_kwargs):
        calls.append(command)
        if "-show_entries" in command:
            return subprocess.CompletedProcess(command, 0, stdout="60\n", stderr="")
        with open(command[-1], "wb") as output:
            output.write(b"compact-full-video")
        return subprocess.CompletedProcess(command, 0, stdout=b"", stderr=b"")

    monkeypatch.setattr("content_agent_sidecar.video_tool.subprocess.run", run)

    result = VideoAnalysisTool._transcode_video_sync(b"large-video", "ffmpeg")

    assert result == b"compact-full-video"
    transcode = calls[1]
    assert "-vf" in transcode
    assert "scale=640:-2:force_original_aspect_ratio=decrease:force_divisible_by=2" in transcode
    assert "-frames:v" not in transcode
    assert "-c:a" in transcode


def test_video_input_unavailable_detects_controlled_gemini_fallback() -> None:
    assert VideoAnalysisTool._video_input_unavailable("VIDEO_INPUT_UNAVAILABLE")
    assert VideoAnalysisTool._video_input_unavailable("未收到视频，请重新上传")
    assert not VideoAnalysisTool._video_input_unavailable(
        '{"video_script_unit":{"episode_no":1}}'
    )


def test_video_model_request_runs_through_sdk_with_private_tracing(monkeypatch) -> None:
    tool = object.__new__(VideoAnalysisTool)
    tool._model = object()
    tool._settings = SimpleNamespace(
        video_model_name="video-model",
        video_model_max_output_tokens=80_000,
        video_model_stream=False,
        video_model_timeout_seconds=5,
        tracing_enabled=False,
        release_id="release-test",
    )
    tool.usage = {}
    tool.trace_ref = ""
    captured: dict[str, object] = {}

    def create_agent(**kwargs):
        captured["agent"] = kwargs
        return SDKAgent(**{**kwargs, "model": None})

    async def run(_agent, input_items, **kwargs):
        assert _agent.input_guardrails and _agent.output_guardrails
        captured["input"] = input_items
        captured["run"] = kwargs
        return SimpleNamespace(
            final_output='{"video_script_unit":{"episode_no":1}}',
            context_wrapper=SimpleNamespace(
                usage=SimpleNamespace(
                    requests=1,
                    input_tokens=120,
                    output_tokens=30,
                    total_tokens=150,
                )
            ),
            raw_responses=[
                SimpleNamespace(request_id="req_video_1", response_id="resp_video_1")
            ],
        )

    monkeypatch.setattr(video_tool_module, "Agent", create_agent)
    monkeypatch.setattr(video_tool_module.Runner, "run", run)

    output = asyncio.run(
        tool._request_video_model("analyze", b"video", "video/mp4")
    )

    assert output == '{"video_script_unit":{"episode_no":1}}'
    content = captured["input"][0]["content"]
    assert content[0] == {"type": "text", "text": "analyze"}
    assert content[1]["type"] == "video_url"
    assert content[1]["video_url"]["url"].startswith("data:video/mp4;base64,")
    settings = captured["agent"]["model_settings"]
    assert settings.max_tokens == _MAX_VIDEO_ANALYSIS_OUTPUT_TOKENS
    assert settings.reasoning.effort == "low"
    assert settings.extra_args == {"response_format": {"type": "json_object"}}
    assert captured["run"]["max_turns"] == 1
    run_config = captured["run"]["run_config"]
    assert run_config.tracing_disabled is True
    assert run_config.trace_include_sensitive_data is False
    assert tool.usage["total_tokens"] == 150
    assert tool.trace_ref == "req_video_1"


def test_streamed_video_model_request_consumes_sdk_events(monkeypatch) -> None:
    tool = object.__new__(VideoAnalysisTool)
    tool._model = object()
    tool._settings = SimpleNamespace(
        video_model_name="gemini-video-model",
        video_model_max_output_tokens=1024,
        video_model_stream=True,
        video_model_timeout_seconds=5,
        tracing_enabled=True,
        release_id="release-test",
    )
    tool.usage = {}
    tool.trace_ref = ""
    streamed = []

    class StreamedRun:
        final_output = '{"video_evidence":[]}'
        context_wrapper = SimpleNamespace(usage=None)
        raw_responses = [SimpleNamespace(request_id="", response_id="resp_video_2")]

        async def stream_events(self):
            streamed.append(True)
            if False:
                yield None

        def cancel(self):
            raise AssertionError("successful SDK stream must not be cancelled")

    monkeypatch.setattr(video_tool_module, "Agent", lambda **kwargs: SDKAgent(**{**kwargs, "model": None}))
    monkeypatch.setattr(
        video_tool_module.Runner,
        "run_streamed",
        lambda *_args, **_kwargs: StreamedRun(),
    )

    output = asyncio.run(
        tool._request_video_model("analyze", b"video", "video/mp4")
    )

    assert output == '{"video_evidence":[]}'
    assert streamed == [True]
    assert tool.trace_ref == "resp_video_2"


def test_discard_cached_forces_a_fresh_video_result() -> None:
    tool = object.__new__(VideoAnalysisTool)
    tool._cache = {"context-1": "invalid", "context-2": "valid"}

    tool.discard_cached("context-1")

    assert tool._cache == {"context-2": "valid"}


def test_transcode_video_sync_uses_long_video_profile(monkeypatch) -> None:
    calls: list[list[str]] = []

    def run(command, **_kwargs):
        calls.append(command)
        if "-show_entries" in command:
            return subprocess.CompletedProcess(command, 0, stdout="960\n", stderr="")
        with open(command[-1], "wb") as output:
            output.write(b"compact-long-video")
        return subprocess.CompletedProcess(command, 0, stdout=b"", stderr=b"")

    monkeypatch.setattr("content_agent_sidecar.video_tool.subprocess.run", run)

    result = VideoAnalysisTool._transcode_video_sync(b"large-video", "ffmpeg")

    assert result == b"compact-long-video"
    transcode = calls[1]
    assert "fps=2,scale=480:-2:force_original_aspect_ratio=decrease:force_divisible_by=2" in transcode
    assert transcode[transcode.index("-b:a") + 1] == "24000"


def test_model_video_filter_preserves_working_profile_and_caps_long_frame_count() -> None:
    assert VideoAnalysisTool._model_video_filter(789) == (
        "fps=6,scale=480:-2:force_original_aspect_ratio=decrease:force_divisible_by=2"
    )
    assert VideoAnalysisTool._model_video_filter(960) == (
        "fps=2,scale=480:-2:force_original_aspect_ratio=decrease:force_divisible_by=2"
    )
    assert VideoAnalysisTool._model_video_filter(1800) == (
        "fps=2,scale=480:-2:force_original_aspect_ratio=decrease:force_divisible_by=2"
    )


def test_transcode_video_sync_reports_ffmpeg_stderr(monkeypatch) -> None:
    def run(command, **_kwargs):
        if "-show_entries" in command:
            return subprocess.CompletedProcess(command, 0, stdout="60\n", stderr="")
        raise subprocess.CalledProcessError(
            1,
            command,
            stderr=b"width not divisible by 2",
        )

    monkeypatch.setattr("content_agent_sidecar.video_tool.subprocess.run", run)

    try:
        VideoAnalysisTool._transcode_video_sync(b"large-video", "ffmpeg")
    except Exception as exc:
        assert "width not divisible by 2" in str(exc)
    else:
        raise AssertionError("expected the FFmpeg failure to be surfaced")
