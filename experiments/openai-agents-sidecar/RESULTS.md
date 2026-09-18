# Compatibility results

Date: 2026-08-26

## Tested stack

- OpenAI Agents SDK: 0.21.1
- Transport: OpenAI-compatible Responses API and `/responses/compact`
- Isolated Go backend: `http://127.0.0.1:8890`
- Sidecar: `http://127.0.0.1:8891`
- Tracing: disabled
- Write tools: enabled only through authenticated Go Runtime commands

## Results

| Case | Result | Observed latency |
| --- | --- | ---: |
| Plain chat | Passed | 5.94 s |
| Capability registry tool | Passed; returned all 3 registered Skills | 11.11 s |
| Project snapshot tool | Passed; matched project state and 2 artifacts | 14.36 s |
| Exact artifact-version tool | Passed; matched the focused version and content | 9.45 s |
| Revision intent safety | Passed; `requires_write=true`, artifact unchanged | 14.25 s |
| Native SDK structured output | Failed on the compatible gateway | 3-7 s |
| Real SSE with capability tool | Passed; deltas, tool events, and one terminal event | 45.89 s |
| Explicit cancellation | Passed; provider run cancelled and terminal event emitted | 0.05 s |
| Automatic timeout | Passed against real gateway with a 2 s test limit | 2.22 s |
| Transient provider retry | Passed; HTTP 500 then success on the second request | local fault test |
| Terminal provider error | Passed; normalized without credentials or upstream details | local fault test |
| Authenticated internal control route | Passed; invalid token rejected before body validation | local contract test |
| Novel Skill control decision | Passed; `propose_capability -> novel_to_script` | 2.98 s |
| Non-novel Skill control decision | Passed; `propose_capability -> non_novel_to_script` | 3.42 s |
| Video Skill control decision | Passed; `propose_capability -> video_reference_creation` | 3.62 s |
| Generic artifact control decision | Passed; `create_artifact`, no Skill | 12.22 s |
| Go sidecar adapter compilation | Passed for `internal/shell` and `cmd/server` | local compile |
| Go sidecar adapter tests | Passed all 5 sidecar adapter cases on isolated Linux host | remote test |
| Complete Go Shell regression | Passed after fixing the nil-generator availability regression found by the suite | remote test |
| Complete HTTP/Runtime regression | Passed with temporary SQLite state and repository test fixtures | 34 s |
| SDK Session and authoritative message history | Passed; bounded recent history and scoped older-message search | local contract test |
| Full-turn controlled execution | Passed; SDK commits exactly once through Runtime | Python and Go contract tests |
| Provider failure before tools | Passed; zero Runtime commits and no fake success | local fault test |
| Unified Agent event envelope | Passed; provider payloads rejected by frontend adapter | Python and frontend tests |
| Exact duplicate request | Passed; replay preserved the original 2 message IDs and produced one durable exchange | live canary |
| Multiple generic artifacts | Passed; `人物设定` and `第一集试写` coexist with independent versions | live canary |
| Cross-turn authoritative memory | Passed; persisted `青铜月桂` and recovered it after a later turn | live canary |
| Sidecar process failure and retry | Passed; HTTP 502 with zero dirty writes, then same-key retry committed once | live fault test |
| Browser projection | Passed; 12 messages and 2 artifacts rendered at 1440 px with no horizontal overflow | headless Edge |
| Current Sidecar regression | Passed: 55 tests | local pre-cutover gate |
| Current frontend regression | Passed: 18 files, 86 tests | local pre-cutover gate |
| Current Go compile gate | Passed: Runtime and HTTP API test binaries plus Server | WDAC blocks newly generated test EXE execution |
| SDK-native compaction | Passed; 102 prior items compacted before the next model turn | live canary |
| Authoritative Goal lifecycle | Passed; set/read/cancel committed through Go transaction | live canary |
| SDK Skill Agent orchestration | Passed; specialist Agent-as-tool precedes terminal commit | live canary |

## Compatibility finding

The gateway accepts Responses model requests, Agents SDK function tools, and
the compaction endpoint. Compaction is executed by the SDK, but this compatible
gateway returns a summary `message/input_text` item rather than OpenAI's opaque
encrypted compaction item. The SDK accepts and persists that compatibility
result; it must not be described as the standard OpenAI compaction item format.

The current gateway/model also rejects an explicit `temperature` after a tool
call. The sidecar therefore leaves temperature unspecified and preserves only
the supported output-token setting. The real streamed tool run completed, but
its first content delta arrived after 32.17 seconds; streaming improves visible
progress after the first byte but does not solve provider first-byte latency.

## Architecture finding

The SDK can replace the thin Eino control graph, but should not become a second
system of record. The Go runtime must continue to own:

- projects and conversations;
- capability manifests and execution contracts;
- runs, checkpoints, pause/resume, and approvals;
- artifacts, versions, lineage, and write idempotency;
- durable messages, project facts, and persisted business state.

The SDK `SQLiteSession` owns persistent model-facing history and tool items. It
is bootstrapped once from the full Go conversation, serializes same-session
Runner turns, and is wrapped by `OpenAIResponsesCompactionSession`. Durable
user-visible messages and business state remain in Go Runtime, so there is no
second business system of record.

## Full SDK runtime status

- The Go-to-sidecar boundary uses a bearer token and rejects invalid credentials
  before parsing the control payload.
- The standard Go startup requires Sidecar and has no Eino decision fallback.
- Sidecar errors fail the turn; retry uses the same idempotency key.
- Go remains authoritative for Guard checks, writes, approvals, Runs,
  Artifacts, versions, durable messages, and events.
- SDK write tools call Runtime commands and cannot bypass idempotency, version,
  project, or approval checks.

## 2026-08-26 full-migration addendum

- Sidecar regression: 82 tests passed.
- Frontend regression: 89 tests passed; production build passed.
- Live SDK-native compaction and early-memory recall passed.
- Live image and video-frame inputs passed.
- Live SDK Artifact patch changed exactly one requested JSON path without a
  regeneration plan.
- Live client abort persisted no turn and left Sidecar healthy.
- Standard startup now runs frontend 8860, Go 8850, and required Sidecar 8871.

The standard stack was started against the main database. All three health
checks passed and a real main-database conversation turn returned the requested
exact response through SDK Runner and the Go commit boundary. The source
routing and standard launcher no longer depend on Eino or the Go
model-execution path.
