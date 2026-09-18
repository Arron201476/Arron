package scriptsandbox

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
)

func TestOCIIntegrationWorkspacePTYInputInterruptAndSnapshot(t *testing.T) {
	sandbox := integrationSandbox(t)
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("%x-%x-%x-%x-%x", random[:4], random[4:6], random[6:8], random[8:10], random[10:])
	handle, err := sandbox.CreateWorkspace(context.Background(), session, strings.Repeat("b", 64), DefaultLimits())
	if handle.ContainerID != "" {
		t.Cleanup(func() {
			if err := sandbox.DeleteWorkspace(context.Background(), handle); err != nil {
				t.Errorf("PTY integration cleanup: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	start := func(id, source string, tty bool) WorkspacePTYResult {
		t.Helper()
		result, err := sandbox.StartWorkspacePTY(context.Background(), handle, id, WorkspacePTYStart{
			Command: WorkspaceCommand{Argv: []string{"python", "-I", "-B", "-u", "-c", source}, Cwd: workspaceRoot},
			TTY:     tty, YieldMillis: 1000,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	id := strings.Repeat("a", 32)
	result := start(id, `import os,pathlib
print('TTY='+str(os.isatty(0)), flush=True)
print('SIZE='+str(tuple(os.get_terminal_size(0))), flush=True)
value = input()
pathlib.Path('terminal.txt').write_text(value)
print('RESULT='+value, flush=True)
`, true)
	if result.ExitCode != nil || !strings.Contains(string(result.Output), "TTY=True") || !strings.Contains(string(result.Output), "SIZE=(80, 24)") {
		t.Fatalf("real PTY was not interactive: %+v", result)
	}
	result, err = sandbox.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, Input: []byte("confirmed\n"), YieldMillis: 1000})
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.InputBytes != 10 || !strings.Contains(string(result.Output), "RESULT=confirmed") {
		t.Fatalf("interactive input did not finish: %+v, %v", result, err)
	}
	snapshot, err := sandbox.ExportWorkspace(context.Background(), handle)
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].Path != "terminal.txt" || snapshot.Entries[0].SizeBytes != 9 {
		t.Fatalf("terminal file was not retained for snapshot: %+v, %v", snapshot.Entries, err)
	}
	id = strings.Repeat("c", 32)
	result = start(id, `import signal,sys,time
signal.signal(signal.SIGINT, lambda *_: sys.exit(23))
print('READY', flush=True)
time.sleep(60)
`, true)
	if result.ExitCode != nil || !strings.Contains(string(result.Output), "READY") {
		t.Fatalf("interrupt fixture did not start: %+v", result)
	}
	result, err = sandbox.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, Input: []byte{3}, YieldMillis: 1000})
	if err != nil || result.ExitCode == nil || *result.ExitCode != 23 {
		t.Fatalf("TTY interrupt did not reach the foreground process: %+v, %v", result, err)
	}
	result = start(strings.Repeat("d", 32), "import os,sys; print('TTY='+str(os.isatty(0))); print('EOF='+str(sys.stdin.read()==''))", false)
	if result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(string(result.Output), "TTY=False") || !strings.Contains(string(result.Output), "EOF=True") {
		t.Fatalf("non-interactive execution did not close stdin: %+v", result)
	}
}
