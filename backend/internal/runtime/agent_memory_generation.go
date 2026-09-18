package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
)

// Fixed-width UTC timestamps preserve chronological order in the lease query.
const memoryGenerationLeaseLayout = "2006-01-02T15:04:05.000000000Z"

func migrateAgentMemoryGenerations(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_generations (
		generation_id TEXT PRIMARY KEY,
		activity_key TEXT NOT NULL,
		segment_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		project_id TEXT NOT NULL REFERENCES projects(project_id),
		user_id TEXT NOT NULL REFERENCES users(user_id),
		source_hash TEXT NOT NULL,
		base_version INTEGER NOT NULL,
		base_hash TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('queued','running','paused','cancelled','completed','failed')),
		phase TEXT NOT NULL CHECK(phase IN ('extraction','consolidation','publication')),
		revision INTEGER NOT NULL CHECK(revision>0),
		attempt INTEGER NOT NULL DEFAULT 0,
		worker_id TEXT NOT NULL DEFAULT '',
		model_id TEXT NOT NULL DEFAULT '',
		token_hash TEXT NOT NULL DEFAULT '',
		lease_until TEXT NOT NULL DEFAULT '',
		started INTEGER NOT NULL DEFAULT 0 CHECK(started IN (0,1)),
		checkpoint_json TEXT NOT NULL DEFAULT '',
		checkpoint_hash TEXT NOT NULL,
		error_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		UNIQUE(activity_key,segment_id),
		FOREIGN KEY(activity_key,segment_id) REFERENCES agent_memory_rollouts(activity_key,segment_id)
	);
	CREATE INDEX IF NOT EXISTS idx_memory_generation_queue ON agent_memory_generations(status,created_at,generation_id);`)
	if err != nil {
		return err
	}
	for _, column := range []string{"extraction_json", "extraction_receipt", "input_plan_json", "input_plan_hash"} {
		present, err := tableHasColumn(db, "agent_memory_generations", column)
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec("ALTER TABLE agent_memory_generations ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	return nil
}

type AgentMemoryGeneration struct {
	GenerationID      string `json:"generation_id"`
	ActivityKey       string `json:"activity_key"`
	SegmentID         string `json:"segment_id"`
	WorkspaceID       string `json:"-"`
	ProjectID         string `json:"project_id"`
	UserID            string `json:"user_id"`
	SourceHash        string `json:"source_hash"`
	BaseVersion       int    `json:"base_version"`
	BaseHash          string `json:"base_hash"`
	Status            string `json:"status"`
	Phase             string `json:"phase"`
	Revision          int    `json:"revision"`
	Attempt           int    `json:"attempt"`
	WorkerID          string `json:"-"`
	ModelID           string `json:"model_id"`
	TokenHash         string `json:"-"`
	LeaseUntil        string `json:"lease_until,omitempty"`
	Started           bool   `json:"started"`
	Checkpoint        string `json:"-"`
	CheckpointHash    string `json:"checkpoint_hash"`
	ErrorCode         string `json:"error_code,omitempty"`
	Extraction        string `json:"-"`
	ExtractionReceipt string `json:"-"`
	InputPlan         string `json:"-"`
	InputPlanHash     string `json:"-"`
}

func memoryGenerationQuery(ctx context.Context, tx *sql.Tx, id string) (AgentMemoryGeneration, error) {
	var job AgentMemoryGeneration
	err := tx.QueryRowContext(ctx, `SELECT generation_id,activity_key,segment_id,workspace_id,project_id,user_id,source_hash,base_version,base_hash,status,phase,revision,attempt,worker_id,model_id,token_hash,lease_until,started,checkpoint_json,checkpoint_hash,error_code,extraction_json,extraction_receipt,input_plan_json,input_plan_hash FROM agent_memory_generations WHERE generation_id=?`, id).Scan(
		&job.GenerationID, &job.ActivityKey, &job.SegmentID, &job.WorkspaceID, &job.ProjectID, &job.UserID, &job.SourceHash, &job.BaseVersion, &job.BaseHash, &job.Status, &job.Phase, &job.Revision, &job.Attempt, &job.WorkerID, &job.ModelID, &job.TokenHash, &job.LeaseUntil, &job.Started, &job.Checkpoint, &job.CheckpointHash, &job.ErrorCode, &job.Extraction, &job.ExtractionReceipt, &job.InputPlan, &job.InputPlanHash)
	if errors.Is(err, sql.ErrNoRows) {
		return job, domainError("AGENT_MEMORY_GENERATION_NOT_FOUND", "Memory generation was not found.")
	}
	if err == nil && sha256Hex([]byte(job.Checkpoint)) != job.CheckpointHash {
		return AgentMemoryGeneration{}, domainError("AGENT_MEMORY_INVALID", "Memory generation checkpoint is corrupt.")
	}
	if err == nil && (job.InputPlan != "" || job.InputPlanHash != "") && (job.InputPlan == "" || len(job.InputPlan) > 16<<20 || sha256Hex([]byte(job.InputPlan)) != job.InputPlanHash) {
		return AgentMemoryGeneration{}, domainError("AGENT_MEMORY_INVALID", "Memory input plan is corrupt.")
	}
	if err == nil && (job.Extraction != "" || job.ExtractionReceipt != "") {
		var receipt memoryExtractionReceipt
		hasMemory, validationErr := validateMemoryExtraction(json.RawMessage(job.Extraction))
		if validationErr != nil || json.Unmarshal([]byte(job.ExtractionReceipt), &receipt) != nil || receipt.Hash != sha256Hex([]byte(job.Extraction)) || receipt.HasMemory != hasMemory {
			return AgentMemoryGeneration{}, domainError("AGENT_MEMORY_INVALID", "Memory extraction receipt is corrupt.")
		}
	}
	return job, err
}

func memoryGenerationService(ctx context.Context) error {
	principal, ok := identity.FromContext(ctx)
	_, activity := AgentActivityFromContext(ctx)
	if !ok || principal.Kind != identity.KindService || activity {
		return domainError("AGENT_ACTIVITY_FORBIDDEN", "Memory generation scheduling requires the internal worker, not a delegated model tool.")
	}
	return nil
}

func (s *Store) memoryGenerationSourceTx(ctx context.Context, tx *sql.Tx, job AgentMemoryGeneration) (AgentMemoryRollout, error) {
	var empty AgentMemoryRollout
	user, err := resolvePrincipalQuery(ctx, tx, identity.Principal{Kind: identity.KindUser, WorkspaceID: job.WorkspaceID, UserID: job.UserID, Role: identity.RoleViewer})
	if err != nil {
		return empty, err
	}
	workspace, err := projectFilesWorkspace(ctx, tx, job.ProjectID, false)
	if err != nil {
		return empty, err
	}
	if workspace != user.WorkspaceID || !user.Allows(identity.RoleEditor) {
		return empty, domainError("WORKSPACE_ACCESS_DENIED", "Memory generation owner no longer has access.")
	}
	current, err := agentMemoryQuery(ctx, tx, user, job.ProjectID, 0)
	if err != nil {
		return empty, err
	}
	ownPublication := false
	if current.Version != job.BaseVersion || current.ContentHash != job.BaseHash {
		ownPublication, err = memoryGenerationOwnPublicationQuery(ctx, tx, job, current)
		if err != nil {
			return empty, err
		}
		if !ownPublication {
			return empty, domainError("AGENT_MEMORY_CONFLICT", "Memory changed after this generation was queued.")
		}
	}
	var raw, requestHash, contentHash string
	var forgotten bool
	err = tx.QueryRowContext(ctx, `SELECT envelope_json,request_hash,content_hash,forgotten FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=? AND workspace_id=? AND project_id=? AND user_id=?`, job.ActivityKey, job.SegmentID, job.WorkspaceID, job.ProjectID, job.UserID).Scan(&raw, &requestHash, &contentHash, &forgotten)
	if err != nil {
		return empty, err
	}
	if forgotten {
		return empty, domainError("AGENT_MEMORY_CONFLICT", "Generation input has been forgotten.")
	}
	var source AgentMemoryRollout
	if contentHash != job.SourceHash || sha256Hex([]byte(raw)) != requestHash || json.Unmarshal([]byte(raw), &source) != nil || validateMemoryRollout(source) != nil || source.ContentHash != contentHash || source.ProjectID != job.ProjectID || source.UserID != job.UserID || source.WorkspaceID != job.WorkspaceID || source.ActivityKey != job.ActivityKey || source.SegmentID != job.SegmentID {
		return empty, domainError("AGENT_MEMORY_INVALID", "Memory generation source failed integrity validation.")
	}
	ref := AgentMemorySnapshot{ActivityKey: source.ActivityKey, WorkspaceID: source.WorkspaceID, ProjectID: source.ProjectID, UserID: source.UserID, Version: source.MemoryVersion, ContentHash: source.MemoryHash, ReadEnabled: source.ReadEnabled}
	if ownPublication {
		if ref.Version > 0 {
			original, err := agentMemoryQuery(ctx, tx, user, job.ProjectID, ref.Version)
			if err != nil {
				return empty, err
			}
			if original.Forgotten || original.ContentHash != ref.ContentHash || (ref.ReadEnabled && !original.Enabled) {
				return empty, domainError("AGENT_MEMORY_CONFLICT", "Generation memory source is no longer available.")
			}
		}
		return source, nil
	}
	if ref.ReadEnabled || current.Version != ref.Version || current.ContentHash != ref.ContentHash {
		ref.ReadEnabled = true
		if err := validateAgentMemoryReferenceQuery(ctx, tx, ref); err != nil {
			return empty, err
		}
	}
	return source, nil
}

func (s *Store) QueueAgentMemoryGeneration(ctx context.Context, projectID, activityKey, segmentID, sourceHash string) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if !memorySegmentPattern.MatchString(segmentID) || !nativeLeaseKeyPattern.MatchString(sourceHash) || len(activityKey) > 270 {
		return empty, domainError("REQUEST_VALIDATION_FAILED", "Memory generation requires an exact saved source.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	// User consent is explicit here. Automatic opt-in scheduling is not implied
	// by possession of a service credential or by a previous memory read.
	transport, ok := identity.FromContext(ctx)
	_, delegated := AgentActivityFromContext(ctx)
	if !ok || !transport.ValidUser() || delegated {
		return empty, domainError("AUTHENTICATION_REQUIRED", "Memory generation requires its user's request.")
	}
	user, err := instructionUserQuery(ctx, tx, projectID, true)
	if err != nil {
		return empty, err
	}
	job, err := s.queueAgentMemoryGenerationTx(ctx, tx, user, projectID, activityKey, segmentID, sourceHash)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return job, nil
}

// Callers establish either direct user authorization or current archive consent
// in this transaction before using the shared source validation and deduplication.
func (s *Store) queueAgentMemoryGenerationTx(ctx context.Context, tx *sql.Tx, user identity.Principal, projectID, activityKey, segmentID, sourceHash string) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	current, err := agentMemoryQuery(ctx, tx, user, projectID, 0)
	if err != nil {
		return empty, err
	}
	job := AgentMemoryGeneration{GenerationID: s.newID("amg"), ActivityKey: activityKey, SegmentID: segmentID, WorkspaceID: user.WorkspaceID, ProjectID: projectID, UserID: user.UserID, SourceHash: sourceHash, BaseVersion: current.Version, BaseHash: current.ContentHash}
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		return empty, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT generation_id FROM agent_memory_generations WHERE activity_key=? AND segment_id=?`, activityKey, segmentID).Scan(&previous)
	if err == nil {
		old, err := memoryGenerationQuery(ctx, tx, previous)
		if err != nil {
			return empty, err
		}
		if old.UserID != job.UserID || old.WorkspaceID != job.WorkspaceID || old.ProjectID != job.ProjectID || old.SourceHash != sourceHash {
			return empty, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Generation source belongs to another owner.")
		}
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, user.WorkspaceID, "storage_bytes", 2048); err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_memory_generations(generation_id,activity_key,segment_id,workspace_id,project_id,user_id,source_hash,base_version,base_hash,status,phase,revision,checkpoint_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,'queued','extraction',1,?,?)`, job.GenerationID, activityKey, segmentID, user.WorkspaceID, projectID, user.UserID, sourceHash, current.Version, current.ContentHash, sha256Hex(nil), formatTime(s.now()))
	if err != nil {
		return empty, err
	}
	job, err = memoryGenerationQuery(ctx, tx, job.GenerationID)
	if err != nil {
		return empty, err
	}
	return job, nil
}

type AgentMemoryGenerationClaim struct {
	ConversationID string                `json:"conversation_id"`
	Job            AgentMemoryGeneration `json:"job"`
	AttemptToken   string                `json:"attempt_token"`
	Source         AgentMemoryRollout    `json:"source"`
	Checkpoint     json.RawMessage       `json:"checkpoint,omitempty"`
	Extraction     json.RawMessage       `json:"extraction,omitempty"`
	InputPlan      json.RawMessage       `json:"input_plan,omitempty"`
	InputPlanHash  string                `json:"input_plan_hash,omitempty"`
}

func (s *Store) ClaimAgentMemoryGeneration(ctx context.Context, workerID, modelID string, leaseSeconds int) (*AgentMemoryGenerationClaim, error) {
	if err := memoryGenerationService(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workerID) == "" || len(workerID) > 256 || strings.TrimSpace(modelID) == "" || len(modelID) > 256 || leaseSeconds < minAttemptLeaseSeconds || leaseSeconds > maxAttemptLeaseSeconds {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "Memory worker identity, model and lease are required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now()
	if err := s.reconcileExpiredMemoryPublicationsTx(ctx, tx, now.UTC().Format(memoryGenerationLeaseLayout)); err != nil {
		return nil, err
	}
	// A worker lost before admission can be replaced. Started work is never
	// blindly replayed, even when the SDK checkpoint might permit a later resume.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status=CASE WHEN started=0 THEN 'queued' ELSE 'paused' END,error_code=CASE WHEN started=0 THEN '' ELSE 'MEMORY_GENERATION_LEASE_LOST' END,token_hash='',lease_until='',revision=revision+1 WHERE status='running' AND lease_until<=?`, now.UTC().Format(memoryGenerationLeaseLayout)); err != nil {
		return nil, err
	}
	if err := s.resumeReadyMemoryApprovalsTx(ctx, tx); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT generation_id FROM agent_memory_generations WHERE status='queued' ORDER BY created_at,generation_id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	job, err := memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		if isDomainErrorCode(err, "AGENT_MEMORY_INVALID") {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='failed',error_code='MEMORY_GENERATION_CHECKPOINT_CORRUPT',revision=revision+1 WHERE generation_id=?`, id); updateErr != nil {
				return nil, updateErr
			}
			return nil, tx.Commit()
		}
		return nil, err
	}
	source, err := s.memoryGenerationSourceTx(ctx, tx, job)
	if err != nil {
		var domain *DomainError
		if !errors.As(err, &domain) && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='cancelled',error_code='MEMORY_GENERATION_SOURCE_REVOKED',revision=revision+1 WHERE generation_id=?`, id); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if job.ModelID != "" && job.ModelID != modelID {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='paused',error_code='MEMORY_GENERATION_MODEL_CHANGED',revision=revision+1 WHERE generation_id=?`, id); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	var conversationID string
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM agent_turns WHERE 'turn:'||agent_turn_id=? AND project_id=?
		UNION ALL SELECT t.conversation_id FROM agent_task_attempts a JOIN agent_tasks t ON t.agent_task_id=a.agent_task_id WHERE 'background:'||a.agent_task_attempt_id=? AND t.project_id=?
		UNION ALL SELECT r.conversation_id FROM execution_attempts a JOIN runs r ON r.run_id=a.run_id WHERE 'execution:'||a.attempt_id=? AND r.project_id=?`,
		job.ActivityKey, job.ProjectID, job.ActivityKey, job.ProjectID, job.ActivityKey, job.ProjectID).Scan(&conversationID)
	if err != nil || conversationID == "" {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, domainError("AGENT_MEMORY_INVALID", "Memory source conversation is unavailable.")
	}
	token := s.newID("amgtok")
	updated, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='running',attempt=attempt+1,worker_id=?,model_id=?,token_hash=?,lease_until=?,started=0,revision=revision+1 WHERE generation_id=? AND status='queued'`, workerID, modelID, sha256Hex([]byte(token)), now.Add(time.Duration(leaseSeconds)*time.Second).UTC().Format(memoryGenerationLeaseLayout), id)
	if err != nil {
		return nil, err
	}
	if count, err := updated.RowsAffected(); err != nil {
		return nil, err
	} else if count != 1 {
		return nil, domainError("AGENT_MEMORY_CONFLICT", "Memory generation was claimed concurrently.")
	}
	job, err = memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	claim := &AgentMemoryGenerationClaim{ConversationID: conversationID, Job: job, AttemptToken: token, Source: source, Checkpoint: json.RawMessage(job.Checkpoint), Extraction: json.RawMessage(job.Extraction), InputPlan: json.RawMessage(job.InputPlan), InputPlanHash: job.InputPlanHash}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *Store) StartAgentMemoryGeneration(ctx context.Context, id, workerID, token string, attempt int) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if err := memoryGenerationService(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	job, err := memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	lease, err := parseTime(job.LeaseUntil)
	if err != nil || job.Status != "running" || workerID != job.WorkerID || attempt != job.Attempt || !lease.After(s.now()) || subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory generation worker lease is no longer valid.")
	}
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		return empty, err
	}
	if !job.Started {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET started=1,revision=revision+1 WHERE generation_id=?`, id); err != nil {
			return empty, err
		}
	}
	job, err = memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return job, nil
}

// Checkpoints are opaque SDK state, not a replacement conversation summary.
// The worker must validate the pinned SDK's state format before restoring it.
func (s *Store) CheckpointAgentMemoryGeneration(ctx context.Context, id, workerID, token string, attempt int, expectedHash string, checkpoint json.RawMessage) (AgentMemoryGeneration, error) {
	return s.checkpointAgentMemoryGeneration(ctx, id, workerID, token, attempt, expectedHash, checkpoint, false)
}

func (s *Store) PauseAgentMemoryGeneration(ctx context.Context, id, workerID, token string, attempt int, expectedHash string, checkpoint json.RawMessage) (AgentMemoryGeneration, error) {
	return s.checkpointAgentMemoryGeneration(ctx, id, workerID, token, attempt, expectedHash, checkpoint, true)
}

func (s *Store) checkpointAgentMemoryGeneration(ctx context.Context, id, workerID, token string, attempt int, expectedHash string, checkpoint json.RawMessage, pause bool) (AgentMemoryGeneration, error) {
	var empty AgentMemoryGeneration
	if err := memoryGenerationService(ctx); err != nil {
		return empty, err
	}
	if len(checkpoint) == 0 || len(checkpoint) > 4*1024*1024 || !json.Valid(checkpoint) || !nativeLeaseKeyPattern.MatchString(expectedHash) {
		return empty, domainError("REQUEST_VALIDATION_FAILED", "A bounded SDK checkpoint and its previous hash are required.")
	}
	pauseCode := "MEMORY_GENERATION_APPROVAL_PENDING"
	if pause {
		var envelope struct {
			PauseKind string `json:"pause_kind"`
		}
		if json.Unmarshal(checkpoint, &envelope) != nil || (envelope.PauseKind != "" && envelope.PauseKind != "approval" && envelope.PauseKind != "user") {
			return empty, domainError("REQUEST_VALIDATION_FAILED", "Memory checkpoint pause kind is invalid.")
		}
		if envelope.PauseKind == "user" {
			pauseCode = "MEMORY_GENERATION_USER_PAUSED"
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	job, err := memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	nextHash := sha256Hex(checkpoint)
	// A user request can arrive after the worker's last renewal but before its
	// approval checkpoint. Preserve that intent, including lost-response retries.
	if pause && (job.ErrorCode == "MEMORY_GENERATION_PAUSE_REQUESTED" ||
		(job.Status == "paused" && job.ErrorCode == "MEMORY_GENERATION_USER_PAUSED" && job.CheckpointHash == nextHash)) {
		pauseCode = "MEMORY_GENERATION_USER_PAUSED"
	}
	lease, leaseErr := parseTime(job.LeaseUntil)
	repeatedPause := pause && job.Status == "paused" && job.ErrorCode == pauseCode && job.CheckpointHash == nextHash
	if (!repeatedPause && (leaseErr != nil || job.Status != "running" || !lease.After(s.now()))) || !job.Started || workerID != job.WorkerID || attempt != job.Attempt || subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory generation worker lease is no longer valid.")
	}
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		return empty, err
	}
	// An exact retry after a lost response does not consume another revision.
	if repeatedPause || (!pause && job.CheckpointHash == nextHash) {
		return job, nil
	}
	if job.CheckpointHash != expectedHash && job.CheckpointHash != nextHash {
		return empty, domainError("AGENT_MEMORY_CONFLICT", "A newer SDK checkpoint has already been saved.")
	}
	if growth := int64(len(checkpoint) - len(job.Checkpoint)); growth > 0 {
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, job.WorkspaceID, "storage_bytes", growth); err != nil {
			return empty, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET checkpoint_json=?,checkpoint_hash=?,revision=revision+1 WHERE generation_id=?`, string(checkpoint), nextHash, id); err != nil {
		return empty, err
	}
	if pause {
		// The retained hash authenticates only an identical pause receipt retry.
		// Paused status fences execution; owner resume revokes the old token hash.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET status='paused',error_code=?,lease_until='' WHERE generation_id=?`, pauseCode, id); err != nil {
			return empty, err
		}
	}
	job, err = memoryGenerationQuery(ctx, tx, id)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return job, nil
}
