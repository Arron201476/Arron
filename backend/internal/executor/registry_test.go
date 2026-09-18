package executor

import (
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestDefaultRegistryMatchesImplementedDeploymentBoundary(t *testing.T) {
	registry := NewDefault()
	workerIDs := registry.WorkerExecutorIDs()
	for _, expected := range []string{
		"workflow.shared_script_quality_review",
		"workflow.video_script_extract",
		"worker.structured_content",
	} {
		if !slices.Contains(workerIDs, expected) {
			t.Fatalf("worker executors = %v, missing %s", workerIDs, expected)
		}
	}
}

func TestCapabilityAssessmentRequiresExecutorsAndProviders(t *testing.T) {
	capabilities, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: projectRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	registry := NewDefault()
	providers := ProviderSet{
		"content_model_provider":    true,
		"document_parser":           true,
		"multimodal_video_provider": true,
		"subtitle_ocr_provider":     true,
		"media_processor":           true,
	}

	novel, _ := capabilities.Get("novel_to_script")
	if assessment := registry.Assess(novel, providers); !assessment.Available {
		t.Fatalf("novel assessment = %+v", assessment)
	}
	video, _ := capabilities.Get("video_reference_creation")
	assessment := registry.Assess(video, providers)
	if !assessment.Available {
		t.Fatalf("video assessment = %+v", assessment)
	}

	assessment = registry.Assess(novel, ProviderSet{"document_parser": true})
	if assessment.Available || assessment.ReasonCode != "PROVIDER_UNAVAILABLE" ||
		!slices.Contains(assessment.Missing, "content_model_provider") {
		t.Fatalf("provider assessment = %+v", assessment)
	}
}

func TestApplyAvailabilityKeepsDefinitionsButHidesUnavailableCapabilities(t *testing.T) {
	capabilities, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: projectRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	availableBefore := capabilities.CountByStatus(capability.Available)
	NewDefault().ApplyAvailability(capabilities, ProviderSet{
		"content_model_provider": true,
		"document_parser":        true,
	})
	video, ok := capabilities.Get("video_reference_creation")
	if !ok || video.Definition == nil || video.Status != capability.Unavailable ||
		video.ReasonCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("video capability after projection = %+v", video)
	}
	if capabilities.CountByStatus(capability.Available) != availableBefore-1 {
		t.Fatalf(
			"available count = %d, want %d",
			capabilities.CountByStatus(capability.Available),
			availableBefore-1,
		)
	}
	inlineSkill, ok := capabilities.Get("outline_critic")
	if !ok || inlineSkill.Status != capability.Available {
		t.Fatalf("inline skill availability = %+v", inlineSkill)
	}
}

func TestApplyAvailabilityPersistsAcrossWorkspaceSkillRefresh(t *testing.T) {
	root := projectRoot(t)
	capabilities, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	NewDefault().ApplyAvailability(capabilities, ProviderSet{
		"document_parser":           true,
		"multimodal_video_provider": true,
		"subtitle_ocr_provider":     true,
		"media_processor":           true,
	})
	workspace, err := capabilities.Fork(capability.SkillRoot{
		Scope: capability.SkillScopeWorkspace,
		Path:  filepath.Join(root, "fixtures", "skills", "stateful"), Priority: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := workspace.Get("story_review_workflow")
	if !ok || entry.Status != capability.Unavailable || entry.ReasonCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("dynamic Skill availability = %+v, present = %v", entry, ok)
	}
	if err := workspace.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, ok = workspace.Get("story_review_workflow")
	if !ok || entry.Status != capability.Unavailable || entry.ReasonCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("refreshed dynamic Skill availability = %+v, present = %v", entry, ok)
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
