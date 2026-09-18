package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

type nativeCleanupLog struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (output *nativeCleanupLog) Write(body []byte) (int, error) {
	n, err := output.Buffer.Write(body)
	output.cancel()
	return n, err
}

func TestNativeCleanupWorkerRejectsUnprivilegedAndUnboundedRuns(t *testing.T) {
	store, _, activity, _ := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, ctx := range []context.Context{context.Background(), mcpOwnerContext(), activity} {
		assertDomainCode(t, manager.RunCleanup(ctx, time.Minute, logger), "AGENT_ACTIVITY_FORBIDDEN")
	}
	service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	for _, interval := range []time.Duration{0, time.Nanosecond, 2 * time.Hour} {
		assertDomainCode(t, manager.RunCleanup(service, interval, logger), "NATIVE_WORKSPACE_REQUEST_INVALID")
	}
	ctx, cancel := context.WithCancel(service)
	cancel()
	if err := manager.RunCleanup(ctx, time.Minute, logger); err != nil {
		t.Fatal(err)
	}
	if engine.creates != 0 || engine.deletes != 0 {
		t.Fatal("rejected or cancelled startup changed the environment")
	}
}

func TestNativeCleanupWorkerRunsBoundedSweepAndRetainsUnknownOutcomes(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "unknown"}[unknown], func(t *testing.T) {
			store, _, activity, access := nativeWorkspaceFixture(t, "conversation")
			engine := newNativeEngineFixture(t)
			manager := nativeManagerForTest(t, store, engine)
			if _, err := manager.Ensure(activity, access); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE native_workspace_leases SET lease_until=? WHERE session_id=?`, formatTime(store.now().Add(-time.Minute)), access.SessionID); err != nil {
				t.Fatal(err)
			}
			if unknown {
				engine.deleteError = errors.New("PRIVATE_ENGINE_DETAIL")
			}
			ctx, cancel := context.WithTimeout(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), 10*time.Second)
			defer cancel()
			output := &nativeCleanupLog{cancel: cancel}
			if err := manager.RunCleanup(ctx, time.Hour, slog.New(slog.NewTextHandler(output, nil))); err != nil {
				t.Fatal(err)
			}
			state := nativeEnvironmentForTest(t, store, access).State
			if (state == "closed") == unknown || engine.deletes != 1 || engine.creates != 1 {
				t.Fatalf("cleanup state=%s creates=%d deletes=%d", state, engine.creates, engine.deletes)
			}
			if strings.Contains(output.String(), "PRIVATE_ENGINE_DETAIL") || output.Len() == 0 {
				t.Fatal("cleanup did not emit a bounded, secret-free outcome")
			}
		})
	}
}

func TestNativeCleanupWorkerCancellationDoesNotConfirmDeletion(t *testing.T) {
	store, _, activity, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Ensure(activity, access); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE native_workspace_leases SET lease_until=? WHERE session_id=?`, formatTime(store.now().Add(-time.Minute)), access.SessionID); err != nil {
		t.Fatal(err)
	}
	engine.deleteStarted, engine.deleteRelease = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.RunCleanup(ctx, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	awaitNativeSignal(t, engine.deleteStarted)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not observe server cancellation")
	}
	if nativeEnvironmentForTest(t, store, access).State == "closed" || engine.deletes != 0 {
		t.Fatal("cancelled engine deletion was recorded as confirmed")
	}
}
