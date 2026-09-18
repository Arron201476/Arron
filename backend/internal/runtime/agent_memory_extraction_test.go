package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryExtractionTransitionsAndOriginalReceipt(t *testing.T) {
	for _, hasMemory := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-memory", true: "consolidation"}[hasMemory], func(t *testing.T) {
			store, job, original := queuedMemoryGenerationFixture(t, "conversation")
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			output := json.RawMessage(`{"rollout_slug":"","rollout_summary":"","raw_memory":""}`)
			if hasMemory {
				output = json.RawMessage(`{"rollout_slug":"fixture","rollout_summary":"summary","raw_memory":"PRIVATE_MEMORY"}`)
			}
			finish := func(ctx context.Context, raw json.RawMessage) (AgentMemoryExtractionResult, error) {
				return store.CompleteAgentMemoryExtraction(ctx, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, raw)
			}
			_, err = finish(original, output)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			_, err = finish(worker, output)
			assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
			if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
				t.Fatal(err)
			}
			result, err := finish(worker, output)
			if err != nil || result.HasMemory != hasMemory || result.ContentHash != sha256Hex(output) {
				t.Fatalf("finish: %+v %v", result, err)
			}
			again, err := finish(worker, output)
			if err != nil || again != result {
				t.Fatalf("receipt retry: %+v %v", again, err)
			}
			_, err = finish(worker, json.RawMessage(`{"rollout_slug":"different","rollout_summary":"summary","raw_memory":"memory"}`))
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			_, err = store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
			assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
			next, err := store.ClaimAgentMemoryGeneration(worker, "worker-next", "model", 60)
			if err != nil {
				t.Fatal(err)
			}
			if hasMemory {
				if next == nil || next.Job.Phase != "consolidation" || string(next.Extraction) != string(output) || len(next.Checkpoint) != 0 || next.Job.Attempt != claim.Job.Attempt+1 {
					t.Fatal("extraction did not transition with immutable output")
				}
			} else if next != nil {
				t.Fatal("empty extraction was requeued")
			}
			if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion, Forget: true, RequestID: "forget-extraction"}); err != nil {
				t.Fatal(err)
			}
			var raw, receipt string
			if err := store.db.QueryRow(`SELECT extraction_json,extraction_receipt FROM agent_memory_generations WHERE generation_id=?`, job.GenerationID).Scan(&raw, &receipt); err != nil || raw != "" || receipt != "" {
				t.Fatal("forget retained extraction material")
			}
			if _, err := finish(worker, output); err == nil {
				t.Fatal("forgotten extraction receipt was revived")
			}
		})
	}
}

func TestMemoryExtractionRejectsNonSDKContract(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"rollout_slug":"s","rollout_summary":"","raw_memory":"m"}`, `{"rollout_slug":null,"rollout_summary":"","raw_memory":""}`, `{"rollout_slug":"","rollout_summary":"","raw_memory":"","extra":1}`} {
		if _, err := validateMemoryExtraction(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid output accepted: %s", raw)
		}
	}
}

func TestMemoryExtractionSDKSlugAndUniqueFields(t *testing.T) {
	for _, raw := range []string{
		`{"rollout_slug":"valid","rollout_slug":"other","rollout_summary":"s","raw_memory":"m"}`,
		`{"rollout_slug":"../private","rollout_summary":"s","raw_memory":"m"}`,
		`{"rollout_slug":"UPPER","rollout_summary":"s","raw_memory":"m"}`,
		`{"rollout_slug":"valid","rollout_summary":"s","raw_memory":"m"} {}`,
	} {
		if _, err := validateMemoryExtraction(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid SDK input accepted")
		}
	}
	for _, slug := range []string{"valid", " valid.md ", "\x1cvalid\x1f"} {
		raw, err := json.Marshal(map[string]string{"rollout_slug": slug, "rollout_summary": "s", "raw_memory": "m"})
		if err != nil {
			t.Fatal(err)
		}
		if hasMemory, err := validateMemoryExtraction(raw); err != nil || !hasMemory {
			t.Fatalf("SDK-compatible slug rejected: %v", err)
		}
	}
	if hasMemory, err := validateMemoryExtraction(json.RawMessage(`{"rollout_slug":"\u001c","rollout_summary":"\u001d","raw_memory":"\u001f"}`)); err != nil || hasMemory {
		t.Fatalf("SDK whitespace mismatch: %v", err)
	}
}
