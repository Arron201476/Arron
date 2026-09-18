package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
)

func TestInstalledStatefulSkillRunsFromOwnWorkflowContract(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	source := filepath.Join(
		testProjectRoot(t), "fixtures", "skills", "stateful", "story-review-workflow",
	)
	installed, err := store.InstallSkillDirectory(ctx, source, "user_stateful_runtime_test")
	if err != nil || !installed.Enabled || installed.Status != "installed" {
		t.Fatalf("InstallSkillDirectory() = %+v, error = %v", installed, err)
	}

	project, err := store.CreateProject(ctx, "动态故事评审")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "premise.txt", "一名记者发现导师隐瞒了旧案证据，必须在真相与家人安全之间选择。")
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "story_review_workflow", Version: "1.0.0", SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "commit_agent_turn",
			IdempotencyKey: "managed-workflow-proposal", RequestHash: "managed-workflow-proposal-v1",
		},
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "使用故事评审流程。", CapabilityRef: reference,
			AttachmentRefs: []agentcontract.AttachmentRef{{
				AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID,
			}},
		},
		Decision: agentcontract.AgentDecision{
			Reply: "请配置评审深度。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "collect_run_configuration", CapabilityRef: reference,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
				RequiresConfirmation: false,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	if exchange.Action == nil || exchange.Invocation == nil ||
		exchange.Action.ActionType != "collect_run_configuration" ||
		exchange.Invocation.ExecutionMode != "stateful_workflow" ||
		exchange.Invocation.Status != "awaiting_confirmation" {
		t.Fatalf("proposal exchange = %+v", exchange)
	}
	var proposedInput map[string]any
	if err := json.Unmarshal(exchange.Action.Input, &proposedInput); err != nil {
		t.Fatalf("decode proposed input: %v", err)
	}
	if proposedInput["project_id"] != project.ProjectID || proposedInput["source_type"] != "story" {
		t.Fatalf("standard Skill input identity = %#v", proposedInput)
	}
	assets, _ := proposedInput["assets"].([]any)
	if len(assets) != 1 || assets[0].(map[string]any)["asset_id"] != asset.Asset.AssetID ||
		assets[0].(map[string]any)["asset_snapshot_id"] != asset.Snapshot.AssetSnapshotID {
		t.Fatalf("standard Skill assets = %#v", proposedInput["assets"])
	}

	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "configure_proposed_action",
			IdempotencyKey: "managed-workflow-config", RequestHash: "managed-workflow-config-v1",
		},
		ProposedActionID: exchange.Action.ProposedActionID,
		ExpectedVersion:  exchange.Action.Version,
		Input:            exchange.Action.Input,
		Config:           json.RawMessage(`{"review_depth":"deep"}`),
	})
	if err != nil {
		t.Fatalf("ConfigureProposedAction() error = %v", err)
	}
	if configured.ActionType != "start_run" || configured.Version != exchange.Action.Version+1 ||
		!configured.RequiresConfirmation || !strings.Contains(string(configured.Config), `"config_ref":"review"`) {
		t.Fatalf("configured action = %+v", configured)
	}

	started, err := store.StartRun(ctx, StartRunCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "start_run",
			IdempotencyKey: "managed-workflow-start", RequestHash: "managed-workflow-start-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version,
		RunKind: "generation", Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID:    configured.ConfirmationMessageID,
		ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if started.Run.Status != "running" || len(started.Steps) != 1 ||
		started.Steps[0].StepID != "create_review" || started.Steps[0].Status != "running" ||
		len(started.Artifacts) != 0 || started.CurrentApproval != nil {
		t.Fatalf("started run = %+v", started)
	}
	tasks, err := store.ListTaskItems(ctx, started.Steps[0].StepRunID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != "pending" {
		t.Fatalf("tasks = %+v, error = %v", tasks, err)
	}

	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID: "sdk-managed-workflow", ExecutorIDs: []string{"worker.structured_content"},
		ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil {
		t.Fatalf("ClaimExecutionTask() = %+v, error = %v", claim, err)
	}
	pack := claim.ContextPack
	if len(pack.AssetContext) != 1 || pack.AssetContext[0].AssetSnapshotID != asset.Snapshot.AssetSnapshotID ||
		pack.RunInputSnapshot == nil || pack.SkillInstructions == nil ||
		!strings.Contains(pack.SkillInstructions.Content, "central conflict") ||
		len(pack.UpstreamContext) != 0 || pack.OutputContract.ArtifactType != "generic_document" {
		t.Fatalf("dynamic context pack = %+v", pack)
	}
	if pack.ProviderResultContract == nil || pack.ProviderResultContract.ArtifactType != "provider_result" ||
		pack.ProviderResultContract.SchemaRef != pack.OutputContract.SchemaRef ||
		string(pack.ProviderResultContract.Schema) != string(pack.OutputContract.Schema) {
		t.Fatalf("direct Skill transport must match its artifact schema: %+v", pack.ProviderResultContract)
	}

	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   json.RawMessage(`{"title":"故事评审","content":"核心冲突成立，但结尾回报需要更清晰。"}`),
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
	}
	committed, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
		AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult() error = %v", err)
	}
	if committed.Artifact.ArtifactType != "generic_document" ||
		committed.ArtifactVersion.Status != "pending_approval" ||
		committed.RunSnapshot.Run.Status != "waiting_approval" || committed.Approval.ApprovalRequestID == "" {
		t.Fatalf("committed = %+v", committed)
	}
	var assetDependencyCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifact_dependencies
		WHERE downstream_artifact_version_id = ? AND upstream_kind = 'asset_snapshot'
			AND upstream_ref_id = ?`,
		committed.ArtifactVersion.ArtifactVersionID, asset.Snapshot.AssetSnapshotID,
	).Scan(&assetDependencyCount); err != nil || assetDependencyCount != 1 {
		t.Fatalf("asset dependency count = %d, error = %v", assetDependencyCount, err)
	}

	completed, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resolve_approval",
			IdempotencyKey: "managed-workflow-approval", RequestHash: "managed-workflow-approval-v1",
		},
		ApprovalRequestID: committed.Approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: committed.Approval.Version,
		SubjectSnapshotHash:     committed.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval() error = %v", err)
	}
	if completed.Run.Status != "completed" || completed.Steps[0].Status != "completed" {
		t.Fatalf("completed run = %+v", completed)
	}
	freshInvocation, err := scanSkillInvocation(store.db.QueryRowContext(ctx, skillInvocationSelect+`
		WHERE skill_invocation_id = ?`, exchange.Invocation.SkillInvocationID))
	if err != nil || freshInvocation.Status != "completed" || freshInvocation.RunID == nil ||
		*freshInvocation.RunID != started.Run.RunID || freshInvocation.AgentTaskID != nil {
		t.Fatalf("invocation = %+v, error = %v", freshInvocation, err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE skill_invocations SET agent_task_id = 'agt_invalid'
		WHERE skill_invocation_id = ?`, freshInvocation.SkillInvocationID); err == nil ||
		!strings.Contains(err.Error(), "cannot bind both Agent Task and Run") {
		t.Fatalf("dual execution binding error = %v", err)
	}
	var uniqueIndex, partialIndex int
	if err := store.db.QueryRowContext(ctx, `
		SELECT "unique", partial FROM pragma_index_list('skill_invocations')
		WHERE name = 'idx_skill_invocations_run_unique'`).Scan(&uniqueIndex, &partialIndex); err != nil ||
		uniqueIndex != 1 || partialIndex != 1 {
		t.Fatalf("Run binding index unique=%d partial=%d, error=%v", uniqueIndex, partialIndex, err)
	}
}

func TestInstalledStatefulSkillUsesSelectedGenericArtifactAsExactSource(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	source := filepath.Join(
		testProjectRoot(t), "fixtures", "skills", "stateful", "story-review-workflow",
	)
	installed, err := store.InstallSkillDirectory(ctx, source, "user_stateful_artifact_source")
	if err != nil || !installed.Enabled {
		t.Fatalf("InstallSkillDirectory() = %+v, error = %v", installed, err)
	}
	project, err := store.CreateProject(ctx, "动态 Skill 读取通用产物")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, err := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "create_generic_artifact",
			IdempotencyKey: "managed-artifact-source", RequestHash: "managed-artifact-source-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "故事梗概",
			Payload: json.RawMessage(`{"content":"记者在导师遗物中发现一份被篡改的旧案记录。"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateGenericArtifact() error = %v", err)
	}
	versionID := artifact.CurrentVersionID
	reference := &agentcontract.CapabilityRef{
		CapabilityID: installed.CapabilityID, Version: "1.0.0", SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "用故事评审 Skill 检查当前梗概。", CapabilityRef: reference,
			ClientContext: agentcontract.ClientContext{
				CurrentArtifactID: &artifact.ArtifactID, CurrentArtifactVersionID: &versionID,
			},
		},
		Decision: agentcontract.AgentDecision{
			Reply: "请配置评审深度。", Intent: "propose_capability", Confidence: 1,
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
	var proposed sourceInputRequest
	if exchange.Action == nil || json.Unmarshal(exchange.Action.Input, &proposed) != nil ||
		len(proposed.ArtifactVersions) != 1 ||
		proposed.ArtifactVersions[0].ArtifactVersionID != versionID ||
		proposed.ArtifactVersions[0].Role != "primary_source" ||
		proposed.UserRequestMessageID != exchange.UserMessage.MessageID {
		t.Fatalf("managed artifact input = %+v", exchange.Action)
	}
	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		ProposedActionID: exchange.Action.ProposedActionID, ExpectedVersion: exchange.Action.Version,
		Input: exchange.Action.Input, Config: json.RawMessage(`{"review_depth":"deep"}`),
	})
	if err != nil {
		t.Fatalf("ConfigureProposedAction() error = %v", err)
	}
	started, err := store.StartRun(ctx, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: installed.CapabilityID, CapabilityVersion: reference.Version,
		RunKind: "generation", Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID:    configured.ConfirmationMessageID,
		ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID: "sdk-managed-artifact", ExecutorIDs: []string{"worker.structured_content"},
		ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil {
		t.Fatalf("ClaimExecutionTask() = %+v, error = %v", claim, err)
	}
	if len(claim.ContextPack.AssetContext) != 0 || len(claim.ContextPack.UpstreamContext) != 1 {
		t.Fatalf("managed artifact context = %+v", claim.ContextPack)
	}
	upstream := claim.ContextPack.UpstreamContext[0]
	if upstream.ArtifactVersionID != versionID || upstream.ArtifactID != artifact.ArtifactID ||
		upstream.ArtifactType != "generic_document" || upstream.SelectionPolicy != "explicit_source" ||
		!strings.Contains(string(upstream.Content), "被篡改的旧案记录") {
		t.Fatalf("managed artifact source = %+v", upstream)
	}
	var sealedInput string
	if err := store.db.QueryRowContext(ctx, `
		SELECT payload_json FROM run_input_snapshot_versions
		WHERE run_input_snapshot_version_id = ? AND status = 'sealed'`,
		started.Run.CurrentInputSnapshotVersionID,
	).Scan(&sealedInput); err != nil || !strings.Contains(sealedInput, versionID) {
		t.Fatalf("sealed managed input = %q, error = %v", sealedInput, err)
	}
}

func TestManagedStatefulSkillPinsArchivedVersionAcrossUpgrade(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	v1Source := filepath.Join(
		testProjectRoot(t), "fixtures", "skills", "stateful", "story-review-workflow",
	)
	installed, err := store.InstallSkillDirectory(ctx, v1Source, "user_stateful_v1")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, "状态化版本固定")
	if err != nil {
		t.Fatal(err)
	}
	helperV1 := writeSkillTestPackage(t, t.TempDir(), "workflow-helper", "workflow_helper", "1.0.0", "inline", "HELPER_AT_ORIGIN_TURN")
	if _, err := store.InstallSkillDirectory(ctx, helperV1, "fixture"); err != nil {
		t.Fatal(err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "premise.txt", "记者发现一份被隐藏的证据。")
	reference := &agentcontract.CapabilityRef{
		CapabilityID: installed.CapabilityID, Version: "1.0.0", SelectionMode: "explicit",
	}
	request := agentcontract.MessageRequest{
		Content: "使用故事评审流程。", CapabilityRef: reference,
		AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}},
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, CommandMeta{IdempotencyKey: "frozen-workflow-turn", RequestHash: "frozen-workflow-turn", CommandType: "create_message"})
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(snapshotTurnContext(turn), CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        request,
		Decision: agentcontract.AgentDecision{
			Reply: "请配置评审深度。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "collect_run_configuration", CapabilityRef: reference,
				Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`),
				RequiresConfirmation: false,
			},
		},
	})
	if err != nil || exchange.Action == nil {
		t.Fatalf("CreateMessageExchange() = %+v, error = %v", exchange, err)
	}
	helperV2 := writeSkillTestPackage(t, t.TempDir(), "workflow-helper", "workflow_helper", "2.0.0", "inline", "HELPER_AFTER_ORIGIN_TURN")
	if _, err := store.InstallSkillDirectory(ctx, helperV2, "fixture"); err != nil {
		t.Fatal(err)
	}

	v2Source := filepath.Join(t.TempDir(), "story-review-workflow")
	if err := copySkillTree(v1Source, v2Source); err != nil {
		t.Fatal(err)
	}
	setJSONFileFieldsForTest(t, filepath.Join(v2Source, "content-agent", "manifest.json"), map[string]any{"version": "2.0.0"})
	setJSONFileFieldsForTest(t, filepath.Join(v2Source, "content-agent", "workflow.json"), map[string]any{"version": "2.0.0"})
	if err := os.WriteFile(
		filepath.Join(v2Source, "schemas", "config.json"),
		[]byte(`{"$defs":{"config":{"type":"object","properties":{"review_depth":{"type":"string","enum":["v2_only"],"default":"v2_only"}}}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(v2Source, "SKILL.md")
	skillData, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	skillData = append(skillData, []byte("\nV2_ONLY_INSTRUCTION_MARKER\n")...)
	if err := os.WriteFile(skillFile, skillData, 0o644); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.InstallSkillDirectory(ctx, v2Source, "user_stateful_v2")
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRegistrySkillVersion(t, store, installed.CapabilityID, "2.0.0", true)
	if activeSkillVersionForTest(t, upgraded).Version != "2.0.0" {
		t.Fatalf("upgraded = %+v", upgraded)
	}
	assertProjectedReviewDepthOptions(t, store, project.ProjectID, installed.CapabilityID, "1.0.0", []string{"concise", "standard", "deep"})

	assertExecutionSkillBinding(t, store, "skill_invocations", "skill_invocation_id", exchange.Invocation.SkillInvocationID, *installed.ActiveVersionID)
	shadowExecutionSkillForTest(t, store, project.ProjectID, v1Source, installed.CapabilityID)
	configured, err := store.ConfigureProposedAction(ctx, ConfigureProposedActionCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "configure_proposed_action",
			IdempotencyKey: "pinned-stateful-config", RequestHash: "pinned-stateful-config-v1",
		},
		ProposedActionID: exchange.Action.ProposedActionID,
		ExpectedVersion:  exchange.Action.Version,
		Input:            exchange.Action.Input,
		Config:           json.RawMessage(`{"review_depth":"deep"}`),
	})
	if err != nil {
		t.Fatalf("ConfigureProposedAction() error = %v", err)
	}
	started, err := store.StartRun(ctx, StartRunCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "start_run",
			IdempotencyKey: "pinned-stateful-start", RequestHash: "pinned-stateful-start-v1",
		},
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: reference.CapabilityID, CapabilityVersion: reference.Version,
		RunKind: "generation", Input: configured.Input, Config: configured.Config, Confirmed: true,
		ProposedActionID: configured.ProposedActionID, ProposedActionVersion: configured.Version,
		ConfirmationMessageID:    configured.ConfirmationMessageID,
		ConfirmationSnapshotHash: configured.SnapshotHash,
	})
	if err != nil {
		t.Fatalf("StartRun(v1 after v2 activation) error = %v", err)
	}
	if started.Run.CapabilityVersion != "1.0.0" {
		t.Fatalf("run version = %s", started.Run.CapabilityVersion)
	}
	assertExecutionSkillBinding(t, store, "runs", "run_id", started.Run.RunID, *installed.ActiveVersionID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open(after upgrade) error = %v", err)
	}
	assertProjectedReviewDepthOptions(t, store, project.ProjectID, installed.CapabilityID, "1.0.0", []string{"concise", "standard", "deep"})
	shadowExecutionSkillForTest(t, store, project.ProjectID, v1Source, installed.CapabilityID)
	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID: "sdk-pinned-workflow", ExecutorIDs: []string{"worker.structured_content"},
		ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil || claim.ContextPack.SkillInstructions == nil {
		t.Fatalf("ClaimExecutionTask() = %+v, error = %v", claim, err)
	}
	instructions := claim.ContextPack.SkillInstructions.Content
	if !strings.Contains(instructions, "central conflict") || strings.Contains(instructions, "V2_ONLY_INSTRUCTION_MARKER") || strings.Contains(instructions, "SHADOW_SAME_VERSION_DO_NOT_EXECUTE") {
		t.Fatalf("pinned instructions = %q", instructions)
	}
	activityCtx := statefulCatalogContext(project, claim)
	assertSnapshotInstructions(t, store, activityCtx, project.ProjectID, "workflow_helper", "1.0.0", "HELPER_AT_ORIGIN_TURN")
	if _, ok, err := store.capabilityEntryForProjectVersion(activityCtx, project.ProjectID, "workflow_helper", "2.0.0"); err != nil || ok {
		t.Fatalf("stateful helper escaped origin version: found=%v %v", ok, err)
	}
	page, err := store.ReadSkillResource(activityCtx, project.ProjectID, reference.CapabilityID, reference.Version, "SKILL.md", 0, 16000)
	if err != nil || !strings.Contains(page.Content, "central conflict") || strings.Contains(page.Content, "SHADOW_SAME_VERSION_DO_NOT_EXECUTE") {
		t.Fatalf("stateful tool lost confirmed primary package: %+v %v", page, err)
	}
	if _, err := store.SetSkillInstallationEnabled(ctx, installed.SkillInstallationID, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSkillResource(activityCtx, project.ProjectID, reference.CapabilityID, reference.Version, "SKILL.md", 0, 1000); err == nil {
		t.Fatal("disabled primary Skill fell through to a same-version shadow")
	}
}

func assertProjectedReviewDepthOptions(
	t *testing.T, store *Store, projectID, capabilityID, version string, want []string,
) {
	t.Helper()
	definition, ok, err := store.CapabilityDefinitionForProject(
		context.Background(), projectID, capabilityID, version, "",
	)
	if err != nil || !ok {
		t.Fatalf("CapabilityDefinitionForProject() ok = %v, error = %v", ok, err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(definition.ConfigSchemas["review"], &schema); err != nil {
		t.Fatalf("decode projected config schema: %v", err)
	}
	if !slices.Equal(schema.Properties["review_depth"].Enum, want) {
		t.Fatalf("projected review_depth options = %v, want %v", schema.Properties["review_depth"].Enum, want)
	}
}

func TestInstalledStatefulSkillRejectsInputAndConfigOutsideOwnSchemas(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadManagedWorkflowRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "动态合同校验")
	if err != nil {
		t.Fatal(err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "premise.txt", "测试梗概")
	entry, ok := store.registry.Get("story_review_workflow")
	if !ok {
		t.Fatal("stateful Skill missing")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, _, _, err = store.prepareManagedWorkflowInputTx(
		ctx, tx, project.ProjectID, entry,
		json.RawMessage(`{"project_id":"`+project.ProjectID+`","source_type":"story","assets":[{"asset_id":"`+asset.Asset.AssetID+`","asset_snapshot_id":"`+asset.Snapshot.AssetSnapshotID+`","role":"primary_source","order":1}],"user_request_message_id":"missing_message","platform_field":true}`), "",
	)
	assertDomainCode(t, err, "CAPABILITY_INPUT_INVALID")
	_, err = normalizeManagedWorkflowConfig(entry, json.RawMessage(`{"review_depth":"unbounded"}`))
	assertDomainCode(t, err, "CAPABILITY_CONFIG_INVALID")
}

func loadManagedWorkflowRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	root := testProjectRoot(t)
	registry, err := capability.LoadRegistry(capability.LoadOptions{
		ProjectRoot: root,
		SkillRoots: []capability.SkillRoot{{
			Scope: capability.SkillScopeWorkspace,
			Path:  filepath.Join(root, "fixtures", "skills", "stateful"), Priority: 200,
		}},
	})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	return registry
}

func testProjectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if override := os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT"); override != "" {
		root = override
	}
	return root
}

func setJSONFileFieldsForTest(t *testing.T, file string, fields map[string]any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		document[key] = value
	}
	data, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
