import asyncio

import pytest
from agents.realtime import RealtimeAgent

from content_agent_sidecar.config import Settings
from content_agent_sidecar import native_realtime
from test_native_realtime import Model, owner


def configure(monkeypatch):
    monkeypatch.setenv("CONTENT_AGENT_REALTIME_ENDPOINT", "wss://example.test/realtime")
    monkeypatch.setenv("CONTENT_AGENT_REALTIME_MODEL", "configured-realtime-model")
    monkeypatch.setenv("CONTENT_AGENT_REALTIME_API_KEY", "realtime-fixture-secret")


@pytest.mark.parametrize("missing", ["ENDPOINT", "MODEL", "API_KEY"])
def test_realtime_does_not_fall_back_to_control_model(monkeypatch, missing):
    configure(monkeypatch)
    monkeypatch.delenv("CONTENT_AGENT_REALTIME_" + missing)
    monkeypatch.setenv("CONTENT_AGENT_CONTROL_MODEL_API_KEY", "control-secret")
    monkeypatch.setenv("CONTENT_AGENT_CONTROL_MODEL_ENDPOINT", "https://control.test/v1")
    monkeypatch.setenv("CONTENT_AGENT_CONTROL_MODEL_MODEL", "control-model")
    with pytest.raises(ValueError, match="explicitly configured"):
        Settings.from_env().realtime_model_config()


@pytest.mark.parametrize("endpoint", ["ws://example.test", "wss://user:secret@example.test", "wss://example.test:0",
                                       "wss://example.test:99999", "wss://example.test/#fragment"])
def test_realtime_endpoint_rejects_invalid_configuration_without_echo(monkeypatch, endpoint):
    configure(monkeypatch)
    monkeypatch.setenv("CONTENT_AGENT_REALTIME_ENDPOINT", endpoint)
    with pytest.raises(ValueError) as result:
        Settings.from_env().realtime_model_config()
    assert endpoint not in str(result.value)


def test_configured_realtime_factory_passes_explicit_settings(monkeypatch):
    configure(monkeypatch)
    model = Model()
    monkeypatch.setattr(native_realtime, "OpenAIRealtimeWebSocketModel", lambda: model)
    settings = Settings.from_env()
    assert "realtime-fixture-secret" not in repr(settings)
    async def run():
        async with native_realtime.configured_realtime_session(settings, RealtimeAgent(name="fixture"), owner()):
            assert model.options["url"] == "wss://example.test/realtime"
            assert model.options["api_key"] == "realtime-fixture-secret"
            assert model.options["initial_model_settings"]["model_name"] == "configured-realtime-model"
        assert model.closed
    asyncio.run(run())
