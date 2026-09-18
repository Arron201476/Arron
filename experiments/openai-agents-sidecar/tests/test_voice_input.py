import base64

import pytest

from content_agent_sidecar.native_voice import MAX_VOICE_TURN_BYTES, VoiceInputError, decode_voice_turn


def payload(raw=b"\x00\x00", **changes):
    return {"format": "pcm16", "sample_rate": 24000, "channels": 1,
            "audio": base64.b64encode(raw).decode("ascii"), **changes}


def test_voice_input_preserves_signed_pcm_samples():
    raw = b"\x00\x80\xff\x7f\xff\xff\x00\x00"
    assert decode_voice_turn(payload(raw)) == raw


@pytest.mark.parametrize("value", [
    {}, None, payload(format="wav"), payload(sample_rate=48000), payload(sample_rate=True),
    payload(channels=True), payload(channels=2), payload(extra="ignored"),
    payload(audio=""), payload(audio="!"), payload(audio="AA=="), payload(audio="AAA=\n"),
    payload(audio="AAB="), payload(audio="\u4e2d"), payload(audio=b"AAA="),
])
def test_voice_input_rejects_invalid_wire_contract(value):
    with pytest.raises(VoiceInputError):
        decode_voice_turn(value)


def test_voice_input_enforces_turn_limit():
    assert len(decode_voice_turn(payload(b"\0" * MAX_VOICE_TURN_BYTES))) == MAX_VOICE_TURN_BYTES
    with pytest.raises(VoiceInputError):
        decode_voice_turn(payload(b"\0" * (MAX_VOICE_TURN_BYTES + 2)))
