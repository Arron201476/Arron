from __future__ import annotations

from dataclasses import dataclass, field
import os
from urllib.parse import urlsplit, urlunsplit


def normalize_openai_base_url(endpoint: str) -> str:
    value = endpoint.strip().rstrip("/")
    if not value:
        return ""

    parsed = urlsplit(value)
    path = parsed.path.rstrip("/")
    suffix = "/chat/completions"
    if path.endswith(suffix):
        path = path[: -len(suffix)]
    return urlunsplit((parsed.scheme, parsed.netloc, path, "", "")).rstrip("/")


def _positive_int(name: str, default: int) -> int:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    value = int(raw)
    if value <= 0:
        raise ValueError(f"{name} must be positive")
    return value


def _native_workspace_enabled() -> bool:
    value = os.getenv("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", "false").strip()
    if value in {"", "0", "f", "F", "false", "False", "FALSE"}:
        return False
    if value in {"1", "t", "T", "true", "True", "TRUE"}:
        return True
    raise ValueError("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED must be a boolean")


def _memory_worker_enabled() -> bool:
    value = os.getenv("CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED", "false").strip().lower()
    if value in {"", "0", "false", "no", "off"}:
        return False
    if value in {"1", "true", "yes", "on"}:
        return True
    raise ValueError("CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED must be a boolean")


@dataclass(frozen=True)
class Settings:
    backend_base_url: str
    model_base_url: str
    model_api_key: str
    model_name: str
    model_timeout_seconds: int
    model_max_output_tokens: int
    model_max_retries: int
    run_timeout_seconds: int
    tracing_enabled: bool
    internal_token: str
    release_id: str = "dev"
    content_model_base_url: str = ""
    content_model_api_key: str = ""
    content_model_name: str = ""
    content_model_timeout_seconds: int = 420
    content_model_max_output_tokens: int = 16384
    content_model_max_retries: int = 2
    session_db_path: str = ":memory:"
    orchestration_max_output_tokens: int = 8192
    ffmpeg_command: str = "ffmpeg"
    task_worker_enabled: bool = False
    memory_worker_enabled: bool = False
    memory_archive_db_path: str = ""
    task_worker_concurrency: int = 1
    task_worker_poll_milliseconds: int = 500
    task_worker_lease_seconds: int = 1500
    video_task_timeout_seconds: int = 1200
    video_model_base_url: str = ""
    video_model_api_key: str = ""
    video_model_name: str = ""
    video_model_timeout_seconds: int = 420
    video_model_max_output_tokens: int = 65535
    video_model_max_retries: int = 2
    video_model_stream: bool = True
    mediakit_endpoint: str = ""
    mediakit_api_key: str = ""
    mediakit_poll_seconds: float = 2.0
    guardrail_max_input_chars: int = 1_000_000
    guardrail_max_output_chars: int = 1_000_000
    native_workspace_enabled: bool = False
    realtime_endpoint: str = ""
    realtime_model_name: str = ""
    realtime_api_key: str = field(default="", repr=False)
    realtime_max_session_seconds: int = 900
    voice_stt_endpoint: str = ""
    voice_stt_model_name: str = ""
    voice_stt_api_key: str = field(default="", repr=False)
    voice_tts_endpoint: str = ""
    voice_tts_model_name: str = ""
    voice_tts_api_key: str = field(default="", repr=False)
    voice_model_timeout_seconds: int = 180
    voice_enabled: bool = False

    @classmethod
    def from_env(cls) -> "Settings":
        endpoint = os.getenv("CONTENT_AGENT_CONTROL_MODEL_ENDPOINT", "")
        return cls(
            backend_base_url=os.getenv(
                "CONTENT_AGENT_BACKEND_BASE_URL", "http://127.0.0.1:8860"
            ).rstrip("/"),
            model_base_url=normalize_openai_base_url(endpoint),
            model_api_key=os.getenv("CONTENT_AGENT_CONTROL_MODEL_API_KEY", "").strip(),
            model_name=os.getenv("CONTENT_AGENT_CONTROL_MODEL_MODEL", "").strip(),
            model_timeout_seconds=_positive_int(
                "CONTENT_AGENT_CONTROL_MODEL_TIMEOUT_SECONDS", 180
            ),
            model_max_output_tokens=_positive_int(
                "CONTENT_AGENT_CONTROL_MODEL_MAX_OUTPUT_TOKENS", 16384
            ),
            model_max_retries=int(
                os.getenv("CONTENT_AGENT_SIDECAR_MODEL_MAX_RETRIES", "2")
            ),
            run_timeout_seconds=_positive_int(
                "CONTENT_AGENT_SIDECAR_RUN_TIMEOUT_SECONDS", 210
            ),
            tracing_enabled=os.getenv(
                "CONTENT_AGENT_SIDECAR_TRACING", "false"
            ).strip().lower()
            in {"1", "true", "yes", "on"},
            internal_token=os.getenv(
                "CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", ""
            ).strip(),
            release_id=os.getenv("CONTENT_AGENT_RELEASE_ID", "dev").strip() or "dev",
            guardrail_max_input_chars=_positive_int("CONTENT_AGENT_SIDECAR_GUARDRAIL_MAX_INPUT_CHARS", 1_000_000),
            guardrail_max_output_chars=_positive_int("CONTENT_AGENT_SIDECAR_GUARDRAIL_MAX_OUTPUT_CHARS", 1_000_000),
            native_workspace_enabled=_native_workspace_enabled(),
            realtime_endpoint=os.getenv("CONTENT_AGENT_REALTIME_ENDPOINT", "").strip(),
            realtime_model_name=os.getenv("CONTENT_AGENT_REALTIME_MODEL", "").strip(),
            realtime_api_key=os.getenv("CONTENT_AGENT_REALTIME_API_KEY", "").strip(),
            realtime_max_session_seconds=_positive_int("CONTENT_AGENT_REALTIME_MAX_SESSION_SECONDS", 900),
            voice_stt_endpoint=os.getenv("CONTENT_AGENT_VOICE_STT_ENDPOINT", "").strip(),
            voice_stt_model_name=os.getenv("CONTENT_AGENT_VOICE_STT_MODEL", "").strip(),
            voice_stt_api_key=os.getenv("CONTENT_AGENT_VOICE_STT_API_KEY", "").strip(),
            voice_tts_endpoint=os.getenv("CONTENT_AGENT_VOICE_TTS_ENDPOINT", "").strip(),
            voice_tts_model_name=os.getenv("CONTENT_AGENT_VOICE_TTS_MODEL", "").strip(),
            voice_tts_api_key=os.getenv("CONTENT_AGENT_VOICE_TTS_API_KEY", "").strip(),
            voice_model_timeout_seconds=_positive_int("CONTENT_AGENT_VOICE_MODEL_TIMEOUT_SECONDS", 180),
            voice_enabled=os.getenv("CONTENT_AGENT_VOICE_ENABLED", "false").strip().lower() in {"1", "true", "yes", "on"},
            content_model_base_url=normalize_openai_base_url(
                os.getenv("CONTENT_AGENT_CONTENT_MODEL_ENDPOINT", "")
            ),
            content_model_api_key=os.getenv(
                "CONTENT_AGENT_CONTENT_MODEL_API_KEY", ""
            ).strip(),
            content_model_name=os.getenv(
                "CONTENT_AGENT_CONTENT_MODEL_MODEL", ""
            ).strip(),
            content_model_timeout_seconds=_positive_int(
                "CONTENT_AGENT_CONTENT_MODEL_TIMEOUT_SECONDS", 420
            ),
            content_model_max_output_tokens=_positive_int(
                "CONTENT_AGENT_CONTENT_MODEL_MAX_OUTPUT_TOKENS", 16384
            ),
            content_model_max_retries=int(
                os.getenv("CONTENT_AGENT_CONTENT_MODEL_MAX_RETRIES", "2")
            ),
            session_db_path=os.getenv(
                "CONTENT_AGENT_SIDECAR_SESSION_DB_PATH",
                ".tmp/openai-agents-sidecar-sessions.db",
            ).strip(),
            orchestration_max_output_tokens=_positive_int(
                "CONTENT_AGENT_SIDECAR_MAX_OUTPUT_TOKENS", 8192
            ),
            ffmpeg_command=os.getenv(
                "CONTENT_AGENT_FFMPEG_COMMAND", "ffmpeg"
            ).strip()
            or "ffmpeg",
            task_worker_enabled=os.getenv(
                "CONTENT_AGENT_SIDECAR_TASK_WORKER_ENABLED", "false"
            ).strip().lower()
            in {"1", "true", "yes", "on"},
            task_worker_concurrency=_positive_int(
                "CONTENT_AGENT_SIDECAR_TASK_WORKER_CONCURRENCY", 1
            ),
            memory_worker_enabled=_memory_worker_enabled(),
            memory_archive_db_path=os.getenv("CONTENT_AGENT_SIDECAR_MEMORY_ARCHIVE_DB_PATH", "").strip(),
            task_worker_poll_milliseconds=_positive_int(
                "CONTENT_AGENT_SIDECAR_TASK_WORKER_POLL_MILLISECONDS", 500
            ),
            task_worker_lease_seconds=_positive_int(
                "CONTENT_AGENT_SIDECAR_TASK_WORKER_LEASE_SECONDS", 1500
            ),
            video_task_timeout_seconds=_positive_int(
                "CONTENT_AGENT_SIDECAR_VIDEO_TASK_TIMEOUT_SECONDS", 1200
            ),
            video_model_base_url=normalize_openai_base_url(
                os.getenv("CONTENT_AGENT_VIDEO_MODEL_ENDPOINT", "")
            ),
            video_model_api_key=os.getenv(
                "CONTENT_AGENT_VIDEO_MODEL_API_KEY", ""
            ).strip(),
            video_model_name=os.getenv(
                "CONTENT_AGENT_VIDEO_MODEL_MODEL", ""
            ).strip(),
            video_model_timeout_seconds=_positive_int(
                "CONTENT_AGENT_VIDEO_MODEL_TIMEOUT_SECONDS", 420
            ),
            video_model_max_output_tokens=_positive_int(
                "CONTENT_AGENT_VIDEO_MODEL_MAX_OUTPUT_TOKENS", 65535
            ),
            video_model_max_retries=int(
                os.getenv("CONTENT_AGENT_VIDEO_MODEL_MAX_RETRIES", "2")
            ),
            video_model_stream=os.getenv(
                "CONTENT_AGENT_VIDEO_MODEL_STREAM", "true"
            ).strip().lower()
            in {"1", "true", "yes", "on"},
            mediakit_endpoint=os.getenv(
                "CONTENT_AGENT_MEDIAKIT_ENDPOINT", ""
            ).strip().rstrip("/"),
            mediakit_api_key=os.getenv(
                "CONTENT_AGENT_MEDIAKIT_API_KEY", ""
            ).strip(),
            mediakit_poll_seconds=float(
                os.getenv("CONTENT_AGENT_MEDIAKIT_POLL_SECONDS", "2")
            ),
        )

    def voice_model_config(self, role: str) -> dict:
        if role not in {"stt", "tts"}:
            raise ValueError("Unknown voice model role")
        if type(self.voice_model_timeout_seconds) is not int or not 1 <= self.voice_model_timeout_seconds <= 600:
            raise ValueError("Voice model timeout must be between 1 and 600 seconds")
        endpoint = getattr(self, f"voice_{role}_endpoint")
        model = getattr(self, f"voice_{role}_model_name")
        key = getattr(self, f"voice_{role}_api_key")
        if any(not isinstance(value, str) or not value.strip() or value != value.strip()
               or not value.isprintable() for value in (endpoint, model, key)):
            raise ValueError("Voice endpoint, model and API key must be explicitly configured")
        try:
            parsed = urlsplit(endpoint)
            valid = (parsed.scheme == "https" and parsed.hostname and parsed.username is None
                     and parsed.password is None and not parsed.fragment and not parsed.query
                     and parsed.port != 0 and "\\" not in endpoint and len(endpoint) <= 8192)
        except ValueError:
            valid = False
        if not valid:
            raise ValueError("Voice endpoint must be an HTTPS base URL without embedded credentials or query")
        return {"model": model, "client": {"base_url": endpoint, "api_key": key,
                "timeout": self.voice_model_timeout_seconds, "max_retries": 0}}

    def realtime_model_config(self) -> dict:
        if type(self.realtime_max_session_seconds) is not int or not 1 <= self.realtime_max_session_seconds <= 3600:
            raise ValueError("Realtime session limit must be between 1 and 3600 seconds")
        if not all((self.realtime_endpoint, self.realtime_model_name, self.realtime_api_key)):
            raise ValueError("Realtime endpoint, model and API key must be explicitly configured")
        try:
            parsed = urlsplit(self.realtime_endpoint)
            valid = (parsed.scheme == "wss" and parsed.hostname and parsed.username is None
                     and parsed.password is None and not parsed.fragment and parsed.port != 0
                     and "\\" not in self.realtime_endpoint and self.realtime_endpoint.isprintable()
                     and len(self.realtime_endpoint) <= 8192)
        except ValueError:
            valid = False
        if not valid:
            raise ValueError("Realtime endpoint must be a secure WebSocket URL without embedded credentials")
        return {"url": self.realtime_endpoint, "api_key": self.realtime_api_key,
                "initial_model_settings": {"model_name": self.realtime_model_name}}

    def validate_model(self) -> None:
        missing = [
            name
            for name, value in (
                ("CONTENT_AGENT_CONTROL_MODEL_ENDPOINT", self.model_base_url),
                ("CONTENT_AGENT_CONTROL_MODEL_API_KEY", self.model_api_key),
                ("CONTENT_AGENT_CONTROL_MODEL_MODEL", self.model_name),
            )
            if not value
        ]
        if missing:
            raise ValueError("missing model configuration: " + ", ".join(missing))
        if self.model_max_retries < 0:
            raise ValueError("CONTENT_AGENT_SIDECAR_MODEL_MAX_RETRIES cannot be negative")
        if self.video_model_max_retries < 0:
            raise ValueError("CONTENT_AGENT_VIDEO_MODEL_MAX_RETRIES cannot be negative")
        if self.content_model_max_retries < 0:
            raise ValueError("CONTENT_AGENT_CONTENT_MODEL_MAX_RETRIES cannot be negative")
        if not 1 <= self.task_worker_concurrency <= 16:
            raise ValueError(
                "CONTENT_AGENT_SIDECAR_TASK_WORKER_CONCURRENCY must be between 1 and 16"
            )
