package scriptsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestNativeWorkspaceSnapshotPythonArchiveRoundTrip(t *testing.T) {
	python := os.Getenv("CONTENT_AGENT_TEST_PYTHON")
	if python == "" {
		t.Skip("set CONTENT_AGENT_TEST_PYTHON to run the isolated real Python/Go archive test")
	}
	if !filepath.IsAbs(python) {
		t.Fatal("test Python path must be absolute")
	}
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	for _, dir := range []string{source, destination, filepath.Join(source, "nested"), filepath.Join(source, "empty")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte{0, 255, 128, 1, 10}
	filename := filepath.Join(source, "nested", "\u4e2d\u6587.bin")
	if err := os.WriteFile(filename, body, 0o640); err != nil {
		t.Fatal(err)
	}
	// Execute only the pure archive functions against this test directory. The
	// production main/process scanner/signalling code must never run on the host.
	bootstrap := "scope = {'__name__': 'workspace_archive_fixture'}\nexec(" + strconv.Quote(workspaceTransferHelper) + ", scope)\n"
	bootstrap += `import json, sys
from pathlib import Path
root, operation = Path(sys.argv[1]), sys.argv[2]
if operation == 'hydrate':
    plan = json.loads(sys.stdin.buffer.readline())
    scope['hydrate_archive'](root, sys.stdin.buffer.read(), plan['entries'], 16777216)
listing = scope['entries'](root, 16777216)
scope['export_archive'](root, listing, sys.stdout.buffer)
`
	run := func(operation, dir string, input io.Reader) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), engineOperationTimeout)
		defer cancel()
		command := exec.CommandContext(ctx, python, "-I", "-B", "-c", bootstrap, dir, operation)
		command.Env = minimalEngineEnvironment()
		command.Stdin = input
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			t.Fatalf("archive fixture failed: %v\n%s", err, stderr.String())
		}
		return output
	}
	first, err := ParseWorkspaceSnapshot(run("export", source, nil), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := json.Marshal(struct {
		Entries []WorkspaceEntry `json:"entries"`
	}{first.Entries})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		input := io.MultiReader(bytes.NewReader(append(bytes.Clone(plan), '\n')), bytes.NewReader(first.Archive))
		restored, err := ParseWorkspaceSnapshot(run("hydrate", destination, input), DefaultLimits())
		if err != nil || !reflect.DeepEqual(first, restored) {
			t.Fatalf("Python/Go round trip mismatch: %v", err)
		}
	}
	actual, err := os.ReadFile(filepath.Join(destination, "nested", "\u4e2d\u6587.bin"))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("binary contents changed: %v", err)
	}
}
