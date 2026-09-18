package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestCreateGenericArtifactPersistsWithoutActiveSkillRun(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "通用产物")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	command := CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "create_generic_artifact",
			IdempotencyKey: "generic-artifact-test", RequestHash: "generic-artifact-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "角色小传",
			Payload: json.RawMessage(`{"content":"林舟是一名谨慎的剑修。"}`),
		},
	}
	created, err := store.CreateGenericArtifact(ctx, command)
	if err != nil {
		t.Fatalf("CreateGenericArtifact() error = %v", err)
	}
	repeated, err := store.CreateGenericArtifact(ctx, command)
	if err != nil || repeated.ArtifactID != created.ArtifactID {
		t.Fatalf("idempotent result = %+v, error = %v", repeated, err)
	}
	artifacts, err := store.ListArtifactsByProject(ctx, project.ProjectID)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactType != "generic_document" ||
		artifacts[0].CapabilityID != "agent_shell" || artifacts[0].Title != "角色小传" {
		t.Fatalf("artifacts = %+v, error = %v", artifacts, err)
	}
	version, err := store.GetArtifactVersion(ctx, created.CurrentVersionID)
	if err != nil || version.Status != "confirmed" {
		t.Fatalf("version = %+v, error = %v", version, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(version.Payload, &payload); err != nil || payload["title"] != "角色小传" || payload["artifact_label"] != "角色小传" {
		t.Fatalf("payload = %#v, error = %v", payload, err)
	}
	refreshed, err := store.GetProject(ctx, project.ProjectID)
	if err != nil || refreshed.ActiveWriteRunID != nil || refreshed.CurrentCapabilityID != nil {
		t.Fatalf("project state = %+v, error = %v", refreshed, err)
	}
	if created.RunID != "" || created.StepRunID != "" || created.OriginType != "agent_turn" || created.OriginID != command.IdempotencyKey {
		t.Fatalf("standalone artifact origin = %+v", created)
	}
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE project_id = ?`, project.ProjectID).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("generic artifact created %d business runs", runCount)
	}
}

func TestCreateGenericArtifactPersistsSourceVersionLineage(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "通用产物血缘")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	source, err := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_generic_artifact", IdempotencyKey: "generic-lineage-source", RequestHash: "generic-lineage-source-v1"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "故事大纲", Payload: json.RawMessage(`{"content":"主角寻找失踪的姐姐。"}`)},
	})
	if err != nil {
		t.Fatalf("CreateGenericArtifact(source) error = %v", err)
	}
	downstream, err := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_generic_artifact", IdempotencyKey: "generic-lineage-downstream", RequestHash: "generic-lineage-downstream-v1"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft:                    agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "场景拆解", Payload: json.RawMessage(`{"content":"场景一：主角进入旧宅。"}`)},
		SourceArtifactVersionIDs: []string{source.CurrentVersionID, source.CurrentVersionID},
	})
	if err != nil {
		t.Fatalf("CreateGenericArtifact(downstream) error = %v", err)
	}
	lineage, err := store.GetArtifactVersionLineage(ctx, downstream.CurrentVersionID, 1)
	if err != nil {
		t.Fatalf("GetArtifactVersionLineage() error = %v", err)
	}
	if len(lineage.Upstream) != 1 ||
		lineage.Upstream[0].UpstreamRefID != source.CurrentVersionID ||
		lineage.Upstream[0].Relation != "derived_from" {
		t.Fatalf("lineage = %+v", lineage)
	}

	otherProject, err := store.CreateProject(ctx, "其他作品")
	if err != nil {
		t.Fatalf("CreateProject(other) error = %v", err)
	}
	_, err = store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{Scope: otherProject.ProjectID, CommandType: "create_generic_artifact", IdempotencyKey: "generic-lineage-cross-project", RequestHash: "generic-lineage-cross-project-v1"},
		ProjectID:   otherProject.ProjectID, ConversationID: otherProject.PrimaryConversationID,
		Draft:                    agentcontract.ArtifactDraft{ArtifactType: "generic_document", Title: "错误引用", Payload: json.RawMessage(`{"content":"不应保存。"}`)},
		SourceArtifactVersionIDs: []string{source.CurrentVersionID},
	})
	assertDomainCode(t, err, "DEPENDENCY_PROJECT_MISMATCH")
}

func TestGenericArtifactCanBecomeAuthoritativeSkillSource(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "中间产物转剧本")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	created, err := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_generic_artifact", IdempotencyKey: "artifact-skill-source", RequestHash: "artifact-skill-source-v1"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "原创故事大纲",
			Payload: json.RawMessage(`{"content":"失业记者林昭发现姐姐失踪与一宗旧案有关。她决定潜入集团寻找证据。"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateGenericArtifact() error = %v", err)
	}
	reference := &agentcontract.CapabilityRef{CapabilityID: "non_novel_to_script", Version: "1.3.0", SelectionMode: "explicit"}
	versionID := created.CurrentVersionID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "用这个大纲生成剧本", CapabilityRef: reference,
			ClientContext: agentcontract.ClientContext{CurrentArtifactID: &created.ArtifactID, CurrentArtifactVersionID: &versionID},
		},
		Decision: agentcontract.AgentDecision{
			Reply: "请确认配置。", Intent: "propose_capability", Confidence: 1, CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "collect_run_configuration", CapabilityRef: reference,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	var source sourceInputRequest
	if err := json.Unmarshal(exchange.Action.Input, &source); err != nil ||
		len(source.ArtifactVersions) != 1 || source.ArtifactVersions[0].ArtifactVersionID != versionID ||
		source.UserRequestMessageID != exchange.UserMessage.MessageID {
		t.Fatalf("enriched artifact source = %+v, error = %v", source, err)
	}
	config := json.RawMessage(`{"config_ref":"creation","payload":{"target_episode_count":3,"episode_duration_minutes":2}}`)
	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		ProposedActionID: exchange.Action.ProposedActionID, ExpectedVersion: exchange.Action.Version,
		Input: exchange.Action.Input, Config: config,
	})
	if err != nil {
		t.Fatalf("ConfigureProposedAction() error = %v", err)
	}
	started, err := store.StartRun(ctx, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version,
		RunKind: "generation", Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID: configured.ConfirmationMessageID, ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	running := approveSnapshotAndResume(t, store, started)
	if currentStepID(running) != "build_material_bank" {
		t.Fatalf("current step = %q", currentStepID(running))
	}
	claim := claimTaskForExecutor(t, store, "worker.structured_content")
	foundSource := false
	for _, upstream := range claim.ContextPack.UpstreamContext {
		if upstream.ArtifactVersionID == versionID && upstream.SelectionPolicy == "explicit_source" && len(upstream.Content) > 0 {
			foundSource = true
		}
	}
	if !foundSource {
		t.Fatalf("artifact source missing from context pack: %+v", claim.ContextPack.UpstreamContext)
	}
}

func TestExplicitSourceIsExcludedFromDeclaredStepInputCoverage(t *testing.T) {
	upstream := []ContextUpstreamArtifact{
		{ArtifactType: "source_input", SelectionPolicy: "latest_confirmed"},
		{ArtifactType: "generic_document", SelectionPolicy: "explicit_source"},
	}

	declared := declaredContextUpstream(upstream)

	if len(declared) != 1 || declared[0].ArtifactType != "source_input" {
		t.Fatalf("declared upstream = %+v", declared)
	}
}

func TestAcceptGenericArtifactRevisionCreatesConfirmedVersionWithoutApproval(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "通用产物修改")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	created, err := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "create_generic_artifact",
			IdempotencyKey: "generic-revision-create", RequestHash: "generic-revision-create-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "人物设定",
			Payload: json.RawMessage(`{"content":"1. 林昭冷静克制。\n2. 她把追查真相视为救赎。\n3. 她会默默守护同伴。"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateGenericArtifact() error = %v", err)
	}
	baseVersionID := created.CurrentVersionID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "把第二条改成她曾因错误判断失去搭档。",
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID:        &created.ArtifactID,
				CurrentArtifactVersionID: &baseVersionID,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "准备修改。", Intent: "revise", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.Revision == nil || exchange.Revision.Status != "queued" {
		t.Fatalf("revision exchange = %+v", exchange)
	}
	attempt, err := store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		ContextPayload:    json.RawMessage(`{"target":"artifact"}`),
		ContextHash:       "generic-revision-context",
		AdapterID:         "generic_document_revision",
		AdapterVersion:    "1.0.0",
	})
	if err != nil {
		t.Fatalf("BeginRevisionAttempt() error = %v", err)
	}
	proposalPayload := json.RawMessage(`{"content":"1. 林昭冷静克制。\n2. 她曾因错误判断失去搭档。\n3. 她会默默守护同伴。","title":"人物设定","artifact_label":"人物设定"}`)
	proposal, err := store.CompleteRevisionAttempt(ctx, CompleteRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		RevisionAttemptID: attempt.RevisionAttemptID,
		ProposalPayload:   proposalPayload,
		ProposalSummary:   "修改第二条人物经历。",
		ProviderID:        "test-model",
	})
	if err != nil {
		t.Fatalf("CompleteRevisionAttempt() error = %v", err)
	}
	accepted, err := store.AcceptRevision(ctx, AcceptRevisionCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "accept_revision",
			IdempotencyKey: "generic-revision-accept", RequestHash: "generic-revision-accept-v1",
		},
		RevisionRequestID: proposal.RevisionRequestID,
		ExpectedVersion:   proposal.Version,
		ActorRef:          "test-user",
	})
	if err != nil {
		t.Fatalf("AcceptRevision() error = %v", err)
	}
	if accepted.Revision.Status != "accepted" || accepted.Version.ArtifactVersion.Status != "confirmed" ||
		accepted.Version.ArtifactVersion.Version != 2 || accepted.Version.ArtifactVersion.ConfirmedAt == nil {
		t.Fatalf("accepted revision = %+v", accepted)
	}
	versions, err := store.ListArtifactVersions(ctx, created.ArtifactID)
	if err != nil || len(versions) != 2 || versions[0].Status != "superseded" ||
		versions[1].Status != "confirmed" || string(versions[1].Payload) != string(proposalPayload) {
		t.Fatalf("artifact versions = %+v, error = %v", versions, err)
	}
	refreshed, err := store.GetArtifact(ctx, created.ArtifactID)
	if err != nil || refreshed.CurrentVersionID != versions[1].ArtifactVersionID {
		t.Fatalf("current artifact = %+v, error = %v", refreshed, err)
	}
	var approvalCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE project_id = ?`, project.ProjectID).Scan(&approvalCount); err != nil {
		t.Fatalf("count approvals: %v", err)
	}
	if approvalCount != 0 {
		t.Fatalf("generic revision created %d approval records", approvalCount)
	}
}

func TestOpenRecoversInterruptedRevision(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	path := filepath.Join(t.TempDir(), "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(path, registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	project, err := store.CreateProject(ctx, "中断修改恢复")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	created, err := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "create_generic_artifact",
			IdempotencyKey: "revision-recovery-create", RequestHash: "revision-recovery-create-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "待修改文档",
			Payload: json.RawMessage(`{"content":"旧内容"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateGenericArtifact() error = %v", err)
	}
	versionID := created.CurrentVersionID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "改成新内容",
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID: &created.ArtifactID, CurrentArtifactVersionID: &versionID,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "准备修改。", Intent: "revise", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	attempt, err := store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		ContextPayload:    json.RawMessage(`{"target":"artifact"}`),
		ContextHash:       "revision-recovery-context", AdapterID: "test", AdapterVersion: "1.0.0",
	})
	if err != nil || attempt.Status != "running" {
		t.Fatalf("BeginRevisionAttempt() = %+v, error = %v", attempt, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path, registry)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	fresh, err := reopened.GetRevisionRequest(ctx, exchange.Revision.RevisionRequestID)
	if err != nil || fresh.Status != "running" {
		t.Fatalf("fresh revision after reopen = %+v, error = %v", fresh, err)
	}
	staleTime := formatTime(reopened.now().Add(-11 * time.Minute))
	if _, err := reopened.db.Exec(
		`UPDATE revision_attempts SET created_at = ? WHERE revision_attempt_id = ?`,
		staleTime, attempt.RevisionAttemptID,
	); err != nil {
		t.Fatalf("age interrupted revision: %v", err)
	}
	if _, err := reopened.db.Exec(
		`UPDATE revision_requests SET updated_at = ? WHERE revision_request_id = ?`,
		staleTime, exchange.Revision.RevisionRequestID,
	); err != nil {
		t.Fatalf("age interrupted revision request: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close fresh reopen: %v", err)
	}

	recoveredStore, err := Open(path, registry)
	if err != nil {
		t.Fatalf("reopen stale revision: %v", err)
	}
	defer recoveredStore.Close()
	recovered, err := recoveredStore.GetRevisionRequest(ctx, exchange.Revision.RevisionRequestID)
	if err != nil || recovered.Status != "failed" || recovered.FailureCode == nil ||
		*recovered.FailureCode != "RUNTIME_RESTART_INTERRUPTED" {
		t.Fatalf("recovered revision = %+v, error = %v", recovered, err)
	}
}
