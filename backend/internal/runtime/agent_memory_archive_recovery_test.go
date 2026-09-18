package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryArchiveMissingSourceClassificationDoesNotHideDatabaseFailures(t *testing.T) {
	for _, err := range []error{sql.ErrNoRows, domainError("EXECUTION_ATTEMPT_NOT_FOUND", "missing"), domainError("AGENT_TASK_ATTEMPT_NOT_FOUND", "missing")} {
		assertDomainCode(t, memoryArchiveSourceLookupError(err), "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED")
	}
	for _, err := range []error{nil, errors.New("database is locked"), context.Canceled, domainError("UNKNOWN_NOT_FOUND", "unknown"), domainError("AGENT_MEMORY_INVALID", "corrupt")} {
		if got := memoryArchiveSourceLookupError(err); got != err {
			t.Fatalf("uncertain failure was reclassified: %v", got)
		}
	}
}

func TestMemoryArchiveRecoveryThreeCompletedSourcesAndDeletion(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, delegated, _ := nativeWorkspaceFixture(t, mode)
			source, err := store.ResolveAgentMemorySnapshot(delegated)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), UpdateAgentMemoryPreferencesCommand{ProjectID: source.ProjectID, ArchiveEnabled: true, RequestID: "recover-completed"})
			if err != nil {
				t.Fatal(err)
			}
			activity, _ := AgentActivityFromContext(delegated)
			// Seed only durable terminal state to isolate recovery authorization.
			var statements []string
			var id string
			switch mode {
			case "conversation":
				id = activity.AgentTurnID
				statements = []string{`UPDATE agent_turns SET status='committed' WHERE agent_turn_id=?`}
			case "background":
				id = activity.AgentTaskAttemptID
				statements = []string{`UPDATE agent_task_attempts SET status='completed' WHERE agent_task_attempt_id=?`, `UPDATE agent_tasks SET status='completed' WHERE agent_task_id=(SELECT agent_task_id FROM agent_task_attempts WHERE agent_task_attempt_id=?)`}
			case "stateful":
				id = activity.ExecutionAttemptID
				statements = []string{`UPDATE execution_attempts SET status='succeeded' WHERE attempt_id=?`, `UPDATE task_items SET status='succeeded' WHERE current_attempt_id=?`}
			}
			for _, statement := range statements {
				if _, err := store.db.Exec(statement, id); err != nil {
					t.Fatal(err)
				}
			}
			service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			command := memoryRolloutFixture(source)
			for i := 0; i < 2; i++ {
				if receipt, err := store.RecoverAgentMemoryArchive(service, command, grant.Revision); err != nil || receipt.ContentHash != command.ContentHash {
					t.Fatalf("completed recovery %d: %+v %v", i, receipt, err)
				}
			}
			missing := source
			kind, _, _ := strings.Cut(source.ActivityKey, ":")
			missing.ActivityKey = kind + ":nonexistent-original-source"
			_, err = store.RecoverAgentMemoryArchive(service, memoryRolloutFixture(missing), grant.Revision)
			assertDomainCode(t, err, "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED")
			if _, err := store.db.Exec(`UPDATE projects SET deleted_at=? WHERE project_id=?`, "2026-09-14T00:00:00Z", source.ProjectID); err != nil {
				t.Fatal(err)
			}
			_, err = store.RecoverAgentMemoryArchive(service, command, grant.Revision)
			assertDomainCode(t, err, "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED")
		})
	}
}

func TestMemoryArchiveRecoveryRequiresIndependentServiceAndCompletedSource(t *testing.T) {
	service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, delegated, _ := nativeWorkspaceFixture(t, mode)
			source, err := store.ResolveAgentMemorySnapshot(delegated)
			if err != nil {
				t.Fatal(err)
			}
			command := memoryRolloutFixture(source)
			for _, denied := range []context.Context{context.Background(), mcpOwnerContext(), delegated} {
				_, err := store.RecoverAgentMemoryArchive(denied, command, 1)
				assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			}
			_, err = store.RecoverAgentMemoryArchive(service, command, 1)
			assertDomainCode(t, err, "AGENT_MEMORY_ARCHIVE_NOT_READY")
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_memory_rollouts`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unconfirmed source was archived: %d %v", count, err)
			}
		})
	}
}

func TestMemoryArchiveRecoveryRechecksOwnerConsentAndForget(t *testing.T) {
	store, _, delegated, _ := nativeWorkspaceFixture(t, "conversation")
	source, err := store.ResolveAgentMemorySnapshot(delegated)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), UpdateAgentMemoryPreferencesCommand{ProjectID: source.ProjectID, ArchiveEnabled: true, RequestID: "recovery-grant"})
	if err != nil {
		t.Fatal(err)
	}
	activity, _ := AgentActivityFromContext(delegated)
	// Isolate recovery authorization from the independently tested commit path.
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='committed' WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	command := memoryRolloutFixture(source)
	foreign := command
	foreign.UserID = "another-owner"
	_, err = store.RecoverAgentMemoryArchive(service, foreign, grant.Revision)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	_, err = store.RecoverAgentMemoryArchive(service, command, grant.Revision+1)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")

	var payload map[string]any
	if err := json.Unmarshal([]byte(command.RolloutJSONL), &payload); err != nil {
		t.Fatal(err)
	}
	payload["terminal_metadata"] = map[string]any{"terminal_state": "failed", "exception_type": "Failure", "exception_message": nil, "has_final_output": false}
	delete(payload, "final_output")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	failed := command
	failed.RolloutJSONL = string(encoded) + "\n"
	failed.ContentHash = sha256Hex([]byte(failed.RolloutJSONL))
	_, err = store.RecoverAgentMemoryArchive(service, failed, grant.Revision)
	assertDomainCode(t, err, "AGENT_MEMORY_ARCHIVE_SOURCE_REVOKED")

	first, err := store.RecoverAgentMemoryArchive(service, command, grant.Revision)
	if err != nil || first.ContentHash != command.ContentHash {
		t.Fatalf("recovery failed: %+v %v", first, err)
	}
	again, err := store.RecoverAgentMemoryArchive(service, command, grant.Revision)
	if err != nil || again != first {
		t.Fatalf("recovery was not idempotent: %+v %v", again, err)
	}
	if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: source.ProjectID, ExpectedVersion: source.Version, Forget: true, RequestID: "forget-recovery"}); err != nil {
		t.Fatal(err)
	}
	_, err = store.RecoverAgentMemoryArchive(service, command, grant.Revision)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
}
