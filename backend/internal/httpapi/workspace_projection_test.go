package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
	"content-agent/backend/internal/workspaceview"
)

func TestProjectCapabilitySchemaRouteBindsTheProposal(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "schema.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Proposal schema")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProject(ctx, "Other project")
	if err != nil {
		t.Fatal(err)
	}
	ref := &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: "1.4.0", SelectionMode: "explicit"}
	exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID, Request: agentcontract.MessageRequest{Content: "Configure", CapabilityRef: ref},
		Decision: agentcontract.AgentDecision{Intent: "propose_capability", Reply: "Configure", Confidence: 1, CapabilityRef: ref,
			ProposedAction: &agentcontract.ProposedActionDraft{ActionType: "collect_run_configuration", CapabilityRef: ref, Input: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}},
	})
	if err != nil || exchange.Action == nil {
		t.Fatalf("proposal: %+v %v", exchange, err)
	}
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	query := "?version=" + ref.Version + "&proposed_action_id=" + exchange.Action.ProposedActionID
	base := "/api/v1/projects/" + project.ProjectID + "/capabilities/" + ref.CapabilityID
	performJSON(t, handler, http.MethodGet, base+query, nil, http.StatusOK)
	performJSON(t, handler, http.MethodGet, "/api/v1/projects/"+other.ProjectID+"/capabilities/"+ref.CapabilityID+query, nil, http.StatusUnprocessableEntity)
	performJSON(t, handler, http.MethodGet, base+"?version=1.4.0&proposed_action_id=missing", nil, http.StatusUnprocessableEntity)
}

func TestProjectSkillResourceRoutesUsePinnedVersionAndBoundedReads(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "resource-fixture")
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: resource-fixture\ndescription: Read fixture resources.\n---\nRead references/guide.txt."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "references", "guide.txt"), []byte("sample-text"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t), SkillRoots: []capability.SkillRoot{{Scope: capability.SkillScopeWorkspace, Path: root}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	project := objectAt(t, performJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "resources"}, http.StatusCreated), "data")
	base := "/api/v1/projects/" + stringAt(t, project, "project_id") + "/capabilities/resource_fixture"
	definition := objectAt(t, performJSON(t, handler, http.MethodGet, base, nil, http.StatusOK), "data")
	endpoint := base + "/skill-resources?version=" + url.QueryEscape(stringAt(t, definition, "version"))
	listed := objectAt(t, performJSON(t, handler, http.MethodGet, endpoint, nil, http.StatusOK), "data")
	if len(arrayAt(t, listed, "items")) != 2 {
		t.Fatalf("resources = %+v", listed)
	}
	page := objectAt(t, performJSON(t, handler, http.MethodGet, endpoint+"&path=references%2Fguide.txt&offset=2&limit=4", nil, http.StatusOK), "data")
	if stringAt(t, page, "content") != "mple" {
		t.Fatalf("resource page = %+v", page)
	}
	performJSON(t, handler, http.MethodGet, endpoint+"&path=..%2Fsecret.txt", nil, http.StatusBadRequest)
	performJSON(t, handler, http.MethodGet, endpoint+"&path=references%2Fguide.txt&offset=-1", nil, http.StatusBadRequest)
	performJSON(t, handler, http.MethodGet, base+"/skill-resources?version=missing", nil, http.StatusBadRequest)
}

func TestWorkspaceProjectionAPIsExposeVersionedRegistries(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	catalogResponse := performJSON(
		t, handler, http.MethodGet, "/api/v1/workspace-projection", nil, http.StatusOK,
	)
	catalog := objectAt(t, catalogResponse, "data")
	if stringAt(t, catalog, "contract_version") != workspaceview.ContractVersion ||
		stringAt(t, catalog, "revision") == "" {
		t.Fatalf("catalog identity = %+v", catalog)
	}
	registries := objectAt(t, catalog, "registries")
	for _, key := range []string{"artifacts", "navigation", "tasks", "approvals", "interactions", "composer"} {
		if len(arrayAt(t, registries, key)) == 0 {
			t.Fatalf("registry %s is empty: %+v", key, registries)
		}
	}
	composer := arrayAt(t, registries, "composer")
	foundNovel := false
	for _, raw := range composer {
		entry := raw.(map[string]any)
		if stringAt(t, entry, "capability_id") != "novel_to_script" {
			continue
		}
		foundNovel = true
		if stringAt(t, entry, "view_key") != "source_materials" ||
			stringAt(t, entry, "default_prompt") == "" ||
			stringAt(t, objectAt(t, entry, "input_binding"), "source_type") != "novel" {
			t.Fatalf("novel composer entry = %+v", entry)
		}
	}
	if !foundNovel {
		t.Fatal("novel composer entry is missing")
	}

	projectResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "Projected Workspace",
	}, http.StatusCreated)
	projectID := stringAt(t, objectAt(t, projectResponse, "data"), "project_id")
	projectionResponse := performJSON(
		t, handler, http.MethodGet, "/api/v1/projects/"+projectID+"/workspace-projection", nil, http.StatusOK,
	)
	projection := objectAt(t, projectionResponse, "data")
	if stringAt(t, projection, "contract_version") != workspaceview.ContractVersion ||
		stringAt(t, projection, "registry_revision") != stringAt(t, catalog, "revision") {
		t.Fatalf("project projection identity = %+v", projection)
	}
	snapshot := objectAt(t, projection, "snapshot")
	project := objectAt(t, snapshot, "project")
	if stringAt(t, project, "project_id") != projectID {
		t.Fatalf("project projection snapshot = %+v", snapshot)
	}
	capabilityResponse := performJSON(
		t, handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/capabilities/novel_to_script?version=1.4.0",
		nil, http.StatusOK,
	)
	capabilityDetail := objectAt(t, capabilityResponse, "data")
	if stringAt(t, capabilityDetail, "version") != "1.4.0" ||
		len(objectAt(t, capabilityDetail, "input_schema")) == 0 ||
		len(objectAt(t, capabilityDetail, "config_schemas")) == 0 {
		t.Fatalf("versioned capability detail = %+v", capabilityDetail)
	}
	performJSON(
		t, handler, http.MethodGet,
		"/api/v1/projects/"+projectID+"/capabilities/novel_to_script?version=0.0.0",
		nil, http.StatusNotFound,
	)
}
