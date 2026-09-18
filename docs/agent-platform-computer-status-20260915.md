# Managed Computer Implementation Status

## G4.229

Computer remains mandatory and incomplete. The fixed SDK exposes AsyncComputer
and ComputerTool; it does not provision this platform's browser, authorization,
audit storage or UI. No existing managed graphical browser executor was found
in the sidecar or runtime tool registry during this inspection.

Added `native_computer.py`: an AsyncComputer adapter with all required mouse,
keyboard, wait and screenshot methods. It forwards actions to a required
ComputerSessionTransport with session ID, generation and monotonically increasing
sequence; it validates authorization/completion receipts. Coordinates, input
sizes and screenshot base64/PNG header/viewport dimensions are bounded. Screenshot
validation is an envelope check, not a full pixel decoder. Actions are serialized.
Cancellation, timeout or invalid receipts render the adapter unusable and request
transport cleanup. Unconfirmed cleanup is attached to the error and explicit
close may retry cleanup, never the action.

`.tmp/goal-g4229-computer-adapter.xml`: 21 isolated tests, 0 failures, errors or
skips. Covers forwarding all SDK methods, identity/approval receipt rejection,
invalid inputs/screenshots and cancellation/timeout with no action replay.
These tests do not run a browser, contact a provider, control a desktop, or prove
Runtime authorization. The transport is currently a protocol, not an implemented
execution service. No production ComputerTool is registered yet.

## G4.230

Added `managed_computer_tool` using the pinned SDK ComputerProvider lifecycle.
It requires one conversation/background/stateful execution identity, rejects
memory-generation contexts, and requires an attempt token for worker modes.
An opener must return a fresh ManagedComputer; SDK disposal calls its close.
The constructor requires a safety-authorizer callback and accepts only literal
boolean True, not a truthy string or object. This is important because the
installed SDK's `execute_computer_actions` checks pending safety checks only
when `on_safety_check` is configured.

`.tmp/goal-g4230-computer-sdk.xml`: 26 tests, 0 failures/errors/skips. New real SDK
Runner tests cover strict safety acknowledgement, no actions on rejection,
click/screenshot dispatch on approval and session disposal on completion. The
model and Runtime transport are isolated fixtures, not a real service/browser.
The constructor is not yet wired into production AgentToolProvider, because
the isolated browser service and persistent authorization do not yet exist.

## G4.231

Added `backend/internal/scriptsandbox/computer_browser.py`, an action dispatcher
for a Runtime-owned isolated Playwright page. It validates action fields and
viewport bounds before dispatch, maps SDK key names and mouse buttons, performs
click/double-click/move/scroll/type/keypress/drag/wait/screenshot/history actions,
and releases held inputs after failure or cancellation. Cleanup attempts all
held keys even when one release fails. It does not launch or attach to browsers
and deliberately has no standalone unauthenticated service endpoint.

`.tmp/goal-g4231-computer-browser.xml`: 42 tests, zero failures/errors/skips,
combining SDK adapter/lifecycle tests and dispatcher tests with mocked Playwright
page methods. This proves dispatch and local cleanup behavior only, not actual
browser rendering, egress isolation, screenshots, or Runtime authorization.
The helper still needs provisioning and a framed Runtime-controlled transport.

## G4.232

The browser helper now has a BrowserSession frame handler bound to an existing
owned page, session ID, generation and viewport. It requires strict bounded JSON
frames, rejects duplicate fields/nonfinite numbers/unknown envelope fields, and
consumes monotonically increasing action sequences before effects. Actions are
serialized and bounded to 30 seconds. Invalid frames, action failure and
cancellation make the session unusable and request context closure. Failed
cleanup can be retried without reopening or replaying actions. Receipts do not
claim authorization; that remains the Runtime caller's responsibility.

`.tmp/goal-g4232-browser-session.xml`: 54 tests, zero failures/errors/skips.
New cases cover duplicate frames, foreign identity/generation, malformed JSON,
in-flight cancellation and cleanup-only retries. Page methods remain mocks;
there is still no live browser, container stream, provisioning or Runtime auth
integration. No user environment or database was operated on.

## G4.233

Added fresh browser provisioning to the helper: Chromium sandbox stays enabled,
a new context has a fixed viewport/device scale, no granted permissions,
downloads disabled, blocked Service Workers and normal HTTPS validation. Startup
and navigation are bounded. Partial startup failure attempts both context and
browser cleanup, and successful sessions own these resources for disposal.
The initial URL is syntax-checked (HTTP(S), no embedded credentials); this is
explicitly NOT an SSRF/egress policy.

Current OCI workspace source fixes `--network none`, `--ipc none`, read-only
filesystem and restrictive process/resource limits. Those existing policies were
not relaxed. Real web browsing needs its own controlled execution/network
profile; the existing Python workspace cannot be claimed as that service.

`.tmp/goal-g4233-browser-provision.xml`: 63 tests, zero failures/errors/skips.
Provisioning tests mock Playwright and check options, partial-failure cleanup and
URL rejection before launch. No browser was launched, no browser dependency or
OCI image was installed, and actual Chromium sandbox compatibility remains
unverified. Production authorization, container communication and network policy
are still missing.

## G4.234

Explicit close now marks the SDK adapter and browser session closed before
waiting for their action lock, cancels the active action task, and prevents
queued actions from starting. If a cancelled operation returns a late success,
that result is rejected. Repeated close calls avoid repeatedly cancelling an
already-cancelling action and still permit cleanup-only retry.

`.tmp/goal-g4234-computer-stop.xml`: 67 tests, zero failures/errors/skips. Added
both adapter-level and executor-level stop tests with queued actions and with
late success after cancellation. No real browser or process was stopped. These
tests cover cooperative cancellation and late completion, not an external
process that ignores cancellation indefinitely; Runtime container termination
and real stop acceptance remain required.

## G4.235

Added a Computer process stream in Go with 256 KiB request frames and bounded
base64 screenshot response frames. It serializes exchanges, rejects multiline
or truncated frames, closes after an uncertain response and refuses replay after
closure. It shares the existing process pipe implementation with an explicit
frame limit; PTY retains its original 2 MiB limit. Bridge closure is not claimed
as proof of OCI container removal.

Go scriptsandbox vet passed. Test source was added for independent Computer/PTY
limits, invalid requests and no replay after failed responses, but Go behavioral
tests were NOT run. No process was launched via the new stream. This is not yet
a complete browser service: fixed container startup, the Python frame-serving
entry point, Runtime authorization and dedicated egress controls remain open.

## Required Next Work

- Implement an owned isolated browser execution service and Runtime transport,
  integrated with existing execution ownership and leases, without connecting to
  the developer's browser or desktop.
- Enforce network/file/credential boundaries at the execution environment, not
  merely through model instructions or a Python URL filter.
- Persist approval, action identity and exact outcomes; expose uncertain outcomes
  for reconciliation rather than automatically retrying UI writes.
- Wire the ComputerTool constructor to production Runtime authorization/service;
  support pause/resume/session expiry without silently starting a fresh session.
- Expose user-visible status, authorization, stop and screenshot controls with
  sensitive-data handling.
- Verify actual browser interactions and clean disposal across execution modes,
  then include them in same-version regression and final independent review.

No deployment, browser launch, user-data/database changes, OCI installation or
system security changes were performed in this batch.
