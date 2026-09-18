package runtime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func externalCheckpointForTest(t *testing.T, calls ...AgentToolCall) json.RawMessage {
	t.Helper()
	items := []map[string]any{}
	for _, call := range calls {
		origin := map[string]any{"type": "mcp", "mcp_server_name": *call.ServerID}
		marker, err := json.Marshal(nativeOutcomeMarker{"agent_tool_outcome.v1", "outcome_unknown", call.AgentToolCallID, call.SDKToolCallID, call.ToolID, call.ArgumentsHash, true})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, map[string]any{"type": "tool_call_item", "tool_name": "mcp_fixture_save_fact", "tool_origin": origin,
			"raw_item": map[string]any{"type": "function_call", "name": "mcp_fixture_save_fact", "call_id": call.SDKToolCallID, "arguments": string(call.ArgumentsSummary)}},
			map[string]any{"type": "tool_call_output_item", "tool_origin": origin, "raw_item": map[string]any{"type": "function_call_output", "call_id": call.SDKToolCallID,
				"output": []any{map[string]any{"type": "input_text", "text": string(marker)}}}})
	}
	raw, err := json.Marshal(map[string]any{"$schemaVersion": "1.16", "current_turn": 2, "max_turns": 8, "current_step": map[string]string{"type": "next_step_run_again"}, "generated_items": items})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestExternalToolCheckpointBindsEveryUnknownCall(t *testing.T) {
	server, code, now := "fixture", "MCP_TOOL_OUTCOME_UNKNOWN", time.Now()
	_, hash, _, err := summarizeAgentToolPayload(json.RawMessage(`{"value":"safe"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	first := AgentToolCall{AgentToolCallID: "tcall-first", SDKToolCallID: "first", ToolID: "mcp:fixture/save_fact", ToolKind: "mcp", ServerID: &server,
		Status: "failed", StartedAt: &now, ErrorCode: &code, ArgumentsSummary: json.RawMessage(`{"value":"safe"}`), ArgumentsHash: hash}
	second := first
	second.AgentToolCallID, second.SDKToolCallID = "tcall-second", "second"
	raw := externalCheckpointForTest(t, first, second)
	if err := validateExternalToolCheckpoint(raw, []AgentToolCall{first, second}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"lost_batch":              func(s map[string]any) { s["generated_items"] = []any{} },
		"raw_exception":           func(s map[string]any) { s["current_step"] = nil },
		"exhausted":               func(s map[string]any) { s["current_turn"] = 8 },
		"missing_parallel_output": func(s map[string]any) { s["generated_items"] = s["generated_items"].([]any)[:3] },
		"duplicate_call": func(s map[string]any) {
			items := s["generated_items"].([]any)
			s["generated_items"] = append(items, items[0])
		},
		"wrong_arguments": func(s map[string]any) {
			s["generated_items"].([]any)[0].(map[string]any)["raw_item"].(map[string]any)["arguments"] = `{"value":"other"}`
		},
		"wrong_origin": func(s map[string]any) {
			s["generated_items"].([]any)[0].(map[string]any)["tool_origin"].(map[string]any)["mcp_server_name"] = "other"
		},
		"provider_success": func(s map[string]any) {
			s["generated_items"].([]any)[1].(map[string]any)["raw_item"].(map[string]any)["output"] = "success"
		},
		"wrong_receipt": func(s map[string]any) {
			s["generated_items"].([]any)[1].(map[string]any)["raw_item"].(map[string]any)["output"] = []any{map[string]any{"type": "input_text", "text": `{"schema_version":"agent_tool_outcome.v1","status":"outcome_unknown","agent_tool_call_id":"other"}`}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var s map[string]any
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			mutate(s)
			changed, _ := json.Marshal(s)
			assertDomainCode(t, validateExternalToolCheckpoint(changed, []AgentToolCall{first, second}), "SDK_TOOL_REPLAY_RISK")
		})
	}
}

func TestExternalToolFactInputIsAtomicImmutableAndScoped(t *testing.T) {
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			store, project, call, _ := outcomeReviewFixture(t, mode)
			defer store.Close()
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			view, err := store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
			if err != nil {
				t.Fatal(err)
			}
			call, err = store.GetAgentToolCall(ctx, call.AgentToolCallID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stateful_workflow" {
				if _, err := store.db.Exec(`UPDATE execution_attempts SET provider_id='openai_agents_sdk' WHERE attempt_id=?`, view.ExecutionID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE task_items SET status='paused' WHERE current_attempt_id=?`, view.ExecutionID); err != nil {
					t.Fatal(err)
				}
			}
			raw := externalCheckpointForTest(t, call)
			prepare := func(ctx context.Context) error {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if err := store.prepareExternalToolResumeTx(ctx, tx, mode, view.ExecutionID, raw); err != nil {
					return err
				}
				return tx.Commit()
			}
			assertDomainCode(t, prepare(ctx), externalToolRecoveryCode)
			command := ResolveAgentToolOutcomeCommand{AgentToolCallID: call.AgentToolCallID, SubjectSnapshotHash: view.SubjectSnapshotHash, RequestID: "checked", Outcome: "applied", Evidence: "Checked remote history."}
			if _, err := store.ResolveAgentToolOutcome(ctx, command); err != nil {
				t.Fatal(err)
			}
			assertDomainCode(t, prepare(identity.WithPrincipal(context.Background(), identity.ServicePrincipal())), "ROLE_FORBIDDEN")
			if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1 WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			assertDomainCode(t, prepare(ctx), "WORKSPACE_QUOTA_EXCEEDED")
			var pending int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_tool_reconciliation_inputs`).Scan(&pending); err != nil || pending != 0 {
				t.Fatal("quota failure left a delivery record", err)
			}
			if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=100000 WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if err := prepare(ctx); err != nil {
				t.Fatal(err)
			}
			if err := prepare(ctx); err != nil {
				t.Fatal(err)
			}
			gate := func() error {
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				return validateExternalToolOutcomesTx(ctx, tx, mode, view.ExecutionID)
			}
			assertDomainCode(t, gate(), externalToolRecoveryCode)
			var inputID, ownerID string
			if err := store.db.QueryRow(`SELECT input_id,input_owner_id FROM agent_tool_reconciliation_inputs WHERE agent_tool_call_id=?`, call.AgentToolCallID).Scan(&inputID, &ownerID); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"withdraw", "revise"} {
				content := ""
				if action == "revise" {
					content = "Changed fact"
				}
				_, err := store.ChangeExecutionInput(ctx, inputChangeCommand(project.ProjectID, mode, ownerID, inputID, action, content, "locked-"+action))
				assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
			}
			table := "agent_turn_inputs"
			switch mode {
			case "conversation":
				items, err := loadAgentTurnInputs(ctx, store.db, ownerID)
				if err != nil || len(items) != 1 || items[0].CanModify {
					t.Fatal("editable/missing fact", err)
				}
			case "background_task":
				table = "agent_task_inputs"
				items, err := loadAgentTaskInputs(ctx, store.db, ownerID)
				if err != nil || len(items) != 1 || items[0].CanModify {
					t.Fatal("editable/missing fact", err)
				}
			case "stateful_workflow":
				table = "execution_inputs"
				items, err := loadExecutionInputs(ctx, store.db, ownerID)
				if err != nil || len(items) != 1 || items[0].CanModify {
					t.Fatal("editable/missing fact", err)
				}
			}
			var content string
			if err := store.db.QueryRow(`SELECT content FROM `+table+` WHERE input_id=?`, inputID).Scan(&content); err != nil || !strings.Contains(content, "Checked remote history.") {
				t.Fatal("missing user fact", err)
			}
			if _, err := store.db.Exec(`UPDATE `+table+` SET content='tampered' WHERE input_id=?`, inputID); err != nil {
				t.Fatal(err)
			}
			assertDomainCode(t, prepare(ctx), "AGENT_TOOL_OUTCOME_REVIEW_CORRUPT")
			if _, err := store.db.Exec(`UPDATE `+table+` SET content=? WHERE input_id=?`, content, inputID); err != nil {
				t.Fatal(err)
			}
			if err := prepare(ctx); err != nil {
				t.Fatal(err)
			}
			boundColumn, boundValue := "agent_task_attempt_id", any(view.ExecutionID)
			if mode == "conversation" {
				boundColumn = "dispatch_generation"
				var generation int
				if err := store.db.QueryRow(`SELECT dispatch_generation FROM agent_turns WHERE agent_turn_id=?`, view.ExecutionID).Scan(&generation); err != nil {
					t.Fatal(err)
				}
				boundValue = generation
			} else if mode == "stateful_workflow" {
				boundColumn = "claim_token_hash"
				tx, err := store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				state, err := loadExecutionToolStateTx(ctx, tx, view.ExecutionID)
				tx.Rollback()
				if err != nil {
					t.Fatal(err)
				}
				boundValue = state.TokenHash
			}
			if _, err := store.db.Exec(`UPDATE `+table+` SET `+boundColumn+`=? WHERE input_id=?`, boundValue, inputID); err != nil {
				t.Fatal(err)
			}
			if err := gate(); err != nil {
				t.Fatal("claimed fact did not allow terminal validation", err)
			}
			if _, err := store.db.Exec(`UPDATE `+table+` SET status='included' WHERE input_id=?`, inputID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE `+table+` SET `+boundColumn+`=NULL WHERE input_id=?`, inputID); err != nil {
				t.Fatal(err)
			}
			if err := gate(); err != nil {
				t.Fatal("later claim rejected an already consumed immutable fact", err)
			}
			// A later SDK phase may not retain the earlier phase's MCP items. Only
			// the durable native input receipt can establish prior consumption.
			raw = json.RawMessage(`{"current_step":null}`)
			if err := prepare(ctx); err != nil {
				t.Fatal("previously consumed fact required replay", err)
			}
			preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{`SELECT COUNT(*) FROM ` + table + ` WHERE input_id=?`, `SELECT COUNT(*) FROM agent_tool_reconciliation_inputs WHERE input_id=?`} {
				var count int
				if err := store.db.QueryRow(query, inputID).Scan(&count); err != nil || count != 0 {
					t.Fatal("deleted evidence retained", err)
				}
			}
		})
	}
}

func TestExternalToolFactSchema54MigrationAndReopen(t *testing.T) {
	store, _, call, path := outcomeReviewFixture(t, "conversation")
	defer func() { store.Close() }()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	view, err := store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveAgentToolOutcome(ctx, ResolveAgentToolOutcomeCommand{AgentToolCallID: call.AgentToolCallID,
		SubjectSnapshotHash: view.SubjectSnapshotHash, RequestID: "migrate", Outcome: "applied", Evidence: "Verified before migration."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_tool_reconciliation_inputs; PRAGMA user_version=54;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=54 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	call, err = store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil {
		t.Fatal(err)
	}
	prepare := func() error {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := store.prepareExternalToolResumeTx(ctx, tx, "conversation", view.ExecutionID, externalCheckpointForTest(t, call)); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := prepare(); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := store.db.QueryRow(`SELECT input_id FROM agent_tool_reconciliation_inputs WHERE agent_tool_call_id=?`, call.AgentToolCallID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepare(); err != nil {
		t.Fatal(err)
	}
	view, err = store.GetAgentToolOutcomeReview(ctx, call.AgentToolCallID)
	if err != nil || view.Resolution == nil || *view.Resolution != *resolved.Resolution {
		t.Fatal("migration changed immutable fact", err)
	}
	inputs, err := loadAgentTurnInputs(ctx, store.db, view.ExecutionID)
	if err != nil || len(inputs) != 1 || inputs[0].InputID != before || inputs[0].CanModify || inputs[0].Status != "received" {
		t.Fatalf("reopen changed protected input: %+v %v", inputs, err)
	}
}
