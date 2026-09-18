package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
)

var episodeTargetPattern = regexp.MustCompile(`(?:第\s*)?(\d+)\s*集`)

var editableRevisionArtifacts = map[string]struct{}{
	"source_manifest": {}, "story_bible": {}, "episode_split": {}, "material_bank": {},
	"story_seed": {}, "series_blueprint": {}, "episode_cards": {},
	"video_script_unit": {}, "script_analysis": {}, "adaptation_brief": {},
	"script_unit": {}, "generic_document": {}, "generic_table": {},
}

var artifactTargetAliases = map[string][]string{
	"source_input":       {"来源材料", "原始材料", "原文"},
	"source_manifest":    {"来源清单", "来源概要", "概要"},
	"story_bible":        {"故事圣经", "人物设定", "世界观"},
	"episode_split":      {"拆集", "分集拆分"},
	"material_bank":      {"素材库", "材料库"},
	"story_seed":         {"故事种子", "核心故事"},
	"series_blueprint":   {"系列蓝图", "全剧结构"},
	"episode_cards":      {"分集规划", "分集卡", "集纲"},
	"video_script_unit":  {"视频还原剧本", "参考剧本", "视频剧本"},
	"script_analysis":    {"剧本分析", "分析"},
	"adaptation_brief":   {"改编brief", "改编 brief", "改编要求"},
	"adaptation_options": {"改编方案", "改编选项", "改编方向"},
	"reference_scripts":  {"参考剧本合集", "视频还原剧本合集", "参考剧本"},
	"script_context":     {"剧本上下文", "创作上下文"},
	"script_handoff":     {"剧本交接", "交接材料"},
	"script_unit":        {"单集剧本", "剧本"},
	"scripts":            {"完整剧本", "剧本合集", "全剧剧本"},
}

type selectionTarget struct {
	ArtifactID   string          `json:"artifact_id"`
	ArtifactType string          `json:"artifact_type"`
	ScopeKey     string          `json:"scope_key"`
	FieldPath    string          `json:"field_path"`
	Entity       json.RawMessage `json:"entity"`
	TextRange    json.RawMessage `json:"text_range"`
	Display      json.RawMessage `json:"display"`
}

func (s *Store) buildTargetResolutionTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	conversationID string,
	requestMessageID string,
	request agentcontract.MessageRequest,
	agentTarget *agentcontract.TargetRef,
	now time.Time,
) (TargetResolution, error) {
	resolution := TargetResolution{
		TargetResolutionID: s.newID("tr"), ProjectID: projectID,
		ConversationID: conversationID, RequestMessageID: requestMessageID,
		Status: "not_found", Source: "semantic_reference", Entity: json.RawMessage(`{}`),
		Display: json.RawMessage(`{}`), Candidates: []TargetCandidate{}, CreatedAt: now,
	}
	if request.SelectionSnapshot != nil {
		var target selectionTarget
		if err := json.Unmarshal(request.SelectionSnapshot.Selection, &target); err != nil {
			return TargetResolution{}, domainError("REQUEST_VALIDATION_FAILED", "选区定位结构无效。")
		}
		if target.ArtifactID == "" || target.ArtifactType == "" {
			return TargetResolution{}, domainError("REQUEST_VALIDATION_FAILED", "选区缺少产物定位。")
		}
		resolution.Status = "resolved"
		resolution.Source = "explicit_selection"
		resolution.ArtifactID = exactStringPointer(target.ArtifactID)
		resolution.ArtifactVersionID = exactStringPointer(request.SelectionSnapshot.ArtifactVersionID)
		resolution.ArtifactType = exactStringPointer(target.ArtifactType)
		resolution.ScopeKey = optionalStringPointer(target.ScopeKey)
		resolution.FieldPath = optionalStringPointer(target.FieldPath)
		resolution.Entity = defaultJSONObject(target.Entity)
		resolution.TextRange = nullableRaw(target.TextRange)
		resolution.Display = defaultJSONObject(target.Display)
		resolvedAt := now
		resolution.ResolvedAt = &resolvedAt
		resolution.TargetHash = exactStringPointer(targetResolutionHash(resolution))
		return resolution, nil
	}
	if agentTarget != nil && agentTarget.TargetType == "artifact" && agentTarget.TargetID != "" {
		var artifactID, versionID, artifactType, scopeKey string
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id,
				CASE WHEN ? = '' THEN a.current_version_id ELSE av.artifact_version_id END,
				a.artifact_type, a.scope_key
			FROM artifacts a
			LEFT JOIN artifact_versions av
				ON av.artifact_id = a.artifact_id AND av.artifact_version_id = ?
			WHERE a.project_id = ? AND a.artifact_id = ?
				AND (? = '' OR av.artifact_version_id IS NOT NULL)`,
			agentTarget.ArtifactVersionID, agentTarget.ArtifactVersionID,
			projectID, agentTarget.TargetID, agentTarget.ArtifactVersionID,
		).Scan(&artifactID, &versionID, &artifactType, &scopeKey)
		if err == nil {
			resolution.Status = "resolved"
			resolution.Source = "agent_tool_selection"
			resolution.ArtifactID = exactStringPointer(artifactID)
			resolution.ArtifactVersionID = exactStringPointer(versionID)
			resolution.ArtifactType = exactStringPointer(artifactType)
			selectedScopeKey := strings.TrimSpace(agentTarget.ScopeKey)
			if selectedScopeKey == "" {
				selectedScopeKey = scopeKey
			}
			resolution.ScopeKey = optionalStringPointer(selectedScopeKey)
			resolution.FieldPath = optionalStringPointer(agentTarget.FieldPath)
			resolvedAt := now
			resolution.ResolvedAt = &resolvedAt
			resolution.TargetHash = exactStringPointer(targetResolutionHash(resolution))
			return resolution, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return TargetResolution{}, err
		}
	}

	candidates, err := s.semanticTargetCandidatesTx(ctx, tx, projectID, request)
	if err != nil {
		return TargetResolution{}, err
	}
	resolution.Candidates = candidates
	if len(candidates) == 0 {
		return resolution, nil
	}
	if len(candidates) > 1 && candidates[0].Score-candidates[1].Score < 0.20 {
		resolution.Status = "ambiguous"
		return resolution, nil
	}
	selected := candidates[0]
	resolution.Status = "resolved"
	resolution.ArtifactID = exactStringPointer(selected.ArtifactID)
	resolution.ArtifactVersionID = exactStringPointer(selected.ArtifactVersionID)
	resolution.ArtifactType = exactStringPointer(selected.ArtifactType)
	resolution.ScopeKey = optionalStringPointer(selected.ScopeKey)
	resolution.FieldPath = optionalStringPointer(selected.FieldPath)
	resolution.Entity = defaultJSONObject(selected.Entity)
	resolution.Display = defaultJSONObject(selected.Display)
	resolvedAt := now
	resolution.ResolvedAt = &resolvedAt
	resolution.TargetHash = exactStringPointer(targetResolutionHash(resolution))
	return resolution, nil
}

func (s *Store) semanticTargetCandidatesTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	request agentcontract.MessageRequest,
) ([]TargetCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, a.artifact_type, a.scope_key,
			a.capability_id, COALESCE(a.run_id,''), COALESCE(a.step_run_id,''), av.payload_json
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id AND av.artifact_id = a.artifact_id
		WHERE a.project_id = ?
		ORDER BY a.updated_at DESC, a.artifact_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidateArtifact struct {
		artifact Artifact
		payload  string
	}
	artifacts := []candidateArtifact{}
	for rows.Next() {
		item := candidateArtifact{artifact: Artifact{ProjectID: projectID}}
		artifact := &item.artifact
		if err := rows.Scan(&artifact.ArtifactID, &artifact.CurrentVersionID, &artifact.ArtifactType, &artifact.ScopeKey,
			&artifact.CapabilityID, &artifact.RunID, &artifact.StepRunID, &item.payload); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	content := strings.ToLower(request.Content)
	wantedEpisode := 0
	if match := episodeTargetPattern.FindStringSubmatch(content); len(match) == 2 {
		wantedEpisode, _ = strconv.Atoi(match[1])
	}
	result := make([]TargetCandidate, 0, 8)
	for _, item := range artifacts {
		artifact := item.artifact
		artifactID, versionID, artifactType, scopeKey := artifact.ArtifactID, artifact.CurrentVersionID, artifact.ArtifactType, artifact.ScopeKey
		score := 0.0
		for _, alias := range artifactTargetAliases[artifactType] {
			if strings.Contains(content, strings.ToLower(alias)) {
				score += 0.65
				break
			}
		}
		displayLabel := artifactDisplayLabel(artifactType, json.RawMessage(item.payload))
		if dynamicLabel := strings.ToLower(strings.TrimSpace(displayLabel)); dynamicLabel != "" && dynamicLabel != strings.ToLower(artifactLabel(artifactType)) &&
			strings.Contains(content, dynamicLabel) {
			score += 0.85
		}
		if request.ClientContext.CurrentArtifactID != nil && *request.ClientContext.CurrentArtifactID == artifactID {
			score += 0.45
		}
		if wantedEpisode > 0 {
			if revisionEpisodeFromScope(scopeKey) == wantedEpisode {
				score += 0.55
			} else if artifactType == "script_unit" || artifactType == "video_script_unit" {
				continue
			}
		}
		if score == 0 {
			continue
		}
		if _, editable := editableRevisionArtifacts[artifactType]; !editable {
			// Custom types must come from a managed Skill's frozen output contract.
			contract, err := s.artifactEditContractTx(ctx, tx, artifact)
			if err != nil {
				return nil, err
			}
			if contract == nil {
				continue
			}
		}
		display, _ := json.Marshal(map[string]string{
			"artifact_label": displayLabel,
			"location_label": scopeDisplay(scopeKey),
		})
		result = append(result, TargetCandidate{
			CandidateID: s.newID("tc"), ArtifactID: artifactID,
			ArtifactVersionID: versionID, ArtifactType: artifactType, ScopeKey: scopeKey,
			Entity: json.RawMessage(`{}`), Display: display, Score: score,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].ArtifactID < result[j].ArtifactID
		}
		return result[i].Score > result[j].Score
	})
	if len(result) > 5 {
		result = result[:5]
	}
	return result, nil
}

func (s *Store) persistTargetResolutionTx(ctx context.Context, tx *sql.Tx, resolution TargetResolution) error {
	entityJSON := string(defaultJSONObject(resolution.Entity))
	displayJSON := string(defaultJSONObject(resolution.Display))
	candidatesJSON, err := json.Marshal(resolution.Candidates)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO target_resolutions(
			target_resolution_id, project_id, conversation_id, request_message_id,
			status, source, artifact_id, artifact_version_id, artifact_type, scope_key,
			field_path, entity_json, text_range_json, display_json, candidates_json,
			target_hash, created_at, resolved_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		resolution.TargetResolutionID, resolution.ProjectID, resolution.ConversationID,
		resolution.RequestMessageID, resolution.Status, resolution.Source,
		nullableString(resolution.ArtifactID), nullableString(resolution.ArtifactVersionID),
		nullableString(resolution.ArtifactType), nullableString(resolution.ScopeKey),
		nullableString(resolution.FieldPath), entityJSON, nullableRawValue(resolution.TextRange),
		displayJSON, string(candidatesJSON), nullableString(resolution.TargetHash),
		formatTime(resolution.CreatedAt), nullableTime(resolution.ResolvedAt),
	)
	return err
}

func (s *Store) createRevisionRequestTx(
	ctx context.Context,
	tx *sql.Tx,
	resolution TargetResolution,
	instruction string,
	activeRun bool,
	now time.Time,
) (RevisionRequest, error) {
	status := "waiting_target_confirmation"
	if resolution.Status == "resolved" {
		status = "queued"
		if activeRun {
			status = "waiting_safe_checkpoint"
		}
	}
	request := RevisionRequest{
		RevisionRequestID: s.newID("rr"), ProjectID: resolution.ProjectID,
		ConversationID: resolution.ConversationID, RequestMessageID: resolution.RequestMessageID,
		TargetResolutionID: resolution.TargetResolutionID, ArtifactID: resolution.ArtifactID,
		BaseVersionID: resolution.ArtifactVersionID, Instruction: strings.TrimSpace(instruction),
		Operation: revisionOperation(instruction), Status: status, ExecutionPolicy: "safe_checkpoint",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO revision_requests(
			revision_request_id, project_id, conversation_id, request_message_id,
			target_resolution_id, artifact_id, base_artifact_version_id, instruction,
			operation, status, execution_policy, version, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.RevisionRequestID, request.ProjectID, request.ConversationID,
		request.RequestMessageID, request.TargetResolutionID, nullableString(request.ArtifactID),
		nullableString(request.BaseVersionID), request.Instruction, request.Operation,
		request.Status, request.ExecutionPolicy, request.Version, formatTime(now), formatTime(now),
	)
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := s.authorizeRevisionExecutionTx(ctx, tx, request, "created"); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

func (s *Store) ListRevisionRequests(ctx context.Context, projectID string) ([]RevisionRequest, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, revisionSelect+`
		WHERE project_id = ? ORDER BY created_at, revision_request_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RevisionRequest{}
	for rows.Next() {
		request, err := scanRevisionRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, request)
	}
	return result, rows.Err()
}

func (s *Store) GetRevisionRequest(ctx context.Context, requestID string) (RevisionRequest, error) {
	return getRevisionRequestQuery(ctx, s.db, requestID)
}

func getRevisionRequestQuery(ctx context.Context, query rowQueryer, requestID string) (RevisionRequest, error) {
	request, err := scanRevisionRequest(query.QueryRowContext(ctx, revisionSelect+`
		WHERE revision_request_id = ?`, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return RevisionRequest{}, domainError("REVISION_REQUEST_NOT_FOUND", "修改请求不存在。")
	}
	return request, err
}

func (s *Store) GetTargetResolution(ctx context.Context, resolutionID string) (TargetResolution, error) {
	resolution, err := scanTargetResolution(s.db.QueryRowContext(ctx, targetResolutionSelect+`
		WHERE target_resolution_id = ?`, resolutionID))
	if errors.Is(err, sql.ErrNoRows) {
		return TargetResolution{}, domainError("TARGET_NOT_FOUND", "定位结果不存在。")
	}
	return resolution, err
}

func (s *Store) ListTargetResolutions(ctx context.Context, projectID string) ([]TargetResolution, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, targetResolutionSelect+`
		WHERE project_id = ? ORDER BY created_at, target_resolution_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TargetResolution{}
	for rows.Next() {
		resolution, err := scanTargetResolution(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, resolution)
	}
	return result, rows.Err()
}

func (s *Store) ResolveTargetCandidate(
	ctx context.Context,
	command ResolveTargetCandidateCommand,
) (RevisionRequest, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionRequest{}, err
	}
	defer tx.Rollback()
	resolution, err := scanTargetResolution(tx.QueryRowContext(ctx, targetResolutionSelect+`
		WHERE target_resolution_id = ?`, command.TargetResolutionID))
	if errors.Is(err, sql.ErrNoRows) {
		return RevisionRequest{}, domainError("TARGET_NOT_FOUND", "定位结果不存在。")
	}
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := authorizeRevisionStartTx(ctx, tx, resolution.ProjectID, command.Scope); err != nil {
		return RevisionRequest{}, err
	}
	if command.Scope == "" {
		command.Scope = resolution.ProjectID
	}
	request, err := scanRevisionRequest(tx.QueryRowContext(ctx, revisionSelect+` WHERE target_resolution_id=? AND project_id=? AND conversation_id=? AND request_message_id=?`, resolution.TargetResolutionID, resolution.ProjectID, resolution.ConversationID, resolution.RequestMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return RevisionRequest{}, domainError("REVISION_CONTEXT_INVALID", "定位结果不属于当前修改请求。")
	}
	if err != nil {
		return RevisionRequest{}, err
	}
	var selected *TargetCandidate
	for index := range resolution.Candidates {
		if resolution.Candidates[index].CandidateID == command.CandidateID {
			selected = &resolution.Candidates[index]
			break
		}
	}
	if selected == nil {
		return RevisionRequest{}, domainError("TARGET_CANDIDATE_INVALID", "定位候选无效。")
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RevisionRequest{}, err
	}
	if hit {
		receipt, err := decodeIdempotentResult[RevisionRequest](cached)
		if err != nil {
			return RevisionRequest{}, err
		}
		if !revisionStartReceiptMatches(receipt, request) || !sameOptionalID(receipt.ArtifactID, &selected.ArtifactID) || !sameOptionalID(receipt.BaseVersionID, &selected.ArtifactVersionID) || resolution.Status != "resolved" || command.ExpectedVersion > 0 && receipt.Version != command.ExpectedVersion+1 {
			return RevisionRequest{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前返修目标选择。")
		}
		return request, nil
	}
	if resolution.Status != "ambiguous" || request.Status != "waiting_target_confirmation" {
		return RevisionRequest{}, domainError("TARGET_STATE_CONFLICT", "当前定位不需要候选确认。")
	}
	if command.ExpectedVersion > 0 && request.Version != command.ExpectedVersion {
		return RevisionRequest{}, domainError("REVISION_VERSION_CONFLICT", "修改请求版本已变化。")
	}
	var bound bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts a JOIN artifact_versions av ON av.artifact_id=a.artifact_id WHERE a.project_id=? AND a.artifact_id=? AND av.artifact_version_id=? AND a.artifact_type=? AND a.scope_key=?)`, resolution.ProjectID, selected.ArtifactID, selected.ArtifactVersionID, selected.ArtifactType, selected.ScopeKey).Scan(&bound); err != nil {
		return RevisionRequest{}, err
	}
	if !bound {
		return RevisionRequest{}, domainError("TARGET_CANDIDATE_INVALID", "定位候选与作品产物不一致。")
	}
	resolvedAt := now
	targetHash := ""
	resolution.Status = "resolved"
	resolution.ArtifactID = exactStringPointer(selected.ArtifactID)
	resolution.ArtifactVersionID = exactStringPointer(selected.ArtifactVersionID)
	resolution.ArtifactType = exactStringPointer(selected.ArtifactType)
	resolution.ScopeKey = optionalStringPointer(selected.ScopeKey)
	resolution.FieldPath = optionalStringPointer(selected.FieldPath)
	resolution.Entity = defaultJSONObject(selected.Entity)
	resolution.Display = defaultJSONObject(selected.Display)
	resolution.ResolvedAt = &resolvedAt
	targetHash = targetResolutionHash(resolution)
	resolution.TargetHash = &targetHash
	if err := updateExactlyOne(ctx, tx, `
		UPDATE target_resolutions SET status = 'resolved', artifact_id = ?,
			artifact_version_id = ?, artifact_type = ?, scope_key = ?, field_path = ?,
			entity_json = ?, display_json = ?, target_hash = ?, resolved_at = ?
		WHERE target_resolution_id = ? AND status = 'ambiguous'`,
		"定位状态已变化。", selected.ArtifactID, selected.ArtifactVersionID, selected.ArtifactType,
		nullableString(resolution.ScopeKey), nullableString(resolution.FieldPath),
		string(resolution.Entity), string(resolution.Display), targetHash, formatTime(now),
		resolution.TargetResolutionID,
	); err != nil {
		return RevisionRequest{}, err
	}
	var activeRun sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT active_write_run_id FROM projects WHERE project_id = ?`, resolution.ProjectID).Scan(&activeRun); err != nil {
		return RevisionRequest{}, err
	}
	nextStatus := "queued"
	if activeRun.Valid {
		nextStatus = "waiting_safe_checkpoint"
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests
		SET artifact_id = ?, base_artifact_version_id = ?, status = ?, version = version + 1, updated_at = ?
		WHERE revision_request_id = ? AND status = 'waiting_target_confirmation' AND version=?`,
		"修改请求已变化。", selected.ArtifactID, selected.ArtifactVersionID, nextStatus, formatTime(now), request.RevisionRequestID, request.Version,
	); err != nil {
		return RevisionRequest{}, err
	}
	request, err = scanRevisionRequest(tx.QueryRowContext(ctx, revisionSelect+` WHERE revision_request_id = ?`, request.RevisionRequestID))
	if err != nil {
		return RevisionRequest{}, err
	}
	if _, err := s.appendEvent(ctx, tx, resolution.ProjectID, nil, nil,
		"target_resolution.resolved", "target_resolution", resolution.TargetResolutionID,
		map[string]any{"artifact_id": selected.ArtifactID}); err != nil {
		return RevisionRequest{}, err
	}
	if _, err := s.appendEvent(ctx, tx, resolution.ProjectID, nil, nil,
		"revision_request."+nextStatus, "revision_request", request.RevisionRequestID, nil); err != nil {
		return RevisionRequest{}, err
	}
	if err := s.authorizeRevisionExecutionTx(ctx, tx, request, "target_confirmation"); err != nil {
		return RevisionRequest{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, request, now); err != nil {
		return RevisionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

func (s *Store) BeginRevisionAttempt(
	ctx context.Context,
	command BeginRevisionAttemptCommand,
) (RevisionAttempt, error) {
	if !jsonObject(command.ContextPayload) || command.ContextHash == "" {
		return RevisionAttempt{}, domainError("REVISION_CONTEXT_INVALID", "修改上下文无效。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionAttempt{}, err
	}
	defer tx.Rollback()
	request, err := scanRevisionRequest(tx.QueryRowContext(ctx, revisionSelect+` WHERE revision_request_id = ?`, command.RevisionRequestID))
	if err != nil {
		return RevisionAttempt{}, err
	}
	if err := s.authorizeRevisionAttemptTx(ctx, tx, request); err != nil {
		return RevisionAttempt{}, err
	}
	if command.ExpectedVersion > 0 && command.ExpectedVersion != request.Version {
		return RevisionAttempt{}, domainError("REVISION_VERSION_CONFLICT", "修改请求已被其他执行领取或推进。")
	}
	if err := validateRevisionTargetTx(ctx, tx, request); err != nil {
		return RevisionAttempt{}, err
	}
	if request.Status == "waiting_safe_checkpoint" {
		var activeRunID, runStatus sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT p.active_write_run_id, r.status
			FROM projects p LEFT JOIN runs r ON r.run_id = p.active_write_run_id
			WHERE p.project_id = ?`, request.ProjectID).Scan(&activeRunID, &runStatus); err != nil {
			return RevisionAttempt{}, err
		}
		safe := !activeRunID.Valid || (runStatus.Valid && (runStatus.String == "waiting_approval" || runStatus.String == "paused" || runStatus.String == "failed"))
		if !safe {
			return RevisionAttempt{}, domainError("REVISION_WAITING_SAFE_CHECKPOINT", "当前步骤尚未到达安全检查点。")
		}
		if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status = 'queued',
			version = version + 1, updated_at = ? WHERE revision_request_id = ? AND status = 'waiting_safe_checkpoint'`,
			"修改请求状态已经变化。", formatTime(now), request.RevisionRequestID); err != nil {
			return RevisionAttempt{}, err
		}
		request.Status = "queued"
	}
	if request.Status == "failed" {
		if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status = 'queued',
			failure_code = NULL, finished_at = NULL, version = version + 1, updated_at = ?
			WHERE revision_request_id = ? AND status = 'failed'`,
			"修改请求状态已经变化。", formatTime(now), request.RevisionRequestID); err != nil {
			return RevisionAttempt{}, err
		}
		request.Status = "queued"
	}
	if request.Status != "queued" {
		return RevisionAttempt{}, domainError("REVISION_STATE_CONFLICT", "修改请求当前不能执行。")
	}
	var earlier int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM revision_requests
		WHERE project_id = ? AND revision_request_id <> ?
		AND status IN ('queued', 'waiting_safe_checkpoint', 'running')
		AND (created_at < ? OR (created_at = ? AND revision_request_id < ?))`,
		request.ProjectID, request.RevisionRequestID, formatTime(request.CreatedAt),
		formatTime(request.CreatedAt), request.RevisionRequestID).Scan(&earlier); err != nil {
		return RevisionAttempt{}, err
	}
	if earlier > 0 {
		return RevisionAttempt{}, domainError("REVISION_QUEUE_ORDER_CONFLICT", "请等待更早提交的修改请求处理完成。")
	}
	if request.ArtifactID == nil || request.BaseVersionID == nil {
		return RevisionAttempt{}, domainError("TARGET_NOT_FOUND", "修改请求尚未定位。")
	}
	var currentVersionID string
	if err := tx.QueryRowContext(ctx, `SELECT current_version_id FROM artifacts WHERE artifact_id = ?`, *request.ArtifactID).Scan(&currentVersionID); err != nil {
		return RevisionAttempt{}, err
	}
	if currentVersionID != *request.BaseVersionID {
		if _, updateErr := tx.ExecContext(ctx, `UPDATE revision_requests SET status = 'stale',
			version = version + 1, updated_at = ?, finished_at = ? WHERE revision_request_id = ?`,
			formatTime(now), formatTime(now), request.RevisionRequestID); updateErr != nil {
			return RevisionAttempt{}, updateErr
		}
		if err := tx.Commit(); err != nil {
			return RevisionAttempt{}, err
		}
		return RevisionAttempt{}, domainError("REVISION_BASE_VERSION_CONFLICT", "目标产物已有新版本，请重新定位。")
	}
	artifact, err := getArtifactQuery(ctx, tx, *request.ArtifactID)
	if err != nil {
		return RevisionAttempt{}, err
	}
	contract, err := s.artifactEditContractTx(ctx, tx, artifact)
	if err != nil {
		return RevisionAttempt{}, err
	}
	var revisionContext map[string]json.RawMessage
	if err := json.Unmarshal(command.ContextPayload, &revisionContext); err != nil {
		return RevisionAttempt{}, err
	}
	delete(revisionContext, "artifact_validation")
	if contract != nil {
		encoded, err := json.Marshal(contract)
		if err != nil {
			return RevisionAttempt{}, err
		}
		revisionContext["artifact_validation"] = encoded
	}
	command.ContextPayload, err = json.Marshal(revisionContext)
	if err != nil {
		return RevisionAttempt{}, err
	}
	command.ContextHash = sha256Hex(command.ContextPayload)
	var attemptNo int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM revision_attempts
		WHERE revision_request_id = ?`, request.RevisionRequestID).Scan(&attemptNo); err != nil {
		return RevisionAttempt{}, err
	}
	attempt := RevisionAttempt{
		RevisionAttemptID: s.newID("rra"), RevisionRequestID: request.RevisionRequestID,
		AttemptNo: attemptNo, Status: "running", ContextHash: command.ContextHash,
		ContextPayload: command.ContextPayload, AdapterID: command.AdapterID,
		AdapterVersion: command.AdapterVersion, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO revision_attempts(
		revision_attempt_id, revision_request_id, attempt_no, status, context_hash,
		context_payload_json, adapter_id, adapter_version, created_at
	) VALUES(?, ?, ?, 'running', ?, ?, ?, ?, ?)`,
		attempt.RevisionAttemptID, attempt.RevisionRequestID, attempt.AttemptNo,
		attempt.ContextHash, string(attempt.ContextPayload), attempt.AdapterID,
		attempt.AdapterVersion, formatTime(now)); err != nil {
		return RevisionAttempt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE revision_requests SET status = 'running',
		version = version + 1, updated_at = ? WHERE revision_request_id = ? AND status = 'queued'`,
		formatTime(now), request.RevisionRequestID); err != nil {
		return RevisionAttempt{}, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil,
		"revision_request.running", "revision_request", request.RevisionRequestID,
		map[string]any{"attempt_id": attempt.RevisionAttemptID}); err != nil {
		return RevisionAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionAttempt{}, err
	}
	return attempt, nil
}

func (s *Store) CompleteRevisionAttempt(
	ctx context.Context,
	command CompleteRevisionAttemptCommand,
) (RevisionRequest, error) {
	if !jsonObject(command.ProposalPayload) {
		return RevisionRequest{}, domainError("REVISION_OUTPUT_SCHEMA_INVALID", "修改结果必须是 JSON 对象。")
	}
	now := s.now()
	responseHash := sha256Hex(command.ProposalPayload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionRequest{}, err
	}
	defer tx.Rollback()
	request, err := s.revisionCompletionTargetTx(ctx, tx, command.RevisionRequestID, command.RevisionAttemptID)
	if err != nil {
		return RevisionRequest{}, err
	}
	artifact, err := getArtifactQuery(ctx, tx, *request.ArtifactID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := s.validateArtifactEditTx(ctx, tx, artifact, command.ProposalPayload); err != nil {
		return RevisionRequest{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_attempts SET status = 'completed',
		provider_id = ?, trace_ref = ?, response_hash = ?, finished_at = ?
		WHERE revision_attempt_id = ? AND revision_request_id = ? AND status = 'running'`,
		"修改执行状态已经变化。", command.ProviderID, command.TraceRef, responseHash,
		formatTime(now), command.RevisionAttemptID, command.RevisionRequestID); err != nil {
		return RevisionRequest{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status = 'proposed',
		proposal_payload_json = ?, proposal_summary = ?, proposal_hash = ?,
		version = version + 1, updated_at = ? WHERE revision_request_id = ? AND status = 'running'`,
		"修改请求状态已经变化。", string(command.ProposalPayload), strings.TrimSpace(command.ProposalSummary),
		responseHash, formatTime(now), command.RevisionRequestID); err != nil {
		return RevisionRequest{}, err
	}
	request, err = scanRevisionRequest(tx.QueryRowContext(ctx, revisionSelect+` WHERE revision_request_id = ?`, command.RevisionRequestID))
	if err != nil {
		return RevisionRequest{}, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil,
		"revision_request.proposed", "revision_request", request.RevisionRequestID,
		map[string]any{"proposal_hash": responseHash}); err != nil {
		return RevisionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

func (s *Store) CompleteRevisionNoChange(
	ctx context.Context,
	command CompleteRevisionNoChangeCommand,
) (RevisionRequest, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionRequest{}, err
	}
	defer tx.Rollback()
	if _, err := s.revisionCompletionTargetTx(ctx, tx, command.RevisionRequestID, command.RevisionAttemptID); err != nil {
		return RevisionRequest{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_attempts SET status = 'completed',
		provider_id = ?, trace_ref = ?, finished_at = ?
		WHERE revision_attempt_id = ? AND revision_request_id = ? AND status = 'running'`,
		"修改执行状态已经变化。", command.ProviderID, command.TraceRef, formatTime(now),
		command.RevisionAttemptID, command.RevisionRequestID); err != nil {
		return RevisionRequest{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status = 'cancelled',
		proposal_summary = ?, failure_code = NULL, version = version + 1,
		updated_at = ?, finished_at = ? WHERE revision_request_id = ? AND status = 'running'`,
		"修改请求状态已经变化。", strings.TrimSpace(command.Summary), formatTime(now),
		formatTime(now), command.RevisionRequestID); err != nil {
		return RevisionRequest{}, err
	}
	request, err := scanRevisionRequest(tx.QueryRowContext(ctx, revisionSelect+` WHERE revision_request_id = ?`, command.RevisionRequestID))
	if err != nil {
		return RevisionRequest{}, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil,
		"revision_request.no_change", "revision_request", request.RevisionRequestID,
		map[string]any{"summary": strings.TrimSpace(command.Summary)}); err != nil {
		return RevisionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

func (s *Store) FailRevisionAttempt(ctx context.Context, command FailRevisionAttemptCommand) error {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_attempts SET status = 'failed', failure_code = ?, finished_at = ?
		WHERE revision_attempt_id = ? AND revision_request_id = ? AND status = 'running'
		AND EXISTS(SELECT 1 FROM revision_requests r WHERE r.revision_request_id=revision_attempts.revision_request_id AND r.status='running')
		AND attempt_no=(SELECT MAX(current.attempt_no) FROM revision_attempts current WHERE current.revision_request_id=revision_attempts.revision_request_id)`,
		"该返修尝试已结束或已被替代。", command.FailureCode, formatTime(now), command.RevisionAttemptID, command.RevisionRequestID); err != nil {
		return err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status = 'failed', failure_code = ?,
		version = version + 1, updated_at = ?, finished_at = ? WHERE revision_request_id = ? AND status = 'running'`,
		"修改请求已结束或已被替代。", command.FailureCode, formatTime(now), formatTime(now), command.RevisionRequestID); err != nil {
		return err
	}
	request, err := scanRevisionRequest(tx.QueryRowContext(ctx, revisionSelect+` WHERE revision_request_id = ?`, command.RevisionRequestID))
	if err != nil {
		return err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil,
		"revision_request.failed", "revision_request", request.RevisionRequestID,
		map[string]any{"failure_code": command.FailureCode}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcceptRevision(ctx context.Context, command AcceptRevisionCommand) (RevisionAcceptResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	defer tx.Rollback()
	request, err := getRevisionRequestQuery(ctx, tx, command.RevisionRequestID)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	if err := authorizeArtifactCommandTx(ctx, tx, request.ProjectID, command.Scope); err != nil {
		return RevisionAcceptResult{}, err
	}
	if command.Scope == "" {
		command.Scope = request.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	if hit {
		return decodeRevisionAcceptReceiptTx(ctx, tx, cached, request, command)
	}
	if request.Status != "proposed" || request.Version != command.ExpectedVersion ||
		request.ArtifactID == nil || request.BaseVersionID == nil || len(request.ProposalPayload) == 0 {
		return RevisionAcceptResult{}, domainError("REVISION_STATE_CONFLICT", "修改稿状态已经变化，请刷新后重试。")
	}
	baseVersion, err := getArtifactVersionQuery(ctx, tx, *request.BaseVersionID)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	artifact, err := getArtifactQuery(ctx, tx, *request.ArtifactID)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	if artifact.ProjectID != request.ProjectID || baseVersion.ArtifactID != artifact.ArtifactID {
		return RevisionAcceptResult{}, domainError("REVISION_STATE_CONFLICT", "修改稿对应的产物版本不属于当前作品。")
	}
	meta := command.CommandMeta
	if meta.IdempotencyKey != "" {
		meta.IdempotencyKey += ":artifact"
	}
	versionCommand := CreateVersionCommand{
		CommandMeta: meta, ArtifactID: *request.ArtifactID, BaseVersionID: *request.BaseVersionID,
		BaseVersion: baseVersion.Version, ChangeMode: "full_payload", NewPayload: request.ProposalPayload,
		ActorRef: command.ActorRef,
	}
	// The proposal, saved version, and downstream plan share one commit.
	result, err := s.applyArtifactVersionTx(ctx, tx, versionCommand)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	now := s.now()
	if err := updateExactlyOne(ctx, tx, `UPDATE revision_requests SET status = 'accepted',
		version = version + 1, updated_at = ?, finished_at = ?
		WHERE revision_request_id = ? AND status = 'proposed' AND version = ?`,
		"修改稿状态已经变化。", formatTime(now), formatTime(now), request.RevisionRequestID, command.ExpectedVersion); err != nil {
		return RevisionAcceptResult{}, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil,
		"revision_request.accepted", "revision_request", request.RevisionRequestID,
		map[string]any{"artifact_version_id": result.ArtifactVersion.ArtifactVersionID}); err != nil {
		return RevisionAcceptResult{}, err
	}
	accepted, err := getRevisionRequestQuery(ctx, tx, request.RevisionRequestID)
	if err != nil {
		return RevisionAcceptResult{}, err
	}
	receipt := RevisionAcceptResult{Revision: accepted, Version: result}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, receipt, now); err != nil {
		return RevisionAcceptResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionAcceptResult{}, err
	}
	return receipt, nil
}

// MarkAutomaticRevisionApplied makes the durable chat reply match a selected
// micro-edit that Runtime has already accepted and versioned automatically.
func (s *Store) MarkAutomaticRevisionApplied(
	ctx context.Context,
	revisionRequestID string,
	agentMessageID string,
	agentDecisionID string,
) error {
	const reply = "已按你的要求完成修改，并保存为新的产物版本。"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, messageRole, decisionJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT rr.status, m.role, ad.decision_json
		FROM revision_requests rr
		JOIN messages m ON m.message_id = ? AND m.project_id = rr.project_id
		JOIN agent_decisions ad ON ad.agent_decision_id = ? AND ad.agent_message_id = m.message_id
		WHERE rr.revision_request_id = ?`, agentMessageID, agentDecisionID, revisionRequestID).
		Scan(&status, &messageRole, &decisionJSON); err != nil {
		return err
	}
	if status != "accepted" || messageRole != "assistant" {
		return domainError("REVISION_STATE_CONFLICT", "修改尚未完成，不能写入完成回复。")
	}
	var decision agentcontract.AgentDecision
	if err := json.Unmarshal([]byte(decisionJSON), &decision); err != nil {
		return err
	}
	decision.Reply = reply
	encodedDecision, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET content = ? WHERE message_id = ?`, reply, agentMessageID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_decisions SET decision_json = ? WHERE agent_decision_id = ?`, string(encodedDecision), agentDecisionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RejectRevision(ctx context.Context, command RejectRevisionCommand) (RevisionRequest, error) {
	return s.finishRevision(ctx, command.CommandMeta, command.RevisionRequestID, command.ExpectedVersion, "rejected")
}

func (s *Store) CancelRevision(ctx context.Context, command CancelRevisionCommand) (RevisionRequest, error) {
	return s.finishRevision(ctx, command.CommandMeta, command.RevisionRequestID, command.ExpectedVersion, "cancelled")
}

func (s *Store) finishRevision(ctx context.Context, meta CommandMeta, requestID string, expectedVersion int, status string) (RevisionRequest, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionRequest{}, err
	}
	defer tx.Rollback()
	request, err := getRevisionRequestQuery(ctx, tx, requestID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := authorizeArtifactCommandTx(ctx, tx, request.ProjectID, meta.Scope); err != nil {
		return RevisionRequest{}, err
	}
	if meta.Scope == "" {
		meta.Scope = request.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return RevisionRequest{}, err
	}
	if hit {
		receipt, err := decodeIdempotentResult[RevisionRequest](cached)
		if err != nil {
			return RevisionRequest{}, err
		}
		if !revisionReceiptMatches(receipt, request, expectedVersion, status) {
			return RevisionRequest{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前修改请求操作。")
		}
		return receipt, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE revision_requests SET status = ?, version = version + 1,
		updated_at = ?, finished_at = ? WHERE revision_request_id = ? AND version = ?
		AND status IN ('waiting_target_confirmation', 'waiting_safe_checkpoint', 'queued', 'proposed', 'failed')`,
		status, formatTime(now), formatTime(now), requestID, expectedVersion)
	if err != nil {
		return RevisionRequest{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return RevisionRequest{}, err
	}
	if count != 1 {
		return RevisionRequest{}, domainError("REVISION_STATE_CONFLICT", "修改请求状态已经变化。")
	}
	request, err = getRevisionRequestQuery(ctx, tx, requestID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if _, err := s.appendEvent(ctx, tx, request.ProjectID, nil, nil,
		"revision_request."+status, "revision_request", request.RevisionRequestID, nil); err != nil {
		return RevisionRequest{}, err
	}
	if err := completeIdempotency(ctx, tx, meta, request, now); err != nil {
		return RevisionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionRequest{}, err
	}
	return request, nil
}

const revisionSelect = `SELECT revision_request_id, project_id, conversation_id,
	request_message_id, target_resolution_id, artifact_id, base_artifact_version_id,
	instruction, operation, status, execution_policy, version, proposal_payload_json,
	proposal_summary, proposal_hash, failure_code, created_at, updated_at, finished_at,
	(SELECT json_extract(e.payload_json,'$.approval_request_id') FROM events e
	 WHERE e.project_id=revision_requests.project_id AND e.subject_type='revision_request'
	 AND e.subject_id=revision_requests.revision_request_id AND e.event_type='revision_request.created'
	 AND json_extract(e.payload_json,'$.source')='approval_revision' ORDER BY e.project_event_seq LIMIT 1)
	FROM revision_requests `

const targetResolutionSelect = `SELECT target_resolution_id, project_id, conversation_id,
	request_message_id, status, source, artifact_id, artifact_version_id, artifact_type,
	scope_key, field_path, entity_json, text_range_json, display_json, candidates_json,
	target_hash, created_at, resolved_at FROM target_resolutions `

func scanRevisionRequest(row rowScanner) (RevisionRequest, error) {
	var request RevisionRequest
	var artifactID, baseVersionID, proposalPayload, proposalSummary, proposalHash, failureCode, sourceApprovalID sql.NullString
	var createdAt, updatedAt string
	var finishedAt sql.NullString
	err := row.Scan(
		&request.RevisionRequestID, &request.ProjectID, &request.ConversationID,
		&request.RequestMessageID, &request.TargetResolutionID, &artifactID, &baseVersionID,
		&request.Instruction, &request.Operation, &request.Status, &request.ExecutionPolicy,
		&request.Version, &proposalPayload, &proposalSummary, &proposalHash, &failureCode,
		&createdAt, &updatedAt, &finishedAt, &sourceApprovalID,
	)
	if err != nil {
		return RevisionRequest{}, err
	}
	request.ArtifactID = nullStringPointer(artifactID)
	request.BaseVersionID = nullStringPointer(baseVersionID)
	request.SourceApprovalRequestID = nullStringPointer(sourceApprovalID)
	if proposalPayload.Valid {
		request.ProposalPayload = json.RawMessage(proposalPayload.String)
	}
	request.ProposalSummary = nullStringPointer(proposalSummary)
	request.ProposalHash = nullStringPointer(proposalHash)
	request.FailureCode = nullStringPointer(failureCode)
	request.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return RevisionRequest{}, err
	}
	request.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return RevisionRequest{}, err
	}
	request.FinishedAt, err = parseNullableTime(finishedAt)
	return request, err
}

func scanTargetResolution(row rowScanner) (TargetResolution, error) {
	var resolution TargetResolution
	var artifactID, versionID, artifactType, scopeKey, fieldPath, textRange, targetHash sql.NullString
	var entityJSON, displayJSON, candidatesJSON, createdAt string
	var resolvedAt sql.NullString
	err := row.Scan(
		&resolution.TargetResolutionID, &resolution.ProjectID, &resolution.ConversationID,
		&resolution.RequestMessageID, &resolution.Status, &resolution.Source, &artifactID,
		&versionID, &artifactType, &scopeKey, &fieldPath, &entityJSON, &textRange,
		&displayJSON, &candidatesJSON, &targetHash, &createdAt, &resolvedAt,
	)
	if err != nil {
		return TargetResolution{}, err
	}
	resolution.ArtifactID = nullStringPointer(artifactID)
	resolution.ArtifactVersionID = nullStringPointer(versionID)
	resolution.ArtifactType = nullStringPointer(artifactType)
	resolution.ScopeKey = nullStringPointer(scopeKey)
	resolution.FieldPath = nullStringPointer(fieldPath)
	resolution.Entity = json.RawMessage(entityJSON)
	if textRange.Valid {
		resolution.TextRange = json.RawMessage(textRange.String)
	}
	resolution.Display = json.RawMessage(displayJSON)
	if err := json.Unmarshal([]byte(candidatesJSON), &resolution.Candidates); err != nil {
		return TargetResolution{}, err
	}
	resolution.TargetHash = nullStringPointer(targetHash)
	resolution.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return TargetResolution{}, err
	}
	resolution.ResolvedAt, err = parseNullableTime(resolvedAt)
	return resolution, err
}

func revisionOperation(instruction string) string {
	lower := strings.ToLower(instruction)
	if strings.Contains(lower, "删除") || strings.Contains(lower, "删掉") {
		return "delete"
	}
	if strings.Contains(lower, "重写") || strings.Contains(lower, "重新写") {
		return "rewrite"
	}
	if strings.Contains(lower, "重新生成") {
		return "regenerate_scope"
	}
	return "revise"
}

func targetResolutionHash(resolution TargetResolution) string {
	payload, _ := json.Marshal(struct {
		ProjectID  string          `json:"project_id"`
		ArtifactID *string         `json:"artifact_id"`
		VersionID  *string         `json:"artifact_version_id"`
		FieldPath  *string         `json:"field_path"`
		Entity     json.RawMessage `json:"entity"`
		TextRange  json.RawMessage `json:"text_range"`
	}{resolution.ProjectID, resolution.ArtifactID, resolution.ArtifactVersionID,
		resolution.FieldPath, resolution.Entity, resolution.TextRange})
	return sha256Hex(payload)
}

func artifactLabel(artifactType string) string {
	labels := map[string]string{
		"source_input": "来源材料", "source_manifest": "来源清单",
		"story_bible": "故事圣经", "episode_split": "拆集规划",
		"material_bank": "素材库", "story_seed": "故事种子",
		"series_blueprint": "系列蓝图", "episode_cards": "分集规划",
		"video_script_unit": "视频还原剧本", "script_analysis": "剧本分析",
		"adaptation_brief": "改编 Brief", "adaptation_options": "改编方案",
		"reference_scripts": "参考剧本合集", "script_context": "剧本上下文",
		"script_handoff": "剧本交接", "script_unit": "单集剧本", "scripts": "完整剧本",
	}
	if label := labels[artifactType]; label != "" {
		return label
	}
	return artifactType
}

func revisionEpisodeFromScope(scope string) int {
	parts := strings.Split(scope, ":")
	if len(parts) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(parts[1])
	return value
}

func scopeDisplay(scope string) string {
	if episode := revisionEpisodeFromScope(scope); episode > 0 {
		return fmt.Sprintf("第 %d 集", episode)
	}
	return ""
}

func defaultJSONObject(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage(`{}`)
	}
	return value
}

func nullableRaw(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	return value
}

func nullableRawValue(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func exactStringPointer(value string) *string { return &value }

func optionalStringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
