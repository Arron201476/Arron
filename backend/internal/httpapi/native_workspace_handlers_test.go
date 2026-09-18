package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
	"content-agent/backend/internal/shell"
)

const nativeHTTPPrefix = "/internal/v1/native-workspaces/"

type nativeHTTPEngine struct {
	handle                        scriptsandbox.WorkspaceHandle
	archive                       scriptsandbox.WorkspaceSnapshot
	creates, reconnects, hydrates int
	onCreate                      func()
	createError                   error
	allowDelete                   bool
	deletes                       int
}

func (e *nativeHTTPEngine) CreateWorkspace(ctx context.Context, id, owner string, limits scriptsandbox.Limits) (scriptsandbox.WorkspaceHandle, error) {
	e.creates++
	e.handle = scriptsandbox.WorkspaceHandle{SessionID: id, OwnerHash: owner, Limits: limits, ContainerID: strings.Repeat("b", 64), PolicyHash: strings.Repeat("c", 64)}
	if e.onCreate != nil {
		e.onCreate()
	}
	return e.handle, e.createError
}

func (e *nativeHTTPEngine) FindWorkspace(context.Context, string, string, scriptsandbox.Limits) (scriptsandbox.WorkspaceHandle, bool, error) {
	return scriptsandbox.WorkspaceHandle{}, false, errors.New("unexpected environment discovery")
}

func (e *nativeHTTPEngine) ReconnectWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle) error {
	e.reconnects++
	if e.handle != handle {
		return errors.New("wrong environment handle")
	}
	return ctx.Err()
}

func (e *nativeHTTPEngine) DeleteWorkspace(_ context.Context, handle scriptsandbox.WorkspaceHandle) error {
	if !e.allowDelete || e.handle != handle {
		return errors.New("HTTP fixture must not delete an unverified environment")
	}
	e.deletes++
	e.handle = scriptsandbox.WorkspaceHandle{}
	return nil
}

func (e *nativeHTTPEngine) ExportWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle) (scriptsandbox.WorkspaceSnapshot, error) {
	if e.handle != handle {
		return scriptsandbox.WorkspaceSnapshot{}, errors.New("wrong export handle")
	}
	return e.archive, ctx.Err()
}

func (e *nativeHTTPEngine) HydrateWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle, data []byte, hash string) (scriptsandbox.WorkspaceSnapshot, error) {
	e.hydrates++
	if e.handle != handle || !bytes.Equal(data, e.archive.Archive) || hash != e.archive.SHA256 {
		return scriptsandbox.WorkspaceSnapshot{}, errors.New("wrong restore target")
	}
	return e.archive, ctx.Err()
}

type nativeHTTPFixture struct {
	server  *Server
	store   *businessruntime.Store
	ctx     context.Context
	headers map[string]string
	session string
	engine  *nativeHTTPEngine
}

func newNativeHTTPFixture(t *testing.T) nativeHTTPFixture {
	t.Helper()
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "native-http-test-service")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "native-http.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner := identity.Principal{Kind: identity.KindUser, UserID: "native-owner", WorkspaceID: "native-workspace", Role: identity.RoleOwner}
	if err := store.BootstrapPrincipal(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), owner)
	project, err := store.CreateProject(ctx, "Native workspace HTTP fixture")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Workspace test"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	turn, err = store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{{Token: "native-owner-token", UserID: owner.UserID, WorkspaceID: owner.WorkspaceID, Role: identity.RoleOwner}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	store.SetScriptSandbox(availablePolicySandbox{})
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	content := []byte{0, 255, 128, 1}
	if err := w.WriteHeader(&tar.Header{Name: "artifact.bin", Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := scriptsandbox.ParseWorkspaceSnapshot(buffer.Bytes(), scriptsandbox.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	engine := &nativeHTTPEngine{archive: archive}
	server.ConfigureNativeWorkspaceEngine(engine)
	headers := map[string]string{"Authorization": "Bearer native-http-test-service", "X-Agent-Project-ID": project.ProjectID,
		"X-Agent-Turn-ID": turn.AgentTurnID, "X-Agent-Dispatch-Generation": strconv.FormatInt(turn.DispatchGeneration, 10), "X-Workspace-Lease-Key": strings.Repeat("a", 64)}
	data := objectAt(t, performJSONWithHeaders(t, server.Handler(), http.MethodGet, nativeHTTPPrefix+"current", nil, headers, http.StatusOK), "data")
	if data["lease"] != nil {
		t.Fatalf("unexpected prior workspace: %#v", data)
	}
	data = objectAt(t, performJSONWithHeaders(t, server.Handler(), http.MethodPost, nativeHTTPPrefix+"reservations", map[string]any{"expected_generation": 0}, headers, http.StatusOK), "data")
	if len(data) != 4 || data["generation"] != float64(1) {
		t.Fatalf("invalid public lease: %#v", data)
	}
	headers["X-Workspace-Lease-Generation"] = "1"
	return nativeHTTPFixture{server, store, ctx, headers, stringAt(t, data, "session_id"), engine}
}

func (f nativeHTTPFixture) policy(t *testing.T, enabled bool, limits *scriptsandbox.Limits) {
	t.Helper()
	current, err := f.store.GetScriptSandboxPolicy(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateScriptSandboxPolicy(f.ctx, businessruntime.UpdateScriptSandboxPolicyCommand{ExpectedVersion: current.Version, Enabled: enabled, Limits: limits}); err != nil {
		t.Fatal(err)
	}
}

func (f nativeHTTPFixture) request(t *testing.T, method, suffix string, body io.Reader, headers map[string]string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, nativeHTTPPrefix+f.session+"/"+suffix, body)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	result := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(result, request)
	if result.Code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, suffix, result.Code, want, result.Body.String())
	}
	return result
}

func TestNativeWorkspaceHTTPLivenessAndShutdownUseCurrentAuthority(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	f.engine.allowDelete = true
	absent := f.request(t, http.MethodPost, "probe", strings.NewReader(`{}`), f.headers, http.StatusOK)
	if !strings.Contains(absent.Body.String(), `"state":"absent"`) || f.engine.creates != 0 {
		t.Fatal("probe created an environment")
	}
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
	ready := f.request(t, http.MethodPost, "probe", strings.NewReader(`{}`), f.headers, http.StatusOK)
	if !strings.Contains(ready.Body.String(), `"state":"ready"`) || f.engine.reconnects != 1 {
		t.Fatal("probe did not verify the existing handle")
	}
	wrong := maps.Clone(f.headers)
	wrong["X-Workspace-Lease-Key"] = strings.Repeat("f", 64)
	f.request(t, http.MethodPost, "shutdown", strings.NewReader(`{}`), wrong, http.StatusForbidden)
	if f.engine.deletes != 0 {
		t.Fatal("invalid lease closed environment")
	}
	for i := 0; i < 2; i++ {
		closed := f.request(t, http.MethodPost, "shutdown", strings.NewReader(`{}`), f.headers, http.StatusOK)
		if !strings.Contains(closed.Body.String(), `"state":"closed"`) {
			t.Fatal("shutdown receipt was not closed")
		}
	}
	closed := f.request(t, http.MethodPost, "probe", strings.NewReader(`{}`), f.headers, http.StatusOK)
	if !strings.Contains(closed.Body.String(), `"state":"closed"`) || f.engine.deletes != 1 || f.engine.creates != 1 {
		t.Fatal("closed workspace was recreated or deleted twice")
	}
}

func TestNativeWorkspaceHTTPRenewalChecksAuthorityBeforeBodyAndNeverCreates(t *testing.T) {
	f := newNativeHTTPFixture(t)
	before := f.request(t, http.MethodGet, "lease", nil, f.headers, http.StatusOK)
	var original, renewed struct {
		Data businessruntime.NativeWorkspaceLease `json:"data"`
	}
	if err := json.Unmarshal(before.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	wrong := maps.Clone(f.headers)
	wrong["X-Workspace-Lease-Key"] = strings.Repeat("f", 64)
	f.request(t, http.MethodPost, "lease/renew", strings.NewReader("invalid json"), wrong, http.StatusForbidden)
	after := f.request(t, http.MethodPost, "lease/renew", strings.NewReader(`{}`), f.headers, http.StatusOK)
	if err := json.Unmarshal(after.Body.Bytes(), &renewed); err != nil {
		t.Fatal(err)
	}
	if original.Data.SessionID != renewed.Data.SessionID || original.Data.Generation != renewed.Data.Generation || original.Data.SnapshotVersion != renewed.Data.SnapshotVersion || renewed.Data.LeaseUntil.Before(original.Data.LeaseUntil) {
		t.Fatal("renewal changed the lease identity or lost its deadline")
	}
	if f.engine.creates != 0 || f.engine.deletes != 0 || strings.Contains(after.Body.String(), f.headers["X-Workspace-Lease-Key"]) {
		t.Fatal("renewal operated the engine or disclosed its holder key")
	}
}

func TestNativeWorkspaceHTTPExactBinaryRoundtripAndRecovery(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
	exported := f.request(t, http.MethodPost, "export", strings.NewReader(`{}`), f.headers, http.StatusOK)
	if !bytes.Equal(exported.Body.Bytes(), f.engine.archive.Archive) || exported.Header().Get("X-Workspace-Snapshot-Version") != "0" {
		t.Fatal("export changed archive or advanced durable version")
	}
	headers := maps.Clone(f.headers)
	headers["Content-Type"] = "application/x-tar"
	headers["X-Workspace-Snapshot-Parent"] = "0"
	headers["X-Workspace-Snapshot-Sha256"] = f.engine.archive.SHA256
	savePath := "snapshots/" + strings.Repeat("d", 32)
	saved := f.request(t, http.MethodPut, savePath, bytes.NewReader(exported.Body.Bytes()), headers, http.StatusOK)
	repeated := f.request(t, http.MethodPut, savePath, bytes.NewReader(exported.Body.Bytes()), headers, http.StatusOK)
	if saved.Body.String() != repeated.Body.String() {
		t.Fatal("idempotent save changed receipt")
	}
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 3 || payload.Data["version"] != float64(1) || payload.Data["session_id"] != f.session || payload.Data["sha256"] != f.engine.archive.SHA256 {
		t.Fatalf("invalid save receipt: %#v", payload.Data)
	}
	read := f.request(t, http.MethodGet, "snapshots/1?sha256="+f.engine.archive.SHA256, nil, f.headers, http.StatusOK)
	if !bytes.Equal(read.Body.Bytes(), exported.Body.Bytes()) || read.Header().Get("X-Workspace-Session-ID") != f.session || read.Header().Get("X-Workspace-Snapshot-Sha256") != f.engine.archive.SHA256 || read.Header().Get("Content-Type") != "application/x-tar" || read.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("binary response contract changed")
	}
	f.request(t, http.MethodGet, "snapshot-receipts/"+strings.Repeat("d", 32)+"?parent_version=0&sha256="+f.engine.archive.SHA256, nil, f.headers, http.StatusOK)
	missing := f.request(t, http.MethodGet, "snapshot-receipts/"+strings.Repeat("e", 32)+"?parent_version=0&sha256="+f.engine.archive.SHA256, nil, f.headers, http.StatusOK)
	if !strings.Contains(missing.Body.String(), `"receipt":null`) {
		t.Fatal("missing exact receipt fell back to another snapshot")
	}
	body := `{"request_id":"` + strings.Repeat("e", 32) + `","version":1,"sha256":"` + f.engine.archive.SHA256 + `"}`
	f.request(t, http.MethodPost, "restore", strings.NewReader(body), f.headers, http.StatusOK)
	f.request(t, http.MethodPost, "restore", strings.NewReader(body), f.headers, http.StatusOK)
	if f.engine.creates != 1 || f.engine.hydrates != 1 {
		t.Fatalf("duplicate recovery effects: creates=%d restores=%d", f.engine.creates, f.engine.hydrates)
	}
	for _, secret := range []string{f.engine.handle.ContainerID, f.engine.handle.OwnerHash, f.engine.handle.SessionID, strings.Repeat("a", 64)} {
		if strings.Contains(saved.Body.String(), secret) {
			t.Fatal("private environment identity leaked")
		}
	}
}

type nativeUnreadBody struct{ reads int }

func (b *nativeUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("unauthorized body was read")
}

func TestNativeWorkspaceHTTPRejectsAuthorityBeforeReadingArchive(t *testing.T) {
	f := newNativeHTTPFixture(t)
	for _, name := range []string{"Authorization", "X-Agent-Turn-ID", "X-Workspace-Lease-Key", "X-Agent-Dispatch-Generation", "X-Workspace-Lease-Generation"} {
		t.Run(name, func(t *testing.T) {
			headers := maps.Clone(f.headers)
			headers[name] = "invalid"
			body := &nativeUnreadBody{}
			r := httptest.NewRequest(http.MethodPut, nativeHTTPPrefix+f.session+"/snapshots/"+strings.Repeat("a", 32), body)
			for k, v := range headers {
				r.Header.Set(k, v)
			}
			out := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(out, r)
			if out.Code < 400 || body.reads != 0 {
				t.Fatalf("invalid authority consumed content: status=%d reads=%d", out.Code, body.reads)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, nativeHTTPPrefix+f.session+"/lease", nil)
	for k, v := range f.headers {
		r.Header.Set(k, v)
	}
	r.Header.Add("X-Workspace-Lease-Generation", "1")
	out := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(out, r)
	if out.Code != http.StatusBadRequest {
		t.Fatalf("duplicate generation status=%d", out.Code)
	}
	if f.engine.creates != 0 {
		t.Fatal("private snapshot API implicitly created an environment")
	}
}

func TestNativeWorkspaceHTTPRejectsAmbiguousOrCorruptSnapshot(t *testing.T) {
	f := newNativeHTTPFixture(t)
	headers := maps.Clone(f.headers)
	headers["Content-Type"] = "application/x-tar"
	headers["X-Workspace-Snapshot-Parent"] = "0"
	headers["X-Workspace-Snapshot-Sha256"] = f.engine.archive.SHA256
	path := "snapshots/" + strings.Repeat("d", 32)
	f.request(t, http.MethodPut, path, strings.NewReader("not an archive"), headers, http.StatusBadRequest)
	unconverted := append(bytes.Clone(f.engine.archive.Archive), make([]byte, 1024)...)
	rawHash := sha256.Sum256(unconverted)
	noncanonicalHeaders := maps.Clone(headers)
	noncanonicalHeaders["X-Workspace-Snapshot-Sha256"] = hex.EncodeToString(rawHash[:])
	f.request(t, http.MethodPut, path, bytes.NewReader(unconverted), noncanonicalHeaders, http.StatusBadRequest)
	badType := maps.Clone(headers)
	badType["Content-Type"] = "application/json"
	f.request(t, http.MethodPut, path, bytes.NewReader(f.engine.archive.Archive), badType, http.StatusUnsupportedMediaType)
	badParent := maps.Clone(headers)
	badParent["X-Workspace-Snapshot-Parent"] = "00"
	f.request(t, http.MethodPut, path, bytes.NewReader(f.engine.archive.Archive), badParent, http.StatusBadRequest)
	f.request(t, http.MethodPut, path, bytes.NewReader(f.engine.archive.Archive), headers, http.StatusOK)
	f.request(t, http.MethodGet, "snapshots/2?sha256="+f.engine.archive.SHA256, nil, f.headers, http.StatusNotFound)
	for _, suffix := range []string{"snapshots/01?sha256=" + f.engine.archive.SHA256, "snapshots/1?sha256=" + f.engine.archive.SHA256 + "&sha256=" + f.engine.archive.SHA256, "snapshots/1?sha256=%zz", "snapshot-receipts/" + strings.Repeat("d", 32) + "?parent_version=00&sha256=" + f.engine.archive.SHA256} {
		f.request(t, http.MethodGet, suffix, nil, f.headers, http.StatusBadRequest)
	}
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{"command":"unapproved"}`), f.headers, http.StatusBadRequest)
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{} {}`), f.headers, http.StatusBadRequest)
	if f.engine.creates != 0 {
		t.Fatal("snapshot transport must not execute model commands")
	}
}

func TestNativeWorkspaceHTTPLimitAndMediaChecksPrecedeBodyRead(t *testing.T) {
	f := newNativeHTTPFixture(t)
	for _, mode := range []string{"too-large", "duplicate-media", "encoded"} {
		t.Run(mode, func(t *testing.T) {
			body := &nativeUnreadBody{}
			r := httptest.NewRequest(http.MethodPut, nativeHTTPPrefix+f.session+"/snapshots/"+strings.Repeat("e", 32), body)
			for key, value := range f.headers {
				r.Header.Set(key, value)
			}
			r.Header.Set("Content-Type", "application/x-tar")
			r.Header.Set("X-Workspace-Snapshot-Parent", "0")
			r.Header.Set("X-Workspace-Snapshot-Sha256", f.engine.archive.SHA256)
			want := http.StatusUnsupportedMediaType
			switch mode {
			case "too-large":
				r.ContentLength = nativeWorkspaceArchiveBytes + 1
				want = http.StatusRequestEntityTooLarge
			case "duplicate-media":
				r.Header.Add("Content-Type", "application/json")
			case "encoded":
				r.Header.Add("Content-Encoding", "gzip")
			}
			out := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(out, r)
			if out.Code != want || body.reads != 0 {
				t.Fatalf("invalid transport read body: status=%d want=%d reads=%d", out.Code, want, body.reads)
			}
		})
	}
}

func TestNativeWorkspaceHTTPRequiresCurrentPolicyAndCapsLimits(t *testing.T) {
	t.Run("policy-and-limits", func(t *testing.T) {
		f := newNativeHTTPFixture(t)
		f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusServiceUnavailable)
		if f.engine.creates != 0 {
			t.Fatal("disabled policy created an environment")
		}
		limits := scriptsandbox.DefaultLimits()
		limits.TimeoutSeconds *= 2
		limits.CPUCount /= 2
		f.policy(t, true, &limits)
		f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
		if f.engine.handle.Limits.TimeoutSeconds != scriptsandbox.DefaultLimits().TimeoutSeconds || f.engine.handle.Limits.CPUCount != limits.CPUCount {
			t.Fatal("host cap or stricter administrator limit was lost")
		}
		f.policy(t, false, nil)
		f.request(t, http.MethodPost, "export", strings.NewReader(`{}`), f.headers, http.StatusServiceUnavailable)
		f.request(t, http.MethodGet, "lease", nil, f.headers, http.StatusOK)
	})
	t.Run("revoke-during-create", func(t *testing.T) {
		f := newNativeHTTPFixture(t)
		f.policy(t, true, nil)
		f.engine.onCreate = func() { f.policy(t, false, nil) }
		f.engine.createError = context.Canceled
		f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusServiceUnavailable)
		if f.engine.creates != 1 {
			t.Fatal("unexpected create count")
		}
		f.policy(t, true, nil)
		f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusConflict)
		if f.engine.creates != 1 {
			t.Fatal("revoked unconfirmed create was replayed")
		}
	})
	t.Run("unconfigured-engine", func(t *testing.T) {
		f := newNativeHTTPFixture(t)
		f.policy(t, true, nil)
		f.server.ConfigureNativeWorkspaceEngine(nil)
		f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusServiceUnavailable)
		if f.engine.creates != 0 {
			t.Fatal("unconfigured engine created a workspace")
		}
	})
}
