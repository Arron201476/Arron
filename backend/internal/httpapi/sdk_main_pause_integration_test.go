//go:build sdk_integration

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func mainPauseFixtureRequest(url, method string, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer pause-service")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("fixture response: %d", response.StatusCode)
	}
	if result != nil {
		return json.NewDecoder(response.Body).Decode(result)
	}
	_, err = io.Copy(io.Discard, response.Body)
	return err
}

func startMainPauseSDKFixture(t *testing.T, backendURL, phase, sessionDB string, turn businessruntime.AgentTurn) (string, func()) {
	t.Helper()
	root := testRoot(t)
	python := os.Getenv("CONTENT_AGENT_SDK_TEST_PYTHON")
	if python == "" {
		python = filepath.Join(root, ".tools", "openai-agents-sidecar-venv", "Scripts", "python.exe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	command := exec.CommandContext(ctx, python, filepath.Join(root, "experiments", "openai-agents-sidecar", "tests", "fixtures", "main_pause_http_server.py"),
		"--backend-url", backendURL, "--turn-id", turn.AgentTurnID, "--project-id", turn.ProjectID, "--conversation-id", turn.ConversationID, "--phase", phase, "--session-db", sessionDB)
	command.Dir = root
	command.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1", "OPENAI_AGENTS_DISABLE_TRACING=1", "PYTHONPATH="+filepath.Join(root, "experiments", "openai-agents-sidecar", "src"))
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var announcement struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(scanner.Bytes(), &announcement) == nil && announcement.URL != "" {
				select {
				case ready <- announcement.URL:
				default:
				}
			}
		}
	}()
	var once sync.Once
	url := ""
	stop := func() {
		once.Do(func() {
			if url != "" {
				_ = mainPauseFixtureRequest(url+"/internal/v1/fixture/stop", http.MethodPost, nil)
			}
			if url == "" {
				cancel()
			}
			// Drain stdout before Wait closes the pipe; context bounds failed startup/shutdown.
			<-drained
			err := command.Wait()
			cancel()
			if stderr.Len() > 0 {
				t.Logf("SDK main fixture %s diagnostics: %s", phase, stderr.String())
			}
			if err != nil {
				t.Errorf("SDK main fixture %s: %v\n%s", phase, err, stderr.String())
			}
		})
	}
	t.Cleanup(stop)
	select {
	case url = <-ready:
	case <-drained:
		stop()
		t.Fatal("SDK fixture exited before readiness")
	case <-time.After(45 * time.Second):
		cancel()
		stop()
		t.Fatal("SDK fixture startup timeout")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := mainPauseFixtureRequest(url+"/healthz", http.MethodGet, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("SDK fixture did not become healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return url, stop
}

func TestSDKMainPauseHTTPRebuildKeepsOriginalTurnAndSession(t *testing.T) {
	runMainPauseHTTPFixture(t, "none")
}

func TestSDKMainInputHTTPRebuildAndModelReceipt(t *testing.T) {
	runMainPauseHTTPFixture(t, "manual")
}

func TestSDKMainInputAutomaticContinuationAndLateInputReceipt(t *testing.T) {
	runMainPauseHTTPFixture(t, "auto")
}

func TestSDKMainInputRevisionHTTPRebuild(t *testing.T)   { runMainPauseHTTPFixture(t, "change") }
func TestSDKMainInputAttachmentHTTPRebuild(t *testing.T) { runMainPauseHTTPFixture(t, "attachment") }
func TestSDKMainModelFailureHTTPRebuild(t *testing.T)    { runMainPauseHTTPFixture(t, "recovery") }

func runMainPauseHTTPFixture(t *testing.T, inputMode string) {
	changedInput := inputMode == "change" || inputMode == "attachment"
	recovery := inputMode == "recovery"
	hasInputs := inputMode != "none" && !recovery
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "pause-service")
	directory := t.TempDir()
	database, sessionDB := filepath.Join(directory, "main.db"), filepath.Join(directory, "sdk-session.db")
	registry := capability.NewEmptyRegistry()
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{{Token: "pause-owner", UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner}})
	if err != nil {
		t.Fatal(err)
	}
	var store *businessruntime.Store
	var handler http.Handler
	var backend *httptest.Server
	open := func() {
		store, err = businessruntime.Open(database, registry)
		if err != nil {
			t.Fatal(err)
		}
		store.SetAgentToolRegistry(tools)
		server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
		if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		handler = server.Handler()
		backend = httptest.NewServer(handler)
	}
	open()
	defer func() { backend.Close(); store.Close() }()
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
	project, err := store.CreateProject(ctx, "Real main SDK pause")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Write a local Skill draft and report completion"}, businessruntime.CommandMeta{CommandType: "create_message", RequestHash: "main-pause-fixture", IdempotencyKey: "b9999999-9999-4999-8999-999999999999"})
	if err != nil {
		t.Fatal(err)
	}
	var originalStart time.Time
	phases := []string{"write", "resume"}
	if inputMode == "auto" {
		phases = []string{"auto"}
	}
	for _, phase := range phases {
		func() {
			fixturePhase := phase
			if hasInputs {
				fixturePhase = "input-" + phase
			} else if recovery {
				fixturePhase = "failure-" + phase
			}
			url, stop := startMainPauseSDKFixture(t, backend.URL, fixturePhase, sessionDB, turn)
			defer stop()
			service, err := shell.NewRequiredSidecarAgentService(url, "pause-service", 50*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var managerLog bytes.Buffer
			manager := newAgentTurnManager(store, shell.NewWithAgentService(registry, service), slog.New(slog.NewTextHandler(&managerLog, nil)))
			managerContext, cancel := context.WithCancel(context.Background())
			go manager.run(managerContext)
			defer func() {
				cancel()
				select {
				case <-manager.done:
					if managerLog.Len() > 0 {
						t.Logf("main manager %s diagnostics: %s", phase, managerLog.String())
					}
				case <-time.After(5 * time.Second):
					t.Error("main SDK manager did not stop")
				}
			}()
			want := "paused"
			if phase == "auto" {
				want = "committed"
			}
			if phase == "resume" {
				performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/agent-turns/"+turn.AgentTurnID+"/resume", map[string]any{}, bearer("pause-owner", "a9999999-9999-4999-8999-999999999999"), http.StatusOK)
				want = "committed"
			}
			deadline := time.Now().Add(40 * time.Second)
			for {
				current, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
				if err != nil {
					t.Fatal(err)
				}
				receiptReady := !hasInputs || phase == "write" || (len(current.AdditionalInputs) > 0 && current.AdditionalInputs[0].Status == "included") || (changedInput && len(current.AdditionalInputs) == 3 && current.AdditionalInputs[2].Status == "included")
				if current.Status == want && receiptReady {
					turn = current
					break
				}
				if current.Status == "failed" || current.Status == "cancelled" || time.Now().After(deadline) {
					t.Fatalf("main SDK %s stopped in %s: %+v", phase, current.Status, current.Observation)
				}
				time.Sleep(25 * time.Millisecond)
			}
			var state struct {
				ModelRequests         int `json:"model_requests"`
				UserItems             int `json:"user_items"`
				FileOutputs           int `json:"file_outputs"`
				AdditionalUserItems   int `json:"additional_user_items"`
				LateUserItems         int `json:"late_user_items"`
				NativeAttachmentCount int `json:"native_attachment_count"`
			}
			if err := mainPauseFixtureRequest(url+"/internal/v1/fixture/state", http.MethodGet, &state); err != nil {
				t.Fatal(err)
			}
			if inputMode == "attachment" && phase == "resume" && state.NativeAttachmentCount != 2 {
				t.Fatal("native main model did not receive attached source and image")
			}
			wantModelRequests, wantUserItems, wantAdditional := 1, 1, 0
			if hasInputs && phase != "write" {
				wantUserItems, wantAdditional = 2, 1
			}
			if phase == "auto" {
				wantModelRequests = 2
			}
			if recovery && phase == "write" {
				wantModelRequests = 2
				if turn.ErrorCode == nil || *turn.ErrorCode != "SDK_MODEL_RECOVERY_REQUIRED" {
					t.Fatal("model recovery cause was not persisted")
				}
			}
			if state.ModelRequests != wantModelRequests || state.UserItems != wantUserItems || state.FileOutputs != 1 || state.AdditionalUserItems != wantAdditional || state.LateUserItems != 0 {
				t.Fatalf("SDK replay or Session loss in %s: %+v", phase, state)
			}
			if hasInputs {
				wantInputs, wantStatus := 1, "included"
				if phase == "write" {
					wantStatus = "received"
				}
				if phase == "auto" {
					wantInputs = 2
				}
				index := 0
				if changedInput && phase == "resume" {
					wantInputs, index = 3, 2
					if len(turn.AdditionalInputs) != 3 || turn.AdditionalInputs[0].Status != "withdrawn" || turn.AdditionalInputs[1].Status != "superseded" {
						t.Fatal("main changed history missing")
					}
				}
				if len(turn.AdditionalInputs) != wantInputs || turn.AdditionalInputs[index].Status != wantStatus || (phase == "auto" && (turn.AdditionalInputs[1].Status != "received" || turn.AdditionalInputs[1].IncludedAt != nil)) {
					t.Fatalf("model receipt does not match actual admission: %+v", turn.AdditionalInputs)
				}
			}
			file, err := store.ReadProjectFile(ctx, project.ProjectID, "skills/main-pause/SKILL.md", 0, 0, 1000)
			if err != nil || file.File.Version != 1 {
				t.Fatalf("file lost or repeated: %+v %v", file, err)
			}
			calls, err := store.ListAgentToolCalls(ctx, project.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			for _, call := range calls {
				if call.SDKToolCallID == "write-file" {
					writes++
					if call.Status != "completed" || call.AgentTurnID == nil || *call.AgentTurnID != turn.AgentTurnID {
						t.Fatalf("write identity: %+v", call)
					}
				}
			}
			if writes != 1 {
				t.Fatalf("write replay count: %d", writes)
			}
			t.Logf("phase=%s original_turn=%s model_requests=%d user_items=%d file_version=%d", phase, turn.AgentTurnID, state.ModelRequests, state.UserItems, file.File.Version)
		}()
		if phase == "write" {
			if changedInput {
				revisePendingInputHTTP(t, handler, project.ProjectID, "conversation", turn.AgentTurnID, turn.AdditionalInputs[0].InputID, turn.AdditionalInputs[0].Content, "pause-owner", inputMode == "attachment")
			}
			originalStart = *turn.StartedAt
			checkpoint, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
			if err != nil || len(checkpoint.RunState) == 0 {
				t.Fatalf("native checkpoint missing: %v", err)
			}
			backend.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
				t.Fatal(err)
			}
		} else if phase == "resume" && (turn.StartedAt == nil || !turn.StartedAt.Equal(originalStart)) {
			t.Fatal("resume restarted original turn")
		}
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil || len(messages) != 2 || messages[1].Content != "MAIN_PAUSE_RESTORED" {
		t.Fatalf("final conversation: %+v %v", messages, err)
	}
}
