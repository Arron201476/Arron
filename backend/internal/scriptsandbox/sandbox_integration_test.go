package scriptsandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOCIIntegrationBlocksNetworkCredentialsAndSkillWrites(t *testing.T) {
	sandbox := integrationSandbox(t)
	t.Setenv("SCRIPT_SANDBOX_SECRET_PROBE", "host-secret-must-not-leak")
	result, outputRoot, err := runIntegrationScript(t, sandbox, "integration_isolation", `
import json, os, pathlib, socket, sys
output = pathlib.Path(sys.argv[2])
checks = {"credential_visible": os.getenv("SCRIPT_SANDBOX_SECRET_PROBE")}
try:
    pathlib.Path("/skill/escape.txt").write_text("unsafe", encoding="utf-8")
    checks["skill_read_only"] = False
except OSError:
    checks["skill_read_only"] = True
probe = socket.socket()
probe.settimeout(1)
try:
    probe.connect(("1.1.1.1", 53))
    checks["network_blocked"] = False
except OSError:
    checks["network_blocked"] = True
finally:
    probe.close()
(output / "checks.json").write_text(json.dumps(checks), encoding="utf-8")
`, DefaultLimits())
	if err != nil {
		t.Fatalf("sandbox execution failed: %v; result=%+v", err, result)
	}
	data, err := os.ReadFile(filepath.Join(outputRoot, "checks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var checks map[string]any
	if err := json.Unmarshal(data, &checks); err != nil {
		t.Fatal(err)
	}
	if checks["credential_visible"] != nil || checks["skill_read_only"] != true || checks["network_blocked"] != true {
		t.Fatalf("isolation checks = %#v", checks)
	}
}

func TestOCIIntegrationEnforcesProcessAndTimeLimits(t *testing.T) {
	sandbox := integrationSandbox(t)
	limits := DefaultLimits()
	limits.ProcessCount = 16
	result, outputRoot, err := runIntegrationScript(t, sandbox, "integration_processes", `
import json, pathlib, subprocess, sys
children = []
for _ in range(64):
    try:
        children.append(subprocess.Popen(["python", "-c", "import time; time.sleep(10)"]))
    except OSError:
        break
(pathlib.Path(sys.argv[2]) / "processes.json").write_text(json.dumps({"started": len(children)}), encoding="utf-8")
for child in children:
    child.terminate()
for child in children:
    child.wait()
`, limits)
	if err != nil {
		t.Fatalf("process-limit script failed: %v; result=%+v", err, result)
	}
	data, err := os.ReadFile(filepath.Join(outputRoot, "processes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Started int `json:"started"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Started >= 64 {
		t.Fatalf("process limit was not enforced: %+v", payload)
	}

	limits = DefaultLimits()
	limits.TimeoutSeconds = 1
	result, _, err = runIntegrationScript(t, sandbox, "integration_timeout", `
import time
time.sleep(10)
`, limits)
	assertSandboxCode(t, err, "SKILL_SCRIPT_TIMEOUT")
}

func TestOCIIntegrationRejectsLinkedOutput(t *testing.T) {
	sandbox := integrationSandbox(t)
	result, _, err := runIntegrationScript(t, sandbox, "integration_link", `
import os, pathlib, sys
os.symlink("/etc/passwd", pathlib.Path(sys.argv[2]) / "credential-link")
`, DefaultLimits())
	assertSandboxCode(t, err, "SKILL_SCRIPT_OUTPUT_ENTRY_UNSAFE")
	if len(result.Artifacts) != 0 {
		t.Fatalf("unsafe output produced artifacts: %+v", result.Artifacts)
	}
}

func integrationSandbox(t *testing.T) *OCI {
	t.Helper()
	if os.Getenv("CONTENT_AGENT_SCRIPT_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set CONTENT_AGENT_SCRIPT_SANDBOX_INTEGRATION=1 with a preloaded digest-pinned image")
	}
	configured, err := NewFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	sandbox, ok := configured.(*OCI)
	if !ok || !sandbox.Status().Available {
		t.Fatalf("OCI sandbox is unavailable: %+v", configured.Status())
	}
	return sandbox
}

func runIntegrationScript(
	t *testing.T,
	sandbox *OCI,
	executionID string,
	source string,
	limits Limits,
) (Result, string, error) {
	t.Helper()
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skill")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "scripts", "probe.py"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "output")
	result, err := sandbox.Execute(context.Background(), Request{
		ExecutionID: executionID,
		SkillRoot:   skillRoot,
		ScriptPath:  "scripts/probe.py",
		Runtime:     "python",
		Input:       json.RawMessage(`{}`),
		WorkRoot:    filepath.Join(root, "work"),
		OutputRoot:  outputRoot,
		Limits:      limits,
	})
	return result, outputRoot, err
}
