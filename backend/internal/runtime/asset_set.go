package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MaxAssetSetEpisodeNo = 10000

var episodeFilenamePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"chinese_episode", regexp.MustCompile(`第\s*([0-9]+)\s*集`)},
	{"chinese_suffix", regexp.MustCompile(`集\s*([0-9]+)`)},
	{"episode_word", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])episode[_ -]*([0-9]+)(?:[^0-9]|$)`)},
	{"ep_prefix", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ep[_ -]*([0-9]+)(?:[^0-9]|$)`)},
	{"e_prefix", regexp.MustCompile(`(?i)(?:^|[^a-z0-9])e([0-9]+)(?:[^0-9]|$)`)},
}

func DetectEpisodeFilenameCandidate(filename string) EpisodeFilenameCandidate {
	raw := strings.TrimSpace(filename)
	base := normalizeFullwidthDigits(strings.TrimSuffix(raw, filepath.Ext(raw)))
	candidates := map[int]string{}
	for _, pattern := range episodeFilenamePatterns {
		matches := pattern.re.FindAllStringSubmatch(base, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			value, err := strconv.Atoi(match[1])
			if err != nil || value > MaxAssetSetEpisodeNo {
				return EpisodeFilenameCandidate{Confidence: "none", Pattern: "out_of_range", Raw: raw}
			}
			if err == nil && value > 0 {
				candidates[value] = pattern.name
			}
		}
	}
	result := EpisodeFilenameCandidate{Confidence: "none", Pattern: "none", Raw: raw}
	if len(candidates) == 0 {
		return result
	}
	if len(candidates) > 1 {
		result.Confidence = "ambiguous"
		result.Pattern = "multiple_conflicting"
		return result
	}
	for episodeNo, pattern := range candidates {
		value := episodeNo
		result.EpisodeNo = &value
		result.Confidence = "high"
		result.Pattern = pattern
	}
	return result
}

func normalizeFullwidthDigits(value string) string {
	return strings.Map(func(char rune) rune {
		if char >= '０' && char <= '９' {
			return '0' + (char - '０')
		}
		return char
	}, value)
}

func (s *Store) CreateAssetSet(ctx context.Context, command CreateAssetSetCommand) (AssetSetSnapshot, error) {
	if command.ProjectID == "" || strings.TrimSpace(command.DisplayName) == "" ||
		(command.Purpose != "run_source_materials" && command.Purpose != "video_reference_source") {
		return AssetSetSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "输入集合请求不完整。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	defer tx.Rollback()
	if err := authorizeAssetSetCommandTx(ctx, tx, command.ProjectID, command.Scope); err != nil {
		return AssetSetSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if hit {
		receipt, err := decodeAssetSetReceiptTx(ctx, tx, cached, command.ProjectID, "", 1, "draft")
		if err == nil && (receipt.AssetSet.Purpose != command.Purpose || receipt.AssetSet.DisplayName != strings.TrimSpace(command.DisplayName) || !sameOptionalID(receipt.AssetSet.CreatedByMessageID, command.CreatedByMessageID)) {
			err = domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前素材批次创建操作。")
		}
		return receipt, err
	}
	if command.CreatedByMessageID != nil {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE message_id=? AND project_id=?)`, *command.CreatedByMessageID, command.ProjectID).Scan(&exists); err != nil {
			return AssetSetSnapshot{}, err
		}
		if !exists {
			return AssetSetSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "来源消息不属于当前作品。")
		}
	}
	setID, versionID := s.newID("aset"), s.newID("asv")
	emptyCompleteness := AssetSetCompleteness{
		DuplicateEpisodeNumbers: []int{}, MissingEpisodeNumbers: []int{}, FailedAssetIDs: []string{},
	}
	completenessJSON, _ := json.Marshal(emptyCompleteness)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_sets(asset_set_id, project_id, purpose, display_name, status,
			current_version_id, current_version, created_by_message_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'collecting', ?, 1, ?, ?, ?)`,
		setID, command.ProjectID, command.Purpose, strings.TrimSpace(command.DisplayName), versionID,
		nullableString(command.CreatedByMessageID), formatTime(now), formatTime(now)); err != nil {
		return AssetSetSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_set_versions(asset_set_version_id, asset_set_id, version, status,
			member_count, completeness_json, created_at)
		VALUES(?, ?, 1, 'draft', 0, ?, ?)`, versionID, setID, string(completenessJSON), formatTime(now)); err != nil {
		return AssetSetSnapshot{}, err
	}
	if _, err := s.appendEvent(ctx, tx, command.ProjectID, nil, nil,
		"asset_set.created", "asset_set", setID, map[string]any{"purpose": command.Purpose, "version": 1}); err != nil {
		return AssetSetSnapshot{}, err
	}
	snapshot, err := getAssetSetSnapshotTx(ctx, tx, setID, "")
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, snapshot, now); err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssetSetSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) GetAssetSet(ctx context.Context, assetSetID string) (AssetSetSnapshot, error) {
	snapshot, err := s.readAssetSet(ctx, assetSetID, "")
	if errors.Is(err, sql.ErrNoRows) {
		return AssetSetSnapshot{}, domainError("ASSET_SET_NOT_FOUND", "输入集合不存在。")
	}
	return snapshot, err
}

func (s *Store) GetAssetSetVersion(ctx context.Context, versionID string) (AssetSetSnapshot, error) {
	snapshot, err := s.readAssetSet(ctx, "", versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetSetSnapshot{}, domainError("ASSET_SET_VERSION_NOT_FOUND", "输入集合版本不存在。")
	}
	return snapshot, err
}

func (s *Store) CreateAssetSetVersion(ctx context.Context, command CreateAssetSetVersionCommand) (AssetSetSnapshot, error) {
	if command.AssetSetID == "" || command.ExpectedCurrentVersion <= 0 || len(command.Changes) == 0 {
		return AssetSetSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "输入集合修改请求不完整。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	defer tx.Rollback()
	current, err := getAssetSetSnapshotTx(ctx, tx, command.AssetSetID, "")
	if errors.Is(err, sql.ErrNoRows) {
		return AssetSetSnapshot{}, domainError("ASSET_SET_NOT_FOUND", "输入集合不存在。")
	}
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := authorizeAssetSetCommandTx(ctx, tx, current.AssetSet.ProjectID, command.Scope); err != nil {
		return AssetSetSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = current.AssetSet.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if hit {
		return decodeAssetSetReceiptTx(ctx, tx, cached, current.AssetSet.ProjectID, command.AssetSetID, command.ExpectedCurrentVersion+1, "draft")
	}
	if current.AssetSet.Status != "collecting" || current.AssetSet.CurrentVersion != command.ExpectedCurrentVersion {
		return AssetSetSnapshot{}, domainError("ASSET_SET_VERSION_CONFLICT", "输入集合版本已经变化或不在收集状态。")
	}
	members := append([]AssetSetMember(nil), current.Members...)
	for _, change := range command.Changes {
		var err error
		members, err = s.applyAssetSetChangeTx(ctx, tx, current.AssetSet, members, change)
		if err != nil {
			return AssetSetSnapshot{}, err
		}
	}
	if assetSetHasCompleteUniqueEpisodeNumbers(members) && !assetSetChangesContainOrder(command.Changes) {
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].EpisodeNo == nil {
				return false
			}
			if members[j].EpisodeNo == nil {
				return true
			}
			return *members[i].EpisodeNo < *members[j].EpisodeNo
		})
		for index := range members {
			members[index].EpisodeOrder = index + 1
		}
	}
	sort.SliceStable(members, func(i, j int) bool {
		if members[i].EpisodeOrder == members[j].EpisodeOrder {
			return members[i].AssetID < members[j].AssetID
		}
		return members[i].EpisodeOrder < members[j].EpisodeOrder
	})
	for index := range members {
		members[index].EpisodeOrder = index + 1
	}
	return s.persistAssetSetVersionTx(ctx, tx, current, members, "draft", nil, command.CommandMeta, now)
}

func assetSetHasCompleteUniqueEpisodeNumbers(members []AssetSetMember) bool {
	if len(members) == 0 {
		return false
	}
	seen := make(map[int]struct{}, len(members))
	for _, member := range members {
		if !member.Included {
			continue
		}
		if member.EpisodeNo == nil {
			return false
		}
		if _, exists := seen[*member.EpisodeNo]; exists {
			return false
		}
		seen[*member.EpisodeNo] = struct{}{}
	}
	return len(seen) > 0
}

func assetSetChangesContainOrder(changes []AssetSetChange) bool {
	for _, change := range changes {
		if change.Operation == "set_episode_order" {
			return true
		}
	}
	return false
}

func (s *Store) applyAssetSetChangeTx(
	ctx context.Context,
	tx *sql.Tx,
	set AssetSet,
	members []AssetSetMember,
	change AssetSetChange,
) ([]AssetSetMember, error) {
	index := -1
	for memberIndex := range members {
		if members[memberIndex].AssetID == change.AssetID {
			index = memberIndex
			break
		}
	}
	switch change.Operation {
	case "add_asset":
		if index >= 0 {
			return nil, domainError("ASSET_SET_MEMBER_CONFLICT", "素材已经在当前输入集合中。")
		}
		var projectID, filename, status string
		if err := tx.QueryRowContext(ctx, `SELECT project_id, original_filename, status FROM assets WHERE asset_id = ?`, change.AssetID).
			Scan(&projectID, &filename, &status); errors.Is(err, sql.ErrNoRows) {
			return nil, domainError("ASSET_NOT_FOUND", "素材不存在。")
		} else if err != nil {
			return nil, err
		}
		if projectID != set.ProjectID {
			return nil, domainError("ASSET_PROJECT_MISMATCH", "素材不属于当前作品。")
		}
		if status != "available" {
			return nil, domainError("ASSET_SOURCE_DELETED", "素材当前不可用。")
		}
		candidate := DetectEpisodeFilenameCandidate(filename)
		member := AssetSetMember{AssetID: change.AssetID, EpisodeOrder: len(members) + 1,
			EpisodeSource: "unassigned", FilenameCandidate: candidate, Included: true}
		if candidate.Confidence == "high" && candidate.EpisodeNo != nil {
			value := *candidate.EpisodeNo
			member.EpisodeNo = &value
			label := fmt.Sprintf("第%d集", value)
			member.EpisodeLabel = &label
			member.EpisodeSource = "filename"
		}
		members = append(members, member)
	case "remove_asset":
		if index < 0 {
			return nil, domainError("ASSET_SET_MEMBER_NOT_FOUND", "输入集合中没有该素材。")
		}
		members = append(members[:index], members[index+1:]...)
	case "set_episode_no":
		if index < 0 || (change.EpisodeNo == nil && !change.ClearEpisodeNo) {
			return nil, domainError("REQUEST_VALIDATION_FAILED", "集号修改请求无效。")
		}
		if change.ClearEpisodeNo {
			members[index].EpisodeNo = nil
			members[index].EpisodeLabel = nil
			members[index].EpisodeSource = "manual_clear"
		} else {
			if *change.EpisodeNo <= 0 || *change.EpisodeNo > MaxAssetSetEpisodeNo {
				return nil, domainError("REQUEST_VALIDATION_FAILED", "集号必须为 1 到 10000 的整数。")
			}
			value := *change.EpisodeNo
			members[index].EpisodeNo = &value
			label := fmt.Sprintf("第%d集", value)
			members[index].EpisodeLabel = &label
			members[index].EpisodeSource = "manual"
		}
	case "set_episode_order":
		if index < 0 || change.EpisodeOrder == nil || *change.EpisodeOrder <= 0 || *change.EpisodeOrder > len(members) {
			return nil, domainError("REQUEST_VALIDATION_FAILED", "排序修改请求无效。")
		}
		member := members[index]
		members = append(members[:index], members[index+1:]...)
		target := *change.EpisodeOrder - 1
		members = append(members, AssetSetMember{})
		copy(members[target+1:], members[target:])
		members[target] = member
		for idx := range members {
			members[idx].EpisodeOrder = idx + 1
		}
	case "exclude_asset":
		if index < 0 || change.ExclusionReason == nil || strings.TrimSpace(*change.ExclusionReason) == "" {
			return nil, domainError("REQUEST_VALIDATION_FAILED", "排除素材必须说明原因。")
		}
		members[index].Included = false
		members[index].ExclusionReason = change.ExclusionReason
	case "include_asset":
		if index < 0 {
			return nil, domainError("ASSET_SET_MEMBER_NOT_FOUND", "输入集合中没有该素材。")
		}
		members[index].Included = true
		members[index].ExclusionReason = nil
	default:
		return nil, domainError("REQUEST_VALIDATION_FAILED", "输入集合修改操作不受支持。")
	}
	return members, nil
}

func (s *Store) CheckAssetSet(ctx context.Context, assetSetID string) (AssetSetSnapshot, error) {
	return s.GetAssetSet(ctx, assetSetID)
}

func (s *Store) SealAssetSet(ctx context.Context, command SealAssetSetCommand) (AssetSetSnapshot, error) {
	if command.AssetSetID == "" || command.ExpectedCurrentVersion <= 0 || !command.UserConfirmedUploadComplete {
		return AssetSetSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "完成上传必须由用户明确确认。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	defer tx.Rollback()
	current, err := getAssetSetSnapshotTx(ctx, tx, command.AssetSetID, "")
	if errors.Is(err, sql.ErrNoRows) {
		return AssetSetSnapshot{}, domainError("ASSET_SET_NOT_FOUND", "输入集合不存在。")
	}
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := authorizeAssetSetCommandTx(ctx, tx, current.AssetSet.ProjectID, command.Scope); err != nil {
		return AssetSetSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = current.AssetSet.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if hit {
		return decodeAssetSetReceiptTx(ctx, tx, cached, current.AssetSet.ProjectID, command.AssetSetID, command.ExpectedCurrentVersion+1, "sealed")
	}
	if current.AssetSet.Status != "collecting" || current.AssetSet.CurrentVersion != command.ExpectedCurrentVersion {
		return AssetSetSnapshot{}, domainError("ASSET_SET_VERSION_CONFLICT", "输入集合版本已经变化或不在收集状态。")
	}
	completeness, err := s.assetSetCompletenessTx(ctx, tx, current.Members)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if completeness.RecognizedEpisodeCount+completeness.UnrecognizedCount == 0 {
		return AssetSetSnapshot{}, domainError("ASSET_SET_INCOMPLETE", "输入集合为空。")
	}
	if len(completeness.DuplicateEpisodeNumbers) > 0 {
		return AssetSetSnapshot{}, domainError("ASSET_SET_DUPLICATE_EPISODE", "输入集合存在重复集号。")
	}
	if completeness.UnrecognizedCount > 0 || len(completeness.FailedAssetIDs) > 0 {
		return AssetSetSnapshot{}, domainError("ASSET_SET_INCOMPLETE", "输入集合存在未确认集号或不可用素材。")
	}
	if len(completeness.MissingEpisodeNumbers) > 0 && !validContinueIncompletePolicy(command.ContinuationPolicy) {
		return AssetSetSnapshot{}, domainError("ASSET_SET_INCOMPLETE", "输入集合缺集，必须明确确认按不完整材料继续。")
	}
	return s.persistAssetSetVersionTx(ctx, tx, current, current.Members, "sealed", command.ContinuationPolicy, command.CommandMeta, now)
}

func validContinueIncompletePolicy(payload json.RawMessage) bool {
	if len(payload) == 0 || string(payload) == "null" {
		return false
	}
	var policy struct {
		Mode        string `json:"mode"`
		ConfirmedBy string `json:"confirmed_by"`
	}
	return json.Unmarshal(payload, &policy) == nil && policy.Mode == "continue_incomplete" && policy.ConfirmedBy == "user"
}

func (s *Store) ReopenAssetSet(ctx context.Context, assetSetID string, expectedVersion int) (AssetSetSnapshot, error) {
	return s.ReopenAssetSetCommand(ctx, ReopenAssetSetCommand{
		AssetSetID: assetSetID, ExpectedCurrentVersion: expectedVersion,
	})
}

func (s *Store) ReopenAssetSetCommand(ctx context.Context, command ReopenAssetSetCommand) (AssetSetSnapshot, error) {
	if command.AssetSetID == "" || command.ExpectedCurrentVersion <= 0 {
		return AssetSetSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "重新收集请求缺少素材批次或版本。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	defer tx.Rollback()
	current, err := getAssetSetSnapshotTx(ctx, tx, command.AssetSetID, "")
	if err != nil {
		return AssetSetSnapshot{}, normalizeAssetSetNotFound(err)
	}
	if err := authorizeAssetSetCommandTx(ctx, tx, current.AssetSet.ProjectID, command.Scope); err != nil {
		return AssetSetSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = current.AssetSet.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if hit {
		return decodeAssetSetReceiptTx(ctx, tx, cached, current.AssetSet.ProjectID, command.AssetSetID, command.ExpectedCurrentVersion+1, "draft")
	}
	if current.AssetSet.Status != "sealed" || current.AssetSet.CurrentVersion != command.ExpectedCurrentVersion {
		return AssetSetSnapshot{}, domainError("ASSET_SET_VERSION_CONFLICT", "只有当前已封存版本可以重新收集。")
	}
	var downstream int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM run_input_snapshot_versions input
		WHERE run_id IN (SELECT run_id FROM runs WHERE project_id = ?)
			AND EXISTS(SELECT 1 FROM json_tree(input.payload_json) ref WHERE ref.key='asset_set_version_id' AND ref.value=?)`, current.AssetSet.ProjectID, current.Version.AssetSetVersionID).Scan(&downstream); err != nil {
		return AssetSetSnapshot{}, err
	}
	if downstream > 0 {
		return AssetSetSnapshot{}, domainError("ASSET_SET_REOPEN_CONFLICT", "该集合版本已经被生成任务引用，必须创建 Revision Run。")
	}
	return s.persistAssetSetVersionTx(ctx, tx, current, current.Members, "draft", nil, command.CommandMeta, now)
}

func (s *Store) persistAssetSetVersionTx(
	ctx context.Context,
	tx *sql.Tx,
	current AssetSetSnapshot,
	members []AssetSetMember,
	status string,
	continuation json.RawMessage,
	meta CommandMeta,
	now time.Time,
) (AssetSetSnapshot, error) {
	completeness, err := s.assetSetCompletenessTx(ctx, tx, members)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	versionID := s.newID("asv")
	version := current.AssetSet.CurrentVersion + 1
	completenessJSON, _ := json.Marshal(completeness)
	if _, err := tx.ExecContext(ctx, `UPDATE asset_set_versions SET status = 'superseded' WHERE asset_set_version_id = ?`, current.Version.AssetSetVersionID); err != nil {
		return AssetSetSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_set_versions(asset_set_version_id, asset_set_id, version, status,
			member_count, completeness_json, continuation_policy_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		versionID, current.AssetSet.AssetSetID, version, status, len(members), string(completenessJSON),
		nullableRawJSON(continuation), formatTime(now)); err != nil {
		return AssetSetSnapshot{}, err
	}
	for _, member := range members {
		memberID := s.newID("asm")
		candidateJSON, _ := json.Marshal(member.FilenameCandidate)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO asset_set_members(asset_set_member_id, asset_set_version_id, asset_id,
				episode_order, episode_no, episode_label, episode_source,
				filename_candidate_json, included, exclusion_reason)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			memberID, versionID, member.AssetID, member.EpisodeOrder, nullableInt(member.EpisodeNo),
			nullableString(member.EpisodeLabel), member.EpisodeSource, string(candidateJSON),
			boolInt(member.Included), nullableString(member.ExclusionReason)); err != nil {
			return AssetSetSnapshot{}, err
		}
	}
	setStatus := "collecting"
	var sealedAt any
	if status == "sealed" {
		setStatus = "sealed"
		sealedAt = formatTime(now)
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE asset_sets SET status = ?, current_version_id = ?, current_version = ?,
			updated_at = ?, sealed_at = ? WHERE asset_set_id = ? AND current_version = ?`,
		"素材批次版本已经变化。",
		setStatus, versionID, version, formatTime(now), sealedAt,
		current.AssetSet.AssetSetID, current.AssetSet.CurrentVersion); err != nil {
		return AssetSetSnapshot{}, err
	}
	eventType := "asset_set.version_created"
	if status == "sealed" {
		eventType = "asset_set.sealed"
	} else if current.AssetSet.Status == "sealed" {
		eventType = "asset_set.reopened"
	}
	if _, err := s.appendEvent(ctx, tx, current.AssetSet.ProjectID, nil, nil,
		eventType, "asset_set", current.AssetSet.AssetSetID,
		map[string]any{"asset_set_version_id": versionID, "version": version}); err != nil {
		return AssetSetSnapshot{}, err
	}
	snapshot, err := getAssetSetSnapshotTx(ctx, tx, current.AssetSet.AssetSetID, "")
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := completeIdempotency(ctx, tx, meta, snapshot, now); err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssetSetSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) assetSetCompletenessTx(ctx context.Context, tx *sql.Tx, members []AssetSetMember) (AssetSetCompleteness, error) {
	result := AssetSetCompleteness{DuplicateEpisodeNumbers: []int{}, MissingEpisodeNumbers: []int{}, FailedAssetIDs: []string{}, OrderConfirmed: true}
	counts := map[int]int{}
	maxEpisode := 0
	for index, member := range members {
		if member.EpisodeOrder != index+1 {
			result.OrderConfirmed = false
		}
		if !member.Included {
			continue
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM assets WHERE asset_id = ?`, member.AssetID).Scan(&status); err != nil {
			return result, err
		}
		if status != "available" {
			result.FailedAssetIDs = append(result.FailedAssetIDs, member.AssetID)
		}
		if member.EpisodeNo == nil {
			result.UnrecognizedCount++
			continue
		}
		if *member.EpisodeNo < 1 || *member.EpisodeNo > MaxAssetSetEpisodeNo {
			return result, domainError("REQUEST_VALIDATION_FAILED", "集号必须为 1 到 10000 的整数。")
		}
		result.RecognizedEpisodeCount++
		counts[*member.EpisodeNo]++
		if *member.EpisodeNo > maxEpisode {
			maxEpisode = *member.EpisodeNo
		}
	}
	for episodeNo, count := range counts {
		if count > 1 {
			result.DuplicateEpisodeNumbers = append(result.DuplicateEpisodeNumbers, episodeNo)
		}
	}
	for episodeNo := 1; episodeNo <= maxEpisode; episodeNo++ {
		if counts[episodeNo] == 0 {
			result.MissingEpisodeNumbers = append(result.MissingEpisodeNumbers, episodeNo)
		}
	}
	sort.Ints(result.DuplicateEpisodeNumbers)
	sort.Strings(result.FailedAssetIDs)
	return result, nil
}

type assetSetQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getAssetSetSnapshotTx(ctx context.Context, query assetSetQuery, assetSetID, versionID string) (AssetSetSnapshot, error) {
	var snapshot AssetSetSnapshot
	var createdBy, sealedAt sql.NullString
	var createdAt, updatedAt, versionCreatedAt string
	where := "aset.asset_set_id = ? AND asv.asset_set_version_id = aset.current_version_id"
	arguments := []any{assetSetID}
	if versionID != "" {
		where = "asv.asset_set_version_id = ?"
		arguments = []any{versionID}
		if assetSetID != "" {
			where += " AND aset.asset_set_id = ?"
			arguments = append(arguments, assetSetID)
		}
	}
	var completenessJSON string
	var continuationJSON sql.NullString
	err := query.QueryRowContext(ctx, `
		SELECT aset.asset_set_id, aset.project_id, aset.purpose, aset.display_name, aset.status,
			aset.current_version_id, aset.current_version, aset.created_by_message_id,
			aset.created_at, aset.updated_at, aset.sealed_at,
			asv.asset_set_version_id, asv.version, asv.status, asv.member_count,
			asv.completeness_json, asv.continuation_policy_json, asv.created_at
		FROM asset_sets aset JOIN asset_set_versions asv ON asv.asset_set_id = aset.asset_set_id
		WHERE `+where, arguments...).Scan(
		&snapshot.AssetSet.AssetSetID, &snapshot.AssetSet.ProjectID, &snapshot.AssetSet.Purpose,
		&snapshot.AssetSet.DisplayName, &snapshot.AssetSet.Status, &snapshot.AssetSet.CurrentVersionID,
		&snapshot.AssetSet.CurrentVersion, &createdBy, &createdAt, &updatedAt, &sealedAt,
		&snapshot.Version.AssetSetVersionID, &snapshot.Version.Version, &snapshot.Version.Status,
		&snapshot.Version.MemberCount, &completenessJSON, &continuationJSON, &versionCreatedAt,
	)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	snapshot.AssetSet.CreatedByMessageID = stringPointer(createdBy)
	snapshot.AssetSet.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	snapshot.AssetSet.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	snapshot.AssetSet.SealedAt, err = optionalTime(sealedAt)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	snapshot.Version.AssetSetID = snapshot.AssetSet.AssetSetID
	snapshot.Version.CreatedAt, err = parseTime(versionCreatedAt)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if err := json.Unmarshal([]byte(completenessJSON), &snapshot.Version.Completeness); err != nil {
		return AssetSetSnapshot{}, err
	}
	if continuationJSON.Valid {
		snapshot.Version.ContinuationPolicy = json.RawMessage(continuationJSON.String)
	}
	rows, err := query.QueryContext(ctx, `
		SELECT asset_set_member_id, asset_id, episode_order, episode_no, episode_label,
			episode_source, filename_candidate_json, included, exclusion_reason
		FROM asset_set_members WHERE asset_set_version_id = ?
		ORDER BY episode_order ASC, asset_set_member_id ASC`, snapshot.Version.AssetSetVersionID)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var member AssetSetMember
		var episodeNo sql.NullInt64
		var episodeLabel, exclusionReason sql.NullString
		var candidateJSON string
		var included int
		if err := rows.Scan(&member.AssetSetMemberID, &member.AssetID, &member.EpisodeOrder,
			&episodeNo, &episodeLabel, &member.EpisodeSource, &candidateJSON, &included, &exclusionReason); err != nil {
			return AssetSetSnapshot{}, err
		}
		member.AssetSetVersionID = snapshot.Version.AssetSetVersionID
		if episodeNo.Valid {
			value := int(episodeNo.Int64)
			member.EpisodeNo = &value
		}
		member.EpisodeLabel = stringPointer(episodeLabel)
		member.ExclusionReason = stringPointer(exclusionReason)
		member.Included = included != 0
		if err := json.Unmarshal([]byte(candidateJSON), &member.FilenameCandidate); err != nil {
			return AssetSetSnapshot{}, err
		}
		snapshot.Members = append(snapshot.Members, member)
	}
	if snapshot.Members == nil {
		snapshot.Members = []AssetSetMember{}
	}
	return snapshot, rows.Err()
}

func normalizeAssetSetNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domainError("ASSET_SET_NOT_FOUND", "输入集合不存在。")
	}
	return err
}

func nullableRawJSON(value json.RawMessage) any {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	return string(value)
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
