package scriptsandbox

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type computerHandshakeConn struct {
	net.Conn
	writing chan struct{}
}

func (c *computerHandshakeConn) Write(data []byte) (int, error) {
	select {
	case c.writing <- struct{}{}:
	default:
	}
	return c.Conn.Write(data)
}

type computerHijackFixture struct {
	*httptest.ResponseRecorder
	connection net.Conn
}

func (w *computerHijackFixture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.connection, bufio.NewReadWriter(bufio.NewReader(w.connection), bufio.NewWriter(w.connection)), nil
}

func TestComputerProxyStopInterruptsBlockedTunnelHandshake(t *testing.T) {
	proxy := proxyFixture(t)
	policy, err := newComputerEgressPolicy([]string{"1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	upstream, remote := net.Pipe()
	t.Cleanup(func() { upstream.Close(); remote.Close() })
	policy.dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	proxy.policy = policy
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	connection := &computerHandshakeConn{Conn: client, writing: make(chan struct{}, 1)}
	request := httptest.NewRequest(http.MethodConnect, "http://example.test/", nil)
	request.Host = "1.1.1.1:443"
	request.URL = &url.URL{Host: request.Host}
	authorizeProxyFixture(request)
	writer := &computerHijackFixture{ResponseRecorder: httptest.NewRecorder(), connection: connection}
	done := make(chan struct{})
	go func() { defer close(done); proxy.ServeHTTP(writer, request) }()
	select {
	case <-connection.writing:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel handshake did not start")
	}
	proxy.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session stop did not interrupt handshake")
	}
}

type computerRoundTripFixture func(*http.Request) (*http.Response, error)

func (f computerRoundTripFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func proxyFixture(t *testing.T) *computerProxy {
	t.Helper()
	policy, err := newComputerEgressPolicy([]string{"example.test"})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newComputerProxy(policy, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Close)
	return proxy
}

func authorizeProxyFixture(request *http.Request) {
	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("computer:"+strings.Repeat("a", 64))))
}

func TestComputerProxyRequiresSessionCredentialBeforeForwarding(t *testing.T) {
	proxy := proxyFixture(t)
	proxy.transport = computerRoundTripFixture(func(*http.Request) (*http.Response, error) {
		t.Fatal("unauthorized request forwarded")
		return nil, nil
	})
	for _, value := range []string{"", "Bearer anything", "Basic invalid", "Basic " + base64.StdEncoding.EncodeToString([]byte("computer:wrong"))} {
		request := httptest.NewRequest("GET", "http://example.test/", nil)
		request.Header.Set("Proxy-Authorization", value)
		writer := httptest.NewRecorder()
		proxy.ServeHTTP(writer, request)
		if writer.Code != http.StatusProxyAuthRequired {
			t.Fatalf("auth bypass: %d", writer.Code)
		}
	}
}

func TestComputerProxyStripsCredentialsAndHopHeadersWithoutFollowingRedirect(t *testing.T) {
	proxy := proxyFixture(t)
	count := 0
	proxy.transport = computerRoundTripFixture(func(r *http.Request) (*http.Response, error) {
		count++
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Private-Hop") != "" || r.RequestURI != "" {
			t.Fatal("proxy metadata leaked upstream")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://foreign.test/"}, "Proxy-Authenticate": []string{"secret"}}, Body: io.NopCloser(strings.NewReader("")), ContentLength: 0}, nil
	})
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	authorizeProxyFixture(request)
	request.Header.Set("Connection", "X-Private-Hop")
	request.Header.Set("X-Private-Hop", "secret")
	writer := httptest.NewRecorder()
	proxy.ServeHTTP(writer, request)
	if writer.Code != 302 || count != 1 || writer.Header().Get("Proxy-Authenticate") != "" {
		t.Fatal("redirect followed or proxy header exposed")
	}
}

func TestComputerProxyRejectsCrossHostAndClosedSessions(t *testing.T) {
	proxy := proxyFixture(t)
	proxy.transport = computerRoundTripFixture(func(*http.Request) (*http.Response, error) { t.Fatal("invalid request forwarded"); return nil, nil })
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	authorizeProxyFixture(request)
	request.Host = "foreign.test"
	writer := httptest.NewRecorder()
	proxy.ServeHTTP(writer, request)
	if writer.Code != http.StatusBadRequest {
		t.Fatal("mismatched Host accepted")
	}
	proxy.Close()
	writer = httptest.NewRecorder()
	proxy.ServeHTTP(writer, request)
	if writer.Code != http.StatusServiceUnavailable {
		t.Fatal("closed session accepted request")
	}
}

func TestComputerProxyRejectsTunnelFramingBeforeDial(t *testing.T) {
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.ContentLength = -1 },
		func(r *http.Request) { r.ContentLength = 1 },
		func(r *http.Request) { r.TransferEncoding = []string{"chunked"} },
		func(r *http.Request) { r.URL.RawQuery = "x=1" },
		func(r *http.Request) { r.URL.ForceQuery = true },
		func(r *http.Request) { r.URL.Fragment = "fragment" },
		func(r *http.Request) { r.URL.Scheme = "https" },
		func(r *http.Request) { r.URL.Opaque = "opaque" },
	} {
		proxy := proxyFixture(t)
		request := httptest.NewRequest(http.MethodConnect, "http://example.test/", nil)
		request.Host = "example.test:443"
		request.URL = &url.URL{Host: request.Host}
		authorizeProxyFixture(request)
		mutate(request)
		writer := httptest.NewRecorder()
		proxy.ServeHTTP(writer, request)
		if writer.Code != http.StatusBadRequest {
			t.Fatalf("invalid tunnel reached hijack/dial: %d", writer.Code)
		}
	}
}

func TestComputerProxyBoundsConcurrentRequests(t *testing.T) {
	proxy := proxyFixture(t)
	for i := 0; i < cap(proxy.slots); i++ {
		proxy.slots <- struct{}{}
	}
	proxy.transport = computerRoundTripFixture(func(*http.Request) (*http.Response, error) {
		t.Fatal("saturated proxy forwarded request")
		return nil, nil
	})
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	authorizeProxyFixture(request)
	writer := httptest.NewRecorder()
	proxy.ServeHTTP(writer, request)
	if writer.Code != http.StatusTooManyRequests {
		t.Fatalf("missing concurrency limit: %d", writer.Code)
	}
}
