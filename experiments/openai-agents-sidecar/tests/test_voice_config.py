from dataclasses import replace

import pytest

from content_agent_sidecar.config import Settings


def configure(monkeypatch):
    for role in ("STT", "TTS"):
        monkeypatch.setenv(f"CONTENT_AGENT_VOICE_{role}_ENDPOINT", f"https://{role.lower()}.test/v1")
        monkeypatch.setenv(f"CONTENT_AGENT_VOICE_{role}_MODEL", f"explicit-{role}")
        monkeypatch.setenv(f"CONTENT_AGENT_VOICE_{role}_API_KEY", f"secret-{role}")


@pytest.mark.parametrize("role", ["stt", "tts"])
@pytest.mark.parametrize("missing", ["ENDPOINT", "MODEL", "API_KEY"])
def test_voice_roles_require_explicit_configuration(monkeypatch, role, missing):
    configure(monkeypatch)
    monkeypatch.delenv(f"CONTENT_AGENT_VOICE_{role.upper()}_{missing}")
    monkeypatch.setenv("CONTENT_AGENT_CONTROL_MODEL_ENDPOINT", "https://control.test/v1")
    monkeypatch.setenv("CONTENT_AGENT_CONTROL_MODEL_API_KEY", "control-secret")
    monkeypatch.setenv("CONTENT_AGENT_CONTROL_MODEL_MODEL", "control-model")
    with pytest.raises(ValueError, match="explicitly configured"):
        Settings.from_env().voice_model_config(role)


@pytest.mark.parametrize("endpoint", ["http://voice.test/v1", "https://user:secret@voice.test/v1",
    "https://voice.test:0/v1", "https://voice.test:99999/v1", "https://voice.test/v1?token=secret",
    "https://voice.test/v1#fragment", "https://voice.test\\private"])
def test_voice_rejects_invalid_endpoint_without_echo(monkeypatch, endpoint):
    configure(monkeypatch)
    monkeypatch.setenv("CONTENT_AGENT_VOICE_STT_ENDPOINT", endpoint)
    with pytest.raises(ValueError) as failure:
        Settings.from_env().voice_model_config("stt")
    assert endpoint not in str(failure.value)


def test_voice_uses_separate_models_and_no_automatic_retry(monkeypatch):
    configure(monkeypatch)
    settings = Settings.from_env()
    assert "secret-STT" not in repr(settings) and "secret-TTS" not in repr(settings)
    for role in ("stt", "tts"):
        config = settings.voice_model_config(role)
        assert config["model"] == f"explicit-{role.upper()}"
        assert config["client"] == {"base_url": f"https://{role}.test/v1",
            "api_key": f"secret-{role.upper()}", "timeout": 180, "max_retries": 0}


@pytest.mark.parametrize("timeout", [0, 601, True, 1.5])
def test_voice_rejects_invalid_model_timeout(monkeypatch, timeout):
    configure(monkeypatch)
    with pytest.raises(ValueError, match="timeout"):
        replace(Settings.from_env(), voice_model_timeout_seconds=timeout).voice_model_config("stt")
