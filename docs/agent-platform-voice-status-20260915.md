# Voice Implementation Status

## G4.244: Dependency and SDK Entry-Point Check

Voice and bidirectional Realtime remain mandatory, not optional scope.
Neither has a production implementation in the sidecar at this checkpoint.

Current evidence from the installed openai-agents 0.21.1 package:

- `agents.voice.VoicePipeline` provides STT, a `VoiceWorkflowBase`, and TTS.
- `SingleAgentVoiceWorkflow` maintains its own input history and calls Runner
  without this platform's context/session/approval configuration. Do not use
  it as a shortcut around existing durable conversation and approval paths.
- `VoicePipelineConfig` defaults to sensitive text/audio in traces. Platform
  construction must explicitly disable sensitive recording by default.
- `StreamedAudioResult.stream()` performs task cleanup on cancellation/close.
  The transport must close the iterator and verify cleanup, not just stop
  sending audio to the frontend.
- Importing `agents.voice` fails because numpy is absent. Installed package
  metadata requires numpy >=2.2,<3 and websockets >=15,<17 for the voice extra.

Changed pyproject.toml to `openai-agents[voice]==0.21.1`; SDK/OpenAI pins were
not upgraded. An installation attempt targeting only the project venv cannot
download dependencies because the configured network proxy refuses connection.
No proxy/gateway settings were changed, and no alternate route was attempted.
The venv has no pip module; the existing host pip was used with its explicit
`--python` target, not a system-wide package installation.

## Required Next Work

Restore approved dependency access; verify actual SDK voice import. Build the
voice workflow around the existing authorized conversation path, including
approval suspension/resume, cancellation, isolation and retention controls.
Implement audio input/output and lifecycle APIs plus microphone/start/stop/
status controls. Realtime requires its own SDK session integration and is not
satisfied by the STT/workflow/TTS pipeline. Verify real model and browser flows,
then same-version regression and final independent global review.

No voice behavior test or live audio session has passed at this checkpoint.

## G4.245: Input Contract and SDK Construction Source

Added `native_voice.py` with bounded single-turn PCM16 input validation:
24kHz mono, strict canonical Base64, whole 16-bit samples and at most 60
seconds per submitted turn. This is not a total realtime session limit.
The SDK AudioInput conversion explicitly reads little-endian PCM and copies
to native int16, without importing optional voice dependencies at app startup.

Added a VoicePipeline factory requiring explicit SDK STT/TTS model objects
and a closeable transcription workflow callback. It disables sensitive
text/audio tracing and does not instantiate an independent conversation
history. The callback is not yet wired to the production durable turn path.

Input-only tests: 17 passed, zero failures/errors/skips, evidence at
`.tmp/goal-g4245-voice-input.xml`. These tests do NOT exercise SDK VoicePipeline,
numpy conversion, TTS/STT, cancellation of SDK tasks, real gateway or browser
audio. Missing dependencies still block those checks. API/UI/session ownership
and Realtime implementation remain outstanding.

## G4.246: Native Realtime Session Construction

The installed `agents.realtime` module imports successfully without numpy.
Added `native_realtime.py` around actual RealtimeRunner/RealtimeSession with
explicit conversation identity, model instance, model name, WSS endpoint and
credential configuration. No default gateway/model fallback or attach-to-call
path is accepted. SDK and remote model tracing are disabled by construction.
The session closes on normal exit, failed connect and caller cancellation.

Eight tests passed with real SDK lifecycle and an in-memory model transport;
zero failures/errors/skips in `.tmp/goal-g4246-realtime-lifecycle.xml`.
Tests cover invalid configuration, successful audio/interrupt forwarding and
cleanup after failed/cancelled connection. They do not use a live WebSocket,
gateway, microphone or durable platform approval. The wrapper still has no
production caller. Session APIs, input ownership checks, approval persistence,
audio playback tracking, retention/usage and user controls remain required.

## G4.247: Realtime Input and Stop Boundary

The lifecycle wrapper now returns ManagedRealtimeConnection instead of a raw
SDK session. It serializes outgoing inputs, rejects sends after close, closes
on unconfirmed transport outcomes and bounds text/PCM16 audio chunks. Empty
audio is accepted only for an explicit commit. Connection input format is
restricted to 24kHz PCM16 to match byte validation. Approval methods are not
exposed before durable approval wiring exists.

The first regression had 31 passes and two pytest setup/teardown errors due to
an oversized generated parameter name exceeding Windows environment limits.
Explicit short test IDs fixed the harness without removing oversized audio.
The final selection passed 32 tests, zero failures/errors/skips; evidence:
`.tmp/goal-g4247-realtime-input-verified.xml`. Real gateway, frontend and
durable approval/session acceptance remain unverified.

## G4.248: Stop and Model Ownership

ManagedRealtimeConnection now suppresses queued events after close and cancels
an active blocked input before waiting for SDK cleanup. Event consumption also
closes its owned session. A weak ownership registry rejects reuse of the same
model instance across managed sessions, including after a completed session.

35 input/lifecycle tests passed with zero failures/errors/skips; evidence:
`.tmp/goal-g4248-realtime-stop.xml`. New cases cover stale queued events,
connection reuse and stopping a blocked send. No production transport, approval
store, browser audio path or deployment was exercised. Original remaining
capabilities and complete acceptance requirements are unchanged.
