package httpapi

import (
	"net/http"
	"strings"
)

// Generation workers have a private workspace, not the parent agent's API scope.
func memoryActivityRouteAllowed(request *http.Request) bool {
	p := strings.Split(request.URL.Path, "/")
	if len(p) < 4 || p[0] != "" || strings.ContainsAny(request.URL.Path, "\\%") {
		return false
	}
	for _, part := range p[1:] {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	method := request.Method
	if len(p) == 5 && p[1] == "api" && p[2] == "v1" && p[3] == "agent-tool-calls" {
		return method == http.MethodGet
	}
	if p[1] != "internal" || p[2] != "v1" {
		return false
	}
	if len(p) == 5 && p[3] == "agent-tools" && p[4] == "catalog" {
		return method == http.MethodGet
	}
	if len(p) == 5 && (p[3] == "agent-instructions" || p[3] == "agent-memory") && p[4] == "snapshot" {
		return method == http.MethodPost
	}
	if p[3] == "agent-tool-calls" {
		if len(p) == 4 {
			return method == http.MethodPost
		}
		if len(p) == 6 && method == http.MethodPost {
			switch p[5] {
			case "start", "complete", "failures", "cancel":
				return true
			}
		}
		return false
	}
	if p[3] != "native-workspaces" {
		return false
	}
	if len(p) == 5 {
		return p[4] == "current" && method == http.MethodGet || p[4] == "reservations" && method == http.MethodPost
	}
	if len(p) == 6 {
		if method == http.MethodGet {
			return p[5] == "lease" || p[5] == "recovery"
		}
		if method == http.MethodPost {
			switch p[5] {
			case "ensure", "probe", "shutdown", "restore", "export", "commands", "files", "pty", "manifest", "memory-publications":
				return true
			}
		}
	}
	if len(p) == 7 {
		switch p[5] {
		case "lease":
			return p[6] == "renew" && method == http.MethodPost
		case "pty":
			return p[6] == "state" && method == http.MethodGet || p[6] == "terminate" && method == http.MethodPost
		case "manifest":
			return (p[6] == "file" || p[6] == "seal") && method == http.MethodPost
		case "snapshots":
			return method == http.MethodGet || method == http.MethodPut
		case "snapshot-receipts":
			return method == http.MethodGet
		}
	}
	return false
}
