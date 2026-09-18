package scriptsandbox

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type computerListenerFixture struct {
	address   net.Addr
	closed    chan struct{}
	once      sync.Once
	acceptErr error
}

func (l *computerListenerFixture) Addr() net.Addr { return l.address }
func (l *computerListenerFixture) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *computerListenerFixture) Accept() (net.Conn, error) {
	if l.acceptErr != nil {
		return nil, l.acceptErr
	}
	<-l.closed
	return nil, net.ErrClosed
}

func newComputerListenerFixture(host string) *computerListenerFixture {
	return &computerListenerFixture{address: &net.TCPAddr{IP: net.ParseIP(host), Port: 3128}, closed: make(chan struct{})}
}

func TestComputerProxyServerRejectsWildcardAndReleasesListener(t *testing.T) {
	listener := newComputerListenerFixture("0.0.0.0")
	if _, err := startComputerProxyServer(context.Background(), listener, []string{"example.test"}); err == nil {
		t.Fatal("wildcard listener accepted")
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("invalid listener leaked")
	}
}

func TestComputerProxyServerCancellationRevokesConfigAndClosesListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := newComputerListenerFixture("172.30.0.1")
	owner, err := startComputerProxyServer(ctx, listener, []string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	config, err := owner.configuration()
	if err != nil || !config.valid() {
		t.Fatal("missing session config")
	}
	cancel()
	select {
	case <-owner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not stop")
	}
	if _, err := owner.configuration(); err == nil {
		t.Fatal("closed proxy issued config")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestComputerProxyServerGeneratesSeparateSessionCredentials(t *testing.T) {
	first, err := startComputerProxyServer(context.Background(), newComputerListenerFixture("172.30.0.1"), []string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := startComputerProxyServer(context.Background(), newComputerListenerFixture("172.30.0.1"), []string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.config.Token == second.config.Token {
		t.Fatal("credential reused")
	}
}

func TestComputerProxyServerListenerFailureClosesSessionWithoutRawError(t *testing.T) {
	listener := newComputerListenerFixture("172.30.0.1")
	listener.acceptErr = errors.New("private listener details")
	owner, err := startComputerProxyServer(context.Background(), listener, []string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("failure did not terminate proxy")
	}
	if _, err := owner.configuration(); err == nil {
		t.Fatal("failed proxy remained usable")
	}
	if err := owner.Close(); err == nil || err.Error() != "computer proxy listener failed" {
		t.Fatal("listener failure was lost or exposed details")
	}
}
