package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func memoryRolloutFixture(source AgentMemorySnapshot) AgentMemoryRollout {
	payload := map[string]any{
		"updated_at":        "2026-09-14T00:00:00+00:00",
		"rollout_id":        sha256Hex([]byte(source.ActivityKey + ":segment-1")),
		"input":             []map[string]any{{"role": "user", "content": "PRIVATE_SOURCE_BODY"}},
		"generated_items":   []map[string]any{},
		"final_output":      "confirmed result",
		"terminal_metadata": map[string]any{"terminal_state": "completed", "exception_type": nil, "exception_message": nil, "has_final_output": true},
	}
	encoded, _ := json.Marshal(payload)
	line := string(encoded) + "\n"
	return AgentMemoryRollout{SchemaVersion: "agent_memory_rollout.v1", ActivityKey: source.ActivityKey, WorkspaceID: source.WorkspaceID, ProjectID: source.ProjectID, UserID: source.UserID, SegmentID: "segment-1", MemoryVersion: source.Version, MemoryHash: source.ContentHash, ReadEnabled: source.ReadEnabled, ContentHash: sha256Hex([]byte(line)), RolloutJSONL: line}
}

func TestMemoryRolloutPrivateFrozenSourceAndForget(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		for _, enabled := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "-empty", true: "-read"}[enabled], func(t *testing.T) {
				store, _, ctx, _ := nativeWorkspaceFixture(t, mode)
				activity, _ := AgentActivityFromContext(ctx)
				if enabled {
					if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, Files: map[string]string{"memory_summary.md": "old"}, Enabled: true, RequestID: "initial"}); err != nil {
						t.Fatal(err)
					}
				}
				source, err := store.ResolveAgentMemorySnapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				command := memoryRolloutFixture(source)
				terminalActivity := activity
				terminalActivity.AllowTerminal = true
				ctx = WithAgentActivity(ctx, terminalActivity)
				first, err := store.SaveAgentMemoryRollout(ctx, command)
				if err != nil || first.ContentHash != command.ContentHash {
					t.Fatalf("save: %+v %v", first, err)
				}
				var archiveRevision int
				var archiveGenerate bool
				var archiveGenerationID string
				if err := store.db.QueryRowContext(ctx, `SELECT archive_revision,archive_generate,archive_generation_id FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=?`, source.ActivityKey, command.SegmentID).Scan(&archiveRevision, &archiveGenerate, &archiveGenerationID); err != nil || archiveRevision != 0 || archiveGenerate || archiveGenerationID != "" {
					t.Fatalf("legacy source acquired an unproven consent receipt: %v", err)
				}
				second, err := store.SaveAgentMemoryRollout(ctx, command)
				if err != nil || first != second {
					t.Fatalf("retry: %+v %v", second, err)
				}
				read, err := store.ReadAgentMemoryRollout(ctx, command.SegmentID)
				if err != nil || !reflect.DeepEqual(read, command) {
					t.Fatalf("read: %+v %v", read, err)
				}
				changed := command
				changed.UserID = "another-user"
				_, err = store.SaveAgentMemoryRollout(ctx, changed)
				assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
				changed = command
				changed.RolloutJSONL = changed.RolloutJSONL[:len(changed.RolloutJSONL)-1] + " \n"
				changed.ContentHash = sha256Hex([]byte(changed.RolloutJSONL))
				_, err = store.SaveAgentMemoryRollout(ctx, changed)
				assertDomainCode(t, err, "IDEMPOTENCY_CONFLICT")
				_, err = store.ReadAgentMemoryRollout(mcpOwnerContext(), command.SegmentID)
				assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
				if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: source.ProjectID, ExpectedVersion: source.Version, Forget: true, RequestID: "forget"}); err != nil {
					t.Fatal(err)
				}
				var body string
				var forgotten bool
				if err := store.db.QueryRowContext(context.Background(), `SELECT envelope_json,forgotten FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=?`, source.ActivityKey, command.SegmentID).Scan(&body, &forgotten); err != nil || body != "" || !forgotten {
					t.Fatalf("forget retained source: %v", err)
				}
				_, err = store.ReadAgentMemoryRollout(ctx, command.SegmentID)
				assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
				_, err = store.SaveAgentMemoryRollout(ctx, command)
				assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			})
		}
	}
}
