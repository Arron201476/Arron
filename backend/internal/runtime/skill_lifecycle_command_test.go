package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"content-agent/backend/internal/capability"
)

func skillLifecycleFixture(t *testing.T, scope capability.SkillScope) (skillMutationBoundaryFixture, SkillInstallation, string) {
	t.Helper()
	f := newSkillMutationBoundaryFixture(t, scope)
	var installed SkillInstallation
	var directory string
	for _, version := range []string{"1.0.0", "2.0.0"} {
		directory = writeSkillTestPackage(t, t.TempDir(), "lifecycle-command", "lifecycle_command", version, "inline", version)
		var err error
		installed, err = f.store.InstallSkillDirectory(f.ctx, directory, "", f.target)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f, installed, directory
}

func lifecycleCommandForTest(item SkillInstallation, action, key string) SkillLifecycleCommand {
	count, enabled := len(item.Events), item.Enabled
	command := SkillLifecycleCommand{InstallationID: item.SkillInstallationID, Action: action, IdempotencyKey: key, Expected: &SkillLifecycleSnapshot{ActiveVersionID: *item.ActiveVersionID, Status: item.Status, Enabled: &enabled, EventCount: &count}}
	if action == "activate" {
		command.Version = "1.0.0"
	}
	return command
}

func TestSkillLifecycleOriginalReceiptNeverReplaysLaterState(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		for _, action := range []string{"enable", "disable", "activate", "uninstall"} {
			t.Run(string(scope)+"/"+action, func(t *testing.T) {
				f, installed, source := skillLifecycleFixture(t, scope)
				if action == "enable" {
					var err error
					installed, err = f.store.SetSkillInstallationEnabled(f.ctx, installed.SkillInstallationID, false, "")
					if err != nil {
						t.Fatal(err)
					}
				}
				command := lifecycleCommandForTest(installed, action, "original-command")
				first, err := f.store.ControlSkillInstallation(f.ctx, command)
				if err != nil {
					t.Fatal(err)
				}
				if first.Receipt.EventID == "" || first.Receipt.Action != action || first.Receipt.ActorRef != f.principal.UserID || len(first.Installation.Events) != len(installed.Events)+1 {
					t.Fatalf("receipt: %+v", first)
				}
				if action == "uninstall" {
					_, err = f.store.InstallSkillDirectory(f.ctx, source, "", f.target)
				} else {
					_, err = f.store.UninstallSkill(f.ctx, installed.SkillInstallationID, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
				retried, err := f.store.ControlSkillInstallation(f.ctx, command)
				if err != nil {
					t.Fatal(err)
				}
				if retried.Receipt != first.Receipt || readSkillMutationState(t, f.store, installed.SkillInstallationID) != before || retried.Installation.Status != before.status {
					t.Fatalf("retry changed later state: %+v", retried)
				}
				changed := command
				changed.Action, changed.Version = "disable", ""
				if action == "disable" {
					changed.Action = "enable"
				}
				_, err = f.store.ControlSkillInstallation(f.ctx, changed)
				assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
			})
		}
	}
}

func TestSkillLifecycleRejectsStaleAndABAState(t *testing.T) {
	for _, field := range []string{"active_version_id", "status", "enabled", "event_count", "aba"} {
		t.Run(field, func(t *testing.T) {
			f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
			command := lifecycleCommandForTest(installed, "uninstall", "stale-command")
			switch field {
			case "active_version_id":
				command.Expected.ActiveVersionID = "old-version"
			case "status":
				command.Expected.Status = "broken"
			case "enabled":
				*command.Expected.Enabled = false
			case "event_count":
				*command.Expected.EventCount++
			case "aba":
				for _, enabled := range []bool{false, true} {
					if _, err := f.store.SetSkillInstallationEnabled(f.ctx, installed.SkillInstallationID, enabled, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
			_, err := f.store.ControlSkillInstallation(f.ctx, command)
			assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
			if readSkillMutationState(t, f.store, installed.SkillInstallationID) != before {
				t.Fatal("stale command wrote state")
			}
		})
	}
}

func TestSkillLifecycleRechecksSnapshotInsideWriteTransaction(t *testing.T) {
	for _, action := range []string{"disable", "activate", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
			command := lifecycleCommandForTest(installed, action, "concurrent-command")
			oldNow := f.store.now
			changed := false
			f.store.now = func() time.Time {
				f.store.now = oldNow
				if _, err := f.store.db.Exec(`UPDATE skill_installations SET enabled=0 WHERE skill_installation_id=?`, installed.SkillInstallationID); err != nil {
					t.Fatal(err)
				}
				changed = true
				return oldNow()
			}
			_, err := f.store.ControlSkillInstallation(f.ctx, command)
			assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
			if !changed {
				t.Fatal("transaction boundary was not reached")
			}
			state := readSkillMutationState(t, f.store, installed.SkillInstallationID)
			if state.enabled || state.events != len(installed.Events) || state.status != installed.Status || state.activeVersion != *installed.ActiveVersionID {
				t.Fatalf("stale command committed: %+v", state)
			}
		})
	}
}

func TestSkillLifecycleAuthorizesBeforeReturningCachedReceipt(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		for _, reason := range skillMutationRevocations(scope) {
			t.Run(string(scope)+"/"+reason, func(t *testing.T) {
				f, installed, _ := skillLifecycleFixture(t, scope)
				command := lifecycleCommandForTest(installed, "disable", "authorized-command")
				if _, err := f.store.ControlSkillInstallation(f.ctx, command); err != nil {
					t.Fatal(err)
				}
				want := f.revoke(t, reason)
				before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
				_, err := f.store.ControlSkillInstallation(f.ctx, command)
				assertDomainCode(t, err, want)
				if readSkillMutationState(t, f.store, installed.SkillInstallationID) != before {
					t.Fatal("revoked retry wrote state")
				}
			})
		}
	}
}

func TestSkillLifecycleReceiptSurvivesStoreRestart(t *testing.T) {
	f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
	command := lifecycleCommandForTest(installed, "disable", "restart-command")
	first, err := f.store.ControlSkillInstallation(f.ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.store.dataRoot, "authorization.db")
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before := readSkillMutationState(t, reopened, installed.SkillInstallationID)
	retried, err := reopened.ControlSkillInstallation(f.ctx, command)
	if err != nil || first.Receipt != retried.Receipt || readSkillMutationState(t, reopened, installed.SkillInstallationID) != before {
		t.Fatalf("restart receipt=%+v err=%v", retried, err)
	}
}

func TestSkillLifecycleRejectsCorruptOrReboundReceipt(t *testing.T) {
	for _, field := range []string{"request_id", "skill_installation_id", "skill_installation_event_id", "skill_version_id", "action", "actor_ref", "event_binding"} {
		t.Run(field, func(t *testing.T) {
			f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
			command := lifecycleCommandForTest(installed, "disable", "receipt-command")
			first, err := f.store.ControlSkillInstallation(f.ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(first.Receipt)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if field == "event_binding" {
				_, err = f.store.db.Exec(`UPDATE skill_installation_events SET payload_json='{}' WHERE skill_installation_event_id=?`, first.Receipt.EventID)
			} else {
				payload[field] = "wrong"
				raw, _ = json.Marshal(payload)
				_, err = f.store.db.Exec(`UPDATE idempotency_records SET response_json=? WHERE command_type='skill_lifecycle' AND idempotency_key=?`, string(raw), command.IdempotencyKey)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
			_, err = f.store.ControlSkillInstallation(f.ctx, command)
			assertDomainCode(t, err, "IDEMPOTENCY_RESULT_INVALID")
			if readSkillMutationState(t, f.store, installed.SkillInstallationID) != before {
				t.Fatal("corrupt receipt caused replay")
			}
		})
	}
}

func TestSkillLifecycleReceiptFailureRollsBackFilesystemAndDatabase(t *testing.T) {
	for _, action := range []string{"disable", "activate", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
			command := lifecycleCommandForTest(installed, action, "atomic-command")
			root, err := f.store.scopedSkillActiveRoot(installationScope(installed))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, installed.SkillName, "SKILL.md")
			beforeBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
			if _, err := f.store.db.Exec(`CREATE TRIGGER reject_skill_receipt BEFORE UPDATE ON idempotency_records WHEN NEW.command_type='skill_lifecycle' BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ControlSkillInstallation(f.ctx, command); err == nil {
				t.Fatal("receipt failure was ignored")
			}
			afterBytes, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(beforeBytes, afterBytes) || readSkillMutationState(t, f.store, installed.SkillInstallationID) != before {
				t.Fatalf("partial commit after receipt failure: %v", err)
			}
			if _, err := f.store.db.Exec(`DROP TRIGGER reject_skill_receipt`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ControlSkillInstallation(f.ctx, command); err != nil {
				t.Fatalf("rolled-back original retry: %v", err)
			}
		})
	}
}

func TestSkillLifecycleRequiresExpectedState(t *testing.T) {
	f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
	_, err := f.store.ControlSkillInstallation(context.Background(), SkillLifecycleCommand{InstallationID: installed.SkillInstallationID, Action: "disable", IdempotencyKey: "missing-snapshot"})
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}

func TestSkillLifecycleCommandRechecksRevocationAtCommit(t *testing.T) {
	for _, action := range []string{"enable", "disable", "activate", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
			if action == "enable" {
				var err error
				installed, err = f.store.SetSkillInstallationEnabled(f.ctx, installed.SkillInstallationID, false, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			command := lifecycleCommandForTest(installed, action, "revoked-at-commit")
			before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
			oldNow := f.store.now
			revoked := false
			f.store.now = func() time.Time { f.store.now = oldNow; f.revoke(t, "role"); revoked = true; return oldNow() }
			_, err := f.store.ControlSkillInstallation(f.ctx, command)
			assertDomainCode(t, err, "ROLE_FORBIDDEN")
			if !revoked || readSkillMutationState(t, f.store, installed.SkillInstallationID) != before {
				t.Fatal("revoked command changed state")
			}
		})
	}
}

func TestSkillLifecycleConcurrentCommandsCommitAtMostOnce(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			f, installed, _ := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
			command := lifecycleCommandForTest(installed, "disable", "concurrent-one")
			other := command
			if !sameKey {
				other.IdempotencyKey = "concurrent-two"
			}
			type outcome struct {
				result SkillLifecycleResult
				err    error
			}
			results := make(chan outcome, 2)
			start := make(chan struct{})
			for _, request := range []SkillLifecycleCommand{command, other} {
				go func(request SkillLifecycleCommand) {
					<-start
					result, err := f.store.ControlSkillInstallation(f.ctx, request)
					results <- outcome{result, err}
				}(request)
			}
			close(start)
			first, second := <-results, <-results
			if sameKey {
				if first.err != nil || second.err != nil || first.result.Receipt != second.result.Receipt {
					t.Fatalf("same-key results: %+v %+v", first, second)
				}
			} else {
				if first.err == nil {
					first, second = second, first
				}
				assertDomainCode(t, first.err, "SKILL_INSTALLATION_STATE_CONFLICT")
				if second.err != nil {
					t.Fatalf("neither command committed: %v", second.err)
				}
			}
			if readSkillMutationState(t, f.store, installed.SkillInstallationID).events != len(installed.Events)+1 {
				t.Fatal("concurrent commands duplicated audit events")
			}
		})
	}
}
