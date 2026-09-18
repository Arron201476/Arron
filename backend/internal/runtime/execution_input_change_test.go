package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestExecutionInputChangeCompetesWithNativeClaim(t *testing.T) {
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	input := appendTurnInput(t, store, turn.AgentTurnID, "Withdraw before claim only")
	start := make(chan struct{})
	changed := make(chan error, 1)
	go func() {
		<-start
		_, err := store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, input.InputID, "withdraw", "", "concurrent-withdraw"))
		changed <- err
	}()
	close(start)
	claimed, err := store.ClaimRunnableAgentTurns(context.Background(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatal("native claim failed", err)
	}
	changeErr := <-changed
	if changeErr == nil {
		if len(claimed[0].AdditionalInputs) != 0 {
			t.Fatal("successfully withdrawn input reached native claim")
		}
	} else {
		assertDomainCode(t, changeErr, "EXECUTION_INPUT_CONFLICT")
		if len(claimed[0].AdditionalInputs) != 1 || claimed[0].AdditionalInputs[0].InputID != input.InputID {
			t.Fatal("winning claim lost its original input")
		}
	}
}

func TestExecutionInputChangeCompetingRevisionsAndTerminalWithdrawal(t *testing.T) {
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	input := appendTurnInput(t, store, turn.AgentTurnID, "Immutable original")
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, key := range []string{"revision-a", "revision-b"} {
		go func(key string) {
			<-start
			_, err := store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, input.InputID, "revise", key, key))
			results <- err
		}(key)
	}
	close(start)
	winners := 0
	for range 2 {
		if err := <-results; err == nil {
			winners++
		} else {
			assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
		}
	}
	if winners != 1 {
		t.Fatal("competing revisions did not choose exactly one winner")
	}
	current, err := store.GetAgentTurn(user, turn.AgentTurnID)
	if err != nil || len(current.AdditionalInputs) != 2 || current.AdditionalInputs[0].Content != input.Content {
		t.Fatal("revision overwrote history or left an orphan", err)
	}
	replacement := current.AdditionalInputs[1]
	for _, content := range []string{strings.Repeat("x", (32<<10)+1), "\x00", "   "} {
		_, err = store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, replacement.InputID, "revise", content, "invalid"))
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='cancelled' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, replacement.InputID, "revise", "Too late", "terminal-revision"))
	assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
	if _, err := store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, replacement.InputID, "withdraw", "", "terminal-withdraw")); err != nil {
		t.Fatal("unclaimed terminal input could not be withdrawn", err)
	}
}

func TestExecutionInputChangeMigration50PreservesCheckpointAndOriginal(t *testing.T) {
	database := filepath.Join(t.TempDir(), "migration.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, claim := sdkResultRepairClaim(t, store)
	input := appendExecutionInputForTest(t, store, project, claim, "migration-original")
	pause := inputCheckpointForTest(t, claim, nil)
	if _, err := store.PauseExecutionForApproval(context.Background(), pause); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := store.db.QueryRow(`SELECT state_json FROM execution_run_states WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_turn_inputs", "agent_task_inputs", "execution_inputs"} {
		for _, column := range []string{"withdrawn_at", "replacement_input_id"} {
			if _, err := store.db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + column); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.db.Exec(`PRAGMA user_version=50`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	var restored, backup string
	if err := store.db.QueryRow(`SELECT state_json FROM execution_run_states WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&restored); err != nil || restored != original {
		t.Fatal("native checkpoint changed", err)
	}
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=50 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	view, err := store.GetExecutionInputs(user, project.ProjectID, claim.Attempt.AttemptID)
	if err != nil || len(view.Inputs) != 1 || view.Inputs[0].InputID != input.InputID || !view.Inputs[0].CanModify {
		t.Fatal("migrated unclaimed input changed", err)
	}
	_, err = store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "stateful_workflow", claim.Attempt.AttemptID, input.InputID, "withdraw", "", "migrate-withdraw"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimExecutionTask(context.Background(), statefulResumeCommand(claim))
	if err != nil || next == nil || len(next.AdditionalInputs) != 0 {
		t.Fatal("withdrawn pre-upgrade input was dispatched", err)
	}
}

func inputChangeCommand(project, mode, id, input, action, content, key string) ChangeExecutionInputCommand {
	return ChangeExecutionInputCommand{CommandMeta: CommandMeta{IdempotencyKey: key, CommandType: "change_execution_input", RequestHash: key + action + content}, ProjectID: project, Mode: mode, ExecutionID: id, InputID: input, Action: action, Content: content}
}

func TestExecutionInputChangesThreeModesAndClaimBoundary(t *testing.T) {
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			var store *Store
			var project, id string
			var appendInput func(string) string
			var claimInputs func() []string
			var publicStatus func() []string
			switch mode {
			case "conversation":
				s, p, turn, _ := pauseTurnFixture(t)
				store, project, id = s, p.ProjectID, turn.AgentTurnID
				appendInput = func(content string) string {
					input, err := store.AppendAgentTurnInput(user, AppendAgentTurnInputCommand{AgentTurnID: id, Content: content})
					if err != nil {
						t.Fatal(err)
					}
					return input.InputID
				}
				claimInputs = func() []string {
					turn := claimInputTurn(t, store, id)
					ids := []string{}
					for _, i := range turn.AdditionalInputs {
						ids = append(ids, i.InputID)
					}
					return ids
				}
				publicStatus = func() []string {
					turn, err := store.GetAgentTurn(user, id)
					if err != nil {
						t.Fatal(err)
					}
					r := []string{}
					for _, i := range turn.AdditionalInputs {
						r = append(r, i.Status)
					}
					return r
				}
			case "background_task":
				s, task, claim := pausedBackgroundStore(t)
				store, project, id = s, task.ProjectID, task.AgentTaskID
				appendInput = func(content string) string {
					input, err := store.AppendAgentTaskInput(user, AppendAgentTaskInputCommand{AgentTaskID: id, Content: content})
					if err != nil {
						t.Fatal(err)
					}
					return input.InputID
				}
				claimInputs = func() []string {
					task, err := store.ClaimAgentTask(context.Background(), claim)
					if err != nil || task == nil {
						t.Fatalf("claim: %v", err)
					}
					ids := []string{}
					for _, i := range task.AdditionalInputs {
						ids = append(ids, i.InputID)
					}
					return ids
				}
				publicStatus = func() []string {
					task, err := store.GetAgentTask(user, id)
					if err != nil {
						t.Fatal(err)
					}
					r := []string{}
					for _, i := range task.AdditionalInputs {
						r = append(r, i.Status)
					}
					return r
				}
			case "stateful_workflow":
				store = openProjectFilesTestStore(t, t.TempDir()+"/inputs.db")
				p, claim := sdkResultRepairClaim(t, store)
				project, id = p.ProjectID, claim.Attempt.AttemptID
				appendInput = func(content string) string {
					input, err := store.AppendExecutionInput(user, AppendExecutionInputCommand{ProjectID: project, AttemptID: id, Content: content})
					if err != nil {
						t.Fatal(err)
					}
					return input.InputID
				}
				claimInputs = func() []string {
					if _, err := store.PauseExecutionForApproval(context.Background(), inputCheckpointForTest(t, claim, nil)); err != nil {
						t.Fatal(err)
					}
					next, err := store.ClaimExecutionTask(context.Background(), statefulResumeCommand(claim))
					if err != nil || next == nil {
						t.Fatalf("claim: %v", err)
					}
					r := []string{}
					for _, i := range next.AdditionalInputs {
						r = append(r, i.InputID)
					}
					return r
				}
				publicStatus = func() []string {
					view, err := store.GetExecutionInputs(user, project, id)
					if err != nil {
						t.Fatal(err)
					}
					r := []string{}
					for _, i := range view.Inputs {
						r = append(r, i.Status)
					}
					return r
				}
			}
			defer store.Close()
			first, second := appendInput("Withdraw this unchanged original"), appendInput("Revise this unchanged original")
			withdraw := inputChangeCommand(project, mode, id, first, "withdraw", "", "withdraw")
			_, err := store.ChangeExecutionInput(context.Background(), withdraw)
			assertDomainCode(t, err, "ROLE_FORBIDDEN")
			service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			_, err = store.ChangeExecutionInput(service, withdraw)
			assertDomainCode(t, err, "ROLE_FORBIDDEN")
			result, err := store.ChangeExecutionInput(user, withdraw)
			if err != nil || result.Status != "withdrawn" {
				t.Fatalf("withdraw: %+v %v", result, err)
			}
			revise := inputChangeCommand(project, mode, id, second, "revise", "Replacement only", "revise")
			result, err = store.ChangeExecutionInput(user, revise)
			if err != nil || result.Status != "superseded" || result.ReplacementInputID == "" {
				t.Fatalf("revise: %+v %v", result, err)
			}
			again, err := store.ChangeExecutionInput(user, revise)
			if err != nil || again != result {
				t.Fatal("revision replay duplicated or changed", again, err)
			}
			statuses := publicStatus()
			if len(statuses) != 3 || statuses[0] != "withdrawn" || statuses[1] != "superseded" || statuses[2] != "received" {
				t.Fatal("history status", statuses)
			}
			ids := claimInputs()
			if len(ids) != 1 || ids[0] != result.ReplacementInputID {
				t.Fatal("withdrawn or replaced original entered SDK claim", ids)
			}
			_, err = store.ChangeExecutionInput(user, inputChangeCommand(project, mode, id, result.ReplacementInputID, "withdraw", "", "after-claim"))
			assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
			_, err = store.ChangeExecutionInput(user, inputChangeCommand(project, mode, id, first, "revise", "Overwritten", "second-change"))
			assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
			if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE user_id=?`, identity.DefaultUserID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ChangeExecutionInput(user, revise); err == nil {
				t.Fatal("stale owner context replayed after revocation")
			}
		})
	}
}
