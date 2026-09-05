package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MFARecord mirrors the user_mfa table while keeping secrets out of the HTTP
// domain model. Backup codes are stored as hashes by the Go implementation.
type MFARecord struct {
	ID          string
	UserID      string
	WorkspaceID string
	Method      string
	Secret      *string
	IsEnabled   bool
	BackupCodes []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (repository *Repository) MFAByUser(ctx context.Context, userID, workspaceID string) (MFARecord, error) {
	var value MFARecord
	err := repository.db.QueryRow(ctx, `
SELECT id::text, user_id::text, workspace_id::text, method, secret,
       COALESCE(is_enabled, false), COALESCE(backup_codes, '{}'::text[]),
       created_at, updated_at
FROM user_mfa
WHERE user_id = $1 AND workspace_id = $2`, userID, workspaceID).Scan(
		&value.ID, &value.UserID, &value.WorkspaceID, &value.Method, &value.Secret,
		&value.IsEnabled, &value.BackupCodes, &value.CreatedAt, &value.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MFARecord{}, ErrNotFound
	}
	return value, err
}

func (repository *Repository) UpsertMFASetup(ctx context.Context, userID, workspaceID, method, secret string) (MFARecord, error) {
	_, err := repository.db.Exec(ctx, `
INSERT INTO user_mfa (id, user_id, workspace_id, method, secret, is_enabled, backup_codes)
VALUES (gen_uuid_v7(), $1, $2, $3, $4, false, '{}'::text[])
ON CONFLICT (user_id) DO UPDATE SET
  workspace_id = EXCLUDED.workspace_id, method = EXCLUDED.method,
  secret = EXCLUDED.secret, is_enabled = false, backup_codes = '{}'::text[],
  updated_at = now()`, userID, workspaceID, method, secret)
	if err != nil {
		return MFARecord{}, err
	}
	return repository.MFAByUser(ctx, userID, workspaceID)
}

func (repository *Repository) EnableMFA(ctx context.Context, userID, workspaceID string, backupCodes []string) error {
	_, err := repository.db.Exec(ctx, `
UPDATE user_mfa
SET is_enabled = true, backup_codes = $3, updated_at = now()
WHERE user_id = $1 AND workspace_id = $2`, userID, workspaceID, backupCodes)
	return err
}

func (repository *Repository) DisableMFA(ctx context.Context, userID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `
UPDATE user_mfa
SET is_enabled = false, secret = NULL, backup_codes = '{}'::text[], updated_at = now()
WHERE user_id = $1 AND workspace_id = $2`, userID, workspaceID)
	return err
}

func (repository *Repository) ReplaceMFABackupCodes(ctx context.Context, userID, workspaceID string, backupCodes []string) error {
	_, err := repository.db.Exec(ctx, `
UPDATE user_mfa
SET backup_codes = $3, updated_at = now()
WHERE user_id = $1 AND workspace_id = $2 AND is_enabled = true`, userID, workspaceID, backupCodes)
	return err
}

// ConsumeMFABackupCode atomically removes a matching code, preventing reuse
// when two login attempts arrive at the same time.
func (repository *Repository) ConsumeMFABackupCode(ctx context.Context, userID, workspaceID, code string) (bool, error) {
	var consumed bool
	err := repository.db.QueryRow(ctx, `
WITH current AS (
  SELECT backup_codes FROM user_mfa
  WHERE user_id = $1 AND workspace_id = $2 AND is_enabled = true
), matched AS (
  SELECT array_agg(item) AS remaining
  FROM current, unnest(current.backup_codes) AS item
  WHERE item <> $3
)
UPDATE user_mfa
SET backup_codes = COALESCE(matched.remaining, '{}'::text[]), updated_at = now()
FROM matched
WHERE user_id = $1 AND workspace_id = $2 AND is_enabled = true
  AND EXISTS (SELECT 1 FROM current WHERE $3 = ANY(current.backup_codes))
RETURNING true`, userID, workspaceID, code).Scan(&consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return consumed, err
}
