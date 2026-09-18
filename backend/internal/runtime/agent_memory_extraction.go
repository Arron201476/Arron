package runtime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var memoryRolloutSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

// Python str.strip also treats the four information separators as whitespace.
func memorySDKTrimSpace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f })
}

type memoryExtractionReceipt struct {
	Hash      string `json:"hash"`
	WorkerID  string `json:"worker_id"`
	Attempt   int    `json:"attempt"`
	TokenHash string `json:"token_hash"`
	HasMemory bool   `json:"has_memory"`
}

type AgentMemoryExtractionResult struct {
	GenerationID string `json:"generation_id"`
	ContentHash  string `json:"content_hash"`
	HasMemory    bool   `json:"has_memory"`
}

func validateMemoryExtraction(raw json.RawMessage) (bool, error) {
	invalid := func() (bool, error) {
		return false, domainError("REQUEST_VALIDATION_FAILED", "Memory extraction requires exactly three unique SDK string fields.")
	}
	if len(raw) == 0 || len(raw) > 4<<20 || !utf8.Valid(raw) {
		return false, domainError("REQUEST_VALIDATION_FAILED", "Memory extraction requires the bounded SDK output contract.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return invalid()
	}
	fields := map[string]string{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		name, ok := key.(string)
		if !ok {
			return invalid()
		}
		if _, exists := fields[name]; exists {
			return invalid()
		}
		var value *string
		if decoder.Decode(&value) != nil || value == nil {
			return invalid()
		}
		fields[name] = *value
	}
	closing, err := decoder.Token()
	var trailing any
	if err != nil || closing != json.Delim('}') || decoder.Decode(&trailing) != io.EOF || len(fields) != 3 {
		return invalid()
	}
	empty := 0
	for _, key := range []string{"rollout_slug", "rollout_summary", "raw_memory"} {
		text, ok := fields[key]
		if !ok {
			return false, domainError("REQUEST_VALIDATION_FAILED", "Memory extraction requires three string fields.")
		}
		if memorySDKTrimSpace(text) == "" {
			empty++
		}
	}
	if empty != 0 && empty != 3 {
		return false, domainError("AGENT_MEMORY_INVALID", "Memory extraction returned partially empty SDK artifacts.")
	}
	if empty == 0 && !memoryRolloutSlugPattern.MatchString(strings.TrimSuffix(memorySDKTrimSpace(fields["rollout_slug"]), ".md")) {
		return false, domainError("AGENT_MEMORY_INVALID", "Memory extraction rollout slug is invalid for SDK storage.")
	}
	return empty == 0, nil
}

func (s *Store) CompleteAgentMemoryExtraction(ctx context.Context, id, workerID, token string, attempt int, output json.RawMessage) (AgentMemoryExtractionResult, error) {
	var empty AgentMemoryExtractionResult
	if err := memoryGenerationService(ctx); err != nil {
		return empty, err
	}
	hasMemory, err := validateMemoryExtraction(output)
	if err != nil {
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
	if _, err := s.memoryGenerationSourceTx(ctx, tx, job); err != nil {
		return empty, err
	}
	hash, tokenHash := sha256Hex(output), sha256Hex([]byte(token))
	result := AgentMemoryExtractionResult{GenerationID: id, ContentHash: hash, HasMemory: hasMemory}
	if job.ExtractionReceipt != "" {
		var receipt memoryExtractionReceipt
		if json.Unmarshal([]byte(job.ExtractionReceipt), &receipt) != nil || receipt.Hash != hash || receipt.WorkerID != workerID || receipt.Attempt != attempt || subtle.ConstantTimeCompare([]byte(receipt.TokenHash), []byte(tokenHash)) != 1 {
			return empty, domainError("AGENT_MEMORY_CONFLICT", "Another extraction result has already been recorded.")
		}
		return result, nil
	}
	lease, err := parseTime(job.LeaseUntil)
	if err != nil || job.Status != "running" || job.Phase != "extraction" || !job.Started || job.WorkerID != workerID || job.Attempt != attempt || !lease.After(s.now()) || subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(tokenHash)) != 1 {
		return empty, domainError("AGENT_ACTIVITY_STALE", "Memory extraction worker lease is no longer valid.")
	}
	receipt, err := json.Marshal(memoryExtractionReceipt{Hash: hash, WorkerID: workerID, Attempt: attempt, TokenHash: tokenHash, HasMemory: hasMemory})
	if err != nil {
		return empty, err
	}
	if growth := int64(len(output) + len(receipt) - len(job.Checkpoint)); growth > 0 {
		if err := s.enforceWorkspaceQuotaTx(ctx, tx, job.WorkspaceID, "storage_bytes", growth); err != nil {
			return empty, err
		}
	}
	status, phase := "queued", "consolidation"
	if !hasMemory {
		status, phase = "completed", "extraction"
	}
	// The next phase gets a new lease; a completed extraction is never rerun.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_memory_generations SET extraction_json=?,extraction_receipt=?,status=?,phase=?,started=0,token_hash='',lease_until='',checkpoint_json='',checkpoint_hash=?,revision=revision+1 WHERE generation_id=?`, string(output), string(receipt), status, phase, sha256Hex(nil), id); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return result, nil
}
