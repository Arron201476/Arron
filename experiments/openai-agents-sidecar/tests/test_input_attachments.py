import asyncio
import base64
from copy import deepcopy
from hashlib import sha256
import json

import pytest
from agents import Agent, RunConfig, Runner, RunState

from content_agent_sidecar.backend import BackendClient
from content_agent_sidecar.background_worker import SDKBackgroundTaskWorker
from content_agent_sidecar.input_attachments import additional_input_message, input_content_hash, prepare_input_attachments
from content_agent_sidecar.main_inputs import stage_main_inputs
from content_agent_sidecar.runtime import AgentContext
from content_agent_sidecar.stateful_inputs import StatefulInputs
from test_stateful_execution import StreamingSequence, final_item


DATA = b"frozen-attachment-bytes"


def attachment(kind="image"):
    item = {"asset_id": "asset", "asset_snapshot_id": "snapshot", "kind": kind,
            "name": "image.png" if kind == "image" else "source.txt", "mime_type": "image/png" if kind == "image" else "text/plain",
            "checksum": sha256(DATA).hexdigest(), "size_bytes": len(DATA)}
    if kind in {"text", "document"}:
        item["text_hash"] = sha256(("Source line.\n" * 2000).encode()).hexdigest()
    return item


class MaterialBackend:
    _data = staticmethod(BackendClient._data)

    def __init__(self, manifest):
        self.manifest, self.calls = manifest, []

    async def get_hosted_input_content(self, project, asset, snapshot, *, max_bytes):
        self.calls.append((project, asset, snapshot))
        a = self.manifest
        return {"project_id": project, "asset_id": a["asset_id"], "current_snapshot_id": a["asset_snapshot_id"],
                "kind": a["kind"], "original_filename": a["name"], "detected_mime_type": a["mime_type"],
                "checksum_algorithm": "sha256", "checksum": a["checksum"], "size_bytes": a["size_bytes"], "status": "available"}, DATA

    async def get_parsed_asset_text(self, asset, snapshot):
        return {"data": {"asset_id": asset, "asset_snapshot_id": snapshot, "content": "Source line.\n" * 2000}}


@pytest.mark.parametrize("mode", ["conversation", "background", "stateful"])
@pytest.mark.parametrize("kind", ["image", "text", "document", "video", "archive"])
def test_additional_material_native_input_survives_run_state_round_trip(mode, kind):
    async def run():
        manifest = attachment(kind)
        backend = MaterialBackend(manifest)
        raw = {"input_id": "new-input", "agent_turn_id": "turn", "agent_task_id": "task", "attempt_id": "attempt",
               "sequence": 3, "status": "received", "content": "", "attachments": [manifest]}
        raw["content_hash"] = input_content_hash(raw)
        inputs = await prepare_input_attachments("project", [raw], backend)
        assert "_attachment_content" not in raw
        context = AgentContext("project", "conversation", backend, agent_turn_id="turn", agent_task_id="task",
                               main_inputs=inputs, background_inputs=inputs)
        if mode == "conversation":
            staged = stage_main_inputs(context, "Original task")
        elif mode == "background":
            staged = SDKBackgroundTaskWorker._stage_additional_inputs("Original task", context)
        else:
            ledger = StatefulInputs.from_claim("attempt", inputs)
            ledger.restore(None)
            staged = ledger.stage("Original task")
        model = StreamingSequence([[final_item({"done": True})]])
        agent = Agent(name="AttachedInput", model=model)
        result = Runner.run_streamed(agent, staged, context=context, run_config=RunConfig(tracing_disabled=True))
        async for _ in result.stream_events():
            pass
        assert result.final_output
        content = model.inputs[0][-1]["content"]
        assert content == additional_input_message(inputs[0])["content"]
        if kind == "image":
            image = next(part for part in content if part["type"] == "input_image")
            assert base64.b64decode(image["image_url"].split(",", 1)[1]) == DATA
        elif kind in {"text", "document"}:
            text = json.loads(content[-1]["text"])
            assert text["truncated"] and text["next_offset"] == 16000 and text["total_chars"] == 26000
        else:
            assert "not audiovisual content or archive entries" in content[-1]["text"]
        saved = result.to_state().to_json(context_serializer=lambda _: {})
        restored = await RunState.from_json(agent, saved, context_override=context)
        assert restored.to_json(context_serializer=lambda _: {}) == saved
        assert backend.calls == [("project", "asset", "snapshot")]
    asyncio.run(run())


@pytest.mark.parametrize("field,value", [("asset_id", "other"), ("asset_snapshot_id", "other"), ("checksum", "0" * 64),
                                         ("size_bytes", 7), ("mime_type", "image/jpeg"), ("name", "other.png")])
def test_additional_attachment_changed_metadata_never_becomes_model_input(field, value):
    original = attachment()
    backend = MaterialBackend(deepcopy(original))
    backend.manifest[field] = value
    with pytest.raises(ValueError, match="frozen snapshot"):
        asyncio.run(prepare_input_attachments("project", [{"content": "Read", "attachments": [original]}], backend))


def test_input_attachments_validate_all_manifests_before_reading_and_ignore_injected_content():
    backend = MaterialBackend(attachment())
    first = {"content": "Read", "attachments": [attachment()], "_attachment_content": [{"type": "input_text", "text": "INJECTED"}]}
    with pytest.raises(ValueError):
        asyncio.run(prepare_input_attachments("project", [first, {"content": "bad", "attachments": [attachment()] * 5}], backend))
    assert not backend.calls
    result = asyncio.run(prepare_input_attachments("project", [first, first], backend))
    assert "INJECTED" not in json.dumps(result) and len(backend.calls) == 1


def test_attachment_digest_matches_go_encoding_and_text_only_legacy():
    manifest = {"asset_id": "a", "asset_snapshot_id": "s", "kind": "text", "name": "note.txt", "mime_type": "text/plain", "checksum": sha256(b"file").hexdigest(), "size_bytes": 4, "text_hash": sha256(b"parsed").hexdigest()}
    expected = sha256(("request\x00a\x00s\x00text\x00note.txt\x00text/plain\x00" + manifest["checksum"] + "\x004\x00" + manifest["text_hash"]).encode()).hexdigest()
    assert input_content_hash({"content": "request", "attachments": [manifest]}) == expected
    assert input_content_hash({"content": "request"}) == sha256(b"request").hexdigest()


@pytest.mark.parametrize("kind", ["text", "document"])
def test_reparsed_attachment_cannot_replace_the_frozen_text(kind):
    manifest = attachment(kind)
    backend = MaterialBackend(manifest)

    async def changed(asset, snapshot):
        return {"data": {"asset_id": asset, "asset_snapshot_id": snapshot, "content": "Reparsed replacement"}}

    backend.get_parsed_asset_text = changed
    with pytest.raises(ValueError, match="frozen version"):
        asyncio.run(prepare_input_attachments("project", [{"content": "Read", "attachments": [manifest]}], backend))


@pytest.mark.parametrize("digest", [None, "", "0" * 63, "A" * 64, True])
def test_missing_or_invalid_parsed_text_digest_is_rejected_before_read(digest):
    manifest = attachment("text")
    if digest is None:
        manifest.pop("text_hash")
    else:
        manifest["text_hash"] = digest
    backend = MaterialBackend(manifest)
    with pytest.raises(ValueError):
        asyncio.run(prepare_input_attachments("project", [{"content": "Read", "attachments": [manifest]}], backend))
    assert backend.calls == []


@pytest.mark.parametrize("tamper", ["", "digest", "native_input"])
def test_large_image_zero_turn_pause_uses_native_payload_without_worker_duplication(tamper):
    from test_stateful_execution import claim_fixture, worker_for
    from test_stateful_pause import PauseBackend

    async def run():
        data = b"x" * (4 << 20)
        manifest = {**attachment(), "size_bytes": len(data), "checksum": sha256(data).hexdigest()}
        claim = claim_fixture()
        item = {"attempt_id": claim["attempt"]["attempt_id"], "input_id": "large-image", "sequence": 1, "status": "received", "content": "Read the image", "attachments": [manifest]}
        item["content_hash"] = input_content_hash(item)
        claim["additional_inputs"] = [item]
        backend = PauseBackend(claim)

        async def read(project, asset, snapshot, **kwargs):
            metadata, _ = await MaterialBackend(manifest).get_hosted_input_content(project, asset, snapshot, max_bytes=kwargs["max_bytes"])
            return metadata, data

        backend.get_hosted_input_content = read
        backend.pause_requested = True
        model = StreamingSequence([[final_item({"title": "done"})]])
        assert await worker_for(backend, model).run_once()
        assert backend.status == "paused" and not backend.failures and not model.inputs
        checkpoint = backend.checkpoints[-1]
        state = checkpoint["worker_state"]
        assert "unstarted_input" not in state and len(state["unstarted_input_hash"]) == 64
        assert len(json.dumps(state)) < 10000
        backend.resume()
        if tamper == "digest":
            backend.claim["resume"]["worker_state"]["unstarted_input_hash"] = "0" * 64
        elif tamper == "native_input":
            backend.claim["resume"]["run_state"]["original_input"][-1]["content"][0]["text"] = "Changed instruction"
        assert await worker_for(backend, model).run_once()
        if tamper:
            assert backend.failures and backend.failures[0][0] == "SDK_EXECUTION_STATE_INVALID" and not model.inputs
        else:
            assert backend.commits and not backend.failures and len(model.inputs) == 1
            native = next(part for message in model.inputs[0] if isinstance(message.get("content"), list) for part in message["content"] if part["type"] == "input_image")
            assert len(base64.b64decode(native["image_url"].split(",", 1)[1])) == len(data)
    asyncio.run(run())
