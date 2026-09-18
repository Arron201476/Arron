package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestEventReadersRecheckCurrentWorkspaceAccess(t *testing.T) {
	for _, mutation := range []struct{ name, statement, code string }{
		{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE user_id='event_reader'`, "WORKSPACE_ACCESS_DENIED"},
		{"user", `UPDATE users SET status='disabled' WHERE user_id='event_reader'`, "WORKSPACE_ACCESS_DENIED"},
		{"workspace", `UPDATE workspaces SET status='deleting'`, "WORKSPACE_ACCESS_DENIED"},
		{"foreign_workspace", "", "PROJECT_NOT_FOUND"},
		{"project", `UPDATE projects SET deleted_at='2026-09-10T00:00:00Z'`, "PROJECT_NOT_FOUND"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "event-access.db"), loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			project, _, run := startNovelRunForLifecycle(t, store, "Event access")
			reader := identity.DefaultLocalPrincipal()
			reader.UserID, reader.Role = "event_reader", identity.RoleViewer
			if err := store.BootstrapPrincipal(context.Background(), reader); err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithPrincipal(context.Background(), reader)
			readers := []struct {
				name string
				read func(context.Context) error
			}{
				{"project_cursor", func(ctx context.Context) error {
					_, err := store.CurrentProjectEventSeq(ctx, project.ProjectID)
					return err
				}},
				{"run_cursor", func(ctx context.Context) error { _, err := store.CurrentRunEventSeq(ctx, run.Run.RunID); return err }},
				{"project_replay", func(ctx context.Context) error {
					_, err := store.ListProjectEvents(ctx, project.ProjectID, 0, 1)
					return err
				}},
				{"run_replay", func(ctx context.Context) error { _, err := store.ListRunEvents(ctx, run.Run.RunID, 0, 1); return err }},
			}
			for _, read := range readers {
				if err := read.read(ctx); err != nil {
					t.Fatalf("viewer %s: %v", read.name, err)
				}
			}
			if mutation.statement != "" {
				if _, err := store.db.Exec(mutation.statement); err != nil {
					t.Fatal(err)
				}
			} else {
				reader.WorkspaceID = "foreign_workspace"
				ctx = identity.WithPrincipal(context.Background(), reader)
			}
			for _, read := range readers {
				want := mutation.code
				if mutation.name == "project" && (read.name == "run_cursor" || read.name == "run_replay") {
					want = "RUN_NOT_FOUND"
				}
				assertDomainCode(t, read.read(ctx), want)
			}
		})
	}
}

func TestEventReplayDoesNotExposeABatchAfterMembershipRevocation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "event-replay.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, _, run := startNovelRunForLifecycle(t, store, "Replay revocation")
	for _, scope := range []string{"project", "run"} {
		list := func(cursor int64) (EventBatch, error) {
			if scope == "run" {
				return store.ListRunEvents(ctx, run.Run.RunID, cursor, 1)
			}
			return store.ListProjectEvents(ctx, project.ProjectID, cursor, 1)
		}
		if err := store.BootstrapPrincipal(context.Background(), identity.DefaultLocalPrincipal()); err != nil {
			t.Fatal(err)
		}
		first, err := list(0)
		if err != nil || len(first.Items) != 1 || !first.HasMore {
			t.Fatalf("%s initial replay: %+v %v", scope, first, err)
		}
		if _, err := store.db.Exec(`UPDATE workspace_memberships SET status='disabled'`); err != nil {
			t.Fatal(err)
		}
		denied, err := list(first.NextSeq)
		assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
		if len(denied.Items) != 0 || denied.CurrentSeq != 0 || denied.NextSeq != 0 {
			t.Fatalf("%s denied replay leaked metadata: %+v", scope, denied)
		}
	}
}
