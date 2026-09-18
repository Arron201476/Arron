package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	defaultLineageDepth = 8
	maxLineageDepth     = 32
)

var artifactDependencyRelations = []string{
	"derived_from",
	"aggregates",
	"configured_by",
	"decided_by",
	"governed_by",
	"references_source",
	"continuity_from",
	"describes",
	"trace_only",
}

func (s *Store) GetArtifactVersionLineage(
	ctx context.Context,
	artifactVersionID string,
	maxDepth int,
) (ArtifactVersionLineage, error) {
	if _, err := s.GetArtifactVersion(ctx, artifactVersionID); err != nil {
		return ArtifactVersionLineage{}, err
	}
	if maxDepth == 0 {
		maxDepth = defaultLineageDepth
	}
	if maxDepth < 1 || maxDepth > maxLineageDepth {
		return ArtifactVersionLineage{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			fmt.Sprintf("依赖查询深度必须在 1 到 %d 之间。", maxLineageDepth),
		)
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE lineage(
			dependency_id, project_id, run_id, origin_type, origin_id, downstream_artifact_version_id,
			upstream_kind, upstream_ref_id, relation, upstream_scope_json,
			downstream_scope_json, impact_policy_id, created_at, depth
		) AS (
			SELECT dependency_id, project_id, COALESCE(run_id, ''), origin_type, origin_id, downstream_artifact_version_id,
				upstream_kind, upstream_ref_id, relation, upstream_scope_json,
				downstream_scope_json, impact_policy_id, created_at, 1
			FROM artifact_dependencies
			WHERE downstream_artifact_version_id = ?
			UNION ALL
			SELECT d.dependency_id, d.project_id, COALESCE(d.run_id, ''), d.origin_type, d.origin_id,
				d.downstream_artifact_version_id, d.upstream_kind,
				d.upstream_ref_id, d.relation, d.upstream_scope_json,
				d.downstream_scope_json, d.impact_policy_id, d.created_at,
				lineage.depth + 1
			FROM artifact_dependencies d
			JOIN lineage
				ON lineage.upstream_kind = 'artifact_version'
				AND d.downstream_artifact_version_id = lineage.upstream_ref_id
			WHERE lineage.depth < ?
		)
		SELECT dependency_id, project_id, run_id, origin_type, origin_id, downstream_artifact_version_id,
			upstream_kind, upstream_ref_id, relation, upstream_scope_json,
			downstream_scope_json, impact_policy_id, created_at, depth
		FROM lineage
		ORDER BY depth ASC, dependency_id ASC`,
		artifactVersionID,
		maxDepth,
	)
	if err != nil {
		return ArtifactVersionLineage{}, err
	}
	defer rows.Close()

	result := ArtifactVersionLineage{
		ArtifactVersionID: artifactVersionID,
		Upstream:          []ArtifactLineageEdge{},
	}
	for rows.Next() {
		var edge ArtifactLineageEdge
		var upstreamScope, downstreamScope, createdAt string
		if err := rows.Scan(
			&edge.DependencyID,
			&edge.ProjectID,
			&edge.RunID,
			&edge.OriginType,
			&edge.OriginID,
			&edge.DownstreamArtifactVersionID,
			&edge.UpstreamKind,
			&edge.UpstreamRefID,
			&edge.Relation,
			&upstreamScope,
			&downstreamScope,
			&edge.ImpactPolicyID,
			&createdAt,
			&edge.Depth,
		); err != nil {
			return ArtifactVersionLineage{}, err
		}
		edge.UpstreamScope = json.RawMessage(upstreamScope)
		edge.DownstreamScope = json.RawMessage(downstreamScope)
		parsed, err := parseTime(createdAt)
		if err != nil {
			return ArtifactVersionLineage{}, err
		}
		edge.CreatedAt = parsed
		result.Upstream = append(result.Upstream, edge)
	}
	return result, rows.Err()
}

func (s *Store) insertArtifactDependencyTx(
	ctx context.Context,
	tx *sql.Tx,
	dependency ArtifactDependency,
	now time.Time,
) error {
	if dependency.ProjectID == "" ||
		dependency.DownstreamArtifactVersionID == "" ||
		dependency.UpstreamRefID == "" ||
		dependency.ImpactPolicyID == "" {
		return domainError("ARTIFACT_DEPENDENCY_INVALID", "产物依赖缺少必要字段。")
	}
	if dependency.UpstreamKind != "artifact_version" &&
		dependency.UpstreamKind != "asset_snapshot" &&
		dependency.UpstreamKind != "config_snapshot" &&
		dependency.UpstreamKind != "decision_snapshot" {
		return domainError("ARTIFACT_DEPENDENCY_INVALID", "产物依赖上游类型不受支持。")
	}
	if !slices.Contains(artifactDependencyRelations, dependency.Relation) {
		return domainError("ARTIFACT_DEPENDENCY_INVALID", "产物依赖关系不受支持。")
	}

	upstreamScope, upstreamScopeHash, err := normalizeDependencyScope(dependency.UpstreamScope)
	if err != nil {
		return err
	}
	downstreamScope, downstreamScopeHash, err := normalizeDependencyScope(dependency.DownstreamScope)
	if err != nil {
		return err
	}

	var downstreamProjectID, downstreamRunID, downstreamOriginType, downstreamOriginID string
	err = tx.QueryRowContext(ctx, `
		SELECT a.project_id, COALESCE(a.run_id, ''), a.origin_type, a.origin_id
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ?`,
		dependency.DownstreamArtifactVersionID,
	).Scan(&downstreamProjectID, &downstreamRunID, &downstreamOriginType, &downstreamOriginID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("ARTIFACT_VERSION_NOT_FOUND", "下游产物版本不存在。")
	}
	if err != nil {
		return err
	}
	if downstreamProjectID != dependency.ProjectID {
		return domainError("DEPENDENCY_PROJECT_MISMATCH", "依赖图不能跨作品。")
	}
	dependency.RunID = downstreamRunID
	dependency.OriginType = downstreamOriginType
	dependency.OriginID = downstreamOriginID

	var upstreamProjectID string
	switch dependency.UpstreamKind {
	case "artifact_version":
		err = tx.QueryRowContext(ctx, `
			SELECT a.project_id
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?`,
			dependency.UpstreamRefID,
		).Scan(&upstreamProjectID)
	case "asset_snapshot":
		err = tx.QueryRowContext(ctx, `
			SELECT project_id FROM asset_snapshots WHERE asset_snapshot_id = ?`,
			dependency.UpstreamRefID,
		).Scan(&upstreamProjectID)
	case "config_snapshot":
		err = tx.QueryRowContext(ctx, `
			SELECT r.project_id
			FROM run_config_snapshots rcs
			JOIN runs r ON r.run_id = rcs.run_id
			WHERE rcs.config_snapshot_id = ?`,
			dependency.UpstreamRefID,
		).Scan(&upstreamProjectID)
	case "decision_snapshot":
		err = tx.QueryRowContext(ctx, `
			SELECT project_id
			FROM run_decision_snapshots
			WHERE decision_snapshot_id = ?`,
			dependency.UpstreamRefID,
		).Scan(&upstreamProjectID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("DEPENDENCY_UPSTREAM_NOT_FOUND", "依赖的上游版本不存在。")
	}
	if err != nil {
		return err
	}
	if upstreamProjectID != dependency.ProjectID {
		return domainError("DEPENDENCY_PROJECT_MISMATCH", "依赖图不能跨作品。")
	}
	if dependency.Relation == "describes" {
		if dependency.UpstreamKind != "artifact_version" {
			return domainError(
				"ARTIFACT_DEPENDENCY_INVALID",
				"交接描述关系必须指向正式产物版本。",
			)
		}
		var downstreamType, downstreamScopeKey, downstreamRunID string
		var upstreamType, upstreamScopeKey, upstreamRunID string
		if err := tx.QueryRowContext(ctx, `
			SELECT downstream.artifact_type, downstream.scope_key, downstream.run_id,
				upstream.artifact_type, upstream.scope_key, upstream.run_id
			FROM artifact_versions downstream_version
			JOIN artifacts downstream
				ON downstream.artifact_id = downstream_version.artifact_id
			JOIN artifact_versions upstream_version
				ON upstream_version.artifact_version_id = ?
			JOIN artifacts upstream
				ON upstream.artifact_id = upstream_version.artifact_id
			WHERE downstream_version.artifact_version_id = ?`,
			dependency.UpstreamRefID,
			dependency.DownstreamArtifactVersionID,
		).Scan(
			&downstreamType,
			&downstreamScopeKey,
			&downstreamRunID,
			&upstreamType,
			&upstreamScopeKey,
			&upstreamRunID,
		); err != nil {
			return err
		}
		if downstreamType != "script_handoff" ||
			upstreamType != "script_unit" ||
			downstreamScopeKey != upstreamScopeKey ||
			downstreamRunID != upstreamRunID ||
			downstreamRunID != dependency.RunID {
			return domainError(
				"ARTIFACT_DEPENDENCY_INVALID",
				"交接描述关系必须绑定同一 Run、同一集的剧本正文。",
			)
		}
	}
	if dependency.UpstreamKind == "artifact_version" {
		cyclic, err := dependencyWouldCycle(
			ctx,
			tx,
			dependency.DownstreamArtifactVersionID,
			dependency.UpstreamRefID,
		)
		if err != nil {
			return err
		}
		if cyclic {
			return domainError("DEPENDENCY_CYCLE_DETECTED", "产物依赖不能形成循环。")
		}
	}
	if dependency.DependencyID == "" {
		dependency.DependencyID = s.newID("dep")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO artifact_dependencies(
			dependency_id, project_id, run_id, origin_type, origin_id, downstream_artifact_version_id,
			upstream_kind, upstream_ref_id, relation, upstream_scope_json,
			upstream_scope_hash, downstream_scope_json, downstream_scope_hash,
			impact_policy_id, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dependency.DependencyID,
		dependency.ProjectID,
		nullableStringPointer(dependency.RunID),
		dependency.OriginType,
		dependency.OriginID,
		dependency.DownstreamArtifactVersionID,
		dependency.UpstreamKind,
		dependency.UpstreamRefID,
		dependency.Relation,
		string(upstreamScope),
		upstreamScopeHash,
		string(downstreamScope),
		downstreamScopeHash,
		dependency.ImpactPolicyID,
		formatTime(now),
	)
	if err != nil && isUniqueConstraint(err) {
		return domainError("ARTIFACT_DEPENDENCY_CONFLICT", "同一产物版本的依赖边重复。")
	}
	return err
}

func (s *Store) copyArtifactDependenciesTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	baseVersionID string,
	newVersionID string,
	now time.Time,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT upstream_kind, upstream_ref_id, relation, upstream_scope_json,
			downstream_scope_json, impact_policy_id
		FROM artifact_dependencies
		WHERE downstream_artifact_version_id = ?
		ORDER BY dependency_id ASC`,
		baseVersionID,
	)
	if err != nil {
		return err
	}
	var dependencies []ArtifactDependency
	for rows.Next() {
		var dependency ArtifactDependency
		var upstreamScope, downstreamScope string
		if err := rows.Scan(
			&dependency.UpstreamKind,
			&dependency.UpstreamRefID,
			&dependency.Relation,
			&upstreamScope,
			&downstreamScope,
			&dependency.ImpactPolicyID,
		); err != nil {
			rows.Close()
			return err
		}
		dependency.ProjectID = projectID
		dependency.RunID = runID
		dependency.DownstreamArtifactVersionID = newVersionID
		dependency.UpstreamScope = json.RawMessage(upstreamScope)
		dependency.DownstreamScope = json.RawMessage(downstreamScope)
		dependencies = append(dependencies, dependency)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, dependency := range dependencies {
		if err := s.insertArtifactDependencyTx(ctx, tx, dependency, now); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDependencyScope(scope json.RawMessage) (json.RawMessage, string, error) {
	if !jsonObject(scope) {
		return nil, "", domainError("ARTIFACT_DEPENDENCY_INVALID", "依赖范围必须是 JSON 对象。")
	}
	var value struct {
		Kind      string  `json:"kind"`
		Key       string  `json:"key"`
		FieldPath *string `json:"field_path"`
	}
	if err := json.Unmarshal(scope, &value); err != nil {
		return nil, "", domainError("ARTIFACT_DEPENDENCY_INVALID", "依赖范围无法读取。")
	}
	if value.Kind == "" || value.Key == "" {
		return nil, "", domainError("ARTIFACT_DEPENDENCY_INVALID", "依赖范围缺少 kind 或 key。")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return normalized, sha256Hex(normalized), nil
}

func dependencyScope(kind, key string) json.RawMessage {
	value, _ := json.Marshal(map[string]any{
		"kind":       kind,
		"key":        key,
		"field_path": nil,
	})
	return value
}

func scopeKind(scopeKey string) string {
	switch {
	case scopeKey == "singleton":
		return "artifact"
	case len(scopeKey) > len("episode:") && scopeKey[:len("episode:")] == "episode:":
		return "episode"
	case len(scopeKey) > len("scene:") && scopeKey[:len("scene:")] == "scene:":
		return "scene"
	case len(scopeKey) > len("asset:") && scopeKey[:len("asset:")] == "asset:":
		return "asset"
	case len(scopeKey) > len("continuity:") && scopeKey[:len("continuity:")] == "continuity:":
		return "continuity"
	default:
		return "collection_item"
	}
}

func dependencyWouldCycle(
	ctx context.Context,
	tx *sql.Tx,
	downstreamVersionID string,
	upstreamVersionID string,
) (bool, error) {
	var cycleCount int
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE ancestors(version_id) AS (
			SELECT ?
			UNION
			SELECT d.upstream_ref_id
			FROM artifact_dependencies d
			JOIN ancestors a ON d.downstream_artifact_version_id = a.version_id
			WHERE d.upstream_kind = 'artifact_version'
		)
		SELECT COUNT(*) FROM ancestors WHERE version_id = ?`,
		upstreamVersionID,
		downstreamVersionID,
	).Scan(&cycleCount)
	return cycleCount > 0, err
}
