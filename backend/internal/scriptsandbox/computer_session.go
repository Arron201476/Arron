package scriptsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
)

// This owner joins transport and proxy lifetimes, not authorization. The Runtime
// must supply an isolated process/listener and persist approval before Execute.
// Close is not evidence that the externally owned container has been removed.
type computerSession struct {
	protocol *computerProtocol
	proxy    *computerProxyServer
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	closeErr error
}

func startComputerSession(ctx context.Context, stream workspacePTYStream, listener net.Listener,
	binding ComputerBinding, initialURL string, hosts []string) (*computerSession, error) {
	if stream == nil {
		if listener != nil {
			_ = listener.Close()
		}
		return nil, errors.New("computer stream unavailable")
	}
	owned, cancel := context.WithCancel(ctx)
	proxy, err := startComputerProxyServer(owned, listener, hosts)
	if err != nil {
		cancel()
		return nil, errors.Join(err, stream.Close())
	}
	config, err := proxy.configuration()
	if err != nil {
		cancel()
		return nil, errors.Join(err, proxy.Close(), stream.Close())
	}
	protocol, err := startComputerProtocol(owned, stream, binding, initialURL, config)
	if err != nil {
		cancel()
		return nil, errors.Join(err, proxy.Close())
	}
	session := &computerSession{protocol: protocol, proxy: proxy, ctx: owned, cancel: cancel}
	go func() {
		// Listener failure also revokes the browser transport, even without an action.
		<-proxy.proxy.ctx.Done()
		_ = session.Close()
	}()
	if owned.Err() != nil || proxy.proxy.ctx.Err() != nil {
		return nil, errors.Join(errors.New("computer session stopped during startup"), session.Close())
	}
	return session, nil
}

func (session *computerSession) Execute(ctx context.Context, action json.RawMessage) (computerReceipt, error) {
	if session.ctx.Err() != nil {
		return computerReceipt{}, errors.New("computer session closed")
	}
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()
	receipt, err := session.protocol.Execute(call, action)
	if err == nil && (call.Err() != nil || session.proxy.proxy.ctx.Err() != nil) {
		err = errors.New("computer action unconfirmed after session stop")
	}
	if err != nil {
		return computerReceipt{}, errors.Join(err, session.Close())
	}
	return receipt, nil
}

func (session *computerSession) Close() error {
	session.once.Do(func() {
		session.cancel()
		// Revoke egress before waiting for the process transport to stop.
		proxyErr := session.proxy.Close()
		session.closeErr = errors.Join(proxyErr, session.protocol.Close())
	})
	return session.closeErr
}
