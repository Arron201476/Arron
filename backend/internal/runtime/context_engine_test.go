package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestBuildAgentTurnContextRetrievesHistoryAndPersistsSummary(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "长期对话")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	for index := 0; index < 16; index++ {
		content := "普通讨论"
		if index == 0 {
			content = "请记住，这个作品的内部代号是北辰。"
		}
		if _, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, content); err != nil {
			t.Fatalf("CreateUserMessage(%d) error = %v", index, err)
		}
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	request := agentcontract.MessageRequest{Content: "我最开始说的作品代号是什么？"}
	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, request)
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest() error = %v", err)
	}
	turn, err := store.BuildAgentTurnContext(
		ctx, project.ProjectID, project.PrimaryConversationID, messages, request, runtimeContext,
	)
	if err != nil {
		t.Fatalf("BuildAgentTurnContext() error = %v", err)
	}
	if len(turn.RecentMessages) != agentRecentMessageLimit {
		t.Fatalf("recent messages = %d", len(turn.RecentMessages))
	}
	if turn.MemoryContext.ConversationSummary == nil ||
		turn.MemoryContext.ConversationSummary.CoveredMessageCount != 4 {
		t.Fatalf("conversation summary = %+v", turn.MemoryContext.ConversationSummary)
	}
	found := false
	for _, message := range turn.MemoryContext.RetrievedMessages {
		if message.Content == "请记住，这个作品的内部代号是北辰。" {
			found = true
		}
	}
	if !found {
		t.Fatalf("retrieved messages = %+v", turn.MemoryContext.RetrievedMessages)
	}
	var summaries int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM conversation_summaries WHERE project_id = ?`, project.ProjectID).Scan(&summaries); err != nil || summaries != 1 {
		t.Fatalf("summary rows = %d, error = %v", summaries, err)
	}
}

func TestRetrieveProjectMemoryOmitsLargeDecisionPayloads(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "长项目记忆")
	if err != nil {
		t.Fatal(err)
	}
	now := formatTime(store.now())
	payload, err := json.Marshal(map[string]string{
		"instruction":   "开始重新生成",
		"worker_output": strings.Repeat("不应进入控制 Agent 的正文", 20000),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO project_memory_entries(
			memory_entry_id, project_id, kind, scope, content, payload_json,
			source_kind, source_ref_id, source_hash, confidence, status,
			created_at, updated_at
		) VALUES(?, ?, 'confirmed_decision', 'run:test', ?, ?,
			'decision_snapshot', 'decision:test', 'hash', 1, 'active', ?, ?)`,
		store.newID("mem"), project.ProjectID,
		"开始 "+strings.Repeat("摘要", 2000), string(payload), now, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := store.retrieveProjectMemory(ctx, project.ProjectID, "开始", agentMemoryEntryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("memory entries = %d, want 1", len(entries))
	}
	if len(entries[0].Payload) != 0 {
		t.Fatalf("decision payload leaked into Agent context: %d bytes", len(entries[0].Payload))
	}
	if len(entries[0].Content) > agentMemoryEntryTextLimit {
		t.Fatalf("decision summary = %d bytes, limit = %d", len(entries[0].Content), agentMemoryEntryTextLimit)
	}
}

func TestRegenerationArtifactContextCarriesReferencesWithoutWorkerPayloads(t *testing.T) {
	rows := make([]agentArtifactRow, 0, 77)
	for episode := 1; episode <= 77; episode++ {
		rows = append(rows, agentArtifactRow{
			ArtifactID: "artifact", ArtifactVersionID: "version", RunID: "run",
			CapabilityID: "novel_to_script", ArtifactType: "script_unit",
			ScopeKey: "episode:" + strconv.Itoa(episode), Status: "confirmed", Version: 1,
			Payload: json.RawMessage(`{"script_text":"` + strings.Repeat("worker正文", 4000) + `"}`),
		})
	}

	references, truncated := materializeArtifactReferences(rows)
	if len(references) != maxFocusedArtifacts || !truncated {
		t.Fatalf("references = %d, truncated = %v", len(references), truncated)
	}
	for _, reference := range references {
		if len(reference.Payload) != 0 || reference.PayloadExcerpt != "" {
			t.Fatalf("worker payload entered regeneration context: %+v", reference)
		}
	}
}

func TestDynamicGenericArtifactCatalogAndTargeting(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "通用产物定位")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	create := func(title string) Artifact {
		artifact, createErr := store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
			ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
			Draft: agentcontract.ArtifactDraft{
				ArtifactType: "generic_document", Title: title,
				Payload: json.RawMessage(`{"content":"测试内容"}`),
			},
		})
		if createErr != nil {
			t.Fatalf("CreateGenericArtifact(%q) error = %v", title, createErr)
		}
		return artifact
	}
	outline := create("大纲")
	episode := create("小说第一集")

	request := agentcontract.MessageRequest{Content: "把大纲里的冲突再加强一点"}
	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, request)
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest() error = %v", err)
	}
	if len(runtimeContext.ArtifactSets) != 2 {
		t.Fatalf("artifact sets = %+v", runtimeContext.ArtifactSets)
	}
	if len(runtimeContext.FocusedArtifacts) != 1 ||
		runtimeContext.FocusedArtifacts[0].ArtifactID != outline.ArtifactID ||
		runtimeContext.FocusedArtifacts[0].ArtifactLabel != "大纲" {
		t.Fatalf("focused artifacts = %+v", runtimeContext.FocusedArtifacts)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolution, err := store.buildTargetResolutionTx(
		ctx, tx, project.ProjectID, project.PrimaryConversationID, "msg_test", request, nil, store.now(),
	)
	if err != nil {
		t.Fatalf("buildTargetResolutionTx() error = %v", err)
	}
	if resolution.Status != "resolved" || resolution.ArtifactID == nil || *resolution.ArtifactID != outline.ArtifactID {
		t.Fatalf("target resolution = %+v", resolution)
	}

	resolution, err = store.buildTargetResolutionTx(
		ctx, tx, project.ProjectID, project.PrimaryConversationID, "msg_sdk_target",
		agentcontract.MessageRequest{Content: "把大纲写得更详细，但实际目标由 SDK 工具选择"},
		&agentcontract.TargetRef{
			TargetType: "artifact", TargetID: episode.ArtifactID,
			ArtifactVersionID: episode.CurrentVersionID,
		},
		store.now(),
	)
	if err != nil {
		t.Fatalf("SDK target buildTargetResolutionTx() error = %v", err)
	}
	if resolution.Status != "resolved" || resolution.Source != "agent_tool_selection" ||
		resolution.ArtifactID == nil || *resolution.ArtifactID != episode.ArtifactID {
		t.Fatalf("SDK target resolution = %+v", resolution)
	}
}

func TestBuildAgentTurnContextIndexesArtifactsAsRebuildableMemory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "产物记忆")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateGenericArtifact(ctx, CreateGenericArtifactCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{
			ArtifactType: "generic_document", Title: "人物关系表",
			Payload: json.RawMessage(`{"content":"林舟与周岚是盟友。"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := agentcontract.MessageRequest{Content: "查看人物关系表"}
	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, request)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BuildAgentTurnContext(
		ctx, project.ProjectID, project.PrimaryConversationID, nil, request, runtimeContext,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.MemoryContext.Entries) != 1 || turn.MemoryContext.Entries[0].Kind != "artifact_catalog" ||
		turn.MemoryContext.Entries[0].Content == "" {
		t.Fatalf("memory entries = %+v", turn.MemoryContext.Entries)
	}
}
