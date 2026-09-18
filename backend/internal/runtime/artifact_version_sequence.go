package runtime

import (
	"context"
	"database/sql"
)

func nextArtifactVersionTx(ctx context.Context, tx *sql.Tx, artifactID string) (int, error) {
	var nextVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM artifact_versions
		WHERE artifact_id = ?`, artifactID).Scan(&nextVersion); err != nil {
		return 0, err
	}
	return nextVersion, nil
}
