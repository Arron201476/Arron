package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"content-agent/backend/internal/capability"
)

type impactTraversalNode struct {
	artifactID        string
	artifactVersionID string
	artifactType      string
	scopeKey          string
	stepID            string
	version           int
	depth             int
	pathLabels        []string
	pathVersionIDs    []string
	relation          string
}

func (s *Store) GetImpactReview(ctx context.Context, impactReviewID string) (ImpactReview, error) {
	review, err := scanImpactReview(s.db.QueryRowContext(ctx, `
		SELECT impact_review_id, project_id, run_id, source_artifact_id,
			old_version_id, new_version_id, change_set_id, status,
			unaffected_summary_json, recommended_regeneration_start_json,
			snapshot_hash, created_at, resolved_at
		FROM impact_reviews WHERE impact_review_id = ?`,
		impactReviewID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ImpactReview{}, domainError("IMPACT_REVIEW_NOT_FOUND", "影响预览不存在。")
	}
	if err != nil {
		return ImpactReview{}, err
	}
	items, err := listImpactReviewItems(ctx, s.db, impactReviewID)
	if err != nil {
		return ImpactReview{}, err
	}
	review.AffectedItems = items
	return review, nil
}

func (s *Store) createArtifactChangeSetTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	artifactVersionID string,
	baseVersionID string,
	scopeKey string,
	changeMode string,
	now time.Time,
) (ArtifactVersionChangeSet, error) {
	if changeMode == "" || changeMode == "full_payload" {
		changeMode = "whole_artifact"
	}
	if changeMode != "whole_artifact" && changeMode != "unknown" {
		return ArtifactVersionChangeSet{}, domainError(
			"CHANGE_SET_INVALID",
			"当前 Runtime 只支持整产物或未知范围变更。",
		)
	}
	changes, err := json.Marshal([]map[string]any{{
		"operation": "replace",
		"scope": map[string]any{
			"kind":       scopeKind(scopeKey),
			"key":        scopeKey,
			"field_path": nil,
		},
	}})
	if err != nil {
		return ArtifactVersionChangeSet{}, err
	}
	signals, err := json.Marshal(map[string]any{
		"continuity_changed":    false,
		"episode_order_changed": false,
		"episode_count_changed": false,
		"scope_precision":       "conservative",
	})
	if err != nil {
		return ArtifactVersionChangeSet{}, err
	}
	changeSet := ArtifactVersionChangeSet{
		ChangeSetID:       s.newID("chg"),
		ProjectID:         projectID,
		ArtifactVersionID: artifactVersionID,
		BaseVersionID:     &baseVersionID,
		ChangeMode:        changeMode,
		Changes:           changes,
		DerivedSignals:    signals,
		CreatedAt:         now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_version_change_sets(
			change_set_id, project_id, artifact_version_id, base_version_id,
			change_mode, changes_json, derived_signals_json, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		changeSet.ChangeSetID,
		changeSet.ProjectID,
		changeSet.ArtifactVersionID,
		baseVersionID,
		changeSet.ChangeMode,
		string(changeSet.Changes),
		string(changeSet.DerivedSignals),
		formatTime(now),
	); err != nil {
		return ArtifactVersionChangeSet{}, err
	}
	return changeSet, nil
}

func (s *Store) createImpactReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	artifact Artifact,
	oldVersionID string,
	oldVersion int,
	newVersionID string,
	changeSet ArtifactVersionChangeSet,
	now time.Time,
) (*ImpactReview, []string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT impact_review_id
		FROM impact_reviews
		WHERE source_artifact_id = ? AND status = 'pending'
		ORDER BY impact_review_id ASC`,
		artifact.ArtifactID,
	)
	if err != nil {
		return nil, nil, err
	}
	var expiredReviewIDs []string
	for rows.Next() {
		var reviewID string
		if err := rows.Scan(&reviewID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		expiredReviewIDs = append(expiredReviewIDs, reviewID)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE impact_reviews
		SET status = 'expired', resolved_at = ?
		WHERE source_artifact_id = ? AND status = 'pending'`,
		formatTime(now),
		artifact.ArtifactID,
	); err != nil {
		return nil, nil, err
	}
	items, recommendedStarts, err := s.calculateImpactItemsTx(
		ctx,
		tx,
		artifact,
		oldVersionID,
		oldVersion,
	)
	if err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return nil, expiredReviewIDs, nil
	}

	unaffectedSummary := json.RawMessage(`{"mode":"conservative","confirmed_unaffected_count":0}`)
	review := ImpactReview{
		ImpactReviewID:               s.newID("imp"),
		ProjectID:                    artifact.ProjectID,
		RunID:                        artifact.RunID,
		SourceArtifactID:             artifact.ArtifactID,
		OldVersionID:                 oldVersionID,
		NewVersionID:                 newVersionID,
		ChangeSetID:                  changeSet.ChangeSetID,
		Status:                       "pending",
		AffectedItems:                items,
		UnaffectedSummary:            unaffectedSummary,
		RecommendedRegenerationStart: recommendedStarts,
		CreatedAt:                    now,
	}
	for index := range review.AffectedItems {
		review.AffectedItems[index].ImpactReviewID = review.ImpactReviewID
		review.AffectedItems[index].ImpactReviewItemID = s.newID("impi")
		review.AffectedItems[index].ItemOrder = index + 1
	}
	snapshotHash, err := impactReviewSnapshotHash(review)
	if err != nil {
		return nil, nil, err
	}
	review.SnapshotHash = snapshotHash
	startsJSON, err := json.Marshal(review.RecommendedRegenerationStart)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO impact_reviews(
			impact_review_id, project_id, run_id, source_artifact_id,
			old_version_id, new_version_id, change_set_id, status,
			unaffected_summary_json, recommended_regeneration_start_json,
			snapshot_hash, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`,
		review.ImpactReviewID,
		review.ProjectID,
		review.RunID,
		review.SourceArtifactID,
		review.OldVersionID,
		review.NewVersionID,
		review.ChangeSetID,
		string(review.UnaffectedSummary),
		string(startsJSON),
		review.SnapshotHash,
		formatTime(now),
	); err != nil {
		return nil, nil, err
	}
	for _, item := range review.AffectedItems {
		pathJSON, err := json.Marshal(item.ImpactPath)
		if err != nil {
			return nil, nil, err
		}
		taskKeysJSON, err := json.Marshal(item.RegenerateTaskKeys)
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO impact_review_items(
				impact_review_item_id, impact_review_id, artifact_id,
				artifact_version_id, artifact_type, scope_key, impact_path_json,
				reason_code, regenerate_from_step_id, regenerate_task_keys_json,
				item_order
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ImpactReviewItemID,
			item.ImpactReviewID,
			item.ArtifactID,
			item.ArtifactVersionID,
			item.ArtifactType,
			item.ScopeKey,
			string(pathJSON),
			item.ReasonCode,
			item.RegenerateFromStepID,
			string(taskKeysJSON),
			item.ItemOrder,
		); err != nil {
			return nil, nil, err
		}
	}
	return &review, expiredReviewIDs, nil
}

func (s *Store) calculateImpactItemsTx(
	ctx context.Context,
	tx *sql.Tx,
	source Artifact,
	oldVersionID string,
	oldVersion int,
) ([]ImpactReviewItem, []string, error) {
	sourceLabel := impactPathLabel(source.ArtifactType, oldVersion, source.ScopeKey)
	queue := []impactTraversalNode{{
		artifactID:        source.ArtifactID,
		artifactVersionID: oldVersionID,
		artifactType:      source.ArtifactType,
		scopeKey:          source.ScopeKey,
		stepID:            "",
		version:           oldVersion,
		depth:             0,
		pathLabels:        []string{sourceLabel},
		pathVersionIDs:    []string{oldVersionID},
	}}
	visited := map[string]bool{oldVersionID: true}
	baseVersionID := oldVersionID
	basePathLabels := []string{sourceLabel}
	basePathVersionIDs := []string{oldVersionID}
	for depth := 1; depth <= maxLineageDepth; depth++ {
		var nextBaseVersionID sql.NullString
		var nextBaseVersion int
		err := tx.QueryRowContext(ctx, `
			SELECT av.base_version_id,
				COALESCE(base.version, 0)
			FROM artifact_versions av
			LEFT JOIN artifact_versions base
				ON base.artifact_version_id = av.base_version_id
			WHERE av.artifact_version_id = ? AND av.artifact_id = ?`,
			baseVersionID,
			source.ArtifactID,
		).Scan(&nextBaseVersionID, &nextBaseVersion)
		if err != nil {
			return nil, nil, err
		}
		if !nextBaseVersionID.Valid {
			break
		}
		if visited[nextBaseVersionID.String] {
			return nil, nil, domainError(
				"DEPENDENCY_CYCLE_DETECTED",
				"产物版本基线形成循环。",
			)
		}
		visited[nextBaseVersionID.String] = true
		basePathLabels = append(
			slices.Clone(basePathLabels),
			impactPathLabel(source.ArtifactType, nextBaseVersion, source.ScopeKey),
		)
		basePathVersionIDs = append(
			slices.Clone(basePathVersionIDs),
			nextBaseVersionID.String,
		)
		queue = append(queue, impactTraversalNode{
			artifactID:        source.ArtifactID,
			artifactVersionID: nextBaseVersionID.String,
			artifactType:      source.ArtifactType,
			scopeKey:          source.ScopeKey,
			version:           nextBaseVersion,
			depth:             0,
			pathLabels:        basePathLabels,
			pathVersionIDs:    basePathVersionIDs,
		})
		baseVersionID = nextBaseVersionID.String
	}
	var nodes []impactTraversalNode

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		rows, err := tx.QueryContext(ctx, `
			SELECT a.artifact_id, av.artifact_version_id, a.artifact_type,
				a.scope_key, sr.step_id, av.version, d.relation
			FROM artifact_dependencies d
			JOIN artifact_versions av
				ON av.artifact_version_id = d.downstream_artifact_version_id
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			JOIN step_runs sr ON sr.step_run_id = a.step_run_id
			WHERE d.upstream_kind = 'artifact_version'
				AND d.upstream_ref_id = ?
				AND d.relation != 'trace_only'
				AND d.project_id = ?
				AND a.current_version_id = av.artifact_version_id
				AND av.status IN ('confirmed', 'pending_approval')
			ORDER BY a.artifact_id ASC, av.artifact_version_id ASC`,
			current.artifactVersionID,
			source.ProjectID,
		)
		if err != nil {
			return nil, nil, err
		}
		var downstream []impactTraversalNode
		for rows.Next() {
			var node impactTraversalNode
			if err := rows.Scan(
				&node.artifactID,
				&node.artifactVersionID,
				&node.artifactType,
				&node.scopeKey,
				&node.stepID,
				&node.version,
				&node.relation,
			); err != nil {
				rows.Close()
				return nil, nil, err
			}
			node.depth = current.depth + 1
			node.pathLabels = append(
				slices.Clone(current.pathLabels),
				impactPathLabel(node.artifactType, node.version, node.scopeKey),
			)
			node.pathVersionIDs = append(
				slices.Clone(current.pathVersionIDs),
				node.artifactVersionID,
			)
			downstream = append(downstream, node)
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
		for _, node := range downstream {
			if slices.Contains(current.pathVersionIDs, node.artifactVersionID) {
				return nil, nil, domainError(
					"DEPENDENCY_CYCLE_DETECTED",
					"影响计算发现循环依赖。",
				)
			}
			if visited[node.artifactVersionID] {
				continue
			}
			visited[node.artifactVersionID] = true
			nodes = append(nodes, node)
			queue = append(queue, node)
		}
	}
	if len(nodes) == 0 {
		return []ImpactReviewItem{}, []string{}, nil
	}

	stepOrder, err := s.capabilityStepOrder(
		ctx, tx, source.ProjectID, source.RunID, source.CapabilityID,
	)
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(nodes, func(left, right int) bool {
		leftOrder := stepOrder[nodes[left].stepID]
		rightOrder := stepOrder[nodes[right].stepID]
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if nodes[left].depth != nodes[right].depth {
			return nodes[left].depth < nodes[right].depth
		}
		return nodes[left].artifactID < nodes[right].artifactID
	})
	items := make([]ImpactReviewItem, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, ImpactReviewItem{
			ArtifactID:           node.artifactID,
			ArtifactVersionID:    node.artifactVersionID,
			ArtifactType:         node.artifactType,
			ScopeKey:             node.scopeKey,
			ImpactPath:           node.pathLabels,
			ReasonCode:           impactReasonCode(node.relation),
			RegenerateFromStepID: node.stepID,
			RegenerateTaskKeys:   []string{node.scopeKey},
		})
	}
	earliestOrder := stepOrder[nodes[0].stepID]
	var recommended []string
	for _, node := range nodes {
		if stepOrder[node.stepID] != earliestOrder {
			break
		}
		if !slices.Contains(recommended, node.stepID) {
			recommended = append(recommended, node.stepID)
		}
	}
	return items, recommended, nil
}

func (s *Store) capabilityStepOrder(
	ctx context.Context,
	query projectWorkspaceQuery,
	projectID, runID, capabilityID string,
) (map[string]int, error) {
	var entry capability.Entry
	var ok bool
	var err error
	if runID != "" {
		entry, ok, err = s.capabilityEntryForRunQuery(ctx, query, runID, capabilityID)
	} else {
		entry, ok, err = s.capabilityEntryForProjectQuery(ctx, query, projectID, capabilityID)
	}
	if err != nil {
		return nil, err
	}
	if !ok || entry.Definition == nil {
		return nil, domainError("CAPABILITY_VERSION_UNAVAILABLE", "影响计算所需能力不可用。")
	}
	order := make(map[string]int, len(entry.Definition.Steps))
	for index, step := range entry.Definition.Steps {
		order[step.ID] = index + 1
	}
	return order, nil
}

func impactReasonCode(relation string) string {
	switch relation {
	case "aggregates":
		return "AGGREGATE_MEMBER_DEPENDENCY"
	case "governed_by":
		return "GOVERNING_INPUT_CHANGED"
	case "references_source":
		return "SOURCE_REFERENCE_CHANGED"
	case "continuity_from":
		return "CONTINUITY_DEPENDENCY"
	default:
		return "WHOLE_ARTIFACT_DEPENDENCY"
	}
}

func impactPathLabel(artifactType string, version int, scopeKey string) string {
	return fmt.Sprintf("%s:v%d:%s", artifactType, version, scopeKey)
}

func impactReviewSnapshotHash(review ImpactReview) (string, error) {
	snapshot := struct {
		ProjectID                    string             `json:"project_id"`
		RunID                        string             `json:"run_id"`
		SourceArtifactID             string             `json:"source_artifact_id"`
		OldVersionID                 string             `json:"old_version_id"`
		NewVersionID                 string             `json:"new_version_id"`
		ChangeSetID                  string             `json:"change_set_id"`
		AffectedItems                []ImpactReviewItem `json:"affected_items"`
		RecommendedRegenerationStart []string           `json:"recommended_regeneration_start"`
	}{
		ProjectID:                    review.ProjectID,
		RunID:                        review.RunID,
		SourceArtifactID:             review.SourceArtifactID,
		OldVersionID:                 review.OldVersionID,
		NewVersionID:                 review.NewVersionID,
		ChangeSetID:                  review.ChangeSetID,
		AffectedItems:                review.AffectedItems,
		RecommendedRegenerationStart: review.RecommendedRegenerationStart,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

func scanImpactReview(row rowScanner) (ImpactReview, error) {
	var review ImpactReview
	var unaffectedSummary, recommendedStarts, createdAt string
	var resolvedAt sql.NullString
	if err := row.Scan(
		&review.ImpactReviewID,
		&review.ProjectID,
		&review.RunID,
		&review.SourceArtifactID,
		&review.OldVersionID,
		&review.NewVersionID,
		&review.ChangeSetID,
		&review.Status,
		&unaffectedSummary,
		&recommendedStarts,
		&review.SnapshotHash,
		&createdAt,
		&resolvedAt,
	); err != nil {
		return ImpactReview{}, err
	}
	review.UnaffectedSummary = json.RawMessage(unaffectedSummary)
	if err := json.Unmarshal([]byte(recommendedStarts), &review.RecommendedRegenerationStart); err != nil {
		return ImpactReview{}, err
	}
	var err error
	review.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ImpactReview{}, err
	}
	review.ResolvedAt, err = optionalTime(resolvedAt)
	return review, err
}

func listImpactReviewItems(
	ctx context.Context,
	queryer interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	},
	impactReviewID string,
) ([]ImpactReviewItem, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT impact_review_item_id, impact_review_id, artifact_id,
			artifact_version_id, artifact_type, scope_key, impact_path_json,
			reason_code, regenerate_from_step_id, regenerate_task_keys_json,
			item_order
		FROM impact_review_items
		WHERE impact_review_id = ?
		ORDER BY item_order ASC, impact_review_item_id ASC`,
		impactReviewID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ImpactReviewItem
	for rows.Next() {
		var item ImpactReviewItem
		var impactPath, taskKeys string
		if err := rows.Scan(
			&item.ImpactReviewItemID,
			&item.ImpactReviewID,
			&item.ArtifactID,
			&item.ArtifactVersionID,
			&item.ArtifactType,
			&item.ScopeKey,
			&impactPath,
			&item.ReasonCode,
			&item.RegenerateFromStepID,
			&taskKeys,
			&item.ItemOrder,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(impactPath), &item.ImpactPath); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(taskKeys), &item.RegenerateTaskKeys); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []ImpactReviewItem{}
	}
	return items, rows.Err()
}
