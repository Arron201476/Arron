package runtime

import (
	"context"
	"database/sql"
	"errors"
)

type NativeWorkspaceRecovery struct {
	Manifest NativeWorkspaceManifest `json:"manifest"`
	Snapshot struct {
		Version int    `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"snapshot"`
}

// ReadRecovery returns authoritative references, not an instruction to replay
// initialization or an implicit permission to discard an uncertain live tree.
func (manager *NativeWorkspaceManager) ReadRecovery(ctx context.Context, access NativeWorkspaceAccess) (NativeWorkspaceRecovery, error) {
	var result NativeWorkspaceRecovery
	if !manager.requirePolicy {
		return result, domainError("NATIVE_WORKSPACE_POLICY_REQUIRED", "Recovery requires current workspace policy.")
	}
	tx, err := manager.store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	lease, err := manager.store.nativeWorkspaceAccessTx(ctx, tx, access)
	if err != nil {
		return result, err
	}
	if err := manager.validatePolicyTx(ctx, tx, lease.workspaceID); err != nil {
		return result, err
	}
	manifest, err := readNativeManifestTx(ctx, tx, access.SessionID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (manifest.status != "completed" || lease.SnapshotVersion < 1) {
		return result, domainError("NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", "Recovery requires sealed resources and a confirmed snapshot.")
	}
	if err != nil {
		return result, err
	}
	files, err := nativeManifestFiles(manifest)
	if err != nil {
		return result, err
	}
	result.Manifest = NativeWorkspaceManifest{SessionID: access.SessionID, ManifestHash: manifest.hash, Files: files}
	result.Snapshot.Version = lease.SnapshotVersion
	if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM native_workspace_snapshots WHERE session_id=? AND version=?`, access.SessionID, lease.SnapshotVersion).Scan(&result.Snapshot.SHA256); err != nil {
		return result, err
	}
	// Reuse the full archive/hash/entry validation before advertising a restore
	// reference. No engine is contacted and no snapshot generation advances.
	if _, err := readNativeWorkspaceSnapshotTx(ctx, tx, access.SessionID, lease.SnapshotVersion, result.Snapshot.SHA256); err != nil {
		return result, err
	}
	return result, nil
}
