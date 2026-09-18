package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
)

const nativeGenerationDirectory = ".agent-memory-input"

type NativeWorkspaceGenerationSource struct {
	GenerationID   string `json:"generation_id"`
	SourceHash     string `json:"source_hash"`
	ExtractionHash string `json:"extraction_hash"`
	PlanHash       string `json:"plan_hash,omitempty"`
}

func (s *Store) nativeGenerationInputsTx(ctx context.Context, tx *sql.Tx, projectID string, source NativeWorkspaceGenerationSource) (map[string]string, error) {
	activity, ok := AgentActivityFromContext(ctx)
	if !ok || activity.ProjectID != projectID || activity.MemoryGenerationID == "" || activity.MemoryGenerationID != source.GenerationID || activity.AllowTerminal {
		return nil, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Generation inputs require their own active memory execution.")
	}
	if _, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity); err != nil {
		return nil, err
	}
	job, err := memoryGenerationQuery(ctx, tx, source.GenerationID)
	if err != nil {
		return nil, err
	}
	if job.Phase != "consolidation" || job.SourceHash != source.SourceHash || job.Extraction == "" || sha256Hex([]byte(job.Extraction)) != source.ExtractionHash {
		return nil, domainError("AGENT_MEMORY_CONFLICT", "Generation input references differ from the confirmed extraction.")
	}
	rollout, err := s.memoryGenerationSourceTx(ctx, tx, job)
	if err != nil {
		return nil, err
	}
	if source.PlanHash != "" {
		var plan AgentMemoryInputPlan
		if job.InputPlanHash != source.PlanHash || job.InputPlan == "" || json.Unmarshal([]byte(job.InputPlan), &plan) != nil {
			return nil, domainError("AGENT_MEMORY_CONFLICT", "Generation input plan differs from its immutable receipt.")
		}
		encoded, err := validateMemoryInputPlan(plan, job, rollout)
		if err != nil {
			return nil, err
		}
		if sha256Hex(encoded) != source.PlanHash {
			return nil, domainError("AGENT_MEMORY_INVALID", "Generation input plan failed canonical validation.")
		}
		return plan.Files, nil
	}
	return map[string]string{"rollout.jsonl": rollout.RolloutJSONL, "extraction.json": job.Extraction}, nil
}

func nativeGenerationInputPath(source NativeWorkspaceGenerationSource, resource string) string {
	if source.PlanHash != "" {
		return resource
	}
	return nativeGenerationDirectory + "/" + resource
}
