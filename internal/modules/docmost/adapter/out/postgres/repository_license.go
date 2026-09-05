package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (repository *Repository) LicenseKey(ctx context.Context, workspaceID string) (string, error) {
	var key string
	err := repository.db.QueryRow(ctx, `
SELECT COALESCE(license_key, '') FROM workspaces
WHERE id = $1 AND deleted_at IS NULL`, workspaceID).Scan(&key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return key, nil
}

func (repository *Repository) SetLicenseKey(ctx context.Context, workspaceID, key string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE workspaces SET license_key = NULLIF($2, ''), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`, workspaceID, key)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) RemoveLicenseKey(ctx context.Context, workspaceID string) error {
	return repository.SetLicenseKey(ctx, workspaceID, "")
}
