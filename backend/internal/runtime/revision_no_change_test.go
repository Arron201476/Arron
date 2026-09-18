package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestCompleteRevisionNoChangeKeepsBaseVersion(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "无需修改")
	if err != nil {
		t.Fatal(err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	versionID := version.ArtifactVersionID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "保持当前内容，不作修改",
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID:        &artifact.ArtifactID,
				CurrentArtifactVersionID: &versionID,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "正在核对修改要求。", Intent: "revise", Confidence: 1},
	})
	if err != nil || exchange.Revision == nil {
		t.Fatalf("CreateMessageExchange() = %+v, error = %v", exchange, err)
	}
	attempt, err := store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		ContextPayload:    []byte(`{"target":"artifact"}`),
		ContextHash:       "no-change-context",
		AdapterID:         "openai_agents_sdk_apply_patch",
		AdapterVersion:    "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteRevisionNoChange(ctx, CompleteRevisionNoChangeCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		RevisionAttemptID: attempt.RevisionAttemptID,
		Summary:           "当前内容无需修改。",
		ProviderID:        "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "cancelled" || completed.ProposalSummary == nil || *completed.ProposalSummary != "当前内容无需修改。" {
		t.Fatalf("completed revision = %+v", completed)
	}
	current, err := store.GetArtifact(ctx, artifact.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentVersionID != versionID {
		t.Fatalf("current version = %s, want %s", current.CurrentVersionID, versionID)
	}
}
