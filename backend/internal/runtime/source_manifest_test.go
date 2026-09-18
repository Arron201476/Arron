package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestSourceManifestStableUnitsFromFixedNovelFixture(t *testing.T) {
	content := fixedNovelFixtureContent(t)

	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	first := buildSourceManifestForTest(t, store, "固定小说样本一", content)
	second := buildSourceManifestForTest(t, store, "固定小说样本二", content)
	if len(first.Units) != 12 || len(second.Units) != 12 {
		t.Fatalf("manifest unit counts = %d, %d; want 12, 12", len(first.Units), len(second.Units))
	}

	expectedIDs := []string{
		"SRC-C001-B001",
		"SRC-C002-B001",
		"SRC-C003-B001",
		"SRC-C004-B001",
		"SRC-C005-B001",
		"SRC-C006-B001",
		"SRC-C007-B001",
		"SRC-C008-B001",
		"SRC-C009-B001",
		"SRC-C010-B001",
		"SRC-C011-B001",
		"SRC-C012-B001",
	}
	for index := range first.Units {
		left := first.Units[index]
		right := second.Units[index]
		if left.SourceUnitID != expectedIDs[index] ||
			left.SourceUnitID != right.SourceUnitID ||
			left.UnitKind != "chapter_block" ||
			left.UnitKind != right.UnitKind ||
			left.ParentLabel != right.ParentLabel ||
			left.Order != index+1 ||
			left.Order != right.Order ||
			left.Summary != right.Summary ||
			left.ContentHash == "" ||
			left.ContentHash != right.ContentHash ||
			first.CoveredSourceUnitIDs[index] != left.SourceUnitID ||
			second.CoveredSourceUnitIDs[index] != right.SourceUnitID {
			t.Fatalf("unstable source unit at index %d: first=%+v second=%+v", index, left, right)
		}
	}
	if first.TruncationRisk || second.TruncationRisk ||
		len(first.MissingOrUnreadScope) != 0 ||
		len(second.MissingOrUnreadScope) != 0 {
		t.Fatalf("manifest coverage is incomplete: first=%+v second=%+v", first, second)
	}
}

func TestNovelSourceAnalysisRejectsIncompleteCoverage(t *testing.T) {
	cursor := mustJSONNoTest(map[string]any{
		"batch": map[string]any{
			"source_units": []map[string]any{
				{
					"source_unit_id":    "SRC-C001-B001",
					"asset_id":          "ast_source",
					"asset_snapshot_id": "ass_source",
				},
				{
					"source_unit_id":    "SRC-C002-B001",
					"asset_id":          "ast_source",
					"asset_snapshot_id": "ass_source",
				},
			},
		},
	})
	sourceUnit := func(sourceUnitID string) map[string]any {
		return map[string]any{
			"source_unit_id": sourceUnitID,
			"source_refs": []map[string]any{{
				"source_type":       "asset_text_range",
				"asset_id":          "ast_source",
				"asset_snapshot_id": "ass_source",
				"source_unit_id":    sourceUnitID,
			}},
		}
	}

	t.Run("missing source unit", func(t *testing.T) {
		result := mustJSONNoTest(map[string]any{
			"source_kind": "novel",
			"units":       []map[string]any{sourceUnit("SRC-C001-B001")},
			"coverage_check": map[string]any{
				"covered_source_unit_ids": []string{"SRC-C001-B001"},
				"missing_source_unit_ids": []string{"SRC-C002-B001"},
				"order_issues":            []string{},
			},
		})
		assertDomainCode(
			t,
			validateNovelSourceAnalysisCheckpoint(cursor, result),
			"BATCH_COVERAGE_INVALID",
		)
	})

	t.Run("reordered source units", func(t *testing.T) {
		result := mustJSONNoTest(map[string]any{
			"source_kind": "novel",
			"units": []map[string]any{
				sourceUnit("SRC-C002-B001"),
				sourceUnit("SRC-C001-B001"),
			},
			"coverage_check": map[string]any{
				"covered_source_unit_ids": []string{
					"SRC-C002-B001",
					"SRC-C001-B001",
				},
				"missing_source_unit_ids": []string{},
				"order_issues":            []string{},
			},
		})
		assertDomainCode(
			t,
			validateNovelSourceAnalysisCheckpoint(cursor, result),
			"BATCH_COVERAGE_INVALID",
		)
	})
}

func fixedNovelFixtureContent(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if override := strings.TrimSpace(os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT")); override != "" {
		projectRoot = override
	}
	content, err := os.ReadFile(filepath.Join(
		projectRoot,
		"acceptance",
		"fixtures",
		"novel",
		"stable-source-sample.txt",
	))
	if err != nil {
		t.Fatalf("ReadFile(fixed novel fixture) error = %v", err)
	}
	return string(content)
}

func buildSourceManifestForTest(
	t *testing.T,
	store *Store,
	title string,
	content string,
) sourceManifestPayload {
	t.Helper()
	project, _, initial := startNovelRunWithContent(t, store, title, content)
	running := approveAndResumeLifecycleRun(t, store, project, initial)
	if running.Run.Status != "running" {
		t.Fatalf("run status after source approval = %s, want running", running.Run.Status)
	}

	artifacts, err := store.ListArtifactsByRun(context.Background(), running.Run.RunID)
	if err != nil {
		t.Fatalf("ListArtifactsByRun() error = %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactType != "source_manifest" {
			continue
		}
		version, err := store.GetArtifactVersion(
			context.Background(),
			artifact.CurrentVersionID,
		)
		if err != nil {
			t.Fatalf("GetArtifactVersion(source_manifest) error = %v", err)
		}
		var manifest sourceManifestPayload
		if err := json.Unmarshal(version.Payload, &manifest); err != nil {
			t.Fatalf("Unmarshal(source_manifest) error = %v", err)
		}
		if version.Status != "confirmed" ||
			manifest.RunInputSnapshotVersionID != running.Run.CurrentInputSnapshotVersionID {
			t.Fatalf("source manifest version = %+v payload = %+v", version, manifest)
		}
		return manifest
	}
	t.Fatal("source_manifest artifact not found")
	return sourceManifestPayload{}
}
