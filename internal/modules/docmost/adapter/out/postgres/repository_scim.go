package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

type SCIMToken struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	TokenLastFour string              `json:"tokenLastFour"`
	LastUsedAt    *time.Time          `json:"lastUsedAt"`
	IsEnabled     bool                `json:"isEnabled"`
	CreatorID     *string             `json:"creatorId"`
	WorkspaceID   string              `json:"workspaceId"`
	CreatedAt     time.Time           `json:"createdAt"`
	UpdatedAt     time.Time           `json:"updatedAt"`
	Creator       *domain.UserSummary `json:"creator,omitempty"`
}

func (repository *Repository) SCIMTokens(ctx context.Context, workspaceID, cursor string, limit int) (domain.Pagination[SCIMToken], error) {
	limit = normalizeLimit(limit)
	rows, err := repository.db.Query(ctx, `
SELECT t.id::text, t.name, t.token_last_four, t.last_used_at, t.is_enabled,
       t.creator_id::text, t.workspace_id::text, t.created_at, t.updated_at,
       u.id::text, u.name, u.avatar_url
FROM scim_tokens t
LEFT JOIN users u ON u.id = t.creator_id
WHERE t.workspace_id = $1 AND t.deleted_at IS NULL
  AND ($2 = '' OR t.id::text > $2)
ORDER BY t.id
LIMIT $3`, workspaceID, cursor, limit+1)
	if err != nil {
		return domain.Pagination[SCIMToken]{}, err
	}
	defer rows.Close()
	items := make([]SCIMToken, 0, limit)
	for rows.Next() {
		var item SCIMToken
		var creatorID, creatorName, creatorAvatar *string
		if err := rows.Scan(&item.ID, &item.Name, &item.TokenLastFour, &item.LastUsedAt, &item.IsEnabled,
			&item.CreatorID, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt,
			&creatorID, &creatorName, &creatorAvatar); err != nil {
			return domain.Pagination[SCIMToken]{}, err
		}
		if creatorID != nil {
			item.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[SCIMToken]{}, err
	}
	hasNext := len(items) > limit
	if hasNext {
		items = items[:limit]
	}
	result := page(items, limit)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = cursor != ""
	if hasNext && len(items) > 0 {
		next := items[len(items)-1].ID
		result.Meta.NextCursor = &next
	}
	return result, nil
}

func (repository *Repository) CreateSCIMToken(ctx context.Context, workspaceID, creatorID, name, tokenHash, tokenLastFour string) (SCIMToken, error) {
	id, err := newUUID()
	if err != nil {
		return SCIMToken{}, err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO scim_tokens (id, name, token_hash, token_last_four, creator_id, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6)`, id, name, tokenHash, tokenLastFour, creatorID, workspaceID)
	if err != nil {
		return SCIMToken{}, err
	}
	return repository.SCIMTokenByID(ctx, id, workspaceID)
}

func (repository *Repository) SCIMTokenByID(ctx context.Context, id, workspaceID string) (SCIMToken, error) {
	var item SCIMToken
	var creatorID, creatorName, creatorAvatar *string
	err := repository.db.QueryRow(ctx, `
SELECT t.id::text, t.name, t.token_last_four, t.last_used_at, t.is_enabled,
       t.creator_id::text, t.workspace_id::text, t.created_at, t.updated_at,
       u.id::text, u.name, u.avatar_url
FROM scim_tokens t
LEFT JOIN users u ON u.id = t.creator_id
WHERE t.id = $1 AND t.workspace_id = $2 AND t.deleted_at IS NULL`, id, workspaceID).Scan(
		&item.ID, &item.Name, &item.TokenLastFour, &item.LastUsedAt, &item.IsEnabled,
		&item.CreatorID, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt,
		&creatorID, &creatorName, &creatorAvatar)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCIMToken{}, ErrNotFound
	}
	if err != nil {
		return SCIMToken{}, err
	}
	if creatorID != nil {
		item.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
	}
	return item, nil
}

func (repository *Repository) UpdateSCIMToken(ctx context.Context, id, workspaceID, name string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE scim_tokens SET name = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID, name)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) RevokeSCIMToken(ctx context.Context, id, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE scim_tokens SET is_enabled = false, deleted_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) SCIMTokenByHash(ctx context.Context, token string) (SCIMToken, error) {
	hash := sha256.Sum256([]byte(token))
	var item SCIMToken
	err := repository.db.QueryRow(ctx, `
SELECT id::text, name, token_last_four, last_used_at, is_enabled,
       creator_id::text, workspace_id::text, created_at, updated_at
FROM scim_tokens
WHERE token_hash = $1 AND is_enabled = true AND deleted_at IS NULL`, hex.EncodeToString(hash[:])).Scan(
		&item.ID, &item.Name, &item.TokenLastFour, &item.LastUsedAt, &item.IsEnabled,
		&item.CreatorID, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCIMToken{}, ErrNotFound
	}
	return item, err
}

func (repository *Repository) TouchSCIMToken(ctx context.Context, id, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE scim_tokens SET last_used_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID)
	return err
}
