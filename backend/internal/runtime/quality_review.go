package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"
)

var qualityReviewStatuses = []string{
	"pending", "running", "passed", "action_required", "superseded", "failed", "cancelled",
}

var qualityReviewRoutes = []string{
	"source_analysis", "story_bible", "episode_plan", "script_generation", "user",
}

type qualityReviewResultPayload struct {
	ReviewVersion      int                `json:"review_version"`
	InputSnapshotHash  string             `json:"input_snapshot_hash"`
	Scope              string             `json:"scope"`
	IssueCounts        QualityIssueCounts `json:"issue_counts"`
	Issues             []json.RawMessage  `json:"issues"`
	RecommendedRoute   *string            `json:"recommended_route"`
	AffectedEpisodeNos []int              `json:"affected_episode_nos"`
}

func (s *Store) StartQualityReview(
	ctx context.Context,
	command StartQualityReviewCommand,
) (QualityReview, error) {
	if command.RunID == "" || command.StepRunID == "" || command.InputSnapshotHash == "" {
		return QualityReview{}, domainError("REQUEST_VALIDATION_FAILED", "质量审核缺少 Run、Step 或输入快照摘要。")
	}
	if command.Scope == "" {
		command.Scope = "full_script"
	}
	if command.Scope != "full_script" && command.Scope != "impacted_scope" {
		return QualityReview{}, domainError("REQUEST_VALIDATION_FAILED", "质量审核范围无效。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QualityReview{}, err
	}
	defer tx.Rollback()

	var projectID, capabilityID, capabilityVersion, stepID, runStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.project_id, r.capability_id, r.capability_version, sr.step_id, r.status
		FROM runs r
		JOIN step_runs sr ON sr.run_id = r.run_id
		WHERE r.run_id = ? AND sr.step_run_id = ?`,
		command.RunID,
		command.StepRunID,
	).Scan(&projectID, &capabilityID, &capabilityVersion, &stepID, &runStatus); errors.Is(err, sql.ErrNoRows) {
		return QualityReview{}, domainError("RUN_NOT_FOUND", "质量审核对应的生成任务不存在。")
	} else if err != nil {
		return QualityReview{}, err
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, command.RunID, capabilityID,
	)
	if registryErr != nil {
		return QualityReview{}, registryErr
	}
	if !ok || entry.Definition == nil || entry.Definition.Version != capabilityVersion ||
		scriptQualityReviewStep(entry.Definition, stepID) == nil {
		return QualityReview{}, domainError("RUN_STATE_CONFLICT", "当前步骤不是剧本质量审核。")
	}
	if runStatus != "running" && runStatus != "paused" {
		return QualityReview{}, domainError("RUN_STATE_CONFLICT", "当前生成任务状态不能开始质量审核。")
	}

	review, err := s.startQualityReviewTx(ctx, tx, projectID, command, now)
	if err != nil {
		return QualityReview{}, err
	}
	if err := tx.Commit(); err != nil {
		return QualityReview{}, err
	}
	return review, nil
}

func (s *Store) startQualityReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	command StartQualityReviewCommand,
	now time.Time,
) (QualityReview, error) {
	existing, err := getQualityReviewBySnapshotTx(ctx, tx, command.RunID, command.InputSnapshotHash)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return QualityReview{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE quality_reviews
		SET status = 'superseded', finished_at = ?
		WHERE run_id = ? AND status IN ('pending', 'running', 'passed', 'action_required')`,
		formatTime(now),
		command.RunID,
	); err != nil {
		return QualityReview{}, err
	}
	reviewID := s.newID("qrev")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quality_reviews(
			quality_review_id, project_id, run_id, step_run_id, input_snapshot_hash,
			status, scope, review_version, issue_counts_json, recommended_route,
			affected_episode_nos_json, result_json, created_at
		) VALUES(?, ?, ?, ?, ?, 'running', ?, 1, ?, NULL, '[]', '{}', ?)`,
		reviewID,
		projectID,
		command.RunID,
		command.StepRunID,
		command.InputSnapshotHash,
		command.Scope,
		`{"blocker":0,"high":0,"medium":0,"low":0}`,
		formatTime(now),
	); err != nil {
		return QualityReview{}, err
	}
	runRef, stepRef := command.RunID, command.StepRunID
	if _, err := s.appendEvent(
		ctx, tx, projectID, &runRef, &stepRef,
		"quality_review.started", "quality_review", reviewID,
		map[string]any{"input_snapshot_hash": command.InputSnapshotHash, "scope": command.Scope},
	); err != nil {
		return QualityReview{}, err
	}
	return getQualityReviewTx(ctx, tx, reviewID)
}

func (s *Store) CompleteQualityReview(
	ctx context.Context,
	command CompleteQualityReviewCommand,
) (QualityReview, error) {
	if command.QualityReviewID == "" || command.InputSnapshotHash == "" || !jsonObject(command.Result) {
		return QualityReview{}, domainError("REQUEST_VALIDATION_FAILED", "质量审核结果请求无效。")
	}
	projectRoot := s.registry.ProjectRoot()
	if projectRoot == "" {
		return QualityReview{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "质量审核结果合同不可用。")
	}
	resultSchema, err := loadContextSchema(
		projectRoot,
		filepath.Join(projectRoot, "capabilities", "v1", "manifest.schema.json"),
		"../../schemas/v1/quality-review.schema.json",
	)
	if err != nil {
		return QualityReview{}, err
	}
	if err := validateEmbeddedJSONSchema(resultSchema, command.Result); err != nil {
		return QualityReview{}, domainError(
			"OUTPUT_SCHEMA_VALIDATION_FAILED",
			fmt.Sprintf("质量审核结果未通过 Schema 校验：%v", err),
		)
	}
	var result struct {
		ReviewVersion      int                `json:"review_version"`
		InputSnapshotHash  string             `json:"input_snapshot_hash"`
		Scope              string             `json:"scope"`
		IssueCounts        QualityIssueCounts `json:"issue_counts"`
		Issues             []json.RawMessage  `json:"issues"`
		RecommendedRoute   *string            `json:"recommended_route"`
		AffectedEpisodeNos []int              `json:"affected_episode_nos"`
	}
	if err := json.Unmarshal(command.Result, &result); err != nil ||
		result.ReviewVersion <= 0 ||
		result.InputSnapshotHash != command.InputSnapshotHash ||
		(result.Scope != "full_script" && result.Scope != "impacted_scope") ||
		result.IssueCounts.Blocker < 0 || result.IssueCounts.High < 0 ||
		result.IssueCounts.Medium < 0 || result.IssueCounts.Low < 0 {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核结果未通过基础合同校验。")
	}
	total := result.IssueCounts.Blocker + result.IssueCounts.High +
		result.IssueCounts.Medium + result.IssueCounts.Low
	if total != len(result.Issues) {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核问题计数与问题列表不一致。")
	}
	if result.RecommendedRoute != nil && !slices.Contains(qualityReviewRoutes, *result.RecommendedRoute) {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核返工路由无效。")
	}
	actionRequired := result.IssueCounts.Blocker+result.IssueCounts.High+result.IssueCounts.Medium > 0
	status := "passed"
	if actionRequired {
		status = "action_required"
		if result.RecommendedRoute == nil {
			return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "需要处理的审核结果必须给出唯一返工路由。")
		}
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QualityReview{}, err
	}
	defer tx.Rollback()
	review, err := getQualityReviewTx(ctx, tx, command.QualityReviewID)
	if err != nil {
		return QualityReview{}, normalizeQualityReviewNotFound(err)
	}
	if review.Status != "running" {
		return QualityReview{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "质量审核状态已经变化。")
	}
	if review.InputSnapshotHash != command.InputSnapshotHash {
		return QualityReview{}, domainError("QUALITY_REVIEW_INPUT_CHANGED", "质量审核输入快照已经变化。")
	}
	counts, _ := json.Marshal(result.IssueCounts)
	episodes, _ := json.Marshal(result.AffectedEpisodeNos)
	if _, err := tx.ExecContext(ctx, `
		UPDATE quality_reviews
		SET status = ?, scope = ?, review_version = ?, issue_counts_json = ?,
			recommended_route = ?, affected_episode_nos_json = ?, result_json = ?,
			finished_at = ?
		WHERE quality_review_id = ? AND status = 'running'`,
		status,
		result.Scope,
		result.ReviewVersion,
		string(counts),
		nullableStringPointerValue(result.RecommendedRoute),
		string(episodes),
		string(command.Result),
		formatTime(now),
		command.QualityReviewID,
	); err != nil {
		return QualityReview{}, err
	}
	eventType := "quality_review.passed"
	if actionRequired {
		eventType = "quality_review.action_required"
	}
	runRef, stepRef := review.RunID, review.StepRunID
	if _, err := s.appendEvent(
		ctx, tx, review.ProjectID, &runRef, &stepRef,
		eventType, "quality_review", review.QualityReviewID,
		map[string]any{"issue_counts": result.IssueCounts, "recommended_route": result.RecommendedRoute},
	); err != nil {
		return QualityReview{}, err
	}
	completed, err := getQualityReviewTx(ctx, tx, command.QualityReviewID)
	if err != nil {
		return QualityReview{}, err
	}
	if err := tx.Commit(); err != nil {
		return QualityReview{}, err
	}
	return completed, nil
}

// completeQualityReviewTx is the transactional variant used by the Worker commit path.
// The public method above remains the standalone API boundary.
func (s *Store) completeQualityReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	command CompleteQualityReviewCommand,
	now time.Time,
) (QualityReview, error) {
	if command.QualityReviewID == "" || command.InputSnapshotHash == "" || !jsonObject(command.Result) {
		return QualityReview{}, domainError("REQUEST_VALIDATION_FAILED", "质量审核结果请求无效。")
	}
	projectRoot := s.registry.ProjectRoot()
	if projectRoot == "" {
		return QualityReview{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "质量审核结果合同不可用。")
	}
	resultSchema, err := loadContextSchema(
		projectRoot,
		filepath.Join(projectRoot, "capabilities", "v1", "manifest.schema.json"),
		"../../schemas/v1/quality-review.schema.json",
	)
	if err != nil {
		return QualityReview{}, err
	}
	if err := validateEmbeddedJSONSchema(resultSchema, command.Result); err != nil {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", fmt.Sprintf("质量审核结果未通过 Schema 校验：%v", err))
	}
	var result qualityReviewResultPayload
	if err := json.Unmarshal(command.Result, &result); err != nil ||
		result.ReviewVersion <= 0 || result.InputSnapshotHash != command.InputSnapshotHash ||
		(result.Scope != "full_script" && result.Scope != "impacted_scope") ||
		result.IssueCounts.Blocker < 0 || result.IssueCounts.High < 0 ||
		result.IssueCounts.Medium < 0 || result.IssueCounts.Low < 0 {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核结果未通过基础合同校验。")
	}
	total := result.IssueCounts.Blocker + result.IssueCounts.High + result.IssueCounts.Medium + result.IssueCounts.Low
	if total != len(result.Issues) {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核问题计数与问题列表不一致。")
	}
	if result.RecommendedRoute != nil && !slices.Contains(qualityReviewRoutes, *result.RecommendedRoute) {
		return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "质量审核返工路由无效。")
	}
	actionRequired := result.IssueCounts.Blocker+result.IssueCounts.High+result.IssueCounts.Medium > 0
	status := "passed"
	if actionRequired {
		status = "action_required"
		if result.RecommendedRoute == nil {
			return QualityReview{}, domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", "需要处理的审核结果必须给出唯一返工路由。")
		}
	}
	review, err := getQualityReviewTx(ctx, tx, command.QualityReviewID)
	if err != nil {
		return QualityReview{}, normalizeQualityReviewNotFound(err)
	}
	if review.Status != "running" {
		return QualityReview{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "质量审核状态已经变化。")
	}
	if review.InputSnapshotHash != command.InputSnapshotHash {
		return QualityReview{}, domainError("QUALITY_REVIEW_INPUT_CHANGED", "质量审核输入快照已经变化。")
	}
	counts, _ := json.Marshal(result.IssueCounts)
	episodes, _ := json.Marshal(result.AffectedEpisodeNos)
	if _, err := tx.ExecContext(ctx, `
		UPDATE quality_reviews
		SET status = ?, scope = ?, review_version = ?, issue_counts_json = ?,
			recommended_route = ?, affected_episode_nos_json = ?, result_json = ?, finished_at = ?
		WHERE quality_review_id = ? AND status = 'running'`,
		status, result.Scope, result.ReviewVersion, string(counts),
		nullableStringPointerValue(result.RecommendedRoute), string(episodes), string(command.Result),
		formatTime(now), command.QualityReviewID); err != nil {
		return QualityReview{}, err
	}
	eventType := "quality_review.passed"
	if actionRequired {
		eventType = "quality_review.action_required"
	}
	runRef, stepRef := review.RunID, review.StepRunID
	if _, err := s.appendEvent(ctx, tx, review.ProjectID, &runRef, &stepRef,
		eventType, "quality_review", review.QualityReviewID,
		map[string]any{"issue_counts": result.IssueCounts, "recommended_route": result.RecommendedRoute}); err != nil {
		return QualityReview{}, err
	}
	return getQualityReviewTx(ctx, tx, command.QualityReviewID)
}

func (s *Store) GetQualityReview(ctx context.Context, reviewID string) (QualityReview, error) {
	review, err := getQualityReviewTx(ctx, s.db, reviewID)
	if err != nil {
		return QualityReview{}, normalizeQualityReviewNotFound(err)
	}
	return review, nil
}

func (s *Store) GetCurrentQualityReview(ctx context.Context, runID string) (QualityReview, error) {
	review, err := scanQualityReview(s.db.QueryRowContext(ctx, `
		SELECT quality_review_id, project_id, run_id, step_run_id, input_snapshot_hash,
			status, scope, review_version, issue_counts_json, recommended_route,
			affected_episode_nos_json, result_json, created_at, finished_at
		FROM quality_reviews
		WHERE run_id = ? AND status != 'superseded'
		ORDER BY created_at DESC, quality_review_id DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return QualityReview{}, domainError("QUALITY_REVIEW_NOT_FOUND", "当前生成任务没有质量审核。")
	}
	return review, err
}

func (s *Store) ResolveQualityReviewAction(
	ctx context.Context,
	command ResolveQualityReviewActionCommand,
) (QualityReviewActionResult, error) {
	if command.QualityReviewID == "" || command.ExpectedReviewStatus == "" ||
		command.InputSnapshotHash == "" || command.ActorRef == "" {
		return QualityReviewActionResult{}, domainError("REQUEST_VALIDATION_FAILED", "质量审核操作请求不完整。")
	}
	if command.Action == "ai_revise" || command.Action == "confirm_change" {
		return s.resolveQualityReviewRegeneration(ctx, command)
	}
	if command.Action == "manual_edit" {
		review, err := s.GetQualityReview(ctx, command.QualityReviewID)
		if err != nil {
			return QualityReviewActionResult{}, err
		}
		if review.Status != command.ExpectedReviewStatus || review.InputSnapshotHash != command.InputSnapshotHash {
			return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "质量审核状态或输入快照已经变化。")
		}
		return QualityReviewActionResult{Review: review}, nil
	}
	if command.Action != "accept_with_risk" {
		return QualityReviewActionResult{}, domainError(
			"QUALITY_REVIEW_ACTION_NOT_ALLOWED",
			"11A 仅开放风险保留记录；返修和编辑动作将在质量审核执行批次接入。",
		)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	defer tx.Rollback()
	review, err := getQualityReviewTx(ctx, tx, command.QualityReviewID)
	if err != nil {
		return QualityReviewActionResult{}, normalizeQualityReviewNotFound(err)
	}
	if review.Status != command.ExpectedReviewStatus || review.Status != "action_required" {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "质量审核状态已经变化。")
	}
	if review.InputSnapshotHash != command.InputSnapshotHash {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_INPUT_CHANGED", "质量审核输入快照已经变化。")
	}
	if review.IssueCounts.Blocker > 0 || review.IssueCounts.High+review.IssueCounts.Medium == 0 {
		return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_ACTION_NOT_ALLOWED", "当前问题级别不允许风险保留。")
	}
	if command.Scope == "" {
		command.Scope = review.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return QualityReviewActionResult{}, err
	}
	if hit {
		return decodeIdempotentResult[QualityReviewActionResult](cached)
	}
	ignoredJSON, _ := json.Marshal(command.IgnoredIssueIDs)
	override := QualityOverride{
		QualityOverrideID: s.newID("qovr"),
		QualityReviewID:   review.QualityReviewID,
		InputSnapshotHash: review.InputSnapshotHash,
		IgnoredIssueIDs:   slices.Clone(command.IgnoredIssueIDs),
		ActorRef:          command.ActorRef,
		ConfirmedAt:       now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quality_overrides(
			quality_override_id, quality_review_id, input_snapshot_hash,
			ignored_issue_ids_json, actor_ref, confirmed_at
		) VALUES(?, ?, ?, ?, ?, ?)`,
		override.QualityOverrideID,
		override.QualityReviewID,
		override.InputSnapshotHash,
		string(ignoredJSON),
		override.ActorRef,
		formatTime(now),
	); err != nil {
		if isUniqueConstraint(err) {
			return QualityReviewActionResult{}, domainError("QUALITY_REVIEW_STATUS_CONFLICT", "该质量审核已经存在风险保留记录。")
		}
		return QualityReviewActionResult{}, err
	}
	runRef, stepRef := review.RunID, review.StepRunID
	if _, err := s.appendEvent(
		ctx, tx, review.ProjectID, &runRef, &stepRef,
		"quality_review.override_confirmed", "quality_override", override.QualityOverrideID,
		map[string]any{"quality_review_id": review.QualityReviewID},
	); err != nil {
		return QualityReviewActionResult{}, err
	}
	if err := s.advanceAcceptedQualityReviewOverrideTx(
		ctx, tx, review, command.ActorRef, now,
	); err != nil {
		return QualityReviewActionResult{}, err
	}
	result := QualityReviewActionResult{Review: review, Override: &override}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return QualityReviewActionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return QualityReviewActionResult{}, err
	}
	return result, nil
}

type qualityReviewQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getQualityReviewTx(ctx context.Context, query qualityReviewQuery, reviewID string) (QualityReview, error) {
	return scanQualityReview(query.QueryRowContext(ctx, `
		SELECT quality_review_id, project_id, run_id, step_run_id, input_snapshot_hash,
			status, scope, review_version, issue_counts_json, recommended_route,
			affected_episode_nos_json, result_json, created_at, finished_at
		FROM quality_reviews WHERE quality_review_id = ?`, reviewID))
}

func getQualityReviewBySnapshotTx(
	ctx context.Context,
	query qualityReviewQuery,
	runID string,
	inputHash string,
) (QualityReview, error) {
	return scanQualityReview(query.QueryRowContext(ctx, `
		SELECT quality_review_id, project_id, run_id, step_run_id, input_snapshot_hash,
			status, scope, review_version, issue_counts_json, recommended_route,
			affected_episode_nos_json, result_json, created_at, finished_at
		FROM quality_reviews WHERE run_id = ? AND input_snapshot_hash = ?`, runID, inputHash))
}

func scanQualityReview(row interface{ Scan(...any) error }) (QualityReview, error) {
	var review QualityReview
	var issueCountsJSON, episodesJSON, resultJSON, createdAt string
	var route, finishedAt sql.NullString
	if err := row.Scan(
		&review.QualityReviewID,
		&review.ProjectID,
		&review.RunID,
		&review.StepRunID,
		&review.InputSnapshotHash,
		&review.Status,
		&review.Scope,
		&review.ReviewVersion,
		&issueCountsJSON,
		&route,
		&episodesJSON,
		&resultJSON,
		&createdAt,
		&finishedAt,
	); err != nil {
		return QualityReview{}, err
	}
	if !slices.Contains(qualityReviewStatuses, review.Status) {
		return QualityReview{}, errors.New("invalid quality review status")
	}
	if err := json.Unmarshal([]byte(issueCountsJSON), &review.IssueCounts); err != nil {
		return QualityReview{}, err
	}
	if err := json.Unmarshal([]byte(episodesJSON), &review.AffectedEpisodeNos); err != nil {
		return QualityReview{}, err
	}
	review.Result = json.RawMessage(resultJSON)
	review.RecommendedRoute = stringPointer(route)
	var err error
	review.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return QualityReview{}, err
	}
	review.FinishedAt, err = optionalTime(finishedAt)
	return review, err
}

func normalizeQualityReviewNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("QUALITY_REVIEW_NOT_FOUND", "质量审核不存在。")
	}
	return err
}

func nullableStringPointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
