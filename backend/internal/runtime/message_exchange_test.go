package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestCreateMessageExchangePersistsDecisionAndAction(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "消息合同")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "novel_to_script",
		Version:      "1.4.0",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content:       "使用小说转剧本",
			CapabilityRef: reference,
		},
		Decision: agentcontract.AgentDecision{
			Reply:         "请确认配置。",
			Intent:        "propose_capability",
			Confidence:    0.98,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType:           "collect_run_configuration",
				CapabilityRef:        reference,
				Input:                json.RawMessage(`{}`),
				Config:               json.RawMessage(`{}`),
				RequiresConfirmation: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.UserMessage.Role != "user" || exchange.AgentMessage.Role != "assistant" ||
		exchange.Decision.Decision.Intent != "propose_capability" ||
		exchange.Decision.Decision.ProposedAction != nil || exchange.Action == nil {
		t.Fatalf("message exchange = %+v", exchange)
	}
	stored, err := store.GetProposedAction(ctx, exchange.Action.ProposedActionID)
	if err != nil || stored.SnapshotHash != exchange.Action.SnapshotHash || stored.Status != "pending" {
		t.Fatalf("stored proposed action = %+v, error = %v", stored, err)
	}
	pendingActions, err := store.ListPendingProposedActions(ctx, project.ProjectID)
	if err != nil || len(pendingActions) != 1 || pendingActions[0].ProposedActionID != stored.ProposedActionID {
		t.Fatalf("pending proposed actions = %+v, error = %v", pendingActions, err)
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil || len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("stored messages = %+v, error = %v", messages, err)
	}
	for _, message := range messages {
		if message.Context == nil || message.Context.RoutingContext.Scope != "invocation" ||
			message.Context.RoutingContext.InvocationID == nil {
			t.Fatalf("message invocation routing = %+v", message.Context)
		}
	}
	var invocation SkillInvocation
	var createdAt, updatedAt string
	if err := store.db.QueryRowContext(ctx, `
		SELECT skill_invocation_id, project_id, conversation_id, user_message_id, agent_message_id,
			capability_id, capability_version, execution_mode, status, proposed_action_id, run_id,
			created_at, updated_at
		FROM skill_invocations WHERE proposed_action_id = ?`, exchange.Action.ProposedActionID).Scan(
		&invocation.SkillInvocationID, &invocation.ProjectID, &invocation.ConversationID,
		&invocation.UserMessageID, &invocation.AgentMessageID, &invocation.CapabilityID,
		&invocation.CapabilityVersion, &invocation.ExecutionMode, &invocation.Status,
		&invocation.ProposedActionID, &invocation.RunID, &createdAt, &updatedAt,
	); err != nil {
		t.Fatalf("load Skill invocation: %v", err)
	}
	if invocation.ExecutionMode != "stateful_workflow" || invocation.Status != "awaiting_confirmation" {
		t.Fatalf("Skill invocation = %+v", invocation)
	}
}

func TestCreateMessageExchangeBindsVideoAssetSetToProposedAction(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "视频批次绑定")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	video := createVideoAsset(t, store, project.ProjectID, "第1集.mp4")
	created, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil {
		t.Fatalf("CreateAssetSet() error = %v", err)
	}
	updated, err := store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: created.AssetSet.AssetSetID, ExpectedCurrentVersion: created.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{{Operation: "add_asset", AssetID: video.Asset.AssetID}},
	})
	if err != nil {
		t.Fatalf("CreateAssetSetVersion() error = %v", err)
	}
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: updated.AssetSet.AssetSetID, ExpectedCurrentVersion: updated.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	})
	if err != nil {
		t.Fatalf("SealAssetSet() error = %v", err)
	}
	assetSetVersionID := sealed.Version.AssetSetVersionID
	container := createTextAsset(t, store, project.ProjectID, "archive-container.txt", "archive")
	containerID := container.Asset.AssetID
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "video_reference_creation", Version: "1.2.0",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "使用视频参考创作 Skill。",
			AttachmentRefs: []agentcontract.AttachmentRef{
				{AssetID: containerID, AssetSnapshotID: container.Snapshot.AssetSnapshotID},
				{AssetID: video.Asset.AssetID, AssetSnapshotID: video.Snapshot.AssetSnapshotID, Hidden: true, ContainerAssetID: &containerID},
			},
			ClientContext: agentcontract.ClientContext{
				CurrentAssetSetVersionID: &assetSetVersionID,
			},
		},
		Decision: agentcontract.AgentDecision{
			Reply: "请确认配置。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "collect_run_configuration", CapabilityRef: reference,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	var input struct {
		AssetSetVersionID string `json:"asset_set_version_id"`
		AssetSetID        string `json:"asset_set_id"`
		CollectionState   string `json:"collection_state"`
	}
	if err := json.Unmarshal(exchange.Action.Input, &input); err != nil {
		t.Fatalf("decode proposed input: %v", err)
	}
	if input.AssetSetVersionID != assetSetVersionID {
		t.Fatalf("asset set version = %q, want %q", input.AssetSetVersionID, assetSetVersionID)
	}
	if input.AssetSetID != sealed.AssetSet.AssetSetID || input.CollectionState != "sealed" {
		t.Fatalf("asset set binding = %+v", input)
	}
	if exchange.Action.ActionType != "start_run" || !exchange.Action.RequiresConfirmation {
		t.Fatalf("sealed video action = %+v", exchange.Action)
	}
	var config struct {
		ConfigRef string `json:"config_ref"`
		Payload   struct {
			FidelityLevel          string `json:"fidelity_level"`
			TimecodePrecision      string `json:"timecode_precision"`
			UncertainContentPolicy string `json:"uncertain_content_policy"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(exchange.Action.Config, &config); err != nil ||
		config.ConfigRef != "extraction" || config.Payload.FidelityLevel != "high" ||
		config.Payload.TimecodePrecision != "second" || config.Payload.UncertainContentPolicy != "mark" {
		t.Fatalf("sealed video config = %+v, error = %v", config, err)
	}
}

func TestCreateMessageExchangePreservesAgentSelectedArtifactSource(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "来源定位")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	_, selectedVersion := seedMessageContextArtifact(t, store, project)
	focusedArtifact, focusedVersion := seedMessageContextArtifact(t, store, project)
	if _, err := store.db.ExecContext(ctx,
		`UPDATE projects SET current_focus_artifact_version_id = ? WHERE project_id = ?`,
		focusedVersion.ArtifactVersionID, project.ProjectID,
	); err != nil {
		t.Fatalf("set project focus: %v", err)
	}
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "non_novel_to_script", Version: "1.3.0",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "使用选定大纲生成剧本",
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID:        &focusedArtifact.ArtifactID,
				CurrentArtifactVersionID: &focusedVersion.ArtifactVersionID,
			},
		},
		Decision: agentcontract.AgentDecision{
			Reply: "请确认配置。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "collect_run_configuration", CapabilityRef: reference,
				Input:  json.RawMessage(`{"artifact_versions":[{"artifact_version_id":"` + selectedVersion.ArtifactVersionID + `","role":"primary_source","order":1}]}`),
				Config: json.RawMessage(`{}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	var input struct {
		ArtifactVersions []struct {
			ArtifactVersionID string `json:"artifact_version_id"`
		} `json:"artifact_versions"`
	}
	if err := json.Unmarshal(exchange.Action.Input, &input); err != nil {
		t.Fatalf("decode proposed input: %v", err)
	}
	if len(input.ArtifactVersions) != 1 || input.ArtifactVersions[0].ArtifactVersionID != selectedVersion.ArtifactVersionID {
		t.Fatalf("artifact source overwritten by focus: %+v", input.ArtifactVersions)
	}
}

func TestCreateMessageExchangeSupersedesHistoricalPendingActionForSameSkill(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "配置卡相关性")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	reference := &agentcontract.CapabilityRef{CapabilityID: "non_novel_to_script", Version: "1.3.0"}
	create := func(content string) MessageExchange {
		exchange, createErr := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
			ConversationID: project.PrimaryConversationID,
			Request:        agentcontract.MessageRequest{Content: content},
			Decision: agentcontract.AgentDecision{
				Reply: "请确认本轮配置。", Intent: "propose_capability", Confidence: 1,
				CapabilityRef: reference,
				ProposedAction: &agentcontract.ProposedActionDraft{
					ActionType: "collect_run_configuration", CapabilityRef: reference,
					Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
				},
			},
		})
		if createErr != nil {
			t.Fatalf("CreateMessageExchange() error = %v", createErr)
		}
		return exchange
	}
	oldExchange := create("旧的大纲")
	newExchange := create("当前大纲")

	oldAction, err := store.GetProposedAction(ctx, oldExchange.Action.ProposedActionID)
	if err != nil || oldAction.Status != "superseded" {
		t.Fatalf("old action = %+v, error = %v", oldAction, err)
	}
	pending, err := store.ListPendingProposedActions(ctx, project.ProjectID)
	if err != nil || len(pending) != 1 || pending[0].ProposedActionID != newExchange.Action.ProposedActionID {
		t.Fatalf("pending actions = %+v, error = %v", pending, err)
	}
	var invocationStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM skill_invocations WHERE proposed_action_id = ?`, oldExchange.Action.ProposedActionID).Scan(&invocationStatus); err != nil || invocationStatus != "superseded" {
		t.Fatalf("old invocation status = %q, error = %v", invocationStatus, err)
	}
}

func TestMessageSelectionContextIsValidatedAndRestored(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "选区定位")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	selection := json.RawMessage(`{"schema_version":"1.0.0","artifact_id":"` + artifact.ArtifactID + `","artifact_type":"script_unit","target_scope":"selection","scope_key":"episode:1","field_path":"script_text","text_range":{"start":4,"end":8,"selected_text":"目标台词"},"display":{"artifact_label":"第 1 集","selected_text_summary":"目标台词"}}`)
	normalized, err := normalizeJSON(selection)
	if err != nil {
		t.Fatalf("normalizeJSON() error = %v", err)
	}
	artifactID, versionID, runID, capabilityID, scopeKey := artifact.ArtifactID, version.ArtifactVersionID, artifact.RunID, artifact.CapabilityID, artifact.ScopeKey
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "把这里改得更有压迫感",
			SelectionSnapshot: &agentcontract.SelectionSnapshot{
				ArtifactVersionID: versionID,
				SnapshotHash:      sha256Hex(normalized),
				Selection:         selection,
			},
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID: &artifactID, CurrentArtifactVersionID: &versionID,
				CurrentRunID: &runID, CurrentCapabilityID: &capabilityID, CurrentScopeKey: &scopeKey,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "已定位到目标台词。", Intent: "chat", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.UserMessage.Context == nil || exchange.UserMessage.Context.SelectionSnapshot == nil {
		t.Fatalf("exchange user context = %+v", exchange.UserMessage.Context)
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil || len(messages) != 2 || messages[0].Context == nil || messages[0].Context.SelectionSnapshot == nil {
		t.Fatalf("ListMessages() = %+v, error = %v", messages, err)
	}
	if messages[1].Context == nil || messages[1].Context.SelectionSnapshot != nil ||
		messages[1].Context.RoutingContext.Scope != "artifact" {
		t.Fatalf("assistant message must inherit only authoritative routing context: %+v", messages[1].Context)
	}

	newVersionID := "av_context_2"
	now := formatTime(store.now())
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO artifact_versions(artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref, creation_reason, base_version_id, created_at)
		VALUES(?, ?, 2, 'draft', '{}', 'script_unit', '1.0.0', 'user', 'test', 'manual_edit', ?, ?)`,
		newVersionID, artifact.ArtifactID, versionID, now); err != nil {
		t.Fatalf("insert newer version: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE artifacts SET current_version_id = ?, updated_at = ? WHERE artifact_id = ?`, newVersionID, now, artifact.ArtifactID); err != nil {
		t.Fatalf("update current version: %v", err)
	}
	_, err = store.PreflightMessage(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{
		Content:           "继续改",
		SelectionSnapshot: &agentcontract.SelectionSnapshot{ArtifactVersionID: versionID, SnapshotHash: sha256Hex(normalized), Selection: selection},
		ClientContext:     agentcontract.ClientContext{CurrentArtifactID: &artifactID, CurrentArtifactVersionID: &versionID},
	})
	assertDomainCode(t, err, "VIEW_CONTEXT_STALE")
}

func TestRevisionRequestFromSelectionCreatesProposalBeforeNewVersion(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "选区修改")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	now := formatTime(store.now())
	if _, err := store.db.Exec(`INSERT INTO approvals(
		approval_request_id, project_id, run_id, step_run_id, scope, status, version,
		title, reason, options_json, subject_kind, subject_ref_id, subject_version,
		subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref
	) VALUES(?, ?, ?, ?, 'artifact', 'approved', 1, '确认剧本', 'fixture',
		'["approve","edit_artifact","request_ai_revision"]', 'artifact_version', ?, 1,
		'fixture_hash', ?, ?, '{"action":"approve"}', 'fixture')`,
		store.newID("apr"), project.ProjectID, artifact.RunID, artifact.StepRunID,
		version.ArtifactVersionID, now, now); err != nil {
		t.Fatalf("insert approval template: %v", err)
	}
	selection := json.RawMessage(`{"schema_version":"1.0.0","artifact_id":"` + artifact.ArtifactID + `","artifact_type":"script_unit","target_scope":"selection","scope_key":"episode:1","field_path":"script_text","text_range":{"start":0,"end":6,"selected_text":"测试目标台词"},"display":{"artifact_label":"第 1 集剧本","selected_text_summary":"测试目标台词"}}`)
	normalized, err := normalizeJSON(selection)
	if err != nil {
		t.Fatalf("normalizeJSON() error = %v", err)
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "把这里改得更有压迫感",
			SelectionSnapshot: &agentcontract.SelectionSnapshot{
				ArtifactVersionID: version.ArtifactVersionID,
				SnapshotHash:      sha256Hex(normalized),
				Selection:         selection,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "准备修改。", Intent: "revise", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.Resolution == nil || exchange.Resolution.Status != "resolved" || exchange.Revision == nil || exchange.Revision.Status != "queued" {
		t.Fatalf("revision exchange = %+v", exchange)
	}
	attempt, err := store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		ContextPayload:    json.RawMessage(`{"target":"selection"}`),
		ContextHash:       "context-hash",
		AdapterID:         "script_unit_revision",
		AdapterVersion:    "1.0.0",
	})
	if err != nil {
		t.Fatalf("BeginRevisionAttempt() error = %v", err)
	}
	if err := store.FailRevisionAttempt(ctx, FailRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		RevisionAttemptID: attempt.RevisionAttemptID,
		FailureCode:       "PROVIDER_REQUEST_INVALID",
	}); err != nil {
		t.Fatalf("FailRevisionAttempt() error = %v", err)
	}
	attempt, err = store.BeginRevisionAttempt(ctx, BeginRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		ContextPayload:    json.RawMessage(`{"target":"selection","retry":true}`),
		ContextHash:       "context-hash-retry",
		AdapterID:         "script_unit_revision",
		AdapterVersion:    "1.0.0",
	})
	if err != nil {
		t.Fatalf("retry BeginRevisionAttempt() error = %v", err)
	}
	if attempt.AttemptNo != 2 {
		t.Fatalf("retry attempt number = %d, want 2", attempt.AttemptNo)
	}
	proposal, err := store.CompleteRevisionAttempt(ctx, CompleteRevisionAttemptCommand{
		RevisionRequestID: exchange.Revision.RevisionRequestID,
		RevisionAttemptID: attempt.RevisionAttemptID,
		ProposalPayload:   json.RawMessage(`{"script_text":"修改后的目标台词"}`),
		ProposalSummary:   "增强台词压迫感。",
		ProviderID:        "test-model",
	})
	if err != nil {
		t.Fatalf("CompleteRevisionAttempt() error = %v", err)
	}
	if proposal.Status != "proposed" {
		t.Fatalf("proposal status = %s", proposal.Status)
	}
	current, err := store.GetArtifactVersion(ctx, artifact.CurrentVersionID)
	if err != nil || current.Version != 1 {
		t.Fatalf("base version changed before acceptance: %+v, error = %v", current, err)
	}
	accepted, err := store.AcceptRevision(ctx, AcceptRevisionCommand{
		CommandMeta:       CommandMeta{Scope: project.ProjectID, CommandType: "accept_revision", IdempotencyKey: "3f42b95d-6028-47ee-a22b-e1ecf3513f36", RequestHash: "accept-revision-v1"},
		RevisionRequestID: proposal.RevisionRequestID,
		ExpectedVersion:   proposal.Version,
		ActorRef:          "test-user",
	})
	if err != nil {
		t.Fatalf("AcceptRevision() error = %v", err)
	}
	if accepted.Revision.Status != "accepted" || accepted.Version.ArtifactVersion.Version != 2 {
		t.Fatalf("accepted revision = %+v", accepted)
	}
	if err := store.MarkAutomaticRevisionApplied(
		ctx,
		accepted.Revision.RevisionRequestID,
		exchange.AgentMessage.MessageID,
		exchange.Decision.AgentDecisionID,
	); err != nil {
		t.Fatalf("MarkAutomaticRevisionApplied() error = %v", err)
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil || len(messages) < 2 || messages[len(messages)-1].Content != "已按你的要求完成修改，并保存为新的产物版本。" {
		t.Fatalf("automatic revision reply was not persisted: messages=%+v, error=%v", messages, err)
	}
}

func TestArtifactRevisionConfirmationInheritsPriorInstruction(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "全局修改确认")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	request := agentcontract.MessageRequest{
		Content: "第1集开头内容太少，并且情节不够吸引人",
		ClientContext: agentcontract.ClientContext{
			CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &version.ArtifactVersionID,
		},
	}
	if _, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        request,
		Decision:       agentcontract.AgentDecision{Reply: "可以修改。", Intent: "chat", Confidence: 1},
	}); err != nil {
		t.Fatalf("create requirement exchange: %v", err)
	}

	request.Content = "改"
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        request,
		Decision: agentcontract.AgentDecision{
			Reply: "重新处理。", Intent: "regenerate", Confidence: 1,
			TargetRef: &agentcontract.TargetRef{TargetType: "artifact", TargetID: artifact.ArtifactID},
		},
	})
	if err != nil {
		t.Fatalf("create confirmation exchange: %v", err)
	}
	if exchange.Decision.Decision.Intent != "revise" {
		t.Fatalf("intent = %q, want revise", exchange.Decision.Decision.Intent)
	}
	if exchange.Revision == nil || exchange.Revision.Instruction != "第1集开头内容太少，并且情节不够吸引人" {
		t.Fatalf("revision = %+v", exchange.Revision)
	}
	if exchange.UserMessage.Content != "改" {
		t.Fatalf("user message = %q, want original confirmation", exchange.UserMessage.Content)
	}
}

func TestArtifactRevisionConfirmationWithoutRequirementClarifies(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "缺少修改要求")
	artifact, version := seedMessageContextArtifact(t, store, project)
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "修改吧",
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &version.ArtifactVersionID,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "开始修改。", Intent: "revise", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.Decision.Decision.Intent != "clarify" || exchange.Revision != nil || exchange.Resolution != nil {
		t.Fatalf("exchange = %+v", exchange)
	}
	if exchange.AgentMessage.Content != "请说明希望修改的具体内容。" {
		t.Fatalf("reply = %q", exchange.AgentMessage.Content)
	}
}

func TestExplicitArtifactRegenerationRemainsRegeneration(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "明确重生成")
	artifact, version := seedMessageContextArtifact(t, store, project)
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "重新生成第1集",
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &version.ArtifactVersionID,
			},
		},
		Decision: agentcontract.AgentDecision{Reply: "重新处理。", Intent: "regenerate", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.Decision.Decision.Intent != "regenerate" || exchange.Resolution == nil || exchange.Revision != nil {
		t.Fatalf("exchange = %+v", exchange)
	}
}

func TestGetCommittedAgentTurnReturnsSDKExchangeBeforeAgentRetry(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "消息幂等")
	const key = "18e57578-7fa4-48d5-a89d-359775b5a4c9"
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "commit_sdk_agent_turn",
			IdempotencyKey: key, RequestHash: "sdk-turn-v1",
		},
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "生成一份文档"},
		Decision:       agentcontract.AgentDecision{Reply: "已完成。", Intent: "chat", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	cached, hit, err := store.GetCommittedAgentTurn(ctx, project.ProjectID, key)
	if err != nil || !hit {
		t.Fatalf("GetCommittedAgentTurn() hit = %v, error = %v", hit, err)
	}
	if cached.UserMessage.MessageID != exchange.UserMessage.MessageID || cached.AgentMessage.MessageID != exchange.AgentMessage.MessageID {
		t.Fatalf("cached exchange = %+v, want %+v", cached, exchange)
	}
	if _, hit, err := store.GetCommittedAgentTurn(ctx, project.ProjectID, "missing"); err != nil || hit {
		t.Fatalf("missing lookup hit = %v, error = %v", hit, err)
	}
}

func TestMessageViewContextRejectsForeignArtifact(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "当前作品")
	foreign, _ := store.CreateProject(ctx, "其他作品")
	artifact, _ := seedMessageContextArtifact(t, store, foreign)
	_, err = store.PreflightMessage(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{
		Content:       "查看这个",
		ClientContext: agentcontract.ClientContext{CurrentArtifactID: &artifact.ArtifactID},
	})
	assertDomainCode(t, err, "RESOURCE_PROJECT_MISMATCH")
}

func seedMessageContextArtifact(t *testing.T, store *Store, project Project) (Artifact, ArtifactVersion) {
	t.Helper()
	now := formatTime(store.now())
	runID, stepRunID := store.newID("run"), store.newID("step")
	artifactID, versionID := store.newID("art"), store.newID("av")
	if _, err := store.db.Exec(`
		INSERT INTO runs(run_id, project_id, conversation_id, capability_id, capability_version, run_kind,
			write_intent, status, current_step_run_id, current_input_snapshot_version_id, input_snapshot_status,
			config_snapshot_json, run_event_seq, ended_at, created_at, updated_at)
		VALUES(?, ?, ?, 'novel_to_script', '1.4.0', 'generation', 1, 'completed', ?, 'input_test', 'sealed', '{}', 0, ?, ?, ?)`,
		runID, project.ProjectID, project.PrimaryConversationID, stepRunID, now, now, now); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO step_runs(step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at, ended_at)
		VALUES(?, ?, 'script_unit', 'completed', 1, 'none', '{}', '{}', ?, ?)`, stepRunID, runID, now, now); err != nil {
		t.Fatalf("insert step run: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifacts(artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
			scope_key, current_version_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'novel_to_script', 'script_unit', 'episode:1', ?, ?, ?)`,
		artifactID, project.ProjectID, runID, stepRunID, versionID, now, now); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_versions(artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref, creation_reason, created_at)
		VALUES(?, ?, 1, 'draft', '{"script_text":"测试目标台词"}', 'script_unit', '1.0.0', 'worker', 'test', 'generation', ?)`,
		versionID, artifactID, now); err != nil {
		t.Fatalf("insert artifact version: %v", err)
	}
	return Artifact{ArtifactID: artifactID, ProjectID: project.ProjectID, RunID: runID, StepRunID: stepRunID, CapabilityID: "novel_to_script", ArtifactType: "script_unit", ScopeKey: "episode:1", CurrentVersionID: versionID},
		ArtifactVersion{ArtifactVersionID: versionID, ArtifactID: artifactID, Version: 1}
}

func TestStartRunConfirmationRejectsTamperAndReplay(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "确认防重放")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "第一章。")
	command := bindStartRunProposal(t, store, novelStartCommand(project, "placeholder", asset))

	tampered := command
	tampered.Config = json.RawMessage(`{"config_ref":"creation","payload":{"target_episode_count":99}}`)
	_, err = store.StartRun(ctx, tampered)
	assertDomainCode(t, err, "CONFIRMATION_SNAPSHOT_MISMATCH")
	pending, err := store.GetProposedAction(ctx, command.ProposedActionID)
	if err != nil || pending.Status != "pending" {
		t.Fatalf("proposal after rejected tamper = %+v, error = %v", pending, err)
	}

	started, err := store.StartRun(ctx, command)
	if err != nil || started.Run.Status != "waiting_approval" {
		t.Fatalf("StartRun(valid confirmation) = %+v, error = %v", started, err)
	}
	replay := command
	replay.CommandMeta = CommandMeta{
		Scope:          project.ProjectID,
		CommandType:    "start_run",
		IdempotencyKey: "77777777-7777-4777-8777-777777777777",
		RequestHash:    "replayed_confirmation",
	}
	_, err = store.StartRun(ctx, replay)
	assertDomainCode(t, err, "CONFIRMATION_STALE")
}

func TestConfigureProposedActionClosesConfigurationToStartRun(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "配置卡闭环")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "第一章。")
	reference := &agentcontract.CapabilityRef{
		CapabilityID:  "novel_to_script",
		Version:       "1.4.0",
		SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content:       "使用小说转剧本",
			CapabilityRef: reference,
		},
		Decision: agentcontract.AgentDecision{
			Reply:         "请填写配置。",
			Intent:        "propose_capability",
			Confidence:    1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType:           "collect_run_configuration",
				CapabilityRef:        reference,
				Input:                json.RawMessage(`{}`),
				Config:               json.RawMessage(`{}`),
				RequiresConfirmation: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"source_type": "novel",
		"assets": []map[string]any{{
			"asset_id":          asset.Asset.AssetID,
			"asset_snapshot_id": asset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"user_notes": []string{},
	})
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	foreignProject, err := store.CreateProject(ctx, "其他作品")
	if err != nil {
		t.Fatalf("CreateProject(foreign) error = %v", err)
	}
	foreignAsset := createTextAsset(t, store, foreignProject.ProjectID, "foreign.txt", "外部材料。")
	foreignInput, err := json.Marshal(map[string]any{
		"source_type": "novel",
		"assets": []map[string]any{{
			"asset_id":          foreignAsset.Asset.AssetID,
			"asset_snapshot_id": foreignAsset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"user_notes": []string{},
	})
	if err != nil {
		t.Fatalf("json.Marshal(foreign input) error = %v", err)
	}
	config := json.RawMessage(`{
		"config_ref":"creation",
		"payload":{
			"target_episode_count":12,
			"episode_duration_minutes":2,
			"preserve_existing_episode_marks":true,
			"expansion_policy":"confirm_if_needed",
			"user_requirements":[]
		}
	}`)
	_, err = store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		ProposedActionID: exchange.Action.ProposedActionID,
		ExpectedVersion:  exchange.Action.Version,
		Input:            foreignInput,
		Config:           config,
	})
	assertDomainCode(t, err, "RESOURCE_PROJECT_MISMATCH")
	pending, err := store.GetProposedAction(ctx, exchange.Action.ProposedActionID)
	if err != nil || pending.ActionType != "collect_run_configuration" || pending.Version != 1 {
		t.Fatalf("action after foreign input = %+v, error = %v", pending, err)
	}

	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		ProposedActionID: exchange.Action.ProposedActionID,
		ExpectedVersion:  exchange.Action.Version,
		Input:            input,
		Config:           config,
	})
	if err != nil {
		t.Fatalf("ConfigureProposedAction() error = %v", err)
	}
	if configured.ActionType != "start_run" || configured.Version != 2 ||
		!configured.RequiresConfirmation || configured.SnapshotHash == exchange.Action.SnapshotHash ||
		configured.CapabilityRef == nil || configured.CapabilityRef.SelectionMode != "explicit" {
		t.Fatalf("configured action = %+v", configured)
	}
	var configuredInput sourceInputRequest
	if err := json.Unmarshal(configured.Input, &configuredInput); err != nil ||
		configuredInput.ProjectID != project.ProjectID ||
		configuredInput.UserRequestMessageID != exchange.UserMessage.MessageID {
		t.Fatalf("configured input = %+v, error = %v", configuredInput, err)
	}

	_, err = store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		ProposedActionID: exchange.Action.ProposedActionID,
		ExpectedVersion:  exchange.Action.Version,
		Input:            input,
		Config:           config,
	})
	assertDomainCode(t, err, "PROPOSED_ACTION_STALE")

	started, err := store.StartRun(ctx, StartRunCommand{
		ProjectID:                project.ProjectID,
		ConversationID:           project.PrimaryConversationID,
		CapabilityID:             reference.CapabilityID,
		CapabilityVersion:        reference.Version,
		RunKind:                  "generation",
		Input:                    configured.Input,
		Config:                   configured.Config,
		Confirmed:                true,
		ProposedActionID:         configured.ProposedActionID,
		ProposedActionVersion:    configured.Version,
		ConfirmationMessageID:    configured.ConfirmationMessageID,
		ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil || started.Run.Status != "waiting_approval" {
		t.Fatalf("StartRun(configured action) = %+v, error = %v", started, err)
	}
	pendingActions, err := store.ListPendingProposedActions(ctx, project.ProjectID)
	if err != nil || len(pendingActions) != 0 {
		t.Fatalf("pending proposed actions after start = %+v, error = %v", pendingActions, err)
	}
	projectActions, err := store.ListProjectProposedActions(ctx, project.ProjectID)
	if err != nil || len(projectActions) != 1 {
		t.Fatalf("project proposed actions after start = %+v, error = %v", projectActions, err)
	}
	if projectActions[0].Status != "consumed" || projectActions[0].ConsumedRunID == nil || *projectActions[0].ConsumedRunID != started.Run.RunID {
		t.Fatalf("consumed proposed action = %+v, want run %q", projectActions[0], started.Run.RunID)
	}
	var invocationStatus, invocationRunID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT status, run_id FROM skill_invocations WHERE proposed_action_id = ?`,
		configured.ProposedActionID,
	).Scan(&invocationStatus, &invocationRunID); err != nil {
		t.Fatalf("query delegated invocation: %v", err)
	}
	if invocationStatus != "delegated_to_run" || invocationRunID != started.Run.RunID {
		t.Fatalf("delegated invocation status=%q run=%q", invocationStatus, invocationRunID)
	}
	for _, messageID := range []string{exchange.UserMessage.MessageID, exchange.AgentMessage.MessageID} {
		var scope, invocationID, runID string
		if err := store.db.QueryRowContext(ctx, `
			SELECT scope, invocation_id, run_id FROM message_routing_contexts WHERE message_id = ?`,
			messageID,
		).Scan(&scope, &invocationID, &runID); err != nil {
			t.Fatalf("query delegated message routing: %v", err)
		}
		if scope != "run" || invocationID == "" || runID != started.Run.RunID {
			t.Fatalf("delegated message routing scope=%q invocation=%q run=%q", scope, invocationID, runID)
		}
	}
}

func TestBindProposedActionInputPersistsValidatedSource(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "输入绑定")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "第一章。")
	reference := &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: "1.4.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "开始改编", CapabilityRef: reference},
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
	input, err := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "source_type": "novel",
		"user_request_message_id": exchange.UserMessage.MessageID, "user_notes": []string{},
		"assets": []map[string]any{{
			"asset_id": asset.Asset.AssetID, "asset_snapshot_id": asset.Snapshot.AssetSnapshotID,
			"role": "primary_source", "order": 1,
		}},
	})
	if err != nil {
		t.Fatalf("json.Marshal(input) error = %v", err)
	}
	bound, err := store.BindProposedActionInput(ctx, BindProposedActionInputCommand{
		ProposedActionID: exchange.Action.ProposedActionID, ExpectedVersion: exchange.Action.Version, Input: input,
	})
	if err != nil {
		t.Fatalf("BindProposedActionInput() error = %v", err)
	}
	if bound.Version != exchange.Action.Version+1 || string(bound.Input) == "{}" {
		t.Fatalf("bound action = %+v", bound)
	}
	persisted, err := store.GetProposedAction(ctx, bound.ProposedActionID)
	if err != nil || persisted.Version != bound.Version || string(persisted.Input) != string(bound.Input) {
		t.Fatalf("persisted action = %+v, error = %v", persisted, err)
	}
	_, err = store.BindProposedActionInput(ctx, BindProposedActionInputCommand{
		ProposedActionID: exchange.Action.ProposedActionID, ExpectedVersion: exchange.Action.Version, Input: input,
	})
	assertDomainCode(t, err, "PROPOSED_ACTION_STALE")
}

func TestUnsupportedSkillReferencePersistsRejectedInvocationWithoutRun(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "rejected invocation")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "novel_to_script", Version: "1.4.0", SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "explain only"},
		Decision: agentcontract.AgentDecision{
			Reply: "not executed", Intent: "unsupported", Confidence: 1, CapabilityRef: reference,
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	var status, runID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(run_id, '') FROM skill_invocations WHERE user_message_id = ?`,
		exchange.UserMessage.MessageID,
	).Scan(&status, &runID); err != nil {
		t.Fatalf("query rejected invocation: %v", err)
	}
	if status != "rejected" || runID != "" {
		t.Fatalf("rejected invocation status=%q run=%v", status, runID)
	}
	var runCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE project_id = ?`, project.ProjectID).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("rejected invocation created %d runs", runCount)
	}
}

func TestRoutingBackfillDoesNotAttachPassiveArtifactViewToChat(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	dbPath := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(dbPath, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	project, _, active := startNovelRunForLifecycle(t, store, "routing backfill")
	if len(active.Artifacts) == 0 {
		t.Fatal("started run has no artifact")
	}
	artifactID := active.Artifacts[0].ArtifactID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content:       "hello",
			ClientContext: agentcontract.ClientContext{CurrentArtifactID: &artifactID},
		},
		Decision: agentcontract.AgentDecision{Reply: "hello", Intent: "chat", Confidence: 1},
	})
	if err != nil {
		store.Close()
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.UserMessage.Context.RoutingContext.Scope != "project" {
		store.Close()
		t.Fatalf("live routing scope = %q", exchange.UserMessage.Context.RoutingContext.Scope)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM message_routing_contexts WHERE message_id IN (?, ?)`,
		exchange.UserMessage.MessageID, exchange.AgentMessage.MessageID); err != nil {
		store.Close()
		t.Fatalf("delete routing rows: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(dbPath, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	for _, messageID := range []string{exchange.UserMessage.MessageID, exchange.AgentMessage.MessageID} {
		var scope string
		if err := reopened.db.QueryRowContext(ctx, `SELECT scope FROM message_routing_contexts WHERE message_id = ?`, messageID).Scan(&scope); err != nil {
			t.Fatalf("query backfilled routing: %v", err)
		}
		if scope != "project" {
			t.Fatalf("backfilled routing for %s = %q", messageID, scope)
		}
	}
}

func TestInspectDuringActiveRunRoutesToActiveRunInsteadOfPassiveArtifactView(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _, active := startNovelRunForLifecycle(t, store, "active inspect routing")
	if len(active.Artifacts) == 0 {
		t.Fatal("started run has no artifact")
	}
	artifactID := active.Artifacts[0].ArtifactID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content:       "我已经确认过了",
			ClientContext: agentcontract.ClientContext{CurrentArtifactID: &artifactID},
		},
		Decision: agentcontract.AgentDecision{Reply: "正在继续处理。", Intent: "inspect", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	routing := exchange.UserMessage.Context.RoutingContext
	if routing.Scope != "run" || routing.RunID == nil || *routing.RunID != active.Run.RunID || routing.ArtifactID != nil {
		t.Fatalf("inspect routing = %+v, want active run %q", routing, active.Run.RunID)
	}
}

func TestArtifactInspectDuringActiveRunKeepsExplicitArtifactContext(t *testing.T) {
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _, active := startNovelRunForLifecycle(t, store, "artifact inspect routing")
	if len(active.Artifacts) == 0 {
		t.Fatal("started run has no artifact")
	}
	artifactID := active.Artifacts[0].ArtifactID
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content:       "解释一下当前产物",
			ClientContext: agentcontract.ClientContext{CurrentArtifactID: &artifactID},
		},
		Decision: agentcontract.AgentDecision{Reply: "这是当前产物。", Intent: "inspect", Confidence: 1},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	routing := exchange.UserMessage.Context.RoutingContext
	if routing.Scope != "artifact" || routing.ArtifactID == nil || *routing.ArtifactID != artifactID {
		t.Fatalf("artifact inspect routing = %+v, want artifact %q", routing, artifactID)
	}
}
