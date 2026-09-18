package runtime

import (
	"context"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryGenerationIndependentExecutionScopeAndNativeLease(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	activity := AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken}
	_, err = store.ResolveAgentActivityPrincipal(worker, activity)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	started, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.ResolveAgentActivityPrincipal(worker, activity)
	if err != nil || user.UserID != job.UserID || user.WorkspaceID != job.WorkspaceID {
		t.Fatalf("principal: %+v %v", user, err)
	}
	ctx := WithAgentActivity(worker, activity)
	instructions, err := store.ResolveAgentInstructionSnapshot(ctx)
	if err != nil || instructions.ActivityKey != "memory:"+job.GenerationID {
		t.Fatalf("instructions: %v", err)
	}
	memory, err := store.ResolveAgentMemorySnapshot(ctx)
	if err != nil || memory.ActivityKey != instructions.ActivityKey || memory.Version != job.BaseVersion || memory.ContentHash != job.BaseHash {
		t.Fatalf("memory snapshot: %v", err)
	}
	lease, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: strings.Repeat("d", 64)})
	if err != nil {
		t.Fatal(err)
	}
	access := NativeWorkspaceAccess{SessionID: lease.SessionID, Generation: lease.Generation, HolderKey: strings.Repeat("d", 64)}
	if _, err := store.ValidateNativeWorkspaceLease(ctx, access); err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{})
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	err = store.ValidateAgentActivityToolCall(ctx, activity, "business-call")
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	for _, modify := range []func(*AgentActivityIdentity){
		func(a *AgentActivityIdentity) { a.AgentTurnID = "mixed" },
		func(a *AgentActivityIdentity) { a.MemoryGenerationAttempt = 0 },
		func(a *AgentActivityIdentity) { a.AttemptToken = "wrong" },
		func(a *AgentActivityIdentity) { a.AllowTerminal = true },
		func(a *AgentActivityIdentity) { a.ProjectID = "other-project" },
	} {
		wrong := activity
		modify(&wrong)
		if _, err := store.ResolveAgentActivityPrincipal(worker, wrong); err == nil {
			t.Fatal("invalid generation identity accepted")
		}
	}
	if _, err := store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "pause", started.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateNativeWorkspaceLease(ctx, access); err == nil {
		t.Fatal("paused generation retained native workspace access")
	}
}
