package scriptsandbox

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const computerProxyBytes = 32 << 20

// A proxy instance belongs to one execution session. The host must place its
// browser behind enforced egress isolation; this handler alone cannot prevent
// a browser from bypassing the proxy or enforce encrypted HTTP path semantics.
type computerProxy struct {
	policy    *computerEgressPolicy
	digest    [32]byte
	transport http.RoundTripper
	slots     chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
}

func newComputerProxy(policy *computerEgressPolicy, token string) (*computerProxy, error) {
	decoded, err := hex.DecodeString(token)
	if policy == nil || err != nil || len(decoded) != 32 || len(token) != 64 {
		return nil, errors.New("computer proxy requires policy and a session credential")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &computerProxy{policy: policy, digest: sha256.Sum256([]byte("computer:" + token)), ctx: ctx, cancel: cancel, slots: make(chan struct{}, 32),
		transport: &http.Transport{Proxy: nil, DialContext: policy.DialContext, DisableKeepAlives: true,
			ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxConnsPerHost: 8}}, nil
}

func (proxy *computerProxy) Close() {
	proxy.cancel()
	if transport, ok := proxy.transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

func (proxy *computerProxy) authenticated(request *http.Request) bool {
	values := request.Header.Values("Proxy-Authorization")
	if len(values) != 1 || len(values[0]) > 512 || !strings.HasPrefix(values[0], "Basic ") {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(values[0], "Basic "))
	if err != nil {
		return false
	}
	digest := sha256.Sum256(decoded)
	return subtle.ConstantTimeCompare(digest[:], proxy.digest[:]) == 1
}

func (proxy *computerProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if proxy.ctx.Err() != nil {
		http.Error(writer, "Computer session closed", http.StatusServiceUnavailable)
		return
	}
	if !proxy.authenticated(request) {
		writer.Header().Set("Proxy-Authenticate", `Basic realm="computer"`)
		http.Error(writer, "Proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	select {
	case proxy.slots <- struct{}{}:
		defer func() { <-proxy.slots }()
	default:
		http.Error(writer, "Computer proxy busy", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 60*time.Second)
	stop := context.AfterFunc(proxy.ctx, cancel)
	defer func() { stop(); cancel() }()
	if request.Method == http.MethodConnect {
		proxy.tunnel(ctx, writer, request)
		return
	}
	if request.URL.Scheme != "http" || request.URL.Host == "" || request.URL.User != nil || request.URL.Opaque != "" ||
		request.URL.Fragment != "" || !strings.EqualFold(request.Host, request.URL.Host) || request.Header.Get("Upgrade") != "" {
		http.Error(writer, "Unsupported proxy request", http.StatusBadRequest)
		return
	}
	if request.ContentLength > computerProxyBytes {
		http.Error(writer, "Request exceeds limit", http.StatusRequestEntityTooLarge)
		return
	}
	out := request.Clone(ctx)
	out.RequestURI = ""
	out.Header = request.Header.Clone()
	stripComputerProxyHeaders(out.Header)
	out.Trailer = nil
	out.TransferEncoding = nil
	if request.Body != nil && request.Body != http.NoBody {
		out.Body = http.MaxBytesReader(writer, request.Body, computerProxyBytes)
	}
	response, err := proxy.transport.RoundTrip(out)
	if err != nil {
		http.Error(writer, "Computer destination unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.ContentLength > computerProxyBytes {
		http.Error(writer, "Response exceeds limit", http.StatusBadGateway)
		return
	}
	stripComputerProxyHeaders(response.Header)
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	if _, err := io.Copy(writer, io.LimitReader(response.Body, computerProxyBytes)); err != nil {
		panic(http.ErrAbortHandler)
	}
	var extra [1]byte
	if count, err := response.Body.Read(extra[:]); count != 0 || (err != nil && !errors.Is(err, io.EOF)) {
		// Never turn a truncated oversized response into a successful full body.
		panic(http.ErrAbortHandler)
	}
}

func stripComputerProxyHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

func (proxy *computerProxy) tunnel(ctx context.Context, writer http.ResponseWriter, request *http.Request) {
	_, port, parseErr := net.SplitHostPort(request.Host)
	if parseErr != nil || port != "443" || request.URL.Host != request.Host || request.URL.User != nil ||
		request.URL.Path != "" || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.Fragment != "" ||
		request.URL.Opaque != "" || request.URL.Scheme != "" || request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		http.Error(writer, "Invalid tunnel request", http.StatusBadRequest)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "Tunnel unavailable", http.StatusNotImplemented)
		return
	}
	upstream, err := proxy.policy.DialContext(ctx, "tcp", request.Host)
	if err != nil {
		http.Error(writer, "Computer destination unavailable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	// Cancellation must also interrupt the handshake flush, before copy loops start.
	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		_ = upstream.Close()
	})
	defer stop()
	deadline := time.Now().Add(60 * time.Second)
	if upstream.SetDeadline(deadline) != nil || client.SetDeadline(deadline) != nil {
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, io.LimitReader(buffered, computerProxyBytes)); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, io.LimitReader(upstream, computerProxyBytes)); done <- struct{}{} }()
	remaining := 2
	select {
	case <-done:
		remaining--
	case <-ctx.Done():
	}
	_ = client.Close()
	_ = upstream.Close()
	for ; remaining > 0; remaining-- {
		<-done
	}
}
