package scriptsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type computerOwnedStreamFixture struct {
	closed         chan struct{}
	once           sync.Once
	startupFailure bool
}

func (f *computerOwnedStreamFixture) Close() error { f.once.Do(func() { close(f.closed) }); return nil }
func (f *computerOwnedStreamFixture) Exchange(_ context.Context, raw []byte) ([]byte, error) {
	var request struct {
		ComputerBinding
		Operation string              `json:"operation"`
		Sequence  int                 `json:"sequence"`
		Proxy     computerProxyConfig `json:"proxy"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Operation == "start" {
		if f.startupFailure || !request.Proxy.valid() {
			return nil, errors.New("startup failed")
		}
		return json.Marshal(computerReceipt{SessionID: request.SessionID, Generation: request.Generation,
			Sequence: 0, Status: "ready", Width: request.Width, Height: request.Height})
	}
	return json.Marshal(computerReceipt{SessionID: request.SessionID, Generation: request.Generation,
		Sequence: request.Sequence, Status: "completed"})
}

func TestComputerSessionOwnsStartupConfigurationActionsAndCleanup(t *testing.T) {
	stream := &computerOwnedStreamFixture{closed: make(chan struct{})}
	listener := newComputerListenerFixture("172.30.0.1")
	session, err := startComputerSession(context.Background(), stream, listener,
		ComputerBinding{"s", 1, 100, 100}, "https://example.test/", []string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	receipt, err := session.Execute(context.Background(), json.RawMessage(`{"type":"wait"}`))
	if err != nil || receipt.Sequence != 1 || receipt.SessionID != "s" {
		t.Fatal("action binding lost")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("stream leaked")
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("listener leaked")
	}
	if _, err := session.Execute(context.Background(), json.RawMessage(`{"type":"wait"}`)); err == nil {
		t.Fatal("closed session accepted action")
	}
}

func TestComputerSessionStartupFailureClosesBothResources(t *testing.T) {
	stream := &computerOwnedStreamFixture{closed: make(chan struct{}), startupFailure: true}
	listener := newComputerListenerFixture("172.30.0.1")
	if _, err := startComputerSession(context.Background(), stream, listener,
		ComputerBinding{"s", 1, 100, 100}, "https://example.test/", []string{"example.test"}); err == nil {
		t.Fatal("startup failure ignored")
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("failed stream leaked")
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("failed proxy leaked")
	}
}

func TestComputerSessionParentCancellationClosesIdleTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &computerOwnedStreamFixture{closed: make(chan struct{})}
	session, err := startComputerSession(ctx, stream, newComputerListenerFixture("172.30.0.1"),
		ComputerBinding{"s", 1, 100, 100}, "https://example.test/", []string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	cancel()
	select {
	case <-stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("idle transport survived cancellation")
	}
}
