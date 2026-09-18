package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func episodeApprovalState(t *testing.T, store *Store, approval Approval) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(
		(SELECT COUNT(*) FROM run_config_snapshots), (SELECT COUNT(*) FROM events),
		(SELECT COUNT(*) FROM idempotency_records),
		(SELECT json_group_array(json_array(status, confirmed_at)) FROM artifact_versions),
		(SELECT json_group_array(json_array(status, updated_at)) FROM runs),
		(SELECT json_group_array(status) FROM step_runs),
		(SELECT json_group_array(status) FROM task_items),
		status, resolved_at, resolution_json) FROM approvals WHERE approval_request_id=?`, approval.ApprovalRequestID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestEpisodeApprovalCommitsModeAndRestoresOriginalReceipt(t *testing.T) {
	for _, test := range []struct{ name, payload, mode string }{
		{"omitted", "", ""},
		{"null-payload", "null", ""},
		{"empty-payload", "{}", ""},
		{"continuous", `{"episode_execution_mode":"continuous"}`, episodeExecutionModeContinuous},
		{"review-each", `{"episode_execution_mode":"review_each"}`, episodeExecutionModeReviewEach},
	} {
		t.Run(test.name, func(t *testing.T) {
			requested := test.mode
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "episode-approval.db")
			registry := loadTestRegistry(t)
			store, err := Open(path, registry)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			_, approval, scriptID, handoffID := prepareEpisodeCheckpoint(t, store)
			if _, err := store.SetEpisodeExecutionMode(ctx, SetEpisodeExecutionModeCommand{RunID: approval.RunID, Mode: episodeExecutionModeReviewEach}); err != nil {
				t.Fatal(err)
			}
			command := ResolveApprovalCommand{CommandMeta: CommandMeta{Scope: approval.ProjectID, CommandType: "resolve_approval", IdempotencyKey: "original", RequestHash: "original:" + test.payload}, ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash, ResolutionPayload: json.RawMessage(test.payload)}
			result, err := store.ResolveApproval(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			expected := requested
			if expected == "" {
				expected = episodeExecutionModeReviewEach
			}
			if result.Run.RunID != approval.RunID || result.Run.EpisodeExecutionMode != expected || result.Run.Status != "paused" {
				t.Fatalf("confirmation result: %+v", result.Run)
			}
			var confirmed, configs int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_version_id IN (?,?) AND status='confirmed'`, scriptID, handoffID).Scan(&confirmed); err != nil || confirmed != 2 {
				t.Fatalf("confirmed outputs=%d err=%v", confirmed, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_config_snapshots WHERE run_id=? AND config_ref=?`, approval.RunID, episodeExecutionConfigRef).Scan(&configs); err != nil {
				t.Fatal(err)
			}
			wantConfigs := 1
			if requested != "" {
				wantConfigs++
				resolved, err := store.GetApproval(ctx, approval.ApprovalRequestID)
				if err != nil {
					t.Fatal(err)
				}
				var receipt struct {
					Mode     string `json:"episode_execution_mode"`
					ConfigID string `json:"execution_config_snapshot_id"`
				}
				if json.Unmarshal(resolved.Resolution, &receipt) != nil || receipt.Mode != requested || receipt.ConfigID == "" {
					t.Fatalf("missing atomic mode receipt: %s", resolved.Resolution)
				}
				var stored string
				if err := store.db.QueryRow(`SELECT json_extract(payload_json,'$.episode_execution_mode') FROM run_config_snapshots WHERE config_snapshot_id=? AND run_id=? AND config_ref=?`, receipt.ConfigID, approval.RunID, episodeExecutionConfigRef).Scan(&stored); err != nil || stored != requested {
					t.Fatalf("mode receipt target=%q err=%v", stored, err)
				}
			}
			if configs != wantConfigs {
				t.Fatalf("mode snapshots=%d want=%d", configs, wantConfigs)
			}
			laterMode := episodeExecutionModeContinuous
			if expected == laterMode {
				laterMode = episodeExecutionModeReviewEach
			}
			if _, err := store.SetEpisodeExecutionMode(ctx, SetEpisodeExecutionModeCommand{RunID: approval.RunID, Mode: laterMode}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path, registry)
			if err != nil {
				t.Fatal(err)
			}
			store = reopened
			before := episodeApprovalState(t, store, approval)
			repeated, err := store.ResolveApproval(ctx, command)
			if err != nil || repeated.Run.EpisodeExecutionMode != expected {
				t.Fatalf("original receipt=%+v err=%v", repeated.Run, err)
			}
			current, err := store.GetRun(ctx, approval.RunID)
			if err != nil || current.EpisodeExecutionMode != laterMode {
				t.Fatalf("replay changed later mode=%q err=%v", current.EpisodeExecutionMode, err)
			}
			if after := episodeApprovalState(t, store, approval); after != before {
				t.Fatal("receipt recovery changed durable state")
			}
			command.RequestHash = "different-payload"
			_, err = store.ResolveApproval(ctx, command)
			assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
		})
	}
}

func TestEpisodeApprovalFailureDoesNotChangeModeOrConfirmOutputs(t *testing.T) {
	for _, scenario := range []string{"invalid-mode", "null-mode", "number-mode", "invalid-payload", "wrong-scope", "stale-approval", "stale-handoff", "failed-next-task", "paused-run", "write-failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "episode-approval.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, approval, _, handoffID := prepareEpisodeCheckpoint(t, store)
			if _, err := store.SetEpisodeExecutionMode(ctx, SetEpisodeExecutionModeCommand{RunID: approval.RunID, Mode: episodeExecutionModeReviewEach}); err != nil {
				t.Fatal(err)
			}
			command := ResolveApprovalCommand{CommandMeta: CommandMeta{Scope: approval.ProjectID, CommandType: "resolve_approval", IdempotencyKey: "original", RequestHash: "original"}, ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash, ResolutionPayload: json.RawMessage(`{"episode_execution_mode":"continuous"}`)}
			switch scenario {
			case "invalid-mode":
				command.ResolutionPayload = json.RawMessage(`{"episode_execution_mode":"automatic"}`)
			case "null-mode":
				command.ResolutionPayload = json.RawMessage(`{"episode_execution_mode":null}`)
			case "number-mode":
				command.ResolutionPayload = json.RawMessage(`{"episode_execution_mode":1}`)
			case "invalid-payload":
				command.ResolutionPayload = json.RawMessage(`[]`)
			case "stale-approval":
				command.ExpectedApprovalVersion++
			case "wrong-scope":
				_, err = store.db.Exec(`UPDATE approvals SET scope='batch_step' WHERE approval_request_id=?`, approval.ApprovalRequestID)
			case "stale-handoff":
				_, err = store.db.Exec(`UPDATE artifact_versions SET payload_json='{"script_artifact_version_id":"other"}' WHERE artifact_version_id=?`, handoffID)
			case "failed-next-task":
				_, err = store.db.Exec(`UPDATE task_items SET status='failed' WHERE step_run_id=? AND item_key='episode:2'`, approval.StepRunID)
			case "paused-run":
				_, err = store.db.Exec(`UPDATE runs SET status='paused' WHERE run_id=?`, approval.RunID)
			case "write-failure":
				_, err = store.db.Exec(`CREATE TRIGGER reject_episode_approval BEFORE UPDATE OF status ON approvals WHEN NEW.status='approved' BEGIN SELECT RAISE(ABORT,'fixture approval write failure'); END`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := episodeApprovalState(t, store, approval)
			if _, err := store.ResolveApproval(ctx, command); err == nil {
				t.Fatal("invalid confirmation succeeded")
			}
			if after := episodeApprovalState(t, store, approval); after != before {
				t.Fatalf("failed confirmation changed state: %s -> %s", before, after)
			}
			run, err := store.GetRun(ctx, approval.RunID)
			if err != nil || run.EpisodeExecutionMode != episodeExecutionModeReviewEach {
				t.Fatalf("failed confirmation changed mode=%q err=%v", run.EpisodeExecutionMode, err)
			}
		})
	}
}
