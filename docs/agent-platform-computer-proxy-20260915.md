# Computer Proxy Implementation Checkpoint

## G4.240

Scope: local source implementation only. No listener, browser, container,
deployment, database migration, credential change, or user project operation.

Implemented in `backend/internal/scriptsandbox/computer_proxy.go`:

- Per-session proxy credential validation before forwarding.
- Bounded concurrency and HTTP request/response size.
- Existing exact-host/public-address egress dial policy.
- Removal of proxy credentials and hop-by-hop headers on forwarding.
- No automatic redirect following.
- CONNECT restricted to port 443 without request body framing or URL extras.
- Session cancellation closes hijacked sockets even during handshake flush.

Added test source for authentication, metadata stripping, redirect behavior,
host mismatch, closed sessions, malformed CONNECT, concurrency saturation,
and stopping a blocked handshake using in-memory pipes.

Verification: `go vet ./internal/scriptsandbox` exited 0. Behavioral Go tests
were NOT executed under the current execution boundary. Full backend
`go build ./...` also exited 0.

## Remaining Integration

The proxy is not an enforced network boundary by itself. Browser isolation,
per-session listener provisioning and credential delivery, durable action
authorization, audit records, production SDK provider wiring, user-facing
controls and actual end-to-end acceptance remain outstanding. CONNECT cannot
inspect encrypted HTTP paths. Do not represent this batch as Computer delivery.

The earlier Computer status document was not updated after its write was
denied. This checkpoint does not claim to replace its missing intermediate
batch records or to establish full capability coverage.

## G4.241: Required Browser Proxy Startup Configuration

The private Go startup protocol and Python browser service now require an
explicit proxy IP, port and 32-byte hex credential. Invalid configuration
is rejected before sending a startup frame or launching a browser. Browser
launch receives separate server/username/password fields; no direct fallback
is implemented. This configuration is not proof of enforced egress isolation.

`go vet ./internal/scriptsandbox` exited 0. The isolated Python proxy-startup
and native adapter selection passed 45 tests. JUnit evidence:
`.tmp/goal-g4241-computer-proxy-startup.xml`. No real browser or network was used.

The patch updating the existing `test_computer_browser_executor.py` fixtures
is still pending in tool cell 1543. It has not been resubmitted. Therefore the
existing browser-service and three-identity integration suites have not been
rerun for this protocol revision and this batch is not fully accepted.

Proxy API reference: https://playwright.dev/python/docs/api/class-browsertype

## G4.242: Proxy Service Ownership

Added `computer_proxy_server.go` to accept an isolation-owned concrete TCP
listener, create a separate random session credential and provide the private
browser startup configuration. The HTTP server has header/read/write/idle
timeouts and bounded headers. Wildcard listeners are rejected.

Cancellation, listener failure and explicit close share an idempotent cleanup
path. Unexpected listener and connection cleanup failures return fixed error
messages, not raw network details. Closed sessions cannot issue configuration.

Added Go test source using a fake listener for rejected binding cleanup,
cancellation, credential separation and listener failure. `go vet
./internal/scriptsandbox` exited 0 after the final edit. Behavioral Go tests
were not executed. No actual listener, browser, OCI instance or network policy
was created or changed. The existing script workspace remains network-none.

This owner is not yet connected to a production isolation provider, durable
authorization or the frontend. The existing fixture patch in cell 1543 remains
live and pending; no replacement write was attempted.

## G4.243: Joined Browser Session Lifetime

Added `computer_session.go` to join the proxy owner and browser protocol.
The generated proxy configuration is passed to the private startup frame.
Startup failure releases transferred resources; parent cancellation or proxy
failure closes idle browser transport; uncertain actions close the session.
Protocol cleanup uncertainty is now preserved instead of silently discarded.

Added test source for startup/action ownership, failed startup cleanup and
idle cancellation. Final `go vet ./internal/scriptsandbox` exited 0. Go
behavioral tests were not run. These are internal components without a
production caller: enforced isolation, durable authorization/audit, SDK
transport wiring, UI controls and real end-to-end acceptance remain required.
The pending fixture patch in cell 1543 was re-polled and remains live.
