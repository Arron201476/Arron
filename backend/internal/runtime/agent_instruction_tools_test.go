package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func beginRuleCall(t *testing.T, store *Store, ctx context.Context, args SavedInstructionArguments, sdkID string) (AgentToolCall, json.RawMessage) {
	t.Helper()
	activity, _ := AgentActivityFromContext(ctx)
	var conversation string
	if err := store.db.QueryRow(`SELECT primary_conversation_id FROM projects WHERE project_id=?`, activity.ProjectID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(args)
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: activity.ProjectID, ConversationID: conversation,
		AgentTurnID: activity.AgentTurnID, AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID,
		AttemptToken: activity.AttemptToken, ToolID: updateSavedInstructionsTool, SDKToolCallID: sdkID, Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	return call, raw
}

func ruleApproval(call AgentToolCall, action string) ResolveAgentToolApprovalCommand {
	return ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: action}
}

func TestAgentInstructionToolAuthorApprovalAndPrivateAudit(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	owner := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, owner)
	call, raw := beginRuleCall(t, store, execution, SavedInstructionArguments{Scope: "user", Content: "PRIVATE_RULE_BODY", Enabled: true}, "rule-first")
	if call.Status != "pending_approval" || call.Approval == nil {
		t.Fatalf("approval missing: %+v", call)
	}
	encoded, _ := json.Marshal(call)
	if strings.Contains(string(encoded), "PRIVATE_RULE_BODY") {
		t.Fatal("private proposal exposed in shared call")
	}
	proposal, err := store.GetAgentInstructionProposal(owner, call.AgentToolCallID)
	if err != nil || !proposal.CanApprove || proposal.Arguments.Content != "PRIVATE_RULE_BODY" {
		t.Fatalf("proposal: %+v %v", proposal, err)
	}
	other := identity.Principal{Kind: identity.KindUser, UserID: "other-admin", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleOwner}
	if err := store.BootstrapPrincipal(owner, other); err != nil {
		t.Fatal(err)
	}
	otherContext := identity.WithPrincipal(context.Background(), other)
	_, err = store.GetAgentInstructionProposal(otherContext, call.AgentToolCallID)
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	_, err = store.ResolveAgentToolApproval(otherContext, ruleApproval(call, "approve"))
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	_, err = store.ResolveAgentToolApproval(execution, ruleApproval(call, "approve"))
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	_, err = store.ApplyAgentInstructionTool(execution, call.AgentToolCallID, call.SDKToolCallID, raw)
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	if _, err := store.ResolveAgentToolApproval(owner, ruleApproval(call, "approve")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(execution, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteAgentToolCall(execution, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`"Tool validation error"`)})
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	doc, err := store.ApplyAgentInstructionTool(execution, call.AgentToolCallID, call.SDKToolCallID, raw)
	if err != nil || doc.Version != 1 || doc.Content != "PRIVATE_RULE_BODY" {
		t.Fatalf("apply: %+v %v", doc, err)
	}
	if _, err := store.UpdateAgentInstructions(owner, instructionUpdate("user", call.ProjectID, "later", "later", 1)); err != nil {
		t.Fatal(err)
	}
	again, err := store.ApplyAgentInstructionTool(execution, call.AgentToolCallID, call.SDKToolCallID, raw)
	if err != nil || again != doc {
		t.Fatalf("receipt replay changed: %+v %v", again, err)
	}
	result, _ := json.Marshal(doc)
	complete, err := store.CompleteAgentToolCall(execution, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(complete)
	if strings.Contains(string(encoded), "PRIVATE_RULE_BODY") || !strings.Contains(string(encoded), "private_instruction_result") {
		t.Fatal("private result exposed in shared audit")
	}
}

func TestAgentInstructionToolStaleApprovalAndRevokedRole(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	owner := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, owner)
	call, _ := beginRuleCall(t, store, execution, SavedInstructionArguments{Scope: "project", Content: "draft", Enabled: true}, "stale")
	if _, err := store.UpdateAgentInstructions(owner, instructionUpdate("project", call.ProjectID, "manual edit", "manual", 0)); err != nil {
		t.Fatal(err)
	}
	_, err := store.ResolveAgentToolApproval(owner, ruleApproval(call, "approve"))
	assertDomainCode(t, err, "AGENT_INSTRUCTIONS_CONFLICT")
	if _, err := store.ResolveAgentToolApproval(owner, ruleApproval(call, "reject")); err != nil {
		t.Fatal(err)
	}
	next, raw := beginRuleCall(t, store, execution, SavedInstructionArguments{Scope: "workspace", Content: "shared", Enabled: true}, "revoked")
	if _, err := store.ResolveAgentToolApproval(owner, ruleApproval(next, "approve")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(execution, StartAgentToolCallCommand{AgentToolCallID: next.AgentToolCallID, ExpectedSDKToolCallID: next.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='editor' WHERE user_id=?`, identity.DefaultUserID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyAgentInstructionTool(execution, next.AgentToolCallID, next.SDKToolCallID, raw)
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
}

func TestAgentInstructionToolStrictArgumentsAndFailureRedaction(t *testing.T) {
	for _, raw := range []string{`{}`, `{"scope":"user","content":"x","expected_version":0}`, `{"scope":"user","content":"x","expected_version":0,"enabled":null}`, `{"scope":"user","content":"x","expected_version":0,"enabled":true,"user_id":"victim"}`} {
		_, err := decodeSavedInstructionArguments(json.RawMessage(raw))
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	store, _, _ := mcpConnectionStore(t)
	owner := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, owner)
	call, _ := beginRuleCall(t, store, execution, SavedInstructionArguments{Scope: "user", Content: "private", Enabled: true}, "error")
	if _, err := store.ResolveAgentToolApproval(owner, ruleApproval(call, "approve")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentToolCall(execution, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailAgentToolCall(execution, FailAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ErrorCode: "RULE_INVALID", ErrorMessage: "PRIVATE_BODY_IN_VALIDATOR_ERROR"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(failed)
	if strings.Contains(string(raw), "PRIVATE_BODY_IN_VALIDATOR_ERROR") {
		t.Fatal("private validation error exposed")
	}
}

func TestAgentInstructionProposalDeletePreviewAndCleanup(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	execution := mcpExecutionContext(t, store, ctx)
	activity, _ := AgentActivityFromContext(execution)
	if _, err := store.UpdateAgentInstructions(ctx, instructionUpdate("project", activity.ProjectID, "old rule", "first", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAgentInstructionSnapshot(execution); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: activity.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	call, _ := beginRuleCall(t, store, execution, SavedInstructionArguments{Scope: "user", Content: "private proposal", Enabled: true}, "to-delete")
	latest, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: activity.ProjectID})
	if err != nil || latest.SnapshotHash == first.SnapshotHash || latest.Impact.InstructionProposalCount != 1 || latest.Impact.InstructionSnapshotCount != 1 || latest.Impact.InstructionVersionCount != 1 {
		t.Fatalf("preview: %+v %v", latest, err)
	}
	if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: activity.ProjectID, PreviewHash: latest.SnapshotHash, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_instruction_versions", "agent_instruction_snapshots", "agent_instruction_proposals"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retained %s: %d %v", table, count, err)
		}
	}
	if _, err := store.GetAgentInstructionProposal(ctx, call.AgentToolCallID); err == nil {
		t.Fatal("deleted project proposal is readable")
	}
}
