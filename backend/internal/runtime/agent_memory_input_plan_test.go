package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryInputPlanFreezesBeforeNativeInitialization(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	output := json.RawMessage(`{"rollout_slug":"fixture","rollout_summary":"summary","raw_memory":"private"}`)
	if _, err := store.CompleteAgentMemoryExtraction(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, output); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	var rollout struct {
		ID string `json:"rollout_id"`
	}
	if err := json.Unmarshal([]byte(next.Source.RolloutJSONL), &rollout); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{nativeGenerationDirectory + "/" + rollout.ID + ".jsonl": next.Source.RolloutJSONL,
		nativeMemoryDirectory + "/raw_memories/" + rollout.ID + ".md":              "private",
		nativeMemoryDirectory + "/rollout_summaries/" + rollout.ID + "_fixture.md": "summary",
		nativeMemoryDirectory + "/raw_memories.md":                                 "private"}
	raw, _ := json.Marshal(files)
	plan := AgentMemoryInputPlan{BaselineVersion: job.BaseVersion, BaselineHash: job.BaseHash, SourceHash: job.SourceHash,
		ExtractionHash: sha256Hex(output), ContentHash: sha256Hex(raw), Files: files}
	prepare := func(ctx context.Context, p AgentMemoryInputPlan) (AgentMemoryInputPlanReceipt, error) {
		return store.PrepareAgentMemoryInputs(ctx, job.GenerationID, "worker", next.AttemptToken, next.Job.Attempt, p)
	}
	if _, err := prepare(worker, plan); err == nil {
		t.Fatal("unstarted worker prepared inputs")
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", next.AttemptToken, next.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	delegated := WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: next.Job.Attempt, AttemptToken: next.AttemptToken})
	if _, err := prepare(delegated, plan); err == nil {
		t.Fatal("delegated tool prepared inputs")
	}
	receipt, err := prepare(worker, plan)
	if err != nil || receipt.ContentHash != plan.ContentHash || receipt.PlanHash == "" {
		t.Fatalf("prepare: %v", err)
	}
	again, err := prepare(worker, plan)
	if err != nil || again != receipt {
		t.Fatal("same plan retry changed receipt")
	}
	holder := sha256Hex([]byte("input-plan-holder"))
	lease, err := store.ReserveNativeWorkspace(delegated, ReserveNativeWorkspaceCommand{HolderKey: holder})
	if err != nil {
		t.Fatal(err)
	}
	access := NativeWorkspaceAccess{SessionID: lease.SessionID, Generation: lease.Generation, HolderKey: holder}
	manager := nativeFileManager(t, store, &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)})
	reference := NativeWorkspaceGenerationSource{GenerationID: job.GenerationID, SourceHash: job.SourceHash, ExtractionHash: sha256Hex(output), PlanHash: receipt.PlanHash}
	manifest, err := manager.PrepareManifest(delegated, access, NativeWorkspaceManifestSources{Generation: &reference})
	if err != nil || len(manifest.Files) != len(files) {
		t.Fatalf("planned manifest: %v", err)
	}
	for _, file := range manifest.Files {
		body, err := manager.ReadManifestFile(delegated, access, manifest.ManifestHash, file.Path)
		if err != nil || string(body) != files[file.Path] || file.ResourcePath != file.Path {
			t.Fatalf("planned resource: %v", err)
		}
	}
	wrongReference := reference
	wrongReference.PlanHash = sha256Hex(nil)
	if _, err := manager.PrepareManifest(delegated, access, NativeWorkspaceManifestSources{Generation: &wrongReference}); err == nil {
		t.Fatal("wrong input plan initialized workspace")
	}
	plan.Files[nativeMemoryDirectory+"/raw_memories.md"] = "changed"
	raw, _ = json.Marshal(plan.Files)
	plan.ContentHash = sha256Hex(raw)
	if _, err := prepare(worker, plan); err == nil {
		t.Fatal("changed input plan replaced frozen files")
	}
	if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion, Forget: true, RequestID: "forget-plan"}); err != nil {
		t.Fatal(err)
	}
	var body, hash string
	if err := store.db.QueryRow(`SELECT input_plan_json,input_plan_hash FROM agent_memory_generations WHERE generation_id=?`, job.GenerationID).Scan(&body, &hash); err != nil || body != "" || hash != "" {
		t.Fatal("forgotten input plan retained")
	}
	if _, err := manager.ReadManifestFile(delegated, access, manifest.ManifestHash, manifest.Files[0].Path); err == nil {
		t.Fatal("forgotten planned resource remained readable")
	}
}
