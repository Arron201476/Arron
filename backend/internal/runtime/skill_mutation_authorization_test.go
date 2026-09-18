package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

type skillMutationBoundaryFixture struct {
	store     *Store
	ctx       context.Context
	target    SkillInstallTarget
	principal identity.Principal
}

func newSkillMutationBoundaryFixture(t *testing.T, scope capability.SkillScope) skillMutationBoundaryFixture {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "authorization.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal := identity.Principal{Kind: identity.KindUser, UserID: "skill_mutation_author", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleAdmin}
	if err := store.BootstrapPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(identity.WithPrincipal(context.Background(), principal), 30*time.Second)
	t.Cleanup(cancel)
	target := SkillInstallTarget{Scope: scope}
	if scope == capability.SkillScopeProject {
		project, err := store.CreateProject(ctx, "Skill mutation boundary")
		if err != nil {
			t.Fatal(err)
		}
		target.ProjectID = project.ProjectID
	}
	return skillMutationBoundaryFixture{store: store, ctx: ctx, target: target, principal: principal}
}

func (f skillMutationBoundaryFixture) revoke(t *testing.T, reason string) string {
	t.Helper()
	var query, code string
	args := []any{f.principal.WorkspaceID, f.principal.UserID}
	switch reason {
	case "role":
		query = `UPDATE workspace_memberships SET role = 'viewer' WHERE workspace_id = ? AND user_id = ?`
		code = "ROLE_FORBIDDEN"
	case "membership":
		query = `UPDATE workspace_memberships SET status = 'disabled' WHERE workspace_id = ? AND user_id = ?`
		code = "WORKSPACE_ACCESS_DENIED"
	case "project":
		query = `UPDATE projects SET deleted_at = ? WHERE project_id = ?`
		args = []any{formatTime(time.Now()), f.target.ProjectID}
		code = "SKILL_INSTALLATION_NOT_FOUND"
	default:
		t.Fatalf("unknown revocation: %s", reason)
	}
	result, err := f.store.db.ExecContext(f.ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("revocation affected %d rows: %v", count, err)
	}
	return code
}

func skillMutationRevocations(scope capability.SkillScope) []string {
	reasons := []string{"role", "membership"}
	if scope == capability.SkillScopeProject {
		reasons = append(reasons, "project")
	}
	return reasons
}

func TestSkillLifecycleRechecksAuthorizationBeforeActiveDirectoryMutation(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		for _, operation := range []string{"enable", "disable", "activate", "uninstall"} {
			for _, reason := range skillMutationRevocations(scope) {
				t.Run(string(scope)+"/"+operation+"/"+reason, func(t *testing.T) {
					f := newSkillMutationBoundaryFixture(t, scope)
					var installed SkillInstallation
					for _, version := range []string{"1.0.0", "2.0.0"} {
						directory := writeSkillTestPackage(t, t.TempDir(), "mutation-boundary", "mutation_boundary", version, "inline", version)
						var err error
						installed, err = f.store.InstallSkillDirectory(f.ctx, directory, "", f.target)
						if err != nil {
							t.Fatal(err)
						}
					}
					if operation == "enable" {
						var err error
						installed, err = f.store.SetSkillInstallationEnabled(f.ctx, installed.SkillInstallationID, false, "")
						if err != nil {
							t.Fatal(err)
						}
					}
					root, err := f.store.scopedSkillActiveRoot(installationScope(installed))
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(root, installed.SkillName, "SKILL.md")
					before, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					beforeInfo, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					beforeState := readSkillMutationState(t, f.store, installed.SkillInstallationID)
					oldNow := f.store.now
					var wantCode string
					// The lifecycle methods read their clock after the initial auth and
					// package checks, immediately before beginning the write transaction.
					f.store.now = func() time.Time {
						f.store.now = oldNow
						wantCode = f.revoke(t, reason)
						return oldNow()
					}
					switch operation {
					case "enable", "disable":
						_, err = f.store.SetSkillInstallationEnabled(f.ctx, installed.SkillInstallationID, operation == "enable", "")
					case "activate":
						_, err = f.store.ActivateSkillVersion(f.ctx, installed.SkillInstallationID, "1.0.0", "")
					case "uninstall":
						_, err = f.store.UninstallSkill(f.ctx, installed.SkillInstallationID, "")
					}
					if wantCode == "" {
						t.Fatal("did not reach the pre-transaction revocation boundary")
					}
					assertDomainCode(t, err, wantCode)
					if got := readSkillMutationState(t, f.store, installed.SkillInstallationID); got != beforeState {
						t.Fatalf("unauthorized lifecycle changed state: before=%+v after=%+v", beforeState, got)
					}
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("unauthorized lifecycle changed active bytes: %v", err)
					}
					afterInfo, err := os.Stat(path)
					if err != nil || !os.SameFile(beforeInfo, afterInfo) {
						t.Fatalf("unauthorized lifecycle replaced the active file: %v", err)
					}
					assertDirectoryEmpty(t, filepath.Join(f.store.skillDataRoot, "activation"))
				})
			}
		}
	}
}

func TestPersistSkillPackageRechecksAuthorizationWithoutDraftGuard(t *testing.T) {
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser, capability.SkillScopeProject} {
		for _, reason := range skillMutationRevocations(scope) {
			t.Run(string(scope)+"/"+reason, func(t *testing.T) {
				f := newSkillMutationBoundaryFixture(t, scope)
				target, err := f.store.resolveSkillInstallTarget(f.ctx, []SkillInstallTarget{f.target})
				if err != nil {
					t.Fatal(err)
				}
				directory := writeSkillTestPackage(t, t.TempDir(), "install-boundary", "install_boundary", "1.0.0", "inline", "Authorized publication only.")
				skill, err := prepareSkillDirectory(f.store.registry.ProjectRoot(), t.TempDir(), directory)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.authorizeSkillScopeMutation(f.ctx, target); err != nil {
					t.Fatal(err)
				}
				wantCode := f.revoke(t, reason)
				_, err = f.store.persistSkillPackage(f.ctx, target, "pending_install_boundary", "directory", "install-boundary", f.principal.UserID, skill, nil)
				assertDomainCode(t, err, wantCode)
				var installations, versions, events int
				if err := f.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM skill_installations), (SELECT COUNT(*) FROM skill_versions), (SELECT COUNT(*) FROM skill_installation_events)`).Scan(&installations, &versions, &events); err != nil {
					t.Fatal(err)
				}
				if installations != 0 || versions != 0 || events != 0 {
					t.Fatalf("unauthorized install wrote rows: installations=%d versions=%d events=%d", installations, versions, events)
				}
				root, err := f.store.scopedSkillActiveRoot(target)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, skill.Name)); !os.IsNotExist(err) {
					t.Fatalf("unauthorized install activated a directory: %v", err)
				}
				assertDirectoryEmpty(t, filepath.Join(f.store.skillDataRoot, "packages"))
				assertDirectoryEmpty(t, filepath.Join(f.store.skillDataRoot, "activation"))
			})
		}
	}
}

type skillMutationState struct {
	status, activeVersion, updatedAt string
	enabled                          bool
	versions, events                 int
}

func readSkillMutationState(t *testing.T, store *Store, installationID string) skillMutationState {
	t.Helper()
	var state skillMutationState
	err := store.db.QueryRow(`
		SELECT status, COALESCE(active_version_id, ''), updated_at, enabled,
		(SELECT COUNT(*) FROM skill_versions WHERE skill_installation_id = si.skill_installation_id),
		(SELECT COUNT(*) FROM skill_installation_events WHERE skill_installation_id = si.skill_installation_id)
		FROM skill_installations si WHERE skill_installation_id = ?`, installationID,
	).Scan(&state.status, &state.activeVersion, &state.updatedAt, &state.enabled, &state.versions, &state.events)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
