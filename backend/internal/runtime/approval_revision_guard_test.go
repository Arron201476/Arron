package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestResolveApprovalRejectsVersionWithActiveRevision(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "approval revision guard")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	selection := json.RawMessage(`{"schema_version":"1.0.0","artifact_id":"` + artifact.ArtifactID + `","artifact_type":"script_unit","target_scope":"selection","scope_key":"episode:1","field_path":"script_text","text_range":{"start":0,"end":6,"selected_text":"测试目标台词"},"display":{"artifact_label":"第 1 集剧本","selected_text_summary":"测试目标台词"}}`)
	normalized, err := normalizeJSON(selection)
	if err != nil {
		t.Fatalf("normalizeJSON() error = %v", err)
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "细化一下",
			SelectionSnapshot: &agentcontract.SelectionSnapshot{
				ArtifactVersionID: version.ArtifactVersionID,
				SnapshotHash:      sha256Hex(normalized),
				Selection:         selection,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "准备修改。", Intent: "revise", Confidence: 1},
	})
	if err != nil || exchange.Revision == nil || exchange.Revision.Status != "queued" {
		t.Fatalf("CreateMessageExchange() = %+v, error = %v", exchange, err)
	}

	approvalID := store.newID("apr")
	now := formatTime(store.now())
	if _, err := store.db.ExecContext(ctx, `INSERT INTO approvals(
		approval_request_id, project_id, run_id, step_run_id, scope, status, version,
		title, reason, options_json, subject_kind, subject_ref_id, subject_version,
		subject_snapshot_hash, requested_at
	) VALUES(?, ?, ?, ?, 'artifact', 'pending', 1, '确认剧本', 'fixture',
		'["approve"]', 'artifact_version', ?, 1, 'fixture_hash', ?)`,
		approvalID, project.ProjectID, artifact.RunID, artifact.StepRunID,
		version.ArtifactVersionID, now); err != nil {
		t.Fatalf("insert approval: %v", err)
	}

	_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resolve_approval",
			IdempotencyKey: "f1111111-1111-4111-8111-111111111111", RequestHash: "approval_revision_guard_v1",
		},
		ApprovalRequestID:       approvalID,
		ExpectedApprovalVersion: 1,
		SubjectSnapshotHash:     "fixture_hash",
		Action:                  "approve",
	})
	assertDomainCode(t, err, "REVISION_IN_PROGRESS")
}
