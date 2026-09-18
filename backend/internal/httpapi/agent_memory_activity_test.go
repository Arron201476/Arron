package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemoryActivityHeadersRejectAmbiguousIdentity(t *testing.T) {
	valid := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/agent-tool-calls", nil)
		r.Header.Set("X-Agent-Project-ID", "project")
		r.Header.Set("X-Agent-Memory-Generation-ID", "generation")
		r.Header.Set("X-Agent-Memory-Generation-Attempt", "2")
		r.Header.Set("X-Agent-Attempt-Token", "token")
		return r
	}
	activity, present := activityHeaders(valid())
	if !present || activity.MemoryGenerationID != "generation" || activity.MemoryGenerationAttempt != 2 || activity.AttemptToken != "token" {
		t.Fatalf("identity: %+v %v", activity, present)
	}
	for _, value := range []string{"", "0", "-1", "02", "+2", "2.0", " 2", "2,3", "999999999999999999999999999999999"} {
		r := valid()
		r.Header.Set("X-Agent-Memory-Generation-Attempt", value)
		a, present := activityHeaders(r)
		if !present || a.MemoryGenerationAttempt != -1 {
			t.Fatalf("accepted noncanonical attempt %q", value)
		}
	}
	for _, name := range []string{"X-Agent-Project-ID", "X-Agent-Memory-Generation-ID", "X-Agent-Memory-Generation-Attempt", "X-Agent-Attempt-Token"} {
		for _, duplicate := range []bool{false, true} {
			r := valid()
			if duplicate {
				r.Header.Add(name, r.Header.Get(name))
			} else {
				r.Header.Del(name)
			}
			a, present := activityHeaders(r)
			if !present || a.MemoryGenerationAttempt != -1 {
				t.Fatalf("accepted missing/duplicate %s", name)
			}
		}
	}
	for _, name := range []string{"X-Agent-Turn-ID", "X-Agent-Task-Attempt-ID", "X-Agent-Execution-Attempt-ID"} {
		r := valid()
		r.Header.Set(name, "")
		a, _ := activityHeaders(r)
		if a.MemoryGenerationAttempt != -1 {
			t.Fatalf("accepted mixed identity header %s", name)
		}
	}
}

func TestMemoryActivityRoutesRemainPrivateAndMethodBound(t *testing.T) {
	for _, item := range []struct{ method, path string }{
		{"POST", "/internal/v1/agent-tool-calls"},
		{"GET", "/internal/v1/agent-tools/catalog"},
		{"POST", "/internal/v1/agent-tool-calls/call/start"},
		{"POST", "/internal/v1/agent-tool-calls/call/complete"},
		{"GET", "/api/v1/agent-tool-calls/call"},
		{"POST", "/internal/v1/agent-instructions/snapshot"},
		{"POST", "/internal/v1/agent-memory/snapshot"},
		{"POST", "/internal/v1/native-workspaces/reservations"},
		{"GET", "/internal/v1/native-workspaces/session/lease"},
		{"POST", "/internal/v1/native-workspaces/session/commands"},
		{"POST", "/internal/v1/native-workspaces/session/memory-publications"},
		{"POST", "/internal/v1/native-workspaces/session/manifest/file"},
		{"PUT", "/internal/v1/native-workspaces/session/snapshots/request"},
		{"GET", "/internal/v1/native-workspaces/session/snapshot-receipts/request"},
	} {
		if !memoryActivityRouteAllowed(httptest.NewRequest(item.method, item.path, nil)) {
			t.Errorf("denied %s %s", item.method, item.path)
		}
		if memoryActivityRouteAllowed(httptest.NewRequest(http.MethodDelete, item.path, nil)) {
			t.Errorf("allowed deletion %s", item.path)
		}
	}
	for _, path := range []string{
		"/api/v1/projects/project", "/api/v1/agent-tool-calls/call/memory-tool-proposal",
		"/api/v1/agent-tool-approvals/approval/resolutions", "/api/v1/agent-tools/configuration",
		"/internal/v1/agent/turn-commits", "/internal/v1/agent-memory/rollouts",
		"/internal/v1/agent-memory/generations/claim", "/internal/v1/agent-tool-calls/call/outputs",
		"/internal/v1/agent-tool-calls/call/instructions", "/internal/v1/agent-tool-calls/call/skill-installations",
		"/internal/v1/native-workspaces/session/publications",
		"/internal/v1/native-workspaces/session/commands/", "/internal/v1/native-workspaces//commands",
		"/internal/v1/native-workspaces/../commands", "/internal/v1/native-workspaces/%2e%2e/commands",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
			if memoryActivityRouteAllowed(httptest.NewRequest(method, path, nil)) {
				t.Errorf("allowed %s %s", method, path)
			}
		}
	}
}
