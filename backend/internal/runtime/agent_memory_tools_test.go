package runtime

import (
	"context"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
)

func TestMemoryPublicationToolPolicyIsConsolidationOnly(t *testing.T) {
	registry := agentToolRegistryForTest(t)
	for _, id := range []string{prepareAgentMemoryPublicationTool, publishAgentMemoryTool} {
		descriptor, ok := registry.Get(id)
		if !ok || !memoryGenerationToolPolicy("consolidation", descriptor) {
			t.Fatalf("missing admitted publication descriptor: %s", id)
		}
		for _, phase := range []string{"extraction", "publication", ""} {
			if memoryGenerationToolPolicy(phase, descriptor) {
				t.Fatalf("publication admitted in %s", phase)
			}
		}
		for _, mutate := range []func(*agenttool.Descriptor){
			func(d *agenttool.Descriptor) { d.ServerID = "external" },
			func(d *agenttool.Descriptor) { d.Name = "other" },
			func(d *agenttool.Descriptor) { d.Access = agenttool.AccessSensitive },
			func(d *agenttool.Descriptor) {
				if d.Approval == agenttool.ApprovalAlways {
					d.Approval = agenttool.ApprovalNever
				} else {
					d.Approval = agenttool.ApprovalAlways
				}
			},
		} {
			wrong := descriptor
			mutate(&wrong)
			if memoryGenerationToolPolicy("consolidation", wrong) {
				t.Fatalf("invalid publication policy admitted: %+v", wrong)
			}
		}
	}
}

func TestMemoryToolEnrollmentUsesPrivateApprovalAndExactIdentity(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	activity := AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken}
	ctx := WithAgentActivity(worker, activity)
	catalog, err := store.AgentToolCatalog(ctx, true)
	if err != nil || len(catalog.Tools) != 3 || len(catalog.MCPServers) != 0 || len(catalog.HostedTools) != 0 {
		t.Fatalf("private catalog: %+v %v", catalog, err)
	}
	for _, descriptor := range catalog.Tools {
		if descriptor.ID != nativeWorkspaceExecTool && descriptor.ID != nativeWorkspacePatchTool && descriptor.ID != nativeWorkspaceStdinTool {
			t.Fatal("private catalog exposed a business or external tool")
		}
	}
	parent, _ := AgentActivityFromContext(original)
	var conversation string
	if err := store.db.QueryRow(`SELECT conversation_id FROM agent_turns WHERE agent_turn_id=?`, parent.AgentTurnID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{ProjectID: job.ProjectID, ConversationID: conversation,
		MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken,
		SDKToolCallID: "memory-native", ToolID: nativeWorkspaceExecTool, Arguments: []byte(`{"command":"PRIVATE_CONTENT"}`)}
	if claim.ConversationID != conversation {
		t.Fatal("worker claim did not preserve the original source conversation")
	}
	command.ConversationID = claim.ConversationID
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || call.Status != "pending_approval" || call.Approval == nil || string(call.ArgumentsSummary) != `{"private_memory_tool":true}` {
		t.Fatalf("begin: %+v %v", call, err)
	}
	proposal, err := store.GetAgentMemoryToolProposal(mcpOwnerContext(), call.AgentToolCallID)
	if err != nil || !proposal.CanApprove || string(proposal.Arguments) != string(command.Arguments) {
		t.Fatalf("proposal: %+v %v", proposal, err)
	}
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: command.SDKToolCallID})
	assertDomainCode(t, err, "AGENT_TOOL_APPROVAL_REQUIRED")
	again, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || again.AgentToolCallID != call.AgentToolCallID {
		t.Fatalf("retry: %+v %v", again, err)
	}
	for _, mutate := range []func(*BeginAgentToolCallCommand){
		func(c *BeginAgentToolCallCommand) { c.MemoryGenerationID = "other" },
		func(c *BeginAgentToolCallCommand) { c.MemoryGenerationAttempt++ },
		func(c *BeginAgentToolCallCommand) { c.AttemptToken = "wrong" },
		func(c *BeginAgentToolCallCommand) { c.AgentTurnID = parent.AgentTurnID },
		func(c *BeginAgentToolCallCommand) { c.ToolID = publishAgentMemoryTool },
		func(c *BeginAgentToolCallCommand) { c.Arguments = []byte(`{"command":"changed"}`) },
	} {
		wrong := command
		mutate(&wrong)
		if _, err := store.BeginAgentToolCall(ctx, wrong); err == nil {
			t.Fatal("conflicting memory registration accepted")
		}
	}
	if _, err := store.BeginAgentToolCall(worker, command); err == nil {
		t.Fatal("unscoped worker registered private call")
	}
	ordinary := command
	ordinary.MemoryGenerationID, ordinary.MemoryGenerationAttempt, ordinary.AttemptToken = "", 0, ""
	if _, err := store.BeginAgentToolCall(original, ordinary); err == nil {
		t.Fatal("business activity reused private SDK call")
	}
}

func TestMemoryToolBindingRejectsCrossModeAndStalePhase(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	activity := AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken}
	ctx := WithAgentActivity(worker, activity)
	parent, _ := AgentActivityFromContext(original)
	var conversation string
	if err := store.db.QueryRow(`SELECT conversation_id FROM agent_turns WHERE agent_turn_id=?`, parent.AgentTurnID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	arguments := []byte(`{"command":"PRIVATE_CONTENT"}`)
	_, err = tx.Exec(`INSERT INTO agent_tool_calls(agent_tool_call_id,workspace_id,project_id,conversation_id,sdk_tool_call_id,tool_id,tool_kind,tool_name,access_mode,approval_policy,max_result_bytes,approval_status,status,arguments_summary_json,arguments_hash,requested_at,updated_at)
		VALUES('memory-call',?,?,?,'sdk-memory','runtime:exec_command','runtime_function','exec_command','sensitive','always',4096,'approved','running','{}',?,?,?)`, job.WorkspaceID, job.ProjectID, conversation, sha256Hex(arguments), formatTime(store.now()), formatTime(store.now()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.bindMemoryGenerationToolTx(ctx, tx, "memory-call", arguments); err != nil {
		t.Fatal(err)
	}
	if bound, err := store.validateMemoryGenerationToolCallTx(ctx, tx, "memory-call"); err != nil || !bound {
		t.Fatalf("binding validation: %v", err)
	}
	if err := validateAgentActivityToolCallQuery(ctx, tx, parent, "memory-call"); err == nil {
		t.Fatal("business execution accepted memory call")
	}
	if _, err := store.validateMemoryGenerationToolCallTx(original, tx, "memory-call"); err == nil {
		t.Fatal("business worker accepted memory call")
	}
	if _, err := store.validateMemoryGenerationToolCallTx(worker, tx, "memory-call"); err == nil {
		t.Fatal("unscoped service accepted memory call")
	}
	if _, err := tx.Exec(`UPDATE agent_tool_calls SET status='pending_approval',approval_status='pending' WHERE agent_tool_call_id='memory-call'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO agent_tool_approvals(agent_tool_approval_id,agent_tool_call_id,workspace_id,project_id,conversation_id,status,version,title,reason,options_json,subject_snapshot_hash,requested_at)
		VALUES('memory-approval','memory-call',?,?,?,'pending',1,'Memory tool','Private tool approval','["approve","reject"]','subject',?)`, job.WorkspaceID, job.ProjectID, conversation, formatTime(store.now())); err != nil {
		t.Fatal(err)
	}
	proposal, err := store.memoryToolProposalTx(mcpOwnerContext(), tx, "memory-call")
	if err != nil || !proposal.CanApprove || !proposal.CanReject || string(proposal.Arguments) != string(arguments) {
		t.Fatalf("private proposal: %+v %v", proposal, err)
	}
	if _, err := store.memoryToolProposalTx(worker, tx, "memory-call"); err == nil {
		t.Fatal("service read private approval without user")
	}
	var public string
	if err := tx.QueryRow(`SELECT arguments_summary_json FROM agent_tool_calls WHERE agent_tool_call_id='memory-call'`).Scan(&public); err != nil || strings.Contains(public, "PRIVATE_CONTENT") {
		t.Fatal("private arguments leaked to shared summary")
	}
	if _, err := tx.Exec(`UPDATE agent_memory_generations SET phase='consolidation' WHERE generation_id=?`, job.GenerationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.validateMemoryGenerationToolCallTx(ctx, tx, "memory-call"); err == nil {
		t.Fatal("old phase tool accepted")
	}
	stale, err := store.memoryToolProposalTx(mcpOwnerContext(), tx, "memory-call")
	if err != nil || stale.CanApprove || !stale.CanReject || len(stale.Arguments) != 0 {
		t.Fatalf("stale private proposal: %+v %v", stale, err)
	}
}
