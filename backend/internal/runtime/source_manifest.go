package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"content-agent/backend/internal/capability"
)

var chapterHeadingPattern = regexp.MustCompile(
	`(?m)^[\t \x{3000}]*(第[0-9〇零一二三四五六七八九十百千万两]+[章节回卷][^\r\n]*)[\t \x{3000}]*$`,
)

type sourceManifestUnit struct {
	SourceUnitID    string   `json:"source_unit_id"`
	AssetID         string   `json:"asset_id"`
	AssetSnapshotID string   `json:"asset_snapshot_id"`
	UnitKind        string   `json:"unit_kind"`
	ParentLabel     string   `json:"parent_label"`
	Order           int      `json:"order"`
	Summary         string   `json:"summary"`
	ContentHash     string   `json:"content_hash"`
	Characters      []string `json:"characters"`
	Locations       []string `json:"locations"`
	Props           []string `json:"props"`
}

type sourceManifestPayload struct {
	ManifestID                string               `json:"manifest_id"`
	ManifestVersion           int                  `json:"manifest_version"`
	ProjectID                 string               `json:"project_id"`
	RunInputSnapshotVersionID string               `json:"run_input_snapshot_version_id"`
	SourceKind                string               `json:"source_kind"`
	Units                     []sourceManifestUnit `json:"units"`
	CoveredSourceUnitIDs      []string             `json:"covered_source_unit_ids"`
	MissingOrUnreadScope      []string             `json:"missing_or_unread_scope"`
	TruncationRisk            bool                 `json:"truncation_risk"`
	CreatedAt                 string               `json:"created_at"`
}

type builtSourceUnit struct {
	ManifestUnit sourceManifestUnit
	StartOffset  int
	EndOffset    int
	Text         string
}

type sourceTextGroup struct {
	kind  string
	label string
	text  string
}

func (s *Store) executeSourceManifestBuildTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	definition *capability.CompiledDefinition,
	stepRunID string,
	step capability.CompiledStep,
	stepInputJSON string,
	now time.Time,
) (runtimeStepOutcome, error) {
	if len(step.InputRefs) != 1 ||
		step.InputRefs[0].ArtifactType != "source_input" ||
		len(step.OutputRefs) != 1 ||
		step.OutputRefs[0].ArtifactType != "source_manifest" ||
		step.OutputRefs[0].Cardinality != "one" ||
		step.OutputRefs[0].InitialStatus != "confirmed" ||
		step.Approval.Required {
		return runtimeStepOutcome{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"来源清单步骤合同无效。",
		)
	}
	var inputs []struct {
		ArtifactVersionID string `json:"artifact_version_id"`
		Status            string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stepInputJSON), &inputs); err != nil ||
		len(inputs) != 1 ||
		inputs[0].ArtifactVersionID == "" ||
		inputs[0].Status != "confirmed" {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"来源清单缺少已确认的来源材料版本。",
		)
	}

	var sourcePayloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT av.payload_json
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ? AND av.status = 'confirmed'
			AND a.project_id = ? AND a.run_id = ? AND a.artifact_type = 'source_input'`,
		inputs[0].ArtifactVersionID,
		run.ProjectID,
		run.RunID,
	).Scan(&sourcePayloadJSON); err != nil {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"来源清单无法读取已确认的来源材料。",
		)
	}
	var source sourceInputPayload
	if err := json.Unmarshal([]byte(sourcePayloadJSON), &source); err != nil ||
		definition == nil ||
		source.SourceKind != definition.InputBinding.SourceType ||
		len(source.Assets) == 0 && len(source.ArtifactVersions) == 0 {
		return runtimeStepOutcome{}, domainError(
			"DEPENDENCY_INCOMPLETE",
			"文本来源材料合同无效。",
		)
	}
	builtUnits, err := s.buildTextSourceUnitsTx(
		ctx,
		tx,
		run.ProjectID,
		source.SourceKind,
		source.Assets,
		source.ArtifactVersions,
	)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	units := make([]sourceManifestUnit, 0, len(builtUnits))
	coveredIDs := make([]string, 0, len(builtUnits))
	for _, unit := range builtUnits {
		units = append(units, unit.ManifestUnit)
		coveredIDs = append(coveredIDs, unit.ManifestUnit.SourceUnitID)
	}
	manifest := sourceManifestPayload{
		ManifestID:                s.newID("srcm"),
		ManifestVersion:           1,
		ProjectID:                 run.ProjectID,
		RunInputSnapshotVersionID: run.CurrentInputSnapshotVersionID,
		SourceKind:                source.SourceKind,
		Units:                     units,
		CoveredSourceUnitIDs:      coveredIDs,
		MissingOrUnreadScope:      []string{},
		TruncationRisk:            false,
		CreatedAt:                 formatTime(now),
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return runtimeStepOutcome{}, err
	}

	artifactID := s.newID("art")
	versionID := s.newID("av")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
			scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, 'source_manifest', 'singleton', ?, ?, ?)`,
		artifactID,
		run.ProjectID,
		run.RunID,
		stepRunID,
		run.CapabilityID,
		versionID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason,
			created_at, confirmed_at
		) VALUES(?, ?, 1, 'confirmed', ?, 'source_manifest', '1.0.0', 'runtime',
			'runtime.build_source_manifest', 'source_indexing', ?, ?)`,
		versionID,
		artifactID,
		string(payload),
		formatTime(now),
		formatTime(now),
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	for _, reference := range source.Assets {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   run.ProjectID,
			RunID:                       run.RunID,
			DownstreamArtifactVersionID: versionID,
			UpstreamKind:                "asset_snapshot",
			UpstreamRefID:               reference.AssetSnapshotID,
			Relation:                    "references_source",
			UpstreamScope:               dependencyScope("asset", "asset:"+reference.AssetID),
			DownstreamScope:             dependencyScope("collection_item", "asset:"+reference.AssetID),
			ImpactPolicyID:              "asset_exact",
		}, now); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	for _, reference := range source.ArtifactVersions {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   run.ProjectID,
			RunID:                       run.RunID,
			DownstreamArtifactVersionID: versionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               reference.ArtifactVersionID,
			Relation:                    "references_source",
			UpstreamScope:               dependencyScope("artifact", "artifact_version:"+reference.ArtifactVersionID),
			DownstreamScope:             dependencyScope("artifact", "singleton"),
			ImpactPolicyID:              "artifact_exact",
		}, now); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs
		SET status = 'completed', attempt_count = 1,
			started_at = COALESCE(started_at, ?), ended_at = ?
		WHERE step_run_id = ? AND status = 'pending'`,
		"来源清单步骤已经变化。",
		formatTime(now),
		formatTime(now),
		stepRunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	nextStepRunID, completed, err := s.advanceRunAfterConfirmedArtifactsTx(
		ctx,
		tx,
		run.RunID,
		stepRunID,
		[]string{inputs[0].ArtifactVersionID, versionID},
		now,
	)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	runRef, stepRef := run.RunID, stepRunID
	for _, event := range []struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}{
		{"artifact.created", "artifact", artifactID, map[string]any{"artifact_type": "source_manifest"}},
		{"artifact.version_created", "artifact_version", versionID, map[string]any{"version": 1}},
		{"source_manifest.created", "artifact_version", versionID, map[string]any{
			"manifest_id": manifest.ManifestID,
			"unit_count":  len(manifest.Units),
		}},
		{"step.completed", "step_run", stepRunID, nil},
	} {
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			event.eventType,
			event.subjectType,
			event.subjectID,
			event.payload,
		); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	return runtimeStepOutcome{NextStepRunID: nextStepRunID, Completed: completed}, nil
}

func (s *Store) buildTextSourceUnitsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	sourceKind string,
	references []assetInputReference,
	artifactReferences []artifactInputReference,
) ([]builtSourceUnit, error) {
	sort.Slice(references, func(left, right int) bool {
		return references[left].Order < references[right].Order
	})
	result := make([]builtSourceUnit, 0)
	chapterNo := 0
	unitNo := 0
	for _, reference := range references {
		var storageRef, blobStatus, filename string
		if err := tx.QueryRowContext(ctx, `
			SELECT ab.storage_ref, ab.status, a.original_filename
			FROM assets a
			JOIN asset_snapshots ass ON ass.asset_id = a.asset_id
			JOIN asset_blobs ab ON ab.blob_id = a.original_blob_id
			WHERE a.asset_id = ? AND ass.asset_snapshot_id = ?
				AND a.project_id = ? AND a.kind = 'text'
				AND a.status = 'available' AND ass.status = 'available'
				AND a.deleted_at IS NULL`,
			reference.AssetID,
			reference.AssetSnapshotID,
			projectID,
		).Scan(&storageRef, &blobStatus, &filename); err != nil {
			return nil, domainError("CONTEXT_ASSET_UNAVAILABLE", "来源文件当前不可读取。")
		}
		if blobStatus != "available" {
			return nil, domainError("CONTEXT_ASSET_UNAVAILABLE", "来源文件当前不可读取。")
		}
		path, err := resolveDataPath(s.dataRoot, storageRef)
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(content) {
			return nil, domainError("CONTEXT_ASSET_UNAVAILABLE", "来源文件不是有效的 UTF-8 文本。")
		}
		groups := splitTextSourceGroups(string(content), filename, sourceKind)
		for _, group := range groups {
			if group.kind == "chapter_block" {
				chapterNo++
			} else {
				unitNo++
			}
			segments := splitSourceTextIntoUnits(group.text)
			for blockIndex, segment := range segments {
				runes := []rune(group.text)
				if segment.start < 0 || segment.end > len(runes) || segment.end <= segment.start {
					return nil, domainError("SOURCE_MANIFEST_INVALID", "来源单元边界无效。")
				}
				fullText := strings.TrimSpace(string(runes[segment.start:segment.end]))
				if fullText == "" {
					continue
				}
				sourceUnitID := ""
				if group.kind == "chapter_block" {
					sourceUnitID = fmt.Sprintf("SRC-C%03d-B%03d", chapterNo, blockIndex+1)
				} else {
					sourceUnitID = fmt.Sprintf("SRC-U%03d-B%03d", unitNo, blockIndex+1)
				}
				manifestUnit := sourceManifestUnit{
					SourceUnitID:    sourceUnitID,
					AssetID:         reference.AssetID,
					AssetSnapshotID: reference.AssetSnapshotID,
					UnitKind:        group.kind,
					ParentLabel:     group.label,
					Order:           len(result) + 1,
					Summary:         segment.text,
					ContentHash:     sha256Hex([]byte(fullText)),
					Characters:      []string{},
					Locations:       []string{},
					Props:           []string{},
				}
				result = append(result, builtSourceUnit{
					ManifestUnit: manifestUnit,
					StartOffset:  segment.start,
					EndOffset:    segment.end,
					Text:         fullText,
				})
			}
		}
	}
	sort.Slice(artifactReferences, func(left, right int) bool {
		return artifactReferences[left].Order < artifactReferences[right].Order
	})
	for _, reference := range artifactReferences {
		var artifactID, payloadJSON string
		title := reference.ArtifactVersionID
		if err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id, av.payload_json
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
			WHERE a.project_id = ? AND av.artifact_version_id = ?
				AND av.status = 'confirmed'
				AND a.artifact_type IN ('generic_document', 'generic_table')`,
			projectID,
			reference.ArtifactVersionID,
		).Scan(&artifactID, &payloadJSON); err != nil {
			return nil, domainError("CONTEXT_REQUIRED_UPSTREAM_MISSING", "来源产物当前不可读取。")
		}
		content := payloadJSON
		var payload map[string]any
		if json.Unmarshal([]byte(payloadJSON), &payload) == nil {
			if value, ok := payload["content"].(string); ok && strings.TrimSpace(value) != "" {
				content = value
			}
			if value, ok := payload["title"].(string); ok && strings.TrimSpace(value) != "" {
				title = value
			}
		}
		groups := splitTextSourceGroups(content, title, sourceKind)
		for _, group := range groups {
			if group.kind == "chapter_block" {
				chapterNo++
			} else {
				unitNo++
			}
			segments := splitSourceTextIntoUnits(group.text)
			for blockIndex, segment := range segments {
				runes := []rune(group.text)
				if segment.start < 0 || segment.end > len(runes) || segment.end <= segment.start {
					return nil, domainError("SOURCE_MANIFEST_INVALID", "来源单元边界无效。")
				}
				fullText := strings.TrimSpace(string(runes[segment.start:segment.end]))
				if fullText == "" {
					continue
				}
				sourceUnitID := fmt.Sprintf("SRC-U%03d-B%03d", unitNo, blockIndex+1)
				if group.kind == "chapter_block" {
					sourceUnitID = fmt.Sprintf("SRC-C%03d-B%03d", chapterNo, blockIndex+1)
				}
				result = append(result, builtSourceUnit{
					ManifestUnit: sourceManifestUnit{
						SourceUnitID: sourceUnitID, AssetID: "artifact:" + artifactID,
						AssetSnapshotID: reference.ArtifactVersionID, UnitKind: group.kind,
						ParentLabel: title, Order: len(result) + 1, Summary: segment.text,
						ContentHash: sha256Hex([]byte(fullText)), Characters: []string{},
						Locations: []string{}, Props: []string{},
					},
					StartOffset: segment.start, EndOffset: segment.end, Text: fullText,
				})
			}
		}
	}
	if len(result) == 0 {
		return nil, domainError("DEPENDENCY_INCOMPLETE", "来源材料没有可建立清单的正文。")
	}
	return result, nil
}

func splitTextSourceGroups(content, filename, sourceKind string) []sourceTextGroup {
	matches := chapterHeadingPattern.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		kind := "document_section"
		if sourceKind == "novel" {
			kind = "story_unit"
		}
		return []sourceTextGroup{{
			kind:  kind,
			label: filename,
			text:  content,
		}}
	}
	result := make([]sourceTextGroup, 0, len(matches)+1)
	if prefix := strings.TrimSpace(content[:matches[0][0]]); prefix != "" {
		result = append(result, sourceTextGroup{
			kind:  "story_unit",
			label: filename + " 前言",
			text:  prefix,
		})
	}
	for index, match := range matches {
		end := len(content)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		label := strings.TrimSpace(content[match[0]:match[1]])
		result = append(result, sourceTextGroup{
			kind:  "chapter_block",
			label: label,
			text:  content[match[0]:end],
		})
	}
	return result
}

func loadSourceManifestFromInputsTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	inputs []struct {
		ArtifactVersionID string `json:"artifact_version_id"`
	},
) (sourceManifestPayload, error) {
	for _, input := range inputs {
		var payload string
		err := tx.QueryRowContext(ctx, `
			SELECT av.payload_json
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ? AND av.status = 'confirmed'
				AND a.project_id = ? AND a.artifact_type = 'source_manifest'`,
			input.ArtifactVersionID,
			projectID,
		).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return sourceManifestPayload{}, err
		}
		var manifest sourceManifestPayload
		if err := json.Unmarshal([]byte(payload), &manifest); err != nil {
			return sourceManifestPayload{}, domainError(
				"SOURCE_MANIFEST_INVALID",
				"来源清单无法读取。",
			)
		}
		return manifest, nil
	}
	return sourceManifestPayload{}, domainError(
		"DEPENDENCY_INCOMPLETE",
		"当前步骤缺少来源清单版本。",
	)
}

func validateBuiltUnitsAgainstManifest(
	built []builtSourceUnit,
	manifest sourceManifestPayload,
) error {
	if len(built) != len(manifest.Units) ||
		len(manifest.CoveredSourceUnitIDs) != len(manifest.Units) ||
		manifest.TruncationRisk ||
		len(manifest.MissingOrUnreadScope) != 0 {
		return domainError("SOURCE_MANIFEST_INVALID", "来源清单覆盖不完整。")
	}
	for index, unit := range built {
		expected := manifest.Units[index]
		actual := unit.ManifestUnit
		if expected.SourceUnitID != actual.SourceUnitID ||
			expected.AssetID != actual.AssetID ||
			expected.AssetSnapshotID != actual.AssetSnapshotID ||
			expected.Order != index+1 ||
			expected.Order != actual.Order ||
			expected.ContentHash != actual.ContentHash ||
			manifest.CoveredSourceUnitIDs[index] != expected.SourceUnitID {
			return domainError(
				"SOURCE_MANIFEST_DRIFTED",
				"来源文件与已固化来源清单不一致。",
			)
		}
	}
	return nil
}
