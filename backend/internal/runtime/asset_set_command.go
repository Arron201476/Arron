package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"content-agent/backend/internal/identity"
)

func authorizeAssetSetCommandTx(ctx context.Context, tx *sql.Tx, projectID, scope string) error {
	if _, active := AgentActivityFromContext(ctx); active {
		return domainError("ROLE_FORBIDDEN", "素材批次修改需要用户提交。")
	}
	if principal, present := identity.FromContext(ctx); present && !principal.ValidUser() {
		return domainError("ROLE_FORBIDDEN", "素材批次修改需要有效用户身份。")
	}
	return authorizeArtifactCommandTx(ctx, tx, projectID, scope)
}

func (s *Store) readAssetSet(ctx context.Context, setID, versionID string) (AssetSetSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := getAssetSetSnapshotTx(ctx, tx, setID, versionID)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, snapshot.AssetSet.ProjectID, false); err != nil {
		return AssetSetSnapshot{}, err
	}
	return snapshot, nil
}

func decodeAssetSetReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, projectID, setID string, version int, status string) (AssetSetSnapshot, error) {
	receipt, err := decodeIdempotentResult[AssetSetSnapshot](cached)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	mismatch := func() (AssetSetSnapshot, error) {
		return AssetSetSnapshot{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识的回执不属于当前素材批次操作。")
	}
	set, saved := receipt.AssetSet, receipt.Version
	setStatus := "collecting"
	if status == "sealed" {
		setStatus = "sealed"
	}
	if set.ProjectID != projectID || setID != "" && set.AssetSetID != setID || set.Status != setStatus || saved.AssetSetID != set.AssetSetID || saved.Version != version || saved.Status != status || set.CurrentVersion != version || set.CurrentVersionID != saved.AssetSetVersionID {
		return mismatch()
	}
	stored, err := getAssetSetSnapshotTx(ctx, tx, "", saved.AssetSetVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return mismatch()
	}
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if stored.AssetSet.AssetSetID != set.AssetSetID || stored.AssetSet.ProjectID != projectID || stored.AssetSet.Purpose != set.Purpose || stored.AssetSet.DisplayName != set.DisplayName || !sameOptionalID(stored.AssetSet.CreatedByMessageID, set.CreatedByMessageID) {
		return mismatch()
	}
	// A later version supersedes this one, but does not change its committed content.
	stored.Version.Status = saved.Status
	storedJSON, err := json.Marshal(stored.Version)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	savedJSON, err := json.Marshal(saved)
	if err != nil {
		return AssetSetSnapshot{}, err
	}
	if !bytes.Equal(storedJSON, savedJSON) || !reflect.DeepEqual(stored.Members, receipt.Members) {
		return mismatch()
	}
	return receipt, nil
}
