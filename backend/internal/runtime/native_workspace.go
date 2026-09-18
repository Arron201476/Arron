package runtime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspaceSchema = `
CREATE TABLE IF NOT EXISTS native_workspace_leases (
	session_id TEXT PRIMARY KEY,
	activity_key TEXT NOT NULL UNIQUE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	user_id TEXT NOT NULL REFERENCES users(user_id),
	execution_epoch TEXT NOT NULL,
	holder_hash TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK(generation > 0),
	lease_until TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('reserved','closing')),
	snapshot_version INTEGER NOT NULL DEFAULT 0 CHECK(snapshot_version >= 0),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_native_workspace_project ON native_workspace_leases(project_id);
CREATE TABLE IF NOT EXISTS native_workspace_snapshots (
	session_id TEXT NOT NULL REFERENCES native_workspace_leases(session_id),
	version INTEGER NOT NULL CHECK(version > 0),
	request_id TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	archive BLOB NOT NULL,
	entries_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(session_id,version),
	UNIQUE(session_id,request_id)
);
`

var nativeLeaseKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var nativeSessionIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func newNativeWorkspaceUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6], id[8] = (id[6]&15)|64, (id[8]&63)|128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

// A reservation owns private execution state only. It neither starts a backend
// nor authorizes a model tool, filesystem publication, or a shell command.
type NativeWorkspaceLease struct {
	SessionID       string    `json:"session_id"`
	Generation      int64     `json:"generation"`
	LeaseUntil      time.Time `json:"lease_until"`
	SnapshotVersion int       `json:"snapshot_version"`
}

type ReserveNativeWorkspaceCommand struct {
	HolderKey          string
	ExpectedGeneration int64
	DispatchGeneration int64
}

type NativeWorkspaceAccess struct {
	SessionID          string
	Generation         int64
	HolderKey          string
	DispatchGeneration int64
}

type NativeWorkspaceSnapshot struct {
	SessionID string                          `json:"session_id"`
	Version   int                             `json:"version"`
	Snapshot  scriptsandbox.WorkspaceSnapshot `json:"snapshot"`
}

type nativeWorkspaceOwner struct {
	activityKey, workspaceID, projectID, userID, epoch string
	leaseUntil                                         time.Time
}

type nativeWorkspaceRecord struct {
	NativeWorkspaceLease
	activityKey, workspaceID, projectID, userID, epoch, holderHash, status string
}

const nativeWorkspaceSelect = `SELECT session_id,generation,lease_until,snapshot_version,
	activity_key,workspace_id,project_id,user_id,execution_epoch,holder_hash,status FROM native_workspace_leases`

func scanNativeWorkspace(row interface{ Scan(...any) error }) (nativeWorkspaceRecord, error) {
	var record nativeWorkspaceRecord
	var until string
	err := row.Scan(&record.SessionID, &record.Generation, &until, &record.SnapshotVersion,
		&record.activityKey, &record.workspaceID, &record.projectID, &record.userID, &record.epoch, &record.holderHash, &record.status)
	if err == nil {
		record.LeaseUntil, err = parseTime(until)
	}
	return record, err
}

func (s *Store) nativeWorkspaceOwnerTx(ctx context.Context, tx *sql.Tx, dispatchGeneration int64, allowPausing bool) (nativeWorkspaceOwner, error) {
	transport, ok := identity.FromContext(ctx)
	activity, active := AgentActivityFromContext(ctx)
	if !ok || transport.Kind != identity.KindService || !active || activity.AllowTerminal {
		return nativeWorkspaceOwner{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "Native workspaces require a live internal execution identity.")
	}
	user, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return nativeWorkspaceOwner{}, err
	}
	if !user.Allows(identity.RoleEditor) {
		return nativeWorkspaceOwner{}, domainError("WORKSPACE_ACCESS_DENIED", "Execution user no longer has editor access.")
	}
	owner := nativeWorkspaceOwner{activityKey: instructionActivityKey(activity), workspaceID: user.WorkspaceID,
		projectID: activity.ProjectID, userID: user.UserID, leaseUntil: s.now().Add(60 * time.Second)}
	var status string
	if activity.AgentTurnID != "" {
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT dispatch_generation,status FROM agent_turns WHERE agent_turn_id=?`, activity.AgentTurnID).Scan(&generation, &status); err != nil {
			return owner, err
		}
		if dispatchGeneration < 1 || dispatchGeneration != generation {
			return owner, domainError("NATIVE_WORKSPACE_EXECUTION_STALE", "Conversation dispatch generation no longer matches.")
		}
		owner.epoch = sha256Hex([]byte(owner.activityKey + ":" + strconv.FormatInt(generation, 10)))
	} else {
		if dispatchGeneration != 0 {
			return owner, domainError("AGENT_ACTIVITY_INVALID", "Only conversation executions have a dispatch generation.")
		}
		owner.epoch = sha256Hex([]byte(activity.AttemptToken))
		var until time.Time
		if activity.MemoryGenerationID != "" {
			job, err := memoryGenerationQuery(ctx, tx, activity.MemoryGenerationID)
			if err != nil {
				return owner, err
			}
			// Each SDK phase has a distinct initialization inventory. Reusing an
			// extraction workspace would prevent consolidation inputs from sealing.
			var legacy bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_leases WHERE activity_key=?)`, owner.activityKey).Scan(&legacy); err != nil {
				return owner, err
			}
			if legacy {
				return owner, domainError("NATIVE_WORKSPACE_EXECUTION_STALE", "Legacy memory workspace has no phase binding; explicit recovery is required.")
			}
			owner.activityKey += ":" + job.Phase
			owner.epoch = sha256Hex([]byte(owner.activityKey + ":" + activity.AttemptToken))
			until, err = parseTime(job.LeaseUntil)
			if err != nil {
				return owner, err
			}
			status = job.Status
		} else if activity.AgentTaskAttemptID != "" {
			state, err := loadAgentTaskAttemptStateTx(ctx, tx, activity.AgentTaskAttemptID)
			if err != nil {
				return owner, err
			}
			until, status = state.LeaseUntil, state.TaskStatus
		} else {
			state, err := loadExecutionToolStateTx(ctx, tx, activity.ExecutionAttemptID)
			if err != nil {
				return owner, err
			}
			if state.OutputRepairOnly {
				return owner, domainError("AGENT_TOOL_EXECUTION_MODE_UNSUPPORTED", "Output-only repair cannot reopen an execution workspace.")
			}
			until, status = state.LeaseUntil, state.RunStatus
		}
		if until.Before(owner.leaseUntil) {
			owner.leaseUntil = until
		}
	}
	if status == "pausing" && !allowPausing {
		return owner, domainError("NATIVE_WORKSPACE_EXECUTION_PAUSING", "A pausing execution cannot acquire another workspace lease.")
	}
	return owner, nil
}

func nativeWorkspaceMatchesOwner(record nativeWorkspaceRecord, owner nativeWorkspaceOwner) bool {
	return record.activityKey == owner.activityKey && record.workspaceID == owner.workspaceID &&
		record.projectID == owner.projectID && record.userID == owner.userID
}

func (s *Store) ReserveNativeWorkspace(ctx context.Context, command ReserveNativeWorkspaceCommand) (NativeWorkspaceLease, error) {
	if !nativeLeaseKeyPattern.MatchString(command.HolderKey) || command.ExpectedGeneration < 0 {
		return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "A bounded random holder key and expected generation are required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	defer tx.Rollback()
	owner, err := s.nativeWorkspaceOwnerTx(ctx, tx, command.DispatchGeneration, false)
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	now, holderHash := s.now(), sha256Hex([]byte(command.HolderKey))
	record, err := scanNativeWorkspace(tx.QueryRowContext(ctx, nativeWorkspaceSelect+` WHERE activity_key=?`, owner.activityKey))
	if errors.Is(err, sql.ErrNoRows) {
		if command.ExpectedGeneration != 0 {
			return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_CONFLICT", "Workspace reservation changed.")
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_workspace_leases WHERE project_id=?`, owner.projectID).Scan(&count); err != nil {
			return NativeWorkspaceLease{}, err
		}
		if count >= 256 {
			return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Project workspace history quota reached.")
		}
		var sessionID string
		sessionID, err = newNativeWorkspaceUUID()
		if err != nil {
			return NativeWorkspaceLease{}, err
		}
		record.NativeWorkspaceLease = NativeWorkspaceLease{SessionID: sessionID, Generation: 1, LeaseUntil: owner.leaseUntil}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_workspace_leases(session_id,activity_key,workspace_id,project_id,user_id,execution_epoch,holder_hash,generation,lease_until,status,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,1,?,'reserved',?,?)`, sessionID, owner.activityKey, owner.workspaceID, owner.projectID, owner.userID, owner.epoch, holderHash, formatTime(owner.leaseUntil), formatTime(now), formatTime(now))
	} else if err == nil {
		if !nativeWorkspaceMatchesOwner(record, owner) || record.status != "reserved" {
			return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "Workspace is not available to this execution.")
		}
		sameHolder := subtle.ConstantTimeCompare([]byte(record.holderHash), []byte(holderHash)) == 1
		if sameHolder && record.epoch == owner.epoch && record.LeaseUntil.After(now) {
			if command.ExpectedGeneration != 0 && command.ExpectedGeneration != record.Generation {
				return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_CONFLICT", "Workspace generation changed.")
			}
		} else {
			if record.epoch == owner.epoch && record.LeaseUntil.After(now) {
				return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_BUSY", "Another client holds the execution workspace.")
			}
			if sameHolder {
				return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_LEASE_EXPIRED", "Expired or superseded holders must not renew their old lease.")
			}
			if command.ExpectedGeneration != record.Generation {
				return NativeWorkspaceLease{}, domainError("NATIVE_WORKSPACE_CONFLICT", "Takeover requires the exact previous generation.")
			}
			record.Generation++
		}
		record.LeaseUntil = owner.leaseUntil
		_, err = tx.ExecContext(ctx, `UPDATE native_workspace_leases SET generation=?,execution_epoch=?,holder_hash=?,lease_until=?,updated_at=? WHERE session_id=?`, record.Generation, owner.epoch, holderHash, formatTime(owner.leaseUntil), formatTime(now), record.SessionID)
	}
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return NativeWorkspaceLease{}, err
	}
	return record.NativeWorkspaceLease, nil
}

func (s *Store) nativeWorkspaceAccessTx(ctx context.Context, tx *sql.Tx, access NativeWorkspaceAccess) (nativeWorkspaceRecord, error) {
	if !nativeSessionIDPattern.MatchString(access.SessionID) || !nativeLeaseKeyPattern.MatchString(access.HolderKey) || access.Generation < 1 {
		return nativeWorkspaceRecord{}, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "Workspace access binding is invalid.")
	}
	owner, err := s.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, true)
	if err != nil {
		return nativeWorkspaceRecord{}, err
	}
	record, err := scanNativeWorkspace(tx.QueryRowContext(ctx, nativeWorkspaceSelect+` WHERE session_id=?`, access.SessionID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && !nativeWorkspaceMatchesOwner(record, owner) {
		return nativeWorkspaceRecord{}, domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "Workspace does not belong to this execution.")
	}
	if err != nil {
		return record, err
	}
	if record.status != "reserved" || record.Generation != access.Generation || record.epoch != owner.epoch ||
		subtle.ConstantTimeCompare([]byte(record.holderHash), []byte(sha256Hex([]byte(access.HolderKey)))) != 1 {
		return record, domainError("NATIVE_WORKSPACE_LEASE_INVALID", "Workspace lease has been superseded.")
	}
	if !record.LeaseUntil.After(s.now()) {
		return record, domainError("NATIVE_WORKSPACE_LEASE_EXPIRED", "Workspace lease expired.")
	}
	return record, nil
}

func (s *Store) ValidateNativeWorkspaceLease(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceLease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	defer tx.Rollback()
	record, err := s.nativeWorkspaceAccessTx(ctx, tx, access)
	return record.NativeWorkspaceLease, err
}

func (s *Store) RenewNativeWorkspaceLease(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceLease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	defer tx.Rollback()
	record, err := s.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	// A pausing owner may finish saving its existing workspace, but renewal
	// never acquires a new lease or revives an expired/superseded holder.
	owner, err := s.nativeWorkspaceOwnerTx(ctx, tx, access.DispatchGeneration, true)
	if err != nil {
		return NativeWorkspaceLease{}, err
	}
	record.LeaseUntil = owner.leaseUntil
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_leases SET lease_until=?,updated_at=? WHERE session_id=?`,
		formatTime(record.LeaseUntil), formatTime(s.now()), record.SessionID); err != nil {
		return NativeWorkspaceLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return NativeWorkspaceLease{}, err
	}
	return record.NativeWorkspaceLease, nil
}

func (s *Store) CurrentNativeWorkspaceLease(ctx context.Context, dispatchGeneration int64) (*NativeWorkspaceLease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	owner, err := s.nativeWorkspaceOwnerTx(ctx, tx, dispatchGeneration, true)
	if err != nil {
		return nil, err
	}
	record, err := scanNativeWorkspace(tx.QueryRowContext(ctx, nativeWorkspaceSelect+` WHERE activity_key=?`, owner.activityKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !nativeWorkspaceMatchesOwner(record, owner) || record.status != "reserved" {
		return nil, domainError("NATIVE_WORKSPACE_SCOPE_MISMATCH", "Workspace is unavailable to the current execution.")
	}
	return &record.NativeWorkspaceLease, nil
}

func (s *Store) SaveNativeWorkspaceSnapshot(ctx context.Context, access NativeWorkspaceAccess, requestID string, expectedVersion int, data []byte) (NativeWorkspaceSnapshot, error) {
	return s.saveNativeWorkspaceSnapshot(ctx, access, requestID, expectedVersion, data, "")
}

func (s *Store) saveNativeWorkspaceSnapshot(ctx context.Context, access NativeWorkspaceAccess, requestID string, expectedVersion int, data []byte, operationID string) (NativeWorkspaceSnapshot, error) {
	if !tenantIdentifierPattern.MatchString(requestID) || expectedVersion < 0 {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "Snapshot request identity and version are required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	defer tx.Rollback()
	record, err := s.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	if err := validateNativeSnapshotOperationTx(ctx, tx, access.SessionID, operationID); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	// Persistence remains bounded by platform defaults until trusted environment
	// policy is wired. SDK/model parameters never select resource limits here.
	snapshot, err := scriptsandbox.ParseWorkspaceSnapshot(data, scriptsandbox.DefaultLimits())
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	if !record.LeaseUntil.After(s.now()) {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_LEASE_EXPIRED", "Workspace lease expired during archive validation.")
	}
	var previousVersion int
	var previousHash string
	err = tx.QueryRowContext(ctx, `SELECT version,content_hash FROM native_workspace_snapshots WHERE session_id=? AND request_id=?`, record.SessionID, requestID).Scan(&previousVersion, &previousHash)
	if err == nil {
		if previousVersion != expectedVersion+1 || previousHash != snapshot.SHA256 {
			return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "Snapshot request ID was reused with different content or parent version.")
		}
		return readNativeWorkspaceSnapshotTx(ctx, tx, record.SessionID, previousVersion, snapshot.SHA256)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return NativeWorkspaceSnapshot{}, err
	}
	if expectedVersion != record.SnapshotVersion {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_SNAPSHOT_CONFLICT", "Workspace snapshot advanced; existing contents were not overwritten.")
	}
	var count int
	var storedBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(archive)+length(CAST(entries_json AS BLOB))),0) FROM native_workspace_snapshots WHERE session_id=?`, record.SessionID).Scan(&count, &storedBytes); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	metadata, err := json.Marshal(snapshot.Entries)
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	additional := int64(len(snapshot.Archive) + len(metadata))
	if count >= 128 || storedBytes+additional > 128<<20 {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_QUOTA_EXCEEDED", "Execution snapshot history quota reached.")
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, record.workspaceID, "storage_bytes", additional); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	version, now := expectedVersion+1, formatTime(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO native_workspace_snapshots(session_id,version,request_id,content_hash,archive,entries_json,created_at) VALUES(?,?,?,?,?,?,?)`, record.SessionID, version, requestID, snapshot.SHA256, snapshot.Archive, string(metadata), now); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_leases SET snapshot_version=?,updated_at=? WHERE session_id=?`, version, now, record.SessionID); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	return NativeWorkspaceSnapshot{SessionID: record.SessionID, Version: version, Snapshot: snapshot}, nil
}

func (s *Store) ReadNativeWorkspaceSnapshot(ctx context.Context, access NativeWorkspaceAccess, version int, expectedHash string) (NativeWorkspaceSnapshot, error) {
	if version < 1 || !nativeLeaseKeyPattern.MatchString(expectedHash) {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "An exact snapshot version and hash are required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	defer tx.Rollback()
	if _, err := s.nativeWorkspaceAccessTx(ctx, tx, access); err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	return readNativeWorkspaceSnapshotTx(ctx, tx, access.SessionID, version, expectedHash)
}

func readNativeWorkspaceSnapshotTx(ctx context.Context, tx *sql.Tx, sessionID string, version int, expectedHash string) (NativeWorkspaceSnapshot, error) {
	var data []byte
	var storedHash, metadata string
	err := tx.QueryRowContext(ctx, `SELECT archive,content_hash,entries_json FROM native_workspace_snapshots WHERE session_id=? AND version=?`, sessionID, version).Scan(&data, &storedHash, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_SNAPSHOT_NOT_FOUND", "Selected snapshot is unavailable; no replacement was selected.")
	}
	if err != nil {
		return NativeWorkspaceSnapshot{}, err
	}
	snapshot, err := scriptsandbox.ParseWorkspaceSnapshot(data, scriptsandbox.DefaultLimits())
	if err != nil || snapshot.SHA256 != storedHash || storedHash != expectedHash || sha256Hex(data) != storedHash {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Snapshot integrity or selected version hash does not match.")
	}
	actualMetadata, _ := json.Marshal(snapshot.Entries)
	if string(actualMetadata) != metadata {
		return NativeWorkspaceSnapshot{}, domainError("NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Snapshot metadata failed integrity validation.")
	}
	return NativeWorkspaceSnapshot{SessionID: sessionID, Version: version, Snapshot: snapshot}, nil
}

func (s *Store) ReadNativeWorkspaceSnapshotReceipt(ctx context.Context, access NativeWorkspaceAccess, requestID string, expectedVersion int, expectedHash string) (NativeWorkspaceSnapshot, bool, error) {
	if !tenantIdentifierPattern.MatchString(requestID) || expectedVersion < 0 || !nativeLeaseKeyPattern.MatchString(expectedHash) {
		return NativeWorkspaceSnapshot{}, false, domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "An exact snapshot save request and content hash are required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	defer tx.Rollback()
	if _, err := s.nativeWorkspaceAccessTx(ctx, tx, access); err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	var version int
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT version,content_hash FROM native_workspace_snapshots WHERE session_id=? AND request_id=?`, access.SessionID, requestID).Scan(&version, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeWorkspaceSnapshot{}, false, nil
	}
	if err != nil {
		return NativeWorkspaceSnapshot{}, false, err
	}
	if version != expectedVersion+1 || hash != expectedHash {
		return NativeWorkspaceSnapshot{}, false, domainError("NATIVE_WORKSPACE_REQUEST_CONFLICT", "Save receipt does not match the exact requested parent and content.")
	}
	result, err := readNativeWorkspaceSnapshotTx(ctx, tx, access.SessionID, version, hash)
	return result, err == nil, err
}

func closeNativeProjectWorkspacesTx(ctx context.Context, tx *sql.Tx, projectID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_pty_operations SET status='deleted',result_json='',result_hash='',reserved_bytes=0,updated_at=? WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, formatTime(now), projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_pty_processes SET status='deleted',updated_at=? WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, formatTime(now), projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_file_operations SET status='deleted',result_json='',result_hash='',reserved_bytes=0,updated_at=? WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, formatTime(now), projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_manifests SET status='deleted',manifest_json='',done_json='[]',updated_at=? WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, formatTime(now), projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_workspace_commands SET status='deleted',stdout=X'',stderr=X'',result_hash='',reserved_bytes=0,error_code='',updated_at=? WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, formatTime(now), projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM native_workspace_snapshots WHERE session_id IN (SELECT session_id FROM native_workspace_leases WHERE project_id=?)`, projectID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE native_workspace_leases SET status='closing',generation=generation+1,holder_hash='',lease_until=?,updated_at=? WHERE project_id=? AND status!='closing'`, formatTime(now), formatTime(now), projectID)
	return err
}
