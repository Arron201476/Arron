package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
)

func nativeWorkspaceEngineFromEnvironment(sandbox scriptsandbox.Sandbox) (businessruntime.NativeWorkspaceEngine, error) {
	enabled, err := booleanEnvironment("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", false)
	if err != nil || !enabled {
		return nil, err
	}
	engine, ok := sandbox.(*scriptsandbox.OCI)
	if !ok || engine == nil {
		return nil, errors.New("native workspace requires the configured isolated OCI adapter; no host fallback is allowed")
	}
	return engine, nil
}

func startNativeWorkspaceCleanup(ctx context.Context, manager *businessruntime.NativeWorkspaceManager, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	if manager == nil {
		close(done)
		return done
	}
	if logger == nil {
		logger = slog.Default()
	}
	principal := identity.Principal{Kind: identity.KindService, DisplayName: "Workspace cleanup", AuthMethod: "host_lifecycle"}
	ctx = identity.WithPrincipal(ctx, principal)
	go func() {
		defer close(done)
		if err := manager.RunCleanup(ctx, time.Minute, logger); err != nil {
			logger.Error("native workspace cleanup stopped unexpectedly")
		}
	}()
	return done
}

func waitNativeWorkspaceCleanup(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
