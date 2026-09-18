package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
)

func packageZIPCommand(t *testing.T, action, key string, installed SkillInstallation, target SkillInstallTarget, version string) (SkillPackageCommand, []byte) {
	t.Helper()
	command := SkillPackageCommand{Action: action, SourceName: "package.zip", Target: target, IdempotencyKey: key}
	if action == "upgrade_zip" {
		snapshot := skillLifecycleSnapshot(installed)
		command.Expected, command.InstallationID = &snapshot, installed.SkillInstallationID
	}
	return command, buildSkillZIP(t, validSkillZIPEntries("package-command", "package_command", version, false))
}

func TestSkillPackageOriginalZIPReceiptNeverReplaysLaterState(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		t.Run(string(scope), func(t *testing.T) {
			f := newSkillMutationBoundaryFixture(t, scope)
			install, archive := packageZIPCommand(t, "install_zip", "install", SkillInstallation{}, f.target, "1.0.0")
			first, err := f.store.ExecuteSkillPackageCommand(f.ctx, install, bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			upgrade, newer := packageZIPCommand(t, "upgrade_zip", "upgrade", first.Installation, f.target, "2.0.0")
			second, err := f.store.ExecuteSkillPackageCommand(f.ctx, upgrade, bytes.NewReader(newer))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.UninstallSkill(f.ctx, second.Installation.SkillInstallationID, ""); err != nil {
				t.Fatal(err)
			}
			reinstall := install
			reinstall.IdempotencyKey = "reinstall"
			if _, err := f.store.ExecuteSkillPackageCommand(f.ctx, reinstall, bytes.NewReader(archive)); err != nil {
				t.Fatal(err)
			}
			before := readSkillMutationState(t, f.store, first.Installation.SkillInstallationID)
			attempts, err := f.store.ListSkillInstallAttempts(f.ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, saved := range []struct {
				command SkillPackageCommand
				archive []byte
				receipt SkillPackageReceipt
			}{{install, archive, first.Receipt}, {upgrade, newer, second.Receipt}} {
				got, err := f.store.ExecuteSkillPackageCommand(f.ctx, saved.command, bytes.NewReader(saved.archive))
				if err != nil || got.Receipt != saved.receipt || readSkillMutationState(t, f.store, first.Installation.SkillInstallationID) != before {
					t.Fatalf("replayed package write: %+v %v", got, err)
				}
			}
			after, err := f.store.ListSkillInstallAttempts(f.ctx, 100)
			if err != nil || len(after) != len(attempts) {
				t.Fatalf("receipt retry added validation attempts: %v", err)
			}
			_, err = f.store.ExecuteSkillPackageCommand(f.ctx, install, bytes.NewReader(newer))
			assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
			install.IdempotencyKey = "silent-overwrite"
			_, err = f.store.ExecuteSkillPackageCommand(f.ctx, install, bytes.NewReader(newer))
			assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
			upgrade.IdempotencyKey = "stale-upgrade"
			_, err = f.store.ExecuteSkillPackageCommand(f.ctx, upgrade, bytes.NewReader(newer))
			assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
		})
	}
}

func TestSkillPackageDirectoryReceiptsSurviveSourceRemoval(t *testing.T) {
	f := newSkillMutationBoundaryFixture(t, capability.SkillScopeUser)
	root := t.TempDir()
	directory := writeSkillTestPackage(t, root, "directory-command", "directory_command", "1.0.0", "inline", "FIRST")
	if err := f.store.registry.AddSkillRoot(capability.SkillRoot{Scope: capability.SkillScopeWorkspace, Path: root}); err != nil {
		t.Fatal(err)
	}
	entry, _ := f.store.registry.Get("directory_command")
	adopt := SkillPackageCommand{Action: "adopt_directory", CapabilityID: entry.Skill.CapabilityID, Version: entry.Skill.Version, ContentHash: entry.Skill.ContentHash, Target: f.target, IdempotencyKey: "adopt"}
	first, err := f.store.ExecuteSkillPackageCommand(f.ctx, adopt, nil)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := f.store.SetSkillInstallationEnabled(f.ctx, first.Installation.SkillInstallationID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	writeSkillTestPackage(t, root, "directory-command", "directory_command", "2.0.0", "inline", "SECOND")
	preview, err := f.store.PreviewSkillDirectoryUpdate(f.ctx, disabled.SkillInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := skillLifecycleSnapshot(disabled)
	update := SkillPackageCommand{Action: "update_directory", InstallationID: disabled.SkillInstallationID, Expected: &snapshot, Version: preview.Version, ContentHash: preview.ContentHash, IdempotencyKey: "directory-update"}
	second, err := f.store.ExecuteSkillPackageCommand(f.ctx, update, nil)
	if err != nil || second.Installation.Enabled {
		t.Fatalf("disabled directory update: %+v %v", second, err)
	}
	if err := os.Remove(filepath.Join(directory, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	before := readSkillMutationState(t, f.store, disabled.SkillInstallationID)
	for _, command := range []SkillPackageCommand{adopt, update} {
		got, err := f.store.ExecuteSkillPackageCommand(f.ctx, command, nil)
		if err != nil || got.Receipt.RequestID != command.IdempotencyKey || readSkillMutationState(t, f.store, disabled.SkillInstallationID) != before {
			t.Fatalf("source missing replay: %+v %v", got, err)
		}
	}
}

func TestSkillPackageRechecksCurrentAuthorityForCachedCommands(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		for _, reason := range skillMutationRevocations(scope) {
			t.Run(string(scope)+"/"+reason, func(t *testing.T) {
				f := newSkillMutationBoundaryFixture(t, scope)
				command, archive := packageZIPCommand(t, "install_zip", "authority", SkillInstallation{}, f.target, "1.0.0")
				first, err := f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive))
				if err != nil {
					t.Fatal(err)
				}
				want := f.revoke(t, reason)
				before := readSkillMutationState(t, f.store, first.Installation.SkillInstallationID)
				_, err = f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive))
				assertDomainCode(t, err, want)
				if readSkillMutationState(t, f.store, first.Installation.SkillInstallationID) != before {
					t.Fatal("revoked replay wrote state")
				}
			})
		}
	}
}

func TestSkillPackageRejectsCorruptReceipt(t *testing.T) {
	for _, field := range []string{"request_id", "skill_installation_id", "skill_installation_event_id", "skill_version_id", "action", "actor_ref", "workspace_id", "scope", "scope_ref", "skill_name", "capability_id", "version", "content_hash", "source_hash", "event_binding"} {
		t.Run(field, func(t *testing.T) {
			f := newSkillMutationBoundaryFixture(t, capability.SkillScopeWorkspace)
			command, archive := packageZIPCommand(t, "install_zip", "corrupt", SkillInstallation{}, f.target, "1.0.0")
			first, err := f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive))
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
				_, err = f.store.db.Exec(`UPDATE idempotency_records SET response_json=? WHERE command_type='skill_package' AND idempotency_key=?`, string(raw), command.IdempotencyKey)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := readSkillMutationState(t, f.store, first.Installation.SkillInstallationID)
			_, err = f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive))
			assertDomainCode(t, err, "IDEMPOTENCY_RESULT_INVALID")
			if readSkillMutationState(t, f.store, first.Installation.SkillInstallationID) != before {
				t.Fatal("corrupt receipt caused a write")
			}
		})
	}
}

func TestSkillPackageReceiptRollbackAndRestart(t *testing.T) {
	f := newSkillMutationBoundaryFixture(t, capability.SkillScopeWorkspace)
	command, archive := packageZIPCommand(t, "install_zip", "initial", SkillInstallation{}, f.target, "1.0.0")
	first, err := f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	upgrade, newer := packageZIPCommand(t, "upgrade_zip", "atomic", first.Installation, f.target, "2.0.0")
	root, err := f.store.scopedSkillActiveRoot(installationScope(first.Installation))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, first.Installation.SkillName, "SKILL.md")
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := readSkillMutationState(t, f.store, first.Installation.SkillInstallationID)
	if _, err := f.store.db.Exec(`CREATE TRIGGER reject_package_receipt BEFORE UPDATE ON idempotency_records WHEN NEW.command_type='skill_package' BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ExecuteSkillPackageCommand(f.ctx, upgrade, bytes.NewReader(newer)); err == nil {
		t.Fatal("receipt failure ignored")
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) || readSkillMutationState(t, f.store, first.Installation.SkillInstallationID) != before {
		t.Fatalf("partial rollback: %v", err)
	}
	if _, err := f.store.db.Exec(`DROP TRIGGER reject_package_receipt`); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.ExecuteSkillPackageCommand(f.ctx, upgrade, bytes.NewReader(newer))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(f.store.dataRoot, "authorization.db")
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dbPath, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ExecuteSkillPackageCommand(f.ctx, upgrade, bytes.NewReader(newer))
	if err != nil || got.Receipt != second.Receipt || len(got.Installation.Events) != len(second.Installation.Events) {
		t.Fatalf("restart replay: %+v %v", got, err)
	}
}

func TestSkillPackageCommitChecksSnapshotInsideTransaction(t *testing.T) {
	f, installed, source := skillLifecycleFixture(t, capability.SkillScopeWorkspace)
	snapshot := skillLifecycleSnapshot(installed)
	skill, err := prepareSkillDirectory(f.store.registry.ProjectRoot(), t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE skill_installations SET enabled=0 WHERE skill_installation_id=?`, installed.SkillInstallationID); err != nil {
		t.Fatal(err)
	}
	before := readSkillMutationState(t, f.store, installed.SkillInstallationID)
	_, err = f.store.persistSkillPackage(f.ctx, installationScope(installed), "never-committed", "directory", "fixture", "fixture", skill, nil, &skillPackageCommitGuard{installationID: installed.SkillInstallationID, expected: &snapshot})
	assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
	if readSkillMutationState(t, f.store, installed.SkillInstallationID) != before {
		t.Fatal("transaction used stale enabled/version state")
	}
}

func TestSkillPackageCommandRequiresKeyAndUpgradeSnapshot(t *testing.T) {
	f := newSkillMutationBoundaryFixture(t, capability.SkillScopeWorkspace)
	_, err := f.store.ExecuteSkillPackageCommand(context.Background(), SkillPackageCommand{Action: "install_zip"}, bytes.NewReader(nil))
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REQUIRED")
	_, err = f.store.ExecuteSkillPackageCommand(f.ctx, SkillPackageCommand{Action: "upgrade_zip", InstallationID: "missing", IdempotencyKey: "no-state"}, bytes.NewReader(nil))
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}

func TestSkillPackagePostCommitReadFailureDoesNotRelabelCompletedAttempt(t *testing.T) {
	f := newSkillMutationBoundaryFixture(t, capability.SkillScopeWorkspace)
	command, archive := packageZIPCommand(t, "install_zip", "committed", SkillInstallation{}, f.target, "1.0.0")
	if _, err := f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	var attemptID string
	if err := f.store.db.QueryRow(`SELECT skill_install_attempt_id FROM skill_install_attempts WHERE status='completed'`).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	f.store.recordSkillInstallFailure(f.ctx, attemptID, context.Canceled)
	var status string
	if err := f.store.db.QueryRow(`SELECT status FROM skill_install_attempts WHERE skill_install_attempt_id=?`, attemptID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("committed installation relabeled %s", status)
	}
}

func TestSkillPackageIgnoredStateUpdateRollsBackNewVersion(t *testing.T) {
	f := newSkillMutationBoundaryFixture(t, capability.SkillScopeWorkspace)
	command, archive := packageZIPCommand(t, "install_zip", "initial", SkillInstallation{}, f.target, "1.0.0")
	first, err := f.store.ExecuteSkillPackageCommand(f.ctx, command, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	before := readSkillMutationState(t, f.store, first.Installation.SkillInstallationID)
	if _, err := f.store.db.Exec(`CREATE TRIGGER ignore_skill_update BEFORE UPDATE ON skill_installations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	upgrade, newer := packageZIPCommand(t, "upgrade_zip", "ignored", first.Installation, f.target, "2.0.0")
	_, err = f.store.ExecuteSkillPackageCommand(f.ctx, upgrade, bytes.NewReader(newer))
	assertDomainCode(t, err, "SKILL_INSTALLATION_STATE_CONFLICT")
	if readSkillMutationState(t, f.store, first.Installation.SkillInstallationID) != before {
		t.Fatal("ignored state update committed an event")
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM skill_versions WHERE skill_installation_id=?`, first.Installation.SkillInstallationID).Scan(&count); err != nil || count != len(first.Installation.Versions) {
		t.Fatalf("partial version commit: %d %v", count, err)
	}
}
