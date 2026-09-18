import pytest

from content_agent_sidecar.config import Settings, normalize_openai_base_url


def test_normalize_full_chat_completions_endpoint() -> None:
    assert (
        normalize_openai_base_url("https://model.example/v1/chat/completions")
        == "https://model.example/v1"
    )


def test_preserve_api_root() -> None:
    assert normalize_openai_base_url("https://model.example/v1/") == "https://model.example/v1"


def test_from_env_uses_durable_sdk_session_defaults(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    monkeypatch.delenv("CONTENT_AGENT_SIDECAR_SESSION_DB_PATH", raising=False)
    monkeypatch.delenv("CONTENT_AGENT_VIDEO_MODEL_MAX_RETRIES", raising=False)

    settings = Settings.from_env()

    assert settings.session_db_path == ".tmp/openai-agents-sidecar-sessions.db"
    assert settings.video_model_max_retries == 2


@pytest.mark.parametrize("value,enabled", [("true", True), (" YES ", True), ("1", True), ("ON", True),
                                         ("false", False), ("NO", False), ("0", False), ("off", False), ("", False)])
def test_memory_generation_flag_is_explicit_and_independent(monkeypatch, value, enabled):
    monkeypatch.setenv("CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED", value)
    monkeypatch.setenv("CONTENT_AGENT_SIDECAR_MEMORY_ARCHIVE_DB_PATH", "  private-archive.sqlite  ")
    resolved = Settings.from_env()
    assert resolved.memory_worker_enabled is enabled
    assert resolved.memory_archive_db_path == "private-archive.sqlite"


@pytest.mark.parametrize("value", ["tru", "enabled", "2", "PRIVATE_INVALID_SETTING"])
def test_memory_generation_flag_rejects_typos_without_echoing_value(monkeypatch, value):
    monkeypatch.setenv("CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED", value)
    with pytest.raises(ValueError, match="must be a boolean") as error:
        Settings.from_env()
    assert value not in str(error.value)


def test_memory_worker_and_archive_recovery_default_to_disabled(monkeypatch):
    monkeypatch.delenv("CONTENT_AGENT_SIDECAR_MEMORY_WORKER_ENABLED", raising=False)
    monkeypatch.delenv("CONTENT_AGENT_SIDECAR_MEMORY_ARCHIVE_DB_PATH", raising=False)
    resolved = Settings.from_env()
    assert resolved.memory_worker_enabled is False
    assert resolved.memory_archive_db_path == ""
