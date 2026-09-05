package postgres

import (
	"context"
	"errors"
	"strings"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

func (repository *Repository) PersonalSpaceByUser(ctx context.Context, workspaceID, userID string) (domain.Space, error) {
	var spaceID string
	err := repository.db.QueryRow(ctx, `
SELECT id::text
FROM spaces
WHERE workspace_id = $1 AND creator_id = $2 AND is_personal = true AND deleted_at IS NULL
LIMIT 1`, workspaceID, userID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Space{}, ErrNotFound
	}
	if err != nil {
		return domain.Space{}, err
	}
	return repository.SpaceByID(ctx, spaceID, workspaceID, userID)
}

func (repository *Repository) CreatePersonalSpace(ctx context.Context, workspaceID, userID string, name string) (domain.Space, error) {
	id, err := newUUID()
	if err != nil {
		return domain.Space{}, err
	}
	memberID, err := newUUID()
	if err != nil {
		return domain.Space{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Personal space"
	}
	slug := slugify(name) + "-" + strings.ToLower(id[:8])
	_, err = repository.db.Exec(ctx, `
INSERT INTO spaces (id, name, slug, visibility, default_role, creator_id, workspace_id, settings, is_personal)
VALUES ($1, $2, $3, 'private', 'writer', $4, $5, '{}'::jsonb, true)`, id, name, slug, userID, workspaceID)
	if err != nil {
		return domain.Space{}, err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO space_members (id, user_id, space_id, role, added_by_id)
VALUES ($1, $2, $3, 'admin', $2)`, memberID, userID, id)
	if err != nil {
		return domain.Space{}, err
	}
	return repository.SpaceByID(ctx, id, workspaceID, userID)
}
