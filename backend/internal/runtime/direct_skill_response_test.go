package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
)

type directResponseFixture struct {
	OutputSchema   json.RawMessage   `json:"output_schema"`
	ProviderSchema json.RawMessage   `json:"provider_schema"`
	Valid          json.RawMessage   `json:"valid"`
	Invalid        []json.RawMessage `json:"invalid"`
}

func loadDirectResponseFixture(t *testing.T) directResponseFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testProjectRoot(t), "fixtures", "skills", "response-contracts", "direct-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture directResponseFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestDirectSkillResponseEnforcesOutputAndProviderSchemas(t *testing.T) {
	fixture := loadDirectResponseFixture(t)
	output := ContextOutputContract{ArtifactType: "generic_document", SchemaRef: "schemas/output.json", SchemaVersion: "1.0.0", Schema: fixture.OutputSchema}
	provider := &ContextOutputContract{ArtifactType: "provider_result", SchemaRef: "schemas/provider.json", Schema: fixture.ProviderSchema}
	combined, err := directSkillResponseContract(output, provider)
	if err != nil {
		t.Fatal(err)
	}
	if combined.ArtifactType != "provider_result" || combined.SchemaRef != output.SchemaRef {
		t.Fatalf("combined contract = %+v", combined)
	}
	pack := StepExecutionContextPack{OutputContract: output, ProviderResultContract: combined}
	if err := validateDirectSkillResponse(pack, fixture.Valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range fixture.Invalid {
		if err := validateEmbeddedJSONSchema(combined.Schema, invalid); err == nil {
			t.Fatalf("SDK-facing schema accepted %s", invalid)
		}
		if err := validateDirectSkillResponse(pack, invalid); err == nil {
			t.Fatalf("commit accepted %s", invalid)
		}
	}
	// Old frozen tasks have separate schemas rather than the new conjunction.
	pack.ProviderResultContract = provider
	for _, invalid := range fixture.Invalid {
		if err := validateDirectSkillResponse(pack, invalid); err == nil {
			t.Fatalf("old context accepted %s", invalid)
		}
	}
}

func TestDirectSkillResponsePreservesAbsentOrIdenticalProviderContract(t *testing.T) {
	fixture := loadDirectResponseFixture(t)
	output := ContextOutputContract{ArtifactType: "generic_document", SchemaRef: "schemas/output.json", SchemaVersion: "1.0.0", Schema: fixture.OutputSchema}
	for _, provider := range []*ContextOutputContract{nil, &output} {
		combined, err := directSkillResponseContract(output, provider)
		if err != nil || string(combined.Schema) != string(output.Schema) || combined.SchemaVersion != output.SchemaVersion {
			t.Fatalf("unchanged schema = %+v, %v", combined, err)
		}
	}
}

func TestInstalledBatchSkillUsesBothDeclaredResponseContracts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := filepath.Join(t.TempDir(), "episode-review-workflow")
	source := filepath.Join(testProjectRoot(t), "fixtures", "skills", "batched", "episode-review-workflow")
	if err := os.CopyFS(directory, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(directory, "content-agent", "workflow.json")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow capability.Manifest
	if err := json.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	ref := "../schemas/provider.json"
	workflow.Steps[0].ProviderResultSchemaRef = &ref
	data, err = json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "schemas", "provider.json"),
		[]byte(`{"type":"object","properties":{"title":{"minLength":6}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	started, _, _ := startInstalledBatchSkillFromDirectory(t, store, directory)
	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID: "sdk-dual-schema", ExecutorIDs: []string{"worker.structured_content"}, ProviderID: "openai", ModelID: "test-model", LeaseSeconds: 60,
	})
	if err != nil || claim == nil || claim.ContextPack.ProviderResultContract == nil {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	payload := json.RawMessage(`{"episode_no":1,"title":"Short","content":"Valid artifact but invalid provider constraints."}`)
	if err := validateEmbeddedJSONSchema(claim.ContextPack.OutputContract.Schema, payload); err != nil {
		t.Fatal(err)
	}
	if err := validateEmbeddedJSONSchema(claim.ContextPack.ProviderResultContract.Schema, payload); err == nil {
		t.Fatal("provider constraint missing from context")
	}
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken, InputSnapshotHash: claim.Attempt.InputSnapshotHash, ResponsePayload: payload,
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("receive = %+v, %v", received, err)
	}
	_, err = store.CommitExecutionResult(ctx, CommitExecutionResultCommand{AttemptID: claim.Attempt.AttemptID, ExpectedResponseHash: *received.ResponseHash})
	assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")
	snapshot, err := store.GetRunSnapshot(ctx, started.Run.RunID)
	if err != nil || len(snapshot.Artifacts) != 0 || snapshot.CurrentApproval != nil {
		t.Fatalf("rejected output committed artifacts: %+v, %v", snapshot, err)
	}
}
