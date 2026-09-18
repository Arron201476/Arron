package runtime

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type AgentMemoryInputPlan struct {
	BaselineVersion int               `json:"baseline_version"`
	BaselineHash    string            `json:"baseline_hash"`
	SourceHash      string            `json:"source_hash"`
	ExtractionHash  string            `json:"extraction_hash"`
	ContentHash     string            `json:"content_hash"`
	Files           map[string]string `json:"files"`
}

type AgentMemoryInputPlanReceipt struct {
	GenerationID string `json:"generation_id"`
	PlanHash     string `json:"plan_hash"`
	ContentHash  string `json:"content_hash"`
}

func validateMemoryInputPlan(plan AgentMemoryInputPlan, job AgentMemoryGeneration, source AgentMemoryRollout) ([]byte, error) {
	invalid := domainError("AGENT_MEMORY_INVALID", "Memory input plan differs from its confirmed private sources.")
	if plan.BaselineVersion != job.BaseVersion || plan.BaselineHash != job.BaseHash || plan.SourceHash != job.SourceHash || plan.ExtractionHash != sha256Hex([]byte(job.Extraction)) || len(plan.Files) < 4 || len(plan.Files) > 7 {
		return nil, invalid
	}
	var rollout struct {
		RolloutID string `json:"rollout_id"`
	}
	var extraction struct {
		Slug string `json:"rollout_slug"`
	}
	if json.Unmarshal([]byte(source.RolloutJSONL), &rollout) != nil || json.Unmarshal([]byte(job.Extraction), &extraction) != nil {
		return nil, invalid
	}
	slug := strings.TrimSuffix(memorySDKTrimSpace(extraction.Slug), ".md")
	if !memoryRolloutSlugPattern.MatchString(slug) {
		return nil, invalid
	}
	rolloutPath := nativeGenerationDirectory + "/" + rollout.RolloutID + ".jsonl"
	allowed := map[string]bool{
		rolloutPath: true,
		nativeMemoryDirectory + "/raw_memories/" + rollout.RolloutID + ".md":                   true,
		nativeMemoryDirectory + "/rollout_summaries/" + rollout.RolloutID + "_" + slug + ".md": true,
		nativeMemoryDirectory + "/raw_memories.md":                                             true,
	}
	for path := range allowed {
		if _, ok := plan.Files[path]; !ok {
			return nil, invalid
		}
	}
	if plan.Files[rolloutPath] != source.RolloutJSONL {
		return nil, invalid
	}
	selectionPath := nativeGenerationDirectory + "/phase_two_selection.json"
	if selection, ok := plan.Files[selectionPath]; ok {
		var payload struct {
			Version   int               `json:"version"`
			UpdatedAt string            `json:"updated_at"`
			Selected  []json.RawMessage `json:"selected"`
		}
		if json.Unmarshal([]byte(selection), &payload) != nil || payload.Version != 1 || payload.UpdatedAt == "" || len(payload.Selected) < 1 || len(payload.Selected) > 256 {
			return nil, invalid
		}
		allowed[selectionPath] = true
	}
	for path, content := range plan.Files {
		if !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') || validateProjectFilePath(path) != nil {
			return nil, invalid
		}
		if !allowed[path] && !((path == nativeMemoryDirectory+"/MEMORY.md" || path == nativeMemoryDirectory+"/memory_summary.md") && content == "") {
			return nil, invalid
		}
	}
	files, err := json.Marshal(plan.Files)
	if err != nil || sha256Hex(files) != plan.ContentHash {
		return nil, invalid
	}
	encoded, err := json.Marshal(map[string]any{"baseline_version": plan.BaselineVersion, "baseline_hash": plan.BaselineHash,
		"source_hash": plan.SourceHash, "extraction_hash": plan.ExtractionHash, "content_hash": plan.ContentHash, "files": plan.Files})
	if err != nil || len(encoded) > 16<<20 {
		return nil, invalid
	}
	return encoded, nil
}

func (s *Store) PrepareAgentMemoryInputs(ctx context.Context, id, workerID, token string, attempt int, plan AgentMemoryInputPlan) (AgentMemoryInputPlanReceipt, error) {
	var empty AgentMemoryInputPlanReceipt
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
	if err != nil || job.Status != "running" || job.Phase != "consolidation" || !job.Started || job.WorkerID != workerID || job.Attempt != attempt || !lease.After(s.now()) || subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(sha256Hex([]byte(token)))) != 1 {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory preparation requires its live independent consolidation worker.")
	}
	source, err := s.memoryGenerationSourceTx(ctx, tx, job)
	if err != nil {
		return empty, err
	}
	encoded, err := validateMemoryInputPlan(plan, job, source)
	if err != nil {
		return empty, err
	}
	hash := sha256Hex(encoded)
	receipt := AgentMemoryInputPlanReceipt{GenerationID: id, PlanHash: hash, ContentHash: plan.ContentHash}
	if job.InputPlan != "" {
		if job.InputPlanHash != hash {
			return empty, domainError("AGENT_MEMORY_CONFLICT", "Memory inputs were already frozen differently.")
		}
		return receipt, nil
	}
	var initialized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_workspace_leases WHERE activity_key IN (?,?))`, "memory:"+id, "memory:"+id+":consolidation").Scan(&initialized); err != nil {
		return empty, err
	}
	if initialized || job.Checkpoint != "" {
		return empty, domainError("AGENT_MEMORY_CONFLICT", "Memory preparation cannot replace an initialized workspace.")
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, job.WorkspaceID, "storage_bytes", int64(len(encoded)+len(hash))); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET input_plan_json=?,input_plan_hash=?,revision=revision+1 WHERE generation_id=?`, string(encoded), hash, id); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return receipt, nil
}
