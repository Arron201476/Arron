package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"content-agent/backend/internal/identity"
)

const maxMemoryRolloutBytes = 4 << 20

var memorySegmentPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func migrateAgentMemoryRollouts(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_rollouts (
		activity_key TEXT NOT NULL REFERENCES agent_memory_snapshots(activity_key),
		segment_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		user_id TEXT NOT NULL REFERENCES users(user_id),
		content_hash TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		envelope_json TEXT NOT NULL,
		forgotten INTEGER NOT NULL DEFAULT 0 CHECK(forgotten IN (0,1)),
		PRIMARY KEY(activity_key,segment_id)
	);`)
	if err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{
		{"archive_revision", "INTEGER NOT NULL DEFAULT 0 CHECK(archive_revision>=0)"},
		{"archive_generate", "INTEGER NOT NULL DEFAULT 0 CHECK(archive_generate IN (0,1))"},
		{"archive_generation_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		present, err := tableHasColumn(db, "agent_memory_rollouts", column.name)
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec("ALTER TABLE agent_memory_rollouts ADD COLUMN " + column.name + " " + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

type AgentMemoryRollout struct {
	SchemaVersion string `json:"schema_version"`
	ActivityKey   string `json:"activity_key"`
	WorkspaceID   string `json:"workspace_id"`
	ProjectID     string `json:"project_id"`
	UserID        string `json:"user_id"`
	SegmentID     string `json:"segment_id"`
	MemoryVersion int    `json:"memory_version"`
	MemoryHash    string `json:"memory_hash"`
	ReadEnabled   bool   `json:"read_enabled"`
	ContentHash   string `json:"content_hash"`
	RolloutJSONL  string `json:"rollout_jsonl"`
}

type AgentMemoryRolloutReceipt struct {
	ActivityKey string `json:"activity_key"`
	SegmentID   string `json:"segment_id"`
	ProjectID   string `json:"project_id"`
	UserID      string `json:"user_id"`
	ContentHash string `json:"content_hash"`
}

func validateMemoryRollout(command AgentMemoryRollout) error {
	invalid := domainError("AGENT_MEMORY_INVALID", "Private memory rollout failed its immutable source contract.")
	if command.SchemaVersion != "agent_memory_rollout.v1" || !memorySegmentPattern.MatchString(command.SegmentID) || command.MemoryVersion < 0 || !nativeLeaseKeyPattern.MatchString(command.MemoryHash) || len(command.RolloutJSONL) > maxMemoryRolloutBytes || !utf8.ValidString(command.RolloutJSONL) || sha256Hex([]byte(command.RolloutJSONL)) != command.ContentHash {
		return invalid
	}
	line := strings.TrimSuffix(command.RolloutJSONL, "\n")
	if strings.ContainsAny(line, "\r\n") {
		return invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &fields) != nil || len(fields) < 5 || len(fields) > 7 {
		return invalid
	}
	for key := range fields {
		switch key {
		case "updated_at", "rollout_id", "input", "generated_items", "terminal_metadata", "interruptions", "final_output":
		default:
			return invalid
		}
	}
	var updatedAt, rolloutID string
	if json.Unmarshal(fields["updated_at"], &updatedAt) != nil || json.Unmarshal(fields["rollout_id"], &rolloutID) != nil || rolloutID != sha256Hex([]byte(command.ActivityKey+":"+command.SegmentID)) {
		return invalid
	}
	if _, err := time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return invalid
	}
	for _, key := range []string{"input", "generated_items", "interruptions"} {
		if key == "interruptions" && fields[key] == nil {
			continue
		}
		var items []map[string]any
		if json.Unmarshal(fields[key], &items) != nil || items == nil {
			return invalid
		}
		for _, item := range items {
			if item == nil {
				return invalid
			}
		}
	}
	var terminal struct {
		State            string  `json:"terminal_state"`
		ExceptionType    *string `json:"exception_type"`
		ExceptionMessage *string `json:"exception_message"`
		HasFinalOutput   bool    `json:"has_final_output"`
	}
	var terminalFields map[string]json.RawMessage
	if json.Unmarshal(fields["terminal_metadata"], &terminalFields) != nil || len(terminalFields) != 4 {
		return invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(fields["terminal_metadata"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&terminal) != nil || decoder.Decode(new(any)) != io.EOF || terminal.ExceptionMessage != nil {
		return invalid
	}
	switch terminal.State {
	case "completed", "interrupted", "cancelled", "failed", "max_turns_exceeded", "guardrail_tripped":
	default:
		return invalid
	}
	hasOutput := len(fields["final_output"]) > 0 && !bytes.Equal(fields["final_output"], []byte("null"))
	if hasOutput != terminal.HasFinalOutput || (terminal.State == "completed") != hasOutput {
		return invalid
	}
	return nil
}

func (s *Store) memoryRolloutSourceTx(ctx context.Context, tx *sql.Tx) (AgentMemorySnapshot, error) {
	transport, ok := identity.FromContext(ctx)
	activity, active := AgentActivityFromContext(ctx)
	if !ok || transport.Kind != identity.KindService || !active {
		return AgentMemorySnapshot{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "Memory rollout access requires its original internal execution.")
	}
	user, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return AgentMemorySnapshot{}, err
	}
	if !user.Allows(identity.RoleEditor) {
		return AgentMemorySnapshot{}, domainError("WORKSPACE_ACCESS_DENIED", "Memory rollout requires its current execution owner.")
	}
	if err := validateAgentMemorySnapshotQuery(ctx, tx, activity, user); err != nil {
		return AgentMemorySnapshot{}, err
	}
	source, err := agentMemorySnapshotQuery(ctx, tx, activity, user)
	if err != nil {
		return source, err
	}
	current, err := agentMemoryQuery(ctx, tx, user, source.ProjectID, 0)
	if err != nil {
		return AgentMemorySnapshot{}, err
	}
	// Even a rollout from an initially empty memory can contain private user
	// information. A later forget must revoke it, not just memory-reading runs.
	if current.Version != source.Version || current.ContentHash != source.ContentHash {
		check := source
		check.ReadEnabled = true
		if err := validateAgentMemoryReferenceQuery(ctx, tx, check); err != nil {
			return AgentMemorySnapshot{}, err
		}
	}
	return source, nil
}

func memoryRolloutMatchesSource(command AgentMemoryRollout, source AgentMemorySnapshot) bool {
	return command.ActivityKey == source.ActivityKey && command.WorkspaceID == source.WorkspaceID && command.ProjectID == source.ProjectID && command.UserID == source.UserID && command.MemoryVersion == source.Version && command.MemoryHash == source.ContentHash && command.ReadEnabled == source.ReadEnabled
}

func (s *Store) SaveAgentMemoryRollout(ctx context.Context, command AgentMemoryRollout) (AgentMemoryRolloutReceipt, error) {
	return s.saveAgentMemoryRollout(ctx, command, nil, false)
}

func (s *Store) ArchiveAgentMemoryRollout(ctx context.Context, command AgentMemoryRollout, consentRevision int) (AgentMemoryRolloutReceipt, error) {
	activity, active := AgentActivityFromContext(ctx)
	if consentRevision < 1 || !active || activity.MemoryGenerationID != "" {
		return AgentMemoryRolloutReceipt{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "Automatic archival requires original execution consent.")
	}
	return s.saveAgentMemoryRollout(ctx, command, &consentRevision, false)
}

func (s *Store) saveAgentMemoryRollout(ctx context.Context, command AgentMemoryRollout, consentRevision *int, recovery bool) (AgentMemoryRolloutReceipt, error) {
	var receipt AgentMemoryRolloutReceipt
	if err := validateMemoryRollout(command); err != nil {
		return receipt, err
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return receipt, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	var source AgentMemorySnapshot
	if recovery {
		source, err = s.memoryArchiveRecoverySourceTx(ctx, tx, command)
	} else {
		source, err = s.memoryRolloutSourceTx(ctx, tx)
	}
	if err != nil {
		return receipt, err
	}
	if !memoryRolloutMatchesSource(command, source) {
		return receipt, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory rollout belongs to another source.")
	}
	autoGenerate := false
	if consentRevision != nil {
		policy, _, err := memoryPreferencesQuery(ctx, tx, identity.Principal{UserID: source.UserID, WorkspaceID: source.WorkspaceID}, source.ProjectID)
		if err != nil {
			return receipt, err
		}
		if !policy.ArchiveEnabled || policy.Revision != *consentRevision {
			return receipt, domainError("AGENT_MEMORY_CONFLICT", "Memory archival consent changed or was revoked.")
		}
		autoGenerate = policy.GenerateEnabled
	}
	requestHash := sha256Hex(encoded)
	var priorHash string
	var forgotten bool
	err = tx.QueryRowContext(ctx, `SELECT request_hash,forgotten FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=?`, command.ActivityKey, command.SegmentID).Scan(&priorHash, &forgotten)
	if err == nil {
		if forgotten {
			return receipt, domainError("AGENT_MEMORY_CONFLICT", "Forgotten memory input cannot be restored.")
		}
		if priorHash != requestHash {
			return receipt, domainError("IDEMPOTENCY_CONFLICT", "Memory segment already has different frozen input.")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return receipt, err
	} else {
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, source.WorkspaceID, "storage_bytes", int64(len(encoded)+1024)); err != nil {
			return receipt, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_memory_rollouts(activity_key,segment_id,workspace_id,project_id,user_id,content_hash,request_hash,envelope_json) VALUES(?,?,?,?,?,?,?,?)`, source.ActivityKey, command.SegmentID, source.WorkspaceID, source.ProjectID, source.UserID, command.ContentHash, requestHash, string(encoded)); err != nil {
			return receipt, err
		}
	}
	generationID := ""
	if autoGenerate {
		user := identity.Principal{Kind: identity.KindUser, UserID: source.UserID, WorkspaceID: source.WorkspaceID}
		job, err := s.queueAgentMemoryGenerationTx(ctx, tx, user, source.ProjectID, source.ActivityKey, command.SegmentID, command.ContentHash)
		if err != nil {
			return receipt, err
		}
		generationID = job.GenerationID
	}
	if consentRevision != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_rollouts SET archive_revision=?,archive_generate=?,archive_generation_id=? WHERE activity_key=? AND segment_id=?`, *consentRevision, autoGenerate, generationID, source.ActivityKey, command.SegmentID); err != nil {
			return receipt, err
		}
	}
	if err := tx.Commit(); err != nil {
		return receipt, err
	}
	return AgentMemoryRolloutReceipt{ActivityKey: source.ActivityKey, SegmentID: command.SegmentID, ProjectID: source.ProjectID, UserID: source.UserID, ContentHash: command.ContentHash}, nil
}

func (s *Store) ReadAgentMemoryRollout(ctx context.Context, segmentID string) (AgentMemoryRollout, error) {
	var result AgentMemoryRollout
	if !memorySegmentPattern.MatchString(segmentID) {
		return result, domainError("REQUEST_VALIDATION_FAILED", "Invalid memory segment.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	source, err := s.memoryRolloutSourceTx(ctx, tx)
	if err != nil {
		return result, err
	}
	return readMemoryRolloutTx(ctx, tx, source, segmentID)
}

func readMemoryRolloutTx(ctx context.Context, tx *sql.Tx, source AgentMemorySnapshot, segmentID string) (AgentMemoryRollout, error) {
	var result AgentMemoryRollout
	var raw, requestHash, contentHash string
	var forgotten bool
	err := tx.QueryRowContext(ctx, `SELECT envelope_json,request_hash,content_hash,forgotten FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=? AND workspace_id=? AND project_id=? AND user_id=?`, source.ActivityKey, segmentID, source.WorkspaceID, source.ProjectID, source.UserID).Scan(&raw, &requestHash, &contentHash, &forgotten)
	if err != nil {
		return result, err
	}
	if forgotten {
		return result, domainError("AGENT_MEMORY_CONFLICT", "Memory input has been forgotten.")
	}
	if sha256Hex([]byte(raw)) != requestHash || json.Unmarshal([]byte(raw), &result) != nil || validateMemoryRollout(result) != nil || result.ContentHash != contentHash || result.SegmentID != segmentID || !memoryRolloutMatchesSource(result, source) {
		return AgentMemoryRollout{}, domainError("AGENT_MEMORY_INVALID", "Saved memory source failed integrity validation.")
	}
	return result, nil
}

type AgentMemoryArchiveReceipt struct {
	AgentMemoryRolloutReceipt
	ConsentRevision int    `json:"consent_revision"`
	GenerateEnabled bool   `json:"generate_enabled"`
	GenerationID    string `json:"generation_id"`
}

func (s *Store) ReadAgentMemoryArchiveReceipt(ctx context.Context, segmentID, contentHash string, consentRevision int) (AgentMemoryArchiveReceipt, error) {
	var receipt AgentMemoryArchiveReceipt
	activity, active := AgentActivityFromContext(ctx)
	if !active || activity.MemoryGenerationID != "" {
		return receipt, domainError("AGENT_ACTIVITY_FORBIDDEN", "Archive receipt requires its original execution.")
	}
	if !memorySegmentPattern.MatchString(segmentID) || !nativeLeaseKeyPattern.MatchString(contentHash) || consentRevision < 1 {
		return receipt, domainError("REQUEST_VALIDATION_FAILED", "Archive receipt requires its original source and consent.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	source, err := s.memoryRolloutSourceTx(ctx, tx)
	if err != nil {
		return receipt, err
	}
	rollout, err := readMemoryRolloutTx(ctx, tx, source, segmentID)
	if err != nil {
		return receipt, err
	}
	if rollout.ContentHash != contentHash {
		return receipt, domainError("AGENT_MEMORY_CONFLICT", "Archive receipt refers to another frozen source.")
	}
	err = tx.QueryRowContext(ctx, `SELECT archive_revision,archive_generate,archive_generation_id FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=?`, source.ActivityKey, segmentID).Scan(&receipt.ConsentRevision, &receipt.GenerateEnabled, &receipt.GenerationID)
	if err != nil {
		return receipt, err
	}
	if receipt.ConsentRevision != consentRevision || receipt.GenerateEnabled != (receipt.GenerationID != "") {
		return AgentMemoryArchiveReceipt{}, domainError("AGENT_MEMORY_CONFLICT", "Archive transaction is not confirmed for this consent.")
	}
	if receipt.GenerateEnabled {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_memory_generations WHERE generation_id=? AND activity_key=? AND segment_id=? AND workspace_id=? AND project_id=? AND user_id=? AND source_hash=?`, receipt.GenerationID, source.ActivityKey, segmentID, source.WorkspaceID, source.ProjectID, source.UserID, contentHash).Scan(&count)
		if err != nil {
			return AgentMemoryArchiveReceipt{}, err
		}
		if count != 1 {
			return AgentMemoryArchiveReceipt{}, domainError("AGENT_MEMORY_INVALID", "Archive generation receipt failed its source binding.")
		}
	}
	receipt.AgentMemoryRolloutReceipt = AgentMemoryRolloutReceipt{ActivityKey: source.ActivityKey, SegmentID: segmentID, ProjectID: source.ProjectID, UserID: source.UserID, ContentHash: contentHash}
	return receipt, nil
}
