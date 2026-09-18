package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocalEnvironmentPreservesProcessValues(t *testing.T) {
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("LOCAL_ONLY=from-file\nEXPLICIT_VALUE=from-file\n")
	if err := os.WriteFile(filepath.Join(backend, ".env.local"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("LOCAL_ONLY"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("LOCAL_ONLY") })
	t.Setenv("EXPLICIT_VALUE", "from-process")

	if err := loadLocalEnvironment(root); err != nil {
		t.Fatalf("loadLocalEnvironment() error = %v", err)
	}
	if got := os.Getenv("LOCAL_ONLY"); got != "from-file" {
		t.Fatalf("LOCAL_ONLY = %q", got)
	}
	if got := os.Getenv("EXPLICIT_VALUE"); got != "from-process" {
		t.Fatalf("EXPLICIT_VALUE = %q", got)
	}
}

func TestConfigureInternalEndpointProxyBypass(t *testing.T) {
	t.Setenv("CONTENT_AGENT_CONTROL_MODEL_ENDPOINT", "https://model.internal.example.com/v1/chat/completions")
	t.Setenv("CONTENT_AGENT_CONTENT_MODEL_ENDPOINT", "https://api.example.com/v1/chat/completions")
	t.Setenv("CONTENT_AGENT_MEDIAKIT_ENDPOINT", "https://mediakit.cn-beijing.volces.com")
	t.Setenv("NO_PROXY", "existing.internal")
	t.Setenv("no_proxy", "")

	configureInternalEndpointProxyBypass()
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		value := os.Getenv(name)
		if !strings.Contains(value, "model.internal.example.com") ||
			!strings.Contains(value, "mediakit.cn-beijing.volces.com") ||
			!strings.Contains(value, ".volcvod.com") ||
			strings.Contains(value, "api.example.com") {
			t.Fatalf("%s = %q", name, value)
		}
	}
}

func TestLoadLocalEnvironmentRejectsInvalidName(t *testing.T) {
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backend, ".env.local"), []byte("INVALID-NAME=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadLocalEnvironment(root); err == nil {
		t.Fatal("loadLocalEnvironment() error = nil, want validation error")
	}
}
