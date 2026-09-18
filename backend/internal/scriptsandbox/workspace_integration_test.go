package scriptsandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestOCIIntegrationNativeWorkspaceSnapshotAndBackgroundResume(t *testing.T) {
	sandbox := integrationSandbox(t)
	newWorkspace := func() WorkspaceHandle {
		t.Helper()
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal(err)
		}
		session := fmt.Sprintf("%x-%x-%x-%x-%x", random[:4], random[4:6], random[6:8], random[8:10], random[10:])
		handle, err := sandbox.CreateWorkspace(context.Background(), session, strings.Repeat("b", 64), DefaultLimits())
		if handle.ContainerID != "" {
			t.Cleanup(func() {
				if err := sandbox.DeleteWorkspace(context.Background(), handle); err != nil {
					t.Errorf("integration workspace cleanup: %v", err)
				}
			})
		}
		if err != nil {
			t.Fatal(err)
		}
		return handle
	}
	handle := newWorkspace()
	command := `import pathlib, subprocess, sys
pathlib.Path('nested').mkdir()
pathlib.Path('empty').mkdir()
pathlib.Path('nested/binary.bin').write_bytes(sys.stdin.buffer.read())
child = subprocess.Popen(['python', '-I', '-B', '-c', 'import time; time.sleep(60)'],
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
print(child.pid)
`
	body := []byte{0, 255, 128, 10}
	result, err := sandbox.ExecuteWorkspace(context.Background(), handle, WorkspaceCommand{Argv: []string{"python", "-I", "-B", "-c", command}, Cwd: workspaceRoot, Stdin: body})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("workspace write: %v, %+v", err, result)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(result.Stdout)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid test child pid: %v", err)
	}
	snapshot, err := sandbox.ExportWorkspace(context.Background(), handle)
	if err != nil || snapshot.SizeBytes != int64(len(body)) || len(snapshot.Entries) != 3 {
		t.Fatalf("export: %v, %+v", err, snapshot)
	}
	stateProbe := "from pathlib import Path; import sys; print(Path('/proc', sys.argv[1], 'stat').read_text().rsplit(')', 1)[1].split()[0])"
	resumed, err := sandbox.ExecuteWorkspace(context.Background(), handle, WorkspaceCommand{Argv: []string{"python", "-I", "-B", "-c", stateProbe, strconv.Itoa(pid)}, Cwd: workspaceRoot})
	if err != nil || resumed.ExitCode != 0 || strings.TrimSpace(string(resumed.Stdout)) != "S" {
		t.Fatalf("snapshot did not resume child: %v, %+v", err, resumed)
	}
	restored := newWorkspace()
	for range 2 {
		actual, err := sandbox.HydrateWorkspace(context.Background(), restored, snapshot.Archive, snapshot.SHA256)
		if err != nil || actual.SHA256 != snapshot.SHA256 {
			t.Fatalf("restore: %v", err)
		}
	}
	read, err := sandbox.ExecuteWorkspace(context.Background(), restored, WorkspaceCommand{Argv: []string{"python", "-I", "-B", "-c", "from pathlib import Path; import sys; sys.stdout.buffer.write(Path('nested/binary.bin').read_bytes())"}, Cwd: workspaceRoot})
	if err != nil || read.ExitCode != 0 || !bytes.Equal(read.Stdout, body) {
		t.Fatalf("binary round trip: %v", err)
	}
}
