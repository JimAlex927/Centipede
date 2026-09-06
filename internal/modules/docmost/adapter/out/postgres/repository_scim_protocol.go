package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SCIMUserResource is the small, provider-neutral projection exposed by the
// SCIM protocol. It intentionally does not contain password or session data.
type SCIMUserResource struct {
	ID          string
	ExternalID  *string
	UserName    string
	DisplayName string
	GivenName   *string
	FamilyName  *string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SCIMUserInput struct {
	ExternalID  *string
	UserName    string
	DisplayName *string
	GivenName   *string
	FamilyName  *string
	Active      *bool
}

type SCIMGroupResource struct {
	ID          string
	ExternalID  *string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Members     []SCIMGroupMember
}

type SCIMGroupMember struct {
	Value   string
	Display string
}

type SCIMGroupInput struct {
	ExternalID  *string
	DisplayName string
	Members     []string
}

func (repository *Repository) SCIMUsers(ctx context.Context, workspaceID, filter string, startIndex, count int) ([]SCIMUserResource, int, error) {
	startIndex, count = normalizeSCIMPage(startIndex, count)
	filter = strings.TrimSpace(filter)
	var total int
	if err := repository.db.QueryRow(ctx, `
SELECT count(*) FROM users
WHERE workspace_id = $1 AND deleted_at IS NULL
  AND ($2 = '' OR lower(email) = lower($2) OR scim_external_id = $2)`, workspaceID, filter).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := repository.db.Query(ctx, `
SELECT id::text, NULLIF(scim_external_id, ''), email, COALESCE(name, ''),
       deactivated_at IS NULL, created_at, updated_at
FROM users
WHERE workspace_id = $1 AND deleted_at IS NULL
  AND ($2 = '' OR lower(email) = lower($2) OR scim_external_id = $2)
ORDER BY id
LIMIT $3 OFFSET $4`, workspaceID, filter, count, startIndex-1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]SCIMUserResource, 0, count)
	for rows.Next() {
		var item SCIMUserResource
		if err := rows.Scan(&item.ID, &item.ExternalID, &item.UserName, &item.DisplayName, &item.Active, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (repository *Repository) SCIMUserByIDOrExternalID(ctx context.Context, key, workspaceID string) (SCIMUserResource, error) {
	var item SCIMUserResource
	err := repository.db.QueryRow(ctx, `
SELECT id::text, NULLIF(scim_external_id, ''), email, COALESCE(name, ''),
       deactivated_at IS NULL, created_at, updated_at
FROM users
WHERE workspace_id = $2 AND deleted_at IS NULL
  AND (id::text = $1 OR scim_external_id = $1)
LIMIT 1`, key, workspaceID).Scan(&item.ID, &item.ExternalID, &item.UserName, &item.DisplayName, &item.Active, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCIMUserResource{}, ErrNotFound
	}
	return item, err
}

func (repository *Repository) CreateSCIMUser(ctx context.Context, workspaceID string, input SCIMUserInput, seatLimit int64) (SCIMUserResource, error) {
	id, err := newUUID()
	if err != nil {
		return SCIMUserResource{}, err
	}
	name := strings.TrimSpace(input.UserName)
	if input.DisplayName != nil && strings.TrimSpace(*input.DisplayName) != "" {
		name = strings.TrimSpace(*input.DisplayName)
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	var deactivatedAt *time.Time
	if !active {
		value := time.Now().UTC()
		deactivatedAt = &value
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return SCIMUserResource{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if active && seatLimit > 0 {
		if err = lockWorkspaceSeat(ctx, tx, workspaceID, seatLimit); err != nil {
			return SCIMUserResource{}, err
		}
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO users (id, name, email, email_verified_at, password, role, workspace_id, settings, has_generated_password, scim_external_id, deactivated_at)
VALUES ($1, $2, lower($3), now(), NULL, 'member', $4, '{}'::jsonb, true, $5, $6)`, id, name, strings.TrimSpace(input.UserName), workspaceID, input.ExternalID, deactivatedAt); err != nil {
		return SCIMUserResource{}, err
	}
	// Newly provisioned users belong to the default Everyone group, matching
	// the membership established by normal SSO provisioning.
	membershipID, err := newUUID()
	if err != nil {
		return SCIMUserResource{}, err
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO group_users (id, user_id, group_id)
SELECT $1, $2, id FROM groups
WHERE workspace_id = $3 AND is_default = true AND deleted_at IS NULL
ON CONFLICT (group_id, user_id) DO NOTHING`, membershipID, id, workspaceID); err != nil {
		return SCIMUserResource{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SCIMUserResource{}, err
	}
	return repository.SCIMUserByIDOrExternalID(ctx, id, workspaceID)
}

func (repository *Repository) UpdateSCIMUser(ctx context.Context, key, workspaceID string, input SCIMUserInput, seatLimit int64) (SCIMUserResource, error) {
	name := ""
	if input.DisplayName != nil {
		name = strings.TrimSpace(*input.DisplayName)
	}
	active := input.Active
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return SCIMUserResource{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Reactivating a previously deactivated SCIM user consumes a seat just
	// like creating a new user. Locking the workspace before checking the
	// current state serializes this transition with SCIM/SSO/invite creation
	// and prevents concurrent reactivations from exceeding the license.
	if active != nil && *active {
		var wasActive bool
		err = tx.QueryRow(ctx, `
SELECT deactivated_at IS NULL
FROM users
WHERE workspace_id = $2 AND deleted_at IS NULL AND (id::text = $1 OR scim_external_id = $1)
LIMIT 1`, key, workspaceID).Scan(&wasActive)
		if errors.Is(err, pgx.ErrNoRows) {
			return SCIMUserResource{}, ErrNotFound
		}
		if err != nil {
			return SCIMUserResource{}, err
		}
		if !wasActive {
			if err = lockWorkspaceSeat(ctx, tx, workspaceID, seatLimit); err != nil {
				return SCIMUserResource{}, err
			}
		}
	}

	result, err := tx.Exec(ctx, `
UPDATE users SET
  email = COALESCE(NULLIF(lower($3), ''), email),
  name = COALESCE(NULLIF($4, ''), name),
  scim_external_id = COALESCE($5, scim_external_id),
  deactivated_at = CASE WHEN $6::boolean IS NULL THEN deactivated_at WHEN $6::boolean THEN NULL ELSE COALESCE(deactivated_at, now()) END,
  updated_at = now()
WHERE workspace_id = $2 AND deleted_at IS NULL AND (id::text = $1 OR scim_external_id = $1)`, key, workspaceID, input.UserName, name, input.ExternalID, active)
	if err != nil {
		return SCIMUserResource{}, err
	}
	if result.RowsAffected() == 0 {
		return SCIMUserResource{}, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return SCIMUserResource{}, err
	}
	return repository.SCIMUserByIDOrExternalID(ctx, key, workspaceID)
}

func (repository *Repository) DeleteSCIMUser(ctx context.Context, key, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE users SET deactivated_at = COALESCE(deactivated_at, now()), updated_at = now()
WHERE workspace_id = $2 AND deleted_at IS NULL AND (id::text = $1 OR scim_external_id = $1)`, key, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) SCIMGroups(ctx context.Context, workspaceID, filter string, startIndex, count int) ([]SCIMGroupResource, int, error) {
	startIndex, count = normalizeSCIMPage(startIndex, count)
	filter = strings.TrimSpace(filter)
	var total int
	if err := repository.db.QueryRow(ctx, `
SELECT count(*) FROM groups
WHERE workspace_id = $1 AND deleted_at IS NULL AND scim_external_id IS NOT NULL
  AND ($2 = '' OR lower(name) = lower($2) OR scim_external_id = $2)`, workspaceID, filter).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := repository.db.Query(ctx, `
SELECT id::text, NULLIF(scim_external_id, ''), name, created_at, updated_at
FROM groups
WHERE workspace_id = $1 AND deleted_at IS NULL AND scim_external_id IS NOT NULL
  AND ($2 = '' OR lower(name) = lower($2) OR scim_external_id = $2)
ORDER BY id LIMIT $3 OFFSET $4`, workspaceID, filter, count, startIndex-1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]SCIMGroupResource, 0, count)
	for rows.Next() {
		var item SCIMGroupResource
		if err := rows.Scan(&item.ID, &item.ExternalID, &item.DisplayName, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		item.Members, err = repository.SCIMGroupMembers(ctx, item.ID, workspaceID)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (repository *Repository) SCIMGroupByIDOrExternalID(ctx context.Context, key, workspaceID string) (SCIMGroupResource, error) {
	var item SCIMGroupResource
	err := repository.db.QueryRow(ctx, `
SELECT id::text, NULLIF(scim_external_id, ''), name, created_at, updated_at
FROM groups
WHERE workspace_id = $2 AND deleted_at IS NULL AND scim_external_id IS NOT NULL
  AND (id::text = $1 OR scim_external_id = $1)
LIMIT 1`, key, workspaceID).Scan(&item.ID, &item.ExternalID, &item.DisplayName, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCIMGroupResource{}, ErrNotFound
	}
	if err != nil {
		return SCIMGroupResource{}, err
	}
	item.Members, err = repository.SCIMGroupMembers(ctx, item.ID, workspaceID)
	return item, err
}

func (repository *Repository) SCIMGroupMembers(ctx context.Context, groupID, workspaceID string) ([]SCIMGroupMember, error) {
	rows, err := repository.db.Query(ctx, `
	SELECT u.id::text, COALESCE(u.name, '')
FROM group_users gu JOIN users u ON u.id = gu.user_id
WHERE gu.group_id = $1 AND u.workspace_id = $2 AND u.deleted_at IS NULL
ORDER BY u.id`, groupID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SCIMGroupMember, 0)
	for rows.Next() {
		var item SCIMGroupMember
		if err := rows.Scan(&item.Value, &item.Display); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) CreateSCIMGroup(ctx context.Context, workspaceID string, creatorID *string, input SCIMGroupInput) (SCIMGroupResource, error) {
	id, err := newUUID()
	if err != nil {
		return SCIMGroupResource{}, err
	}
	externalID := input.ExternalID
	if externalID == nil || strings.TrimSpace(*externalID) == "" {
		externalID = &id
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return SCIMGroupResource{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `
INSERT INTO groups (id, name, is_default, is_external, scim_external_id, creator_id, workspace_id)
		VALUES ($1, $2, false, true, $3, $4, $5)`, id, strings.TrimSpace(input.DisplayName), externalID, creatorID, workspaceID); err != nil {
		return SCIMGroupResource{}, err
	}
	if err = replaceSCIMGroupMembers(ctx, tx, id, workspaceID, input.Members); err != nil {
		return SCIMGroupResource{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SCIMGroupResource{}, err
	}
	return repository.SCIMGroupByIDOrExternalID(ctx, id, workspaceID)
}

func (repository *Repository) UpdateSCIMGroup(ctx context.Context, key, workspaceID string, input SCIMGroupInput, replaceMembers bool) (SCIMGroupResource, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return SCIMGroupResource{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err = tx.QueryRow(ctx, `
SELECT id::text FROM groups
WHERE workspace_id = $2 AND deleted_at IS NULL AND scim_external_id IS NOT NULL
  AND (id::text = $1 OR scim_external_id = $1)`, key, workspaceID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return SCIMGroupResource{}, ErrNotFound
	} else if err != nil {
		return SCIMGroupResource{}, err
	}
	if _, err = tx.Exec(ctx, `
UPDATE groups SET name = COALESCE(NULLIF($3, ''), name), scim_external_id = COALESCE($4, scim_external_id), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID, input.DisplayName, input.ExternalID); err != nil {
		return SCIMGroupResource{}, err
	}
	if replaceMembers {
		if err = replaceSCIMGroupMembers(ctx, tx, id, workspaceID, input.Members); err != nil {
			return SCIMGroupResource{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return SCIMGroupResource{}, err
	}
	return repository.SCIMGroupByIDOrExternalID(ctx, id, workspaceID)
}

func (repository *Repository) DeleteSCIMGroup(ctx context.Context, key, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `
UPDATE groups SET deleted_at = now(), updated_at = now()
WHERE workspace_id = $2 AND is_default = false AND deleted_at IS NULL
  AND (id::text = $1 OR scim_external_id = $1)`, key, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func replaceSCIMGroupMembers(ctx context.Context, tx pgx.Tx, groupID, workspaceID string, members []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM group_users WHERE group_id = $1`, groupID); err != nil {
		return err
	}
	for _, member := range members {
		memberID, err := newUUID()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `
INSERT INTO group_users (id, group_id, user_id)
SELECT $1, $2, u.id FROM users u
WHERE u.workspace_id = $3 AND u.deleted_at IS NULL
  AND (u.id::text = $4 OR u.scim_external_id = $4)
ON CONFLICT (group_id, user_id) DO NOTHING`, memberID, groupID, workspaceID, strings.TrimSpace(member)); err != nil {
			return err
		}
	}
	return nil
}

func normalizeSCIMPage(startIndex, count int) (int, int) {
	if startIndex < 1 {
		startIndex = 1
	}
	if count <= 0 || count > 1000 {
		count = 100
	}
	return startIndex, count
}
