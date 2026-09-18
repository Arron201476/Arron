package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func TestBackgroundSkillCreatesTaskWithoutBusinessRunAndCompletesWithArtifact(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	source := filepath.Join(
		testProjectRoot(t), "fixtures", "skills", "background", "story-research-digest",
	)
	installed, err := store.InstallSkillDirectory(ctx, source, "user_background_runtime_test")
	if err != nil || !installed.Enabled || installed.Status != "installed" {
		t.Fatalf("InstallSkillDirectory() = %+v, error = %v", installed, err)
	}
	project, err := store.CreateProject(ctx, "后台调研")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "story_research_digest", Version: "1.0.0", SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta:    CommandMeta{Scope: project.ProjectID, CommandType: "commit_agent_turn", IdempotencyKey: "background-turn", RequestHash: "background-turn-v1"},
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "后台整理这些材料", CapabilityRef: reference},
		Decision: agentcontract.AgentDecision{
			Reply: "已加入后台任务。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "start_background_task", CapabilityRef: reference,
				Input: json.RawMessage(`{"topic":"悬疑故事"}`), Config: json.RawMessage(`{}`),
				RequiresConfirmation: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.Invocation == nil || exchange.Invocation.ExecutionMode != "background_task" ||
		exchange.Invocation.Status != "delegated_to_task" || exchange.TaskRef == nil ||
		exchange.TaskRef.Status != "queued" || exchange.Action == nil || exchange.Action.Status != "consumed" {
		t.Fatalf("background exchange = %+v", exchange)
	}
	var runCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE project_id = ?`, project.ProjectID).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("background Skill created %d business runs", runCount)
	}

	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "sdk-background-1", ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil {
		t.Fatalf("ClaimAgentTask() = %+v, error = %v", claim, err)
	}
	if claim.SkillName != "story-research-digest" || claim.Instructions == "" ||
		claim.Task.AgentTaskID != exchange.TaskRef.AgentTaskID {
		t.Fatalf("claim = %+v", claim)
	}
	progressed, err := store.UpdateAgentTaskProgress(ctx, UpdateAgentTaskProgressCommand{
		AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
		Current: 1, Total: 2, Message: "整理事实",
	})
	if err != nil || progressed.ProgressCurrent != 1 || progressed.ProgressTotal != 2 {
		t.Fatalf("UpdateAgentTaskProgress() = %+v, error = %v", progressed, err)
	}
	completed, err := store.CompleteAgentTask(ctx, CompleteAgentTaskCommand{
		AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
		Result: json.RawMessage(`{"summary":"调研完成"}`), Usage: json.RawMessage(`{"input_tokens":10,"output_tokens":20}`),
		ArtifactDraft: &agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "创作调研摘要",
			Payload: json.RawMessage(`{"content":"事实、推断与创作建议。"}`),
		},
	})
	if err != nil {
		t.Fatalf("CompleteAgentTask() error = %v", err)
	}
	if completed.Status != "completed" || completed.ResultArtifactID == nil ||
		completed.ResultArtifactVersionID == nil || completed.CompletedAt == nil {
		t.Fatalf("completed task = %+v", completed)
	}
	artifact, err := store.GetArtifact(ctx, *completed.ResultArtifactID)
	if err != nil || artifact.RunID != "" || artifact.OriginType != "agent_task" ||
		artifact.OriginID != completed.AgentTaskID || artifact.CapabilityID != reference.CapabilityID {
		t.Fatalf("task artifact = %+v, error = %v", artifact, err)
	}
	freshInvocation, err := scanSkillInvocation(store.db.QueryRowContext(ctx, skillInvocationSelect+`
		WHERE skill_invocation_id = ?`, exchange.Invocation.SkillInvocationID))
	if err != nil || freshInvocation.Status != "completed" || freshInvocation.RunID != nil ||
		freshInvocation.AgentTaskID == nil || *freshInvocation.AgentTaskID != completed.AgentTaskID {
		t.Fatalf("invocation = %+v, error = %v", freshInvocation, err)
	}
}

func TestManagedBackgroundSkillProposalPinsArchivedVersionAcrossUpgrade(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser} {
		t.Run(string(scope), func(t *testing.T) { testManagedBackgroundSkillSnapshot(t, scope) })
	}
}

func testManagedBackgroundSkillSnapshot(t *testing.T, scope capability.SkillScope) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	v1Source := writeSkillTestPackage(
		t, filepath.Join(t.TempDir(), "v1"), "managed-background-pin",
		"managed_background_pin", "1.0.0", "background_task", "BACKGROUND_V1_INSTRUCTIONS",
	)
	setJSONFileFieldsForTest(
		t, filepath.Join(v1Source, "content-agent", "manifest.json"),
		map[string]any{"requires_user_confirmation": true},
	)
	installed, err := store.InstallSkillDirectory(ctx, v1Source, "user_background_v1", SkillInstallTarget{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "后台版本固定")
	if err != nil {
		t.Fatal(err)
	}
	reference := &agentcontract.CapabilityRef{
		CapabilityID: installed.CapabilityID, Version: "1.0.0", SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "后台执行固定版本。", CapabilityRef: reference,
		},
		Decision: agentcontract.AgentDecision{
			Reply: "请确认后台任务。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "start_background_task", CapabilityRef: reference,
				Input: json.RawMessage(`{"topic":"version pin"}`), Config: json.RawMessage(`{}`),
				RequiresConfirmation: true,
			},
		},
	})
	if err != nil || exchange.Action == nil || exchange.Invocation == nil || exchange.TaskRef != nil ||
		exchange.Invocation.Status != "awaiting_confirmation" {
		t.Fatalf("CreateMessageExchange() = %+v, error = %v", exchange, err)
	}
	if scope == capability.SkillScopeUser {
		other := identity.Principal{Kind: identity.KindUser, UserID: "user_snapshot_other", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor}
		if err := store.BootstrapPrincipal(ctx, other); err != nil {
			t.Fatal(err)
		}
		_, err := store.StartAgentTask(identity.WithPrincipal(ctx, other), StartAgentTaskCommand{
			ProjectID: project.ProjectID, ProposedActionID: exchange.Action.ProposedActionID,
			CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version,
		})
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
	}

	v2Source := writeSkillTestPackage(
		t, filepath.Join(t.TempDir(), "v2"), "managed-background-pin",
		"managed_background_pin", "2.0.0", "background_task", "BACKGROUND_V2_INSTRUCTIONS",
	)
	setJSONFileFieldsForTest(
		t, filepath.Join(v2Source, "content-agent", "manifest.json"),
		map[string]any{"requires_user_confirmation": true},
	)
	if _, err := store.InstallSkillDirectory(ctx, v2Source, "user_background_v2", SkillInstallTarget{Scope: scope}); err != nil {
		t.Fatal(err)
	}
	selection, err := store.CapabilityRegistryForProject(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	assertRegistrySkillVersion(t, selection, installed.CapabilityID, "2.0.0", true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open(after upgrade) error = %v", err)
	}
	assertExecutionSkillBinding(t, store, "skill_invocations", "skill_invocation_id", exchange.Invocation.SkillInvocationID, *installed.ActiveVersionID)
	shadowExecutionSkillForTest(t, store, project.ProjectID, v1Source, installed.CapabilityID)

	task, err := store.StartAgentTask(ctx, StartAgentTaskCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "start_agent_task",
			IdempotencyKey: "pinned-background-start", RequestHash: "pinned-background-start-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version,
		Input: exchange.Action.Input, Config: exchange.Action.Config, Confirmed: true,
		ProposedActionID: exchange.Action.ProposedActionID, ProposedActionVersion: exchange.Action.Version,
		ConfirmationMessageID:    exchange.Action.ConfirmationMessageID,
		ConfirmationSnapshotHash: exchange.Action.SnapshotHash,
	})
	if err != nil || task.CapabilityVersion != "1.0.0" {
		t.Fatalf("StartAgentTask(v1 after v2 activation) = %+v, error = %v", task, err)
	}
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "sdk-pinned-background", ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil || claim.Instructions != "BACKGROUND_V1_INSTRUCTIONS" {
		t.Fatalf("ClaimAgentTask() = %+v, error = %v", claim, err)
	}
	activityCtx := WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken})
	page, err := store.ReadSkillResource(activityCtx, project.ProjectID, installed.CapabilityID, "1.0.0", "SKILL.md", 0, 16000)
	if err != nil || strings.Contains(page.Content, "SHADOW_SAME_VERSION_DO_NOT_EXECUTE") || !strings.Contains(page.Content, "BACKGROUND_V1_INSTRUCTIONS") {
		t.Fatalf("background resources lost immutable package: %+v err=%v", page, err)
	}
}

func TestBackgroundTaskRetryAndCancellationAreDurable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	task := createBackgroundTaskForTest(t, store)
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "sdk-background-1", ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil {
		t.Fatalf("ClaimAgentTask() = %+v, error = %v", claim, err)
	}
	requeued, err := store.FailAgentTask(ctx, FailAgentTaskCommand{
		AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
		ErrorCode: "MODEL_TIMEOUT", ErrorMessage: "timeout", Retryable: true,
	})
	if err != nil || requeued.Status != "queued" || requeued.AttemptCount != 1 {
		t.Fatalf("FailAgentTask(retryable) = %+v, error = %v", requeued, err)
	}
	second, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "sdk-background-2", ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || second == nil || second.Attempt.AttemptNo != 2 {
		t.Fatalf("second claim = %+v, error = %v", second, err)
	}
	cancelled, err := store.CancelAgentTask(ctx, CancelAgentTaskCommand{
		CommandMeta: CommandMeta{Scope: task.ProjectID, CommandType: "cancel_agent_task", IdempotencyKey: "cancel-agent-task", RequestHash: "cancel-agent-task-v1"},
		AgentTaskID: task.AgentTaskID, ActorRef: "test-user",
	})
	if err != nil || cancelled.Status != "cancelled" || !cancelled.CancelRequested {
		t.Fatalf("CancelAgentTask() = %+v, error = %v", cancelled, err)
	}
	_, err = store.CompleteAgentTask(ctx, CompleteAgentTaskCommand{
		AgentTaskAttemptID: second.Attempt.AgentTaskAttemptID, AttemptToken: second.AttemptToken,
		Result: json.RawMessage(`{}`), Usage: json.RawMessage(`{}`),
	})
	assertDomainCode(t, err, "AGENT_TASK_CANCELLED")
	retried, err := store.RetryAgentTask(ctx, RetryAgentTaskCommand{
		CommandMeta: CommandMeta{Scope: task.ProjectID, CommandType: "retry_agent_task", IdempotencyKey: "retry-agent-task", RequestHash: "retry-agent-task-v1"},
		AgentTaskID: task.AgentTaskID, ActorRef: "test-user",
	})
	if err != nil || retried.Status != "queued" || retried.CancelRequested {
		t.Fatalf("RetryAgentTask() = %+v, error = %v", retried, err)
	}
	attempts, err := store.ListAgentTaskAttempts(ctx, task.AgentTaskID)
	if err != nil || len(attempts) != 2 || attempts[0].Status != "failed" || attempts[1].Status != "cancelled" {
		t.Fatalf("attempts = %+v, error = %v", attempts, err)
	}
}

func createBackgroundTaskForTest(t *testing.T, store *Store) AgentTask {
	t.Helper()
	project, err := store.CreateProject(context.Background(), "后台任务重试")
	if err != nil {
		t.Fatal(err)
	}
	reference := &agentcontract.CapabilityRef{CapabilityID: "story_research_digest", Version: "1.0.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(context.Background(), CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "后台调研", CapabilityRef: reference},
		Decision: agentcontract.AgentDecision{
			Reply: "已加入后台任务。", Intent: "propose_capability", Confidence: 1, CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "start_background_task", CapabilityRef: reference,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`), RequiresConfirmation: false,
			},
		},
	})
	if err != nil || exchange.TaskRef == nil {
		t.Fatalf("create background task = %+v, error = %v", exchange, err)
	}
	return *exchange.TaskRef
}

func loadBackgroundTaskRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if override := os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT"); override != "" {
		root = override
	}
	registry, err := capability.LoadRegistry(capability.LoadOptions{
		ProjectRoot: root,
		SkillRoots: []capability.SkillRoot{{
			Scope:    capability.SkillScopeWorkspace,
			Path:     filepath.Join(root, "fixtures", "skills", "background"),
			Priority: 200,
		}},
	})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	return registry
}

func TestExpiredBackgroundTaskLeaseIsRequeued(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := createBackgroundTaskForTest(t, store)
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "sdk-background-1", ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 30,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, error = %v", claim, err)
	}
	store.now = func() time.Time { return claim.Attempt.LeaseUntil.Add(time.Second) }
	second, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "sdk-background-2", ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 30,
	})
	if err != nil || second == nil || second.Task.AgentTaskID != task.AgentTaskID || second.Attempt.AttemptNo != 2 {
		t.Fatalf("reclaimed task = %+v, error = %v", second, err)
	}
}

func TestBackgroundTaskHeartbeatRenewsLeaseWithoutRevivingCancelledAttempt(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := createBackgroundTaskForTest(t, store)
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{
		WorkerID: "worker", ProviderID: "openai", ModelID: "test", LeaseSeconds: 30,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, error = %v", claim, err)
	}
	now := claim.Attempt.LeaseUntil.Add(-time.Second)
	store.now = func() time.Time { return now }
	heartbeat := UpdateAgentTaskProgressCommand{
		AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken,
		Current: 1, Total: 3, Message: "working", LeaseSeconds: 60,
	}
	if _, err := store.UpdateAgentTaskProgress(ctx, heartbeat); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListAgentTaskAttempts(ctx, task.AgentTaskID)
	if err != nil || len(attempts) != 1 || !attempts[0].LeaseUntil.After(claim.Attempt.LeaseUntil) {
		t.Fatalf("renewed attempts = %+v, error = %v", attempts, err)
	}
	if _, err := store.CancelAgentTask(ctx, CancelAgentTaskCommand{
		CommandMeta: CommandMeta{Scope: task.ProjectID, CommandType: "cancel_agent_task", IdempotencyKey: "cancel-heartbeat", RequestHash: "cancel-heartbeat"},
		AgentTaskID: task.AgentTaskID, ActorRef: "test-user",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgentTaskProgress(ctx, heartbeat); err == nil {
		t.Fatal("cancelled task accepted heartbeat")
	}
}
