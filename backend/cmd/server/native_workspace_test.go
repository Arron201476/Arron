package main

import (
	"context"
	"testing"

	"content-agent/backend/internal/scriptsandbox"
)

func TestNativeWorkspaceStartupIsExplicitAndReusesOnlyOCI(t *testing.T) {
	for _, value := range []string{"", "false", "0"} {
		t.Setenv("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", value)
		engine, err := nativeWorkspaceEngineFromEnvironment(nil)
		if engine != nil || err != nil {
			t.Fatalf("disabled configuration touched the adapter: %v %v", engine, err)
		}
	}
	t.Setenv("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", "sometimes")
	if engine, err := nativeWorkspaceEngineFromEnvironment(nil); err == nil || engine != nil {
		t.Fatal("invalid boolean was accepted")
	}
	t.Setenv("CONTENT_AGENT_NATIVE_WORKSPACE_ENABLED", "true")
	var missingOCI *scriptsandbox.OCI
	for _, sandbox := range []scriptsandbox.Sandbox{nil, missingOCI, scriptsandbox.Disabled{}} {
		if engine, err := nativeWorkspaceEngineFromEnvironment(sandbox); err == nil || engine != nil {
			t.Fatal("enabled native workspace accepted an absent isolated adapter")
		}
	}
	// Configuration only: never call or execute this zero-value engine fixture.
	configured := &scriptsandbox.OCI{}
	engine, err := nativeWorkspaceEngineFromEnvironment(configured)
	if err != nil || engine != configured {
		t.Fatalf("configured engine was replaced: %v %v", engine, err)
	}
}

func TestNativeWorkspaceCleanupDisabledAndShutdownWait(t *testing.T) {
	done := startNativeWorkspaceCleanup(context.Background(), nil, nil)
	if err := waitNativeWorkspaceCleanup(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitNativeWorkspaceCleanup(ctx, make(chan struct{})); err != context.Canceled {
		t.Fatalf("unfinished cleanup was treated as confirmed: %v", err)
	}
}
