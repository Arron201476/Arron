package scriptsandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// The isolation owner supplies a listener reachable only from the owned browser
// network. This component does not create or certify that network boundary.
type computerProxyServer struct {
	proxy    *computerProxy
	server   *http.Server
	config   computerProxyConfig
	done     chan struct{}
	once     sync.Once
	err      error
	closeErr error
}

func startComputerProxyServer(ctx context.Context, listener net.Listener, hosts []string) (*computerProxyServer, error) {
	if listener == nil {
		return nil, errors.New("computer proxy listener unavailable")
	}
	transferred := false
	defer func() {
		if !transferred {
			_ = listener.Close()
		}
	}()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address == nil || address.Zone != "" {
		return nil, errors.New("computer proxy requires an explicit TCP address")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, errors.New("computer proxy credential generation failed")
	}
	config := computerProxyConfig{Host: address.IP.String(), Port: address.Port, Token: hex.EncodeToString(secret[:])}
	if !config.valid() {
		return nil, errors.New("computer proxy requires a concrete listener address")
	}
	policy, err := newComputerEgressPolicy(hosts)
	if err != nil {
		return nil, err
	}
	proxy, err := newComputerProxy(policy, config.Token)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 10 * time.Second,
		MaxHeaderBytes: 64 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	owner := &computerProxyServer{proxy: proxy, server: server, config: config, done: make(chan struct{})}
	stop := context.AfterFunc(ctx, owner.shutdown)
	transferred = true
	go func() {
		defer close(owner.done)
		defer stop()
		defer owner.shutdown()
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			owner.err = errors.New("computer proxy listener failed")
		}
	}()
	return owner, nil
}

func (owner *computerProxyServer) configuration() (computerProxyConfig, error) {
	if owner.proxy.ctx.Err() != nil {
		return computerProxyConfig{}, errors.New("computer proxy session closed")
	}
	return owner.config, nil
}

func (owner *computerProxyServer) shutdown() {
	owner.once.Do(func() {
		owner.proxy.Close()
		if err := owner.server.Close(); err != nil {
			owner.closeErr = errors.New("computer proxy connection cleanup unconfirmed")
		}
	})
}

func (owner *computerProxyServer) Close() error {
	owner.shutdown()
	<-owner.done
	return errors.Join(owner.err, owner.closeErr)
}
