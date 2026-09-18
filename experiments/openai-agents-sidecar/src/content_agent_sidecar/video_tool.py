from __future__ import annotations

import asyncio
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

from agents import Agent, AsyncOpenAI, ModelSettings, OpenAIChatCompletionsModel, Runner
from openai.types.shared.reasoning import Reasoning

from .backend import BackendClient
from .config import Settings
from .guardrails import SDKGuardrailPolicy
from .observability import privacy_safe_run_config


class VideoToolError(RuntimeError):
    pass


_MAX_INLINE_VIDEO_BYTES = 8 * 1024 * 1024
_TARGET_TRANSCODE_BYTES = 7 * 1024 * 1024
_MAX_VIDEO_ANALYSIS_OUTPUT_TOKENS = 65_535
_ADAPTIVE_FRAME_BUDGET = 3_600
_ADAPTIVE_FRAME_RATE_THRESHOLD_SECONDS = 14 * 60


class VideoAnalysisTool:
    def __init__(self, settings: Settings, backend: BackendClient) -> None:
        missing = [
            name
            for name, value in (
                ("CONTENT_AGENT_VIDEO_MODEL_ENDPOINT", settings.video_model_base_url),
                ("CONTENT_AGENT_VIDEO_MODEL_API_KEY", settings.video_model_api_key),
                ("CONTENT_AGENT_VIDEO_MODEL_MODEL", settings.video_model_name),
                ("CONTENT_AGENT_MEDIAKIT_ENDPOINT", settings.mediakit_endpoint),
                ("CONTENT_AGENT_MEDIAKIT_API_KEY", settings.mediakit_api_key),
            )
            if not value
        ]
        if missing:
            raise VideoToolError("missing video tool configuration: " + ", ".join(missing))
        self._settings = settings
        self._backend = backend
        client = AsyncOpenAI(
            api_key=settings.video_model_api_key,
            base_url=settings.video_model_base_url,
            timeout=settings.video_model_timeout_seconds,
            max_retries=settings.video_model_max_retries,
        )
        self._model = OpenAIChatCompletionsModel(
            model=settings.video_model_name,
            openai_client=client,
        )
        self.usage: dict[str, Any] = {}
        self.trace_ref = ""
        self.last_result = ""
        self.source_duration_ms = 0
        self.subtitle_timeline: list[dict[str, Any]] = []
        self._cache: dict[str, str] = {}
        self._subtitle_cache: dict[str, list[dict[str, Any]]] = {}
        self._prepared_video_cache: dict[str, tuple[bytes, str, int]] = {}

    async def analyze(self, pack: dict[str, Any], instructions: str) -> str:
        self.last_result = ""
        self.usage = {}
        self.trace_ref = ""
        self.source_duration_ms = 0
        self.subtitle_timeline = []
        context_hash = str(pack.get("context_hash") or "")
        if context_hash and context_hash in self._cache:
            self.subtitle_timeline = list(self._subtitle_cache.get(context_hash, []))
            self.last_result = self._cache[context_hash]
            return self.last_result
        assets = [
            item
            for item in pack.get("asset_context") or []
            if isinstance(item, dict) and item.get("kind") == "video"
        ]
        if len(assets) != 1:
            raise VideoToolError("video task must contain exactly one video asset")
        asset = assets[0]
        data, mime_type = await self._backend.get_video_content(
            str(asset.get("asset_id") or ""),
            str(asset.get("asset_snapshot_id") or ""),
        )
        prepared = self._prepared_video_cache.get(context_hash) if context_hash else None
        if prepared is None:
            model_data, model_mime_type = await self._prepare_model_video(data, mime_type)
            if context_hash:
                self._prepared_video_cache[context_hash] = (
                    model_data,
                    model_mime_type,
                    self.source_duration_ms,
                )
        else:
            model_data, model_mime_type, self.source_duration_ms = prepared
        gemini_primary = "gemini" in self._settings.video_model_name.lower()
        subtitle_error = ""
        subtitles = list(self._subtitle_cache.get(context_hash, [])) if context_hash else []
        if not subtitles:
            try:
                subtitles = await self._extract_subtitles(data, mime_type, context_hash)
                if context_hash:
                    self._subtitle_cache[context_hash] = list(subtitles)
            except VideoToolError as exc:
                subtitle_error = str(exc)
        self.subtitle_timeline = list(subtitles)
        cursor = pack.get("task_cursor") or {}
        video_cursor = cursor.get("video") if isinstance(cursor, dict) else {}
        continuity = {
            "known_characters": (video_cursor or {}).get("known_characters") or [],
            "character_registry": (video_cursor or {}).get("character_registry") or [],
            "previous_episode_end": (video_cursor or {}).get("previous_episode_end") or "",
        }
        subtitle_section = (
            "\n\nSUBTITLE_TIMELINE_EVIDENCE:\n"
            + json.dumps(subtitles, ensure_ascii=False)
            if subtitles
            else ""
        )
        effective_instructions = self._effective_video_instructions(
            instructions, has_subtitles=bool(subtitles)
        )
        prompt = (
            effective_instructions
            + "\n\n# Complete Video Tool\n"
            "The complete source video is attached. Analyze the full timeline, not sampled frames. "
            "Use the subtitle timeline as verbatim dialogue evidence and the video for speakers, "
            "actions, scenes, overlays, props, and chronology. Do not invent unseen content.\n\n"
            + self._timeline_bounds_prompt(self.source_duration_ms)
            + subtitle_section
            + "\n\nSAME_PROJECT_CHARACTER_CONTINUITY:\n"
            + json.dumps(continuity, ensure_ascii=False)
            + "\n角色注册表仅来自同一作品、同一 Run 的前序集。"
            "当前视频确认是同一人物时，必须输出 canonical_name；"
            "新人物可以新增，无法确认身份时使用未知人物并标记 uncertainty，禁止猜测近似姓名。"
            + ("\nSUBTITLE_OCR_STATUS:\nunavailable: " + subtitle_error if subtitle_error else "")
        )
        if gemini_primary:
            if subtitles:
                prompt += (
                    "\n\nVIDEO_INPUT_MODE:\nfull_video_with_timed_subtitle_evidence\n"
                    "Use the complete attached video for speakers, actions, scene boundaries, and "
                    "chronology. The timed subtitle evidence is the authoritative dialogue list. "
                    "Do not repeat subtitle text in the model output; annotate each subtitle_id. "
                    "The video remains one complete input and must not be treated as segments."
                )
            else:
                prompt += (
                    "\n\nVIDEO_INPUT_MODE:\nfull_video_primary\n"
                    "No subtitle timeline is provided on this primary path. Read the attached "
                    "complete video directly, including burned-in subtitles and audio. If the video "
                    "is not visible, return exactly VIDEO_INPUT_UNAVAILABLE and do not invent content."
                )
        result = await self._request_video_model(prompt, model_data, model_mime_type)
        if gemini_primary and self._video_input_unavailable(result):
            subtitles = await self._extract_subtitles(data, mime_type, context_hash)
            self.subtitle_timeline = list(subtitles)
            if context_hash:
                self._subtitle_cache[context_hash] = list(subtitles)
            fallback_prompt = (
                prompt
                + "\n\nSUBTITLE_TIMELINE_EVIDENCE:\n"
                + json.dumps(subtitles, ensure_ascii=False)
                + "\n\nThe primary request could not access the video. This is the single "
                "subtitle-grounded fallback attempt. Preserve every confirmed subtitle line "
                "and use the attached complete video for speakers, actions, and scenes."
            )
            result = await self._request_video_model(
                fallback_prompt, model_data, model_mime_type
            )
        self.last_result = result
        if context_hash:
            self._cache[context_hash] = result
        return result

    def discard_cached(self, context_hash: str) -> None:
        if context_hash:
            self._cache.pop(context_hash, None)

    @staticmethod
    def _effective_video_instructions(
        instructions: str, *, has_subtitles: bool
    ) -> str:
        if not has_subtitles:
            return instructions
        return instructions.partition("\n\nRUNTIME_OUTPUT_CONTRACT:\n")[0].rstrip()

    @staticmethod
    def _timeline_bounds_prompt(source_duration_ms: int) -> str:
        return (
            f"SOURCE_VIDEO_DURATION_MS:\n{source_duration_ms}\n"
            "Every source time range must stay between 0 and SOURCE_VIDEO_DURATION_MS. "
            "Never extend, extrapolate, or invent timeline evidence beyond the source duration.\n\n"
        )

    async def _request_video_model(
        self, prompt: str, model_data: bytes, model_mime_type: str
    ) -> str:
        encoded = base64.b64encode(model_data).decode("ascii")
        media_type = (
            "image_url"
            if "gemini" in self._settings.video_model_name.lower()
            else "video_url"
        )
        model_settings = ModelSettings(
            max_tokens=min(
                self._settings.video_model_max_output_tokens,
                _MAX_VIDEO_ANALYSIS_OUTPUT_TOKENS,
            ),
            reasoning=Reasoning(effort="low"),
            preserve_raw_usage=True,
            extra_args={"response_format": {"type": "json_object"}},
        )
        agent = Agent(
            name="完整视频分析 Agent",
            instructions=(
                "只分析用户附带的完整视频，并严格按用户给出的契约返回一个 JSON 对象。"
                "不要补写未在视频或字幕证据中出现的内容。"
            ),
            model=self._model,
            model_settings=model_settings,
        )
        input_items = [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {
                        "type": media_type,
                        media_type: {
                            "url": f"data:{model_mime_type};base64,{encoded}"
                        },
                    },
                ],
            }
        ]
        run_config = privacy_safe_run_config(
            workflow_name="content-agent-complete-video-analysis",
            tracing_enabled=self._settings.tracing_enabled,
            release_id=self._settings.release_id,
        )
        agent = SDKGuardrailPolicy.from_settings(self._settings).protect(agent)
        if self._settings.video_model_stream:
            result = Runner.run_streamed(
                agent,
                input_items,
                max_turns=1,
                run_config=run_config,
            )
            try:
                async with asyncio.timeout(self._settings.video_model_timeout_seconds):
                    async for _ in result.stream_events():
                        pass
            except TimeoutError as exc:
                result.cancel()
                raise VideoToolError("Agents SDK video analysis timed out") from exc
        else:
            try:
                result = await asyncio.wait_for(
                    Runner.run(
                        agent,
                        input_items,
                        max_turns=1,
                        run_config=run_config,
                    ),
                    timeout=self._settings.video_model_timeout_seconds,
                )
            except TimeoutError as exc:
                raise VideoToolError("Agents SDK video analysis timed out") from exc
        content = result.final_output
        if not isinstance(content, str) or not content.strip():
            raise VideoToolError("video model returned empty content")
        usage = getattr(getattr(result, "context_wrapper", None), "usage", None)
        if usage is not None:
            self.usage = {
                "requests": int(getattr(usage, "requests", 0) or 0),
                "input_tokens": int(getattr(usage, "input_tokens", 0) or 0),
                "output_tokens": int(getattr(usage, "output_tokens", 0) or 0),
                "total_tokens": int(getattr(usage, "total_tokens", 0) or 0),
            }
        responses = getattr(result, "raw_responses", None) or []
        if responses:
            response = responses[-1]
            self.trace_ref = str(
                getattr(response, "request_id", "")
                or getattr(response, "response_id", "")
                or ""
            )
        return content.strip()

    @staticmethod
    def _video_input_unavailable(content: str) -> bool:
        normalized = content.strip().lower()
        return any(
            marker in normalized
            for marker in (
                "video_input_unavailable",
                "未接收到任何视频",
                "未收到视频",
                "没有提供完整视频",
                "没有视频上下文",
                "unable to access the video",
                "video was not provided",
                "no video was provided",
            )
        )

    async def _prepare_model_video(self, data: bytes, mime_type: str) -> tuple[bytes, str]:
        duration = await asyncio.to_thread(
            self._probe_video_duration_sync,
            data,
            self._settings.ffmpeg_command,
        )
        self.source_duration_ms = max(1, round(duration * 1000))
        if len(data) <= _MAX_INLINE_VIDEO_BYTES:
            return data, mime_type
        transcoded = await asyncio.to_thread(
            self._transcode_video_sync,
            data,
            self._settings.ffmpeg_command,
        )
        if len(transcoded) > _MAX_INLINE_VIDEO_BYTES:
            raise VideoToolError(
                "complete video remains above the inline model limit after transcoding"
            )
        return transcoded, "video/mp4"

    @staticmethod
    def _probe_video_duration_sync(data: bytes, ffmpeg_command: str) -> float:
        ffmpeg = Path(ffmpeg_command)
        ffprobe = ffmpeg.with_name(
            "ffprobe.exe" if ffmpeg.suffix.lower() == ".exe" else "ffprobe"
        )
        ffprobe_command = str(ffprobe) if ffprobe.exists() else "ffprobe"
        try:
            with tempfile.TemporaryDirectory(prefix="content-agent-video-probe-") as root:
                source = Path(root) / "source.mp4"
                source.write_bytes(data)
                probe = subprocess.run(
                    [
                        ffprobe_command,
                        "-v",
                        "error",
                        "-show_entries",
                        "format=duration",
                        "-of",
                        "default=noprint_wrappers=1:nokey=1",
                        str(source),
                    ],
                    capture_output=True,
                    check=True,
                    text=True,
                    timeout=30,
                )
                return max(float(probe.stdout.strip()), 1.0)
        except (OSError, subprocess.SubprocessError, ValueError) as exc:
            raise VideoToolError(f"video duration probe failed: {exc}") from exc

    @staticmethod
    def _transcode_video_sync(data: bytes, ffmpeg_command: str) -> bytes:
        ffmpeg = Path(ffmpeg_command)
        ffprobe = ffmpeg.with_name("ffprobe.exe" if ffmpeg.suffix.lower() == ".exe" else "ffprobe")
        ffprobe_command = str(ffprobe) if ffprobe.exists() else "ffprobe"
        try:
            with tempfile.TemporaryDirectory(prefix="content-agent-model-video-") as root:
                source = Path(root) / "source.mp4"
                output = Path(root) / "model-input.mp4"
                source.write_bytes(data)
                probe = subprocess.run(
                    [
                        ffprobe_command,
                        "-v",
                        "error",
                        "-show_entries",
                        "format=duration",
                        "-of",
                        "default=noprint_wrappers=1:nokey=1",
                        str(source),
                    ],
                    capture_output=True,
                    check=True,
                    text=True,
                    timeout=30,
                )
                duration = max(float(probe.stdout.strip()), 1.0)
                long_video = duration > 300
                audio_bitrate = 24_000 if long_video else 48_000
                target_video_bitrate = int(
                    (_TARGET_TRANSCODE_BYTES * 8 / duration) - audio_bitrate
                )
                minimum_video_bitrate = 16_000 if long_video else 160_000
                target_video_bitrate = max(
                    minimum_video_bitrate,
                    min(700_000, target_video_bitrate),
                )
                video_filter = VideoAnalysisTool._model_video_filter(duration)
                for _attempt in range(3):
                    subprocess.run([
                        ffmpeg_command,
                        "-hide_banner",
                        "-loglevel",
                        "error",
                        "-i",
                        str(source),
                        "-map",
                        "0:v:0",
                        "-map",
                        "0:a?",
                        "-vf",
                        video_filter,
                        "-c:v",
                        "libx264",
                        "-preset",
                        "veryfast",
                        "-b:v",
                        str(target_video_bitrate),
                        "-maxrate",
                        str(target_video_bitrate),
                        "-bufsize",
                        str(target_video_bitrate * 2),
                        "-c:a",
                        "aac",
                        "-b:a",
                        str(audio_bitrate),
                        "-movflags",
                        "+faststart",
                        "-y",
                        str(output),
                    ], capture_output=True, check=True, timeout=600)
                    result = output.read_bytes()
                    if len(result) <= _MAX_INLINE_VIDEO_BYTES:
                        break
                    ratio = _TARGET_TRANSCODE_BYTES / max(len(result), 1)
                    target_video_bitrate = max(
                        8_000,
                        int(target_video_bitrate * ratio * 0.9),
                    )
        except subprocess.CalledProcessError as exc:
            stderr = exc.stderr.decode(errors="replace") if isinstance(exc.stderr, bytes) else str(exc.stderr or "")
            detail = stderr.strip()[-1000:] or str(exc)
            raise VideoToolError(f"complete video transcoding failed: {detail}") from exc
        except (OSError, subprocess.SubprocessError, ValueError) as exc:
            raise VideoToolError(f"complete video transcoding failed: {exc}") from exc
        if not result:
            raise VideoToolError("complete video transcoding produced empty output")
        return result

    @staticmethod
    def _model_video_filter(duration: float) -> str:
        if duration <= 300:
            return (
                "scale=640:-2:force_original_aspect_ratio=decrease:"
                "force_divisible_by=2"
            )
        frame_rate = 6
        if duration > _ADAPTIVE_FRAME_RATE_THRESHOLD_SECONDS:
            frame_rate = 2
        return (
            f"fps={frame_rate},"
            "scale=480:-2:force_original_aspect_ratio=decrease:"
            "force_divisible_by=2"
        )

    async def _extract_subtitles(
        self, data: bytes, mime_type: str, context_hash: str
    ) -> list[dict[str, Any]]:
        return await asyncio.to_thread(
            self._extract_subtitles_sync, data, mime_type, context_hash
        )

    def _extract_subtitles_sync(
        self, data: bytes, mime_type: str, context_hash: str
    ) -> list[dict[str, Any]]:
        prepared = self._mediakit_json("POST", "/api/v1/tools-sync/request-media-upload-url", {})
        upload = prepared.get("result") or {}
        upload_url = str(upload.get("upload_url") or "")
        file_id = str(upload.get("file_id") or "")
        if not prepared.get("success") or not upload_url or not file_id:
            raise VideoToolError("MediaKit upload preparation failed")
        headers = {
            str(item.get("key")): str(item.get("value"))
            for item in upload.get("upload_headers") or []
            if isinstance(item, dict) and item.get("key")
        }
        headers.setdefault("Content-Type", mime_type)
        self._request("PUT", upload_url, data, headers, authorized=False)
        if "://" not in file_id:
            file_id = "mediakit://" + file_id
        token_hash = hashlib.sha256(context_hash.encode("utf-8") + b"\0" + data).hexdigest()[:40]
        submitted = self._mediakit_json(
            "POST",
            "/api/v1/tools/video-ocr",
            {
                "video_url": file_id,
                "mode": "Subtitle",
                "client_token": "content-agent-" + token_hash,
            },
        )
        task_id = str(submitted.get("task_id") or "")
        if not submitted.get("success") or not task_id:
            raise VideoToolError("MediaKit subtitle task submission failed")
        for _ in range(max(1, int(self._settings.video_model_timeout_seconds / self._settings.mediakit_poll_seconds))):
            result = self._mediakit_json("GET", "/api/v1/tasks/" + quote(task_id, safe=""), None)
            status = str(result.get("status") or "")
            if status == "completed":
                subtitles = (result.get("result") or {}).get("subtitles") or []
                normalized = [
                    {
                        "subtitle_id": f"subtitle_{index + 1:04d}",
                        "start_ms": int(float(item.get("start_time") or 0) * 1000),
                        "end_ms": int(float(item.get("end_time") or 0) * 1000),
                        "kind": "dialogue_subtitle",
                        "text": str(item.get("subtitle_text") or "").strip(),
                    }
                    for index, item in enumerate(subtitles)
                    if isinstance(item, dict) and str(item.get("subtitle_text") or "").strip()
                ]
                if not normalized:
                    raise VideoToolError("MediaKit returned no subtitles")
                return normalized
            if status == "failed":
                raise VideoToolError("MediaKit subtitle task failed")
            import time

            time.sleep(self._settings.mediakit_poll_seconds)
        raise VideoToolError("MediaKit subtitle task timed out")

    def _mediakit_json(
        self, method: str, path: str, payload: dict[str, Any] | None
    ) -> dict[str, Any]:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        raw = self._request(
            method,
            self._settings.mediakit_endpoint + path,
            body,
            {"Content-Type": "application/json"} if body is not None else {},
            authorized=True,
        )
        try:
            decoded = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise VideoToolError("MediaKit returned invalid JSON") from exc
        if not isinstance(decoded, dict):
            raise VideoToolError("MediaKit returned invalid response")
        return decoded

    def _request(
        self,
        method: str,
        url: str,
        body: bytes | None,
        headers: dict[str, str],
        *,
        authorized: bool,
    ) -> bytes:
        request_headers = dict(headers)
        if authorized:
            request_headers["Authorization"] = "Bearer " + self._settings.mediakit_api_key
        try:
            with urlopen(
                Request(url, data=body, headers=request_headers, method=method),
                timeout=self._settings.video_model_timeout_seconds,
            ) as response:
                return response.read(8 * 1024 * 1024 + 1)
        except (HTTPError, URLError, TimeoutError) as exc:
            raise VideoToolError(f"video tool request failed: {exc}") from exc
