package postgres

import (
	"context"
	"time"

	"centipede/internal/modules/docmost/domain"
	"github.com/jackc/pgx/v5"
)

type APIKey struct {
	ID          string
	Name        string
	CreatorID   string
	WorkspaceID string
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Creator     domain.UserSummary
}

func (repository *Repository) APIKeys(ctx context.Context, workspaceID, userID string, adminView bool, cursor string, limit int) (domain.Pagination[APIKey], error) {
	limit = normalizeLimit(limit)
	rows, err := repository.db.Query(ctx, `
SELECT k.id::text, COALESCE(k.name, ''), k.creator_id::text, k.workspace_id::text,
       k.expires_at, k.last_used_at, k.created_at, k.updated_at,
       u.id::text, u.name, u.avatar_url
FROM api_keys k
JOIN users u ON u.id = k.creator_id
WHERE k.workspace_id = $1 AND k.deleted_at IS NULL
  AND ($2 OR k.creator_id = $3)
  AND ($4 = '' OR k.id::text > $4)
ORDER BY k.id
LIMIT $5`, workspaceID, adminView, userID, cursor, limit+1)
	if err != nil {
		return domain.Pagination[APIKey]{}, err
	}
	defer rows.Close()
	items := make([]APIKey, 0, limit)
	for rows.Next() {
		var item APIKey
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatorID, &item.WorkspaceID, &item.ExpiresAt, &item.LastUsedAt, &item.CreatedAt, &item.UpdatedAt, &item.Creator.ID, &item.Creator.Name, &item.Creator.AvatarURL); err != nil {
			return domain.Pagination[APIKey]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[APIKey]{}, err
	}
	hasNext := len(items) > limit
	if hasNext {
		items = items[:limit]
	}
	result := page(items, limit)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = cursor != ""
	if hasNext {
		next := items[len(items)-1].ID
		result.Meta.NextCursor = &next
	}
	return result, nil
}

func (repository *Repository) CreateAPIKey(ctx context.Context, workspaceID, creatorID string, name string, expiresAt *time.Time) (APIKey, error) {
	id, err := newUUID()
	if err != nil {
		return APIKey{}, err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO api_keys (id, name, creator_id, workspace_id, expires_at)
VALUES ($1, $2, $3, $4, $5)`, id, name, creatorID, workspaceID, expiresAt)
	if err != nil {
		return APIKey{}, err
	}
	return repository.APIKeyByID(ctx, id, workspaceID, creatorID, false)
}

func (repository *Repository) APIKeyByID(ctx context.Context, id, workspaceID, userID string, adminView bool) (APIKey, error) {
	var item APIKey
	err := repository.db.QueryRow(ctx, `
SELECT k.id::text, COALESCE(k.name, ''), k.creator_id::text, k.workspace_id::text,
       k.expires_at, k.last_used_at, k.created_at, k.updated_at,
       u.id::text, u.name, u.avatar_url
FROM api_keys k
JOIN users u ON u.id = k.creator_id
WHERE k.id = $1 AND k.workspace_id = $2 AND k.deleted_at IS NULL
  AND ($3 OR k.creator_id = $4)`, id, workspaceID, adminView, userID).Scan(
		&item.ID, &item.Name, &item.CreatorID, &item.WorkspaceID, &item.ExpiresAt, &item.LastUsedAt,
		&item.CreatedAt, &item.UpdatedAt, &item.Creator.ID, &item.Creator.Name, &item.Creator.AvatarURL,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return APIKey{}, ErrNotFound
		}
		return APIKey{}, err
	}
	return item, nil
}

func (repository *Repository) UpdateAPIKey(ctx context.Context, id, workspaceID, userID, name string, adminView bool) (APIKey, error) {
	result, err := repository.db.Exec(ctx, `
UPDATE api_keys SET name = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  AND ($3 OR creator_id = $5)`, id, workspaceID, adminView, name, userID)
	if err != nil {
		return APIKey{}, err
	}
	if result.RowsAffected() == 0 {
		return APIKey{}, ErrNotFound
	}
	return repository.APIKeyByID(ctx, id, workspaceID, userID, adminView)
}

func (repository *Repository) RevokeAPIKey(ctx context.Context, id, workspaceID, userID string, adminView bool) error {
	result, err := repository.db.Exec(ctx, `
UPDATE api_keys SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  AND ($3 OR creator_id = $4)`, id, workspaceID, adminView, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) APIKeyActive(ctx context.Context, id, workspaceID, userID string) (bool, error) {
	var active bool
	err := repository.db.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM api_keys
  WHERE id = $1 AND workspace_id = $2 AND creator_id = $3
    AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())
)`, id, workspaceID, userID).Scan(&active)
	return active, err
}

func (repository *Repository) TouchAPIKey(ctx context.Context, id, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `
UPDATE api_keys SET last_used_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID)
	return err
}
