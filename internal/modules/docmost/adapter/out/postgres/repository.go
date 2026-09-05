package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("docmost entity not found")
	ErrInvalidInput = errors.New("invalid docmost input")
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

type rowScanner interface {
	Scan(dest ...any) error
}

const workspaceColumns = `
id::text, name, description, logo, hostname, default_space_id::text,
custom_domain, COALESCE(settings, '{}'::jsonb), status,
COALESCE(enforce_sso, false), COALESCE(email_domains, '{}'::varchar[]),
COALESCE(default_role, 'member'), plan, COALESCE(enforce_mfa, false),
COALESCE(trash_retention_days, 30), created_at, updated_at`

func scanWorkspace(row rowScanner) (domain.Workspace, error) {
	var value domain.Workspace
	err := row.Scan(
		&value.ID, &value.Name, &value.Description, &value.Logo, &value.Hostname,
		&value.DefaultSpaceID, &value.CustomDomain, &value.Settings, &value.Status,
		&value.EnforceSSO, &value.EmailDomains, &value.DefaultRole, &value.Plan,
		&value.EnforceMFA, &value.TrashRetentionDays, &value.CreatedAt, &value.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, ErrNotFound
	}
	return value, err
}

func (repository *Repository) FirstWorkspace(ctx context.Context) (domain.Workspace, error) {
	return scanWorkspace(repository.db.QueryRow(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at LIMIT 1`))
}

func (repository *Repository) WorkspaceByID(ctx context.Context, id string) (domain.Workspace, error) {
	return scanWorkspace(repository.db.QueryRow(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1 AND deleted_at IS NULL`, id))
}

func (repository *Repository) OnlyWorkspace(ctx context.Context) (domain.Workspace, error) {
	rows, err := repository.db.Query(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at LIMIT 2`)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer rows.Close()
	items := make([]domain.Workspace, 0, 2)
	for rows.Next() {
		item, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			return domain.Workspace{}, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return domain.Workspace{}, err
	}
	if len(items) != 1 {
		return domain.Workspace{}, ErrNotFound
	}
	return items[0], nil
}

const userColumns = `
id::text, name, email, email_verified_at, password, avatar_url, role,
workspace_id::text, locale, timezone, COALESCE(settings, '{}'::jsonb),
last_active_at, last_login_at, deactivated_at, deleted_at, created_at,
updated_at, COALESCE(has_generated_password, false)`

func scanUser(row rowScanner) (domain.User, error) {
	var value domain.User
	err := row.Scan(
		&value.ID, &value.Name, &value.Email, &value.EmailVerifiedAt, &value.Password,
		&value.AvatarURL, &value.Role, &value.WorkspaceID, &value.Locale, &value.Timezone,
		&value.Settings, &value.LastActiveAt, &value.LastLoginAt, &value.DeactivatedAt,
		&value.DeletedAt, &value.CreatedAt, &value.UpdatedAt, &value.HasGeneratedPassword,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return value, err
}

func (repository *Repository) UserByEmail(ctx context.Context, email, workspaceID string) (domain.User, error) {
	return scanUser(repository.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1) AND workspace_id = $2 AND deleted_at IS NULL`, strings.TrimSpace(email), workspaceID))
}

func (repository *Repository) UserByID(ctx context.Context, id, workspaceID string) (domain.User, error) {
	return scanUser(repository.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID))
}

func (repository *Repository) CountActiveUsers(ctx context.Context, workspaceID string) (int64, error) {
	var count int64
	err := repository.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE workspace_id = $1 AND deleted_at IS NULL AND deactivated_at IS NULL`, workspaceID).Scan(&count)
	return count, err
}

func (repository *Repository) MarkLogin(ctx context.Context, userID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE users SET last_login_at = now(), last_active_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2`, userID, workspaceID)
	return err
}

func (repository *Repository) CreateSession(ctx context.Context, userID, workspaceID, userAgent, ipAddress string, expiresAt time.Time) (string, error) {
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO user_sessions (id, user_id, workspace_id, user_agent, ip_address, expires_at)
VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')::inet, $6)`, id, userID, workspaceID, userAgent, ipAddress, expiresAt)
	return id, err
}

func (repository *Repository) SessionActive(ctx context.Context, sessionID, userID, workspaceID string) (bool, error) {
	var active bool
	err := repository.db.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM user_sessions
  WHERE id = $1 AND user_id = $2 AND workspace_id = $3
    AND revoked_at IS NULL AND expires_at > now()
)`, sessionID, userID, workspaceID).Scan(&active)
	return active, err
}

func (repository *Repository) RevokeSession(ctx context.Context, sessionID, userID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND workspace_id = $3`, sessionID, userID, workspaceID)
	return err
}

type SetupInput struct {
	WorkspaceName string
	Name          string
	Email         string
	PasswordHash  string
}

func (repository *Repository) Setup(ctx context.Context, input SetupInput) (domain.Workspace, domain.User, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	workspaceID, err := newUUID()
	if err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	userID, err := newUUID()
	if err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	groupID, err := newUUID()
	if err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	spaceID, err := newUUID()
	if err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	groupUserID, _ := newUUID()
	userMemberID, _ := newUUID()
	groupMemberID, _ := newUUID()

	if _, err = tx.Exec(ctx, `INSERT INTO workspaces (id, name, default_role, settings) VALUES ($1, $2, 'member', '{}'::jsonb)`, workspaceID, input.WorkspaceName); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO users (id, name, email, email_verified_at, password, role, workspace_id, settings)
VALUES ($1, $2, lower($3), now(), $4, 'owner', $5, '{"preferences":{"fullPageWidth":false,"pageEditMode":"edit","editorToolbar":true}}'::jsonb)`, userID, input.Name, input.Email, input.PasswordHash, workspaceID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO groups (id, name, description, is_default, creator_id, workspace_id) VALUES ($1, 'Everyone', 'Everyone in the workspace', true, $2, $3)`, groupID, userID, workspaceID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO group_users (id, group_id, user_id) VALUES ($1, $2, $3)`, groupUserID, groupID, userID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO spaces (id, name, slug, visibility, default_role, creator_id, workspace_id, settings)
VALUES ($1, 'General', 'general', 'private', 'writer', $2, $3, '{}'::jsonb)`, spaceID, userID, workspaceID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO space_members (id, user_id, space_id, role, added_by_id) VALUES ($1, $2, $3, 'admin', $2)`, userMemberID, userID, spaceID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO space_members (id, group_id, space_id, role, added_by_id) VALUES ($1, $2, $3, 'writer', $4)`, groupMemberID, groupID, spaceID, userID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE workspaces SET default_space_id = $1 WHERE id = $2`, spaceID, workspaceID); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	workspace, err := repository.WorkspaceByID(ctx, workspaceID)
	if err != nil {
		return domain.Workspace{}, domain.User{}, err
	}
	user, err := repository.UserByID(ctx, userID, workspaceID)
	return workspace, user, err
}

type UserUpdate struct {
	Name      *string
	Locale    *string
	Timezone  *string
	AvatarURL *string
	Settings  json.RawMessage
}

func (repository *Repository) UpdateUser(ctx context.Context, userID, workspaceID string, input UserUpdate) (domain.User, error) {
	_, err := repository.db.Exec(ctx, `
UPDATE users SET
  name = COALESCE($3, name), locale = COALESCE($4, locale),
  timezone = COALESCE($5, timezone), avatar_url = COALESCE($6, avatar_url),
  settings = COALESCE($7::jsonb, settings), updated_at = now()
WHERE id = $1 AND workspace_id = $2`, userID, workspaceID, input.Name, input.Locale, input.Timezone, input.AvatarURL, nullableJSON(input.Settings))
	if err != nil {
		return domain.User{}, err
	}
	return repository.UserByID(ctx, userID, workspaceID)
}

type WorkspaceUpdate struct {
	Name        *string
	Description *string
	Logo        *string
	Hostname    *string
	Settings    json.RawMessage
}

func (repository *Repository) UpdateWorkspace(ctx context.Context, workspaceID string, input WorkspaceUpdate) (domain.Workspace, error) {
	_, err := repository.db.Exec(ctx, `
UPDATE workspaces SET
  name = COALESCE($2, name), description = COALESCE($3, description),
  logo = COALESCE($4, logo), hostname = COALESCE($5, hostname),
  settings = COALESCE($6::jsonb, settings), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`, workspaceID, input.Name, input.Description, input.Logo, input.Hostname, nullableJSON(input.Settings))
	if err != nil {
		return domain.Workspace{}, err
	}
	return repository.WorkspaceByID(ctx, workspaceID)
}

func (repository *Repository) HostnameExists(ctx context.Context, hostname string) (bool, error) {
	var exists bool
	err := repository.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces WHERE lower(hostname) = lower($1) AND deleted_at IS NULL)`, hostname).Scan(&exists)
	return exists, err
}

func (repository *Repository) WorkspaceMembers(ctx context.Context, workspaceID string, limit int) (domain.Pagination[domain.User], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+userColumns+` FROM users WHERE workspace_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT $2`, workspaceID, limit)
	if err != nil {
		return domain.Pagination[domain.User]{}, err
	}
	defer rows.Close()
	items := make([]domain.User, 0)
	for rows.Next() {
		item, scanErr := scanUser(rows)
		if scanErr != nil {
			return domain.Pagination[domain.User]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

const spaceColumns = `
s.id::text, s.name, s.description, s.slug, s.logo, s.visibility,
s.default_role, s.creator_id::text, s.workspace_id::text,
COALESCE(s.settings, '{}'::jsonb), COALESCE(s.is_personal, false),
s.created_at, s.updated_at,
(SELECT count(*) FROM space_members count_sm WHERE count_sm.space_id = s.id AND count_sm.deleted_at IS NULL),
sm.user_id::text, sm.role`

func scanSpace(row rowScanner) (domain.Space, error) {
	var value domain.Space
	var memberUserID, memberRole *string
	err := row.Scan(
		&value.ID, &value.Name, &value.Description, &value.Slug, &value.Logo,
		&value.Visibility, &value.DefaultRole, &value.CreatorID, &value.WorkspaceID,
		&value.Settings, &value.IsPersonal, &value.CreatedAt, &value.UpdatedAt,
		&value.MemberCount, &memberUserID, &memberRole,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Space{}, ErrNotFound
	}
	if err == nil && memberUserID != nil && memberRole != nil {
		value.Membership = &domain.Membership{UserID: *memberUserID, Role: *memberRole}
	}
	return value, err
}

func (repository *Repository) Spaces(ctx context.Context, workspaceID, userID string, limit int) (domain.Pagination[domain.Space], error) {
	rows, err := repository.db.Query(ctx, `
SELECT `+spaceColumns+`
FROM spaces s
LEFT JOIN space_members sm ON sm.space_id = s.id AND sm.user_id = $2 AND sm.deleted_at IS NULL
WHERE s.workspace_id = $1 AND s.deleted_at IS NULL
ORDER BY s.name NULLS LAST LIMIT $3`, workspaceID, userID, limit)
	if err != nil {
		return domain.Pagination[domain.Space]{}, err
	}
	defer rows.Close()
	items := make([]domain.Space, 0)
	for rows.Next() {
		item, scanErr := scanSpace(rows)
		if scanErr != nil {
			return domain.Pagination[domain.Space]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) SpaceByID(ctx context.Context, id, workspaceID, userID string) (domain.Space, error) {
	return scanSpace(repository.db.QueryRow(ctx, `
SELECT `+spaceColumns+`
FROM spaces s
LEFT JOIN space_members sm ON sm.space_id = s.id AND sm.user_id = $3 AND sm.deleted_at IS NULL
WHERE s.id = $1 AND s.workspace_id = $2 AND s.deleted_at IS NULL`, id, workspaceID, userID))
}

func (repository *Repository) SpaceRole(ctx context.Context, spaceID, workspaceID, userID string) (string, error) {
	var role string
	err := repository.db.QueryRow(ctx, `
SELECT candidate.role
FROM (
  SELECT sm.role
  FROM space_members sm
  WHERE sm.space_id = $1 AND sm.user_id = $3 AND sm.deleted_at IS NULL
  UNION ALL
  SELECT sm.role
  FROM space_members sm
  JOIN group_users gu ON gu.group_id = sm.group_id
  WHERE sm.space_id = $1 AND gu.user_id = $3 AND sm.deleted_at IS NULL
  UNION ALL
  SELECT s.default_role
  FROM spaces s
  WHERE s.id = $1 AND s.workspace_id = $2 AND s.deleted_at IS NULL AND s.visibility = 'public'
) candidate
ORDER BY CASE candidate.role WHEN 'admin' THEN 3 WHEN 'writer' THEN 2 ELSE 1 END DESC
LIMIT 1`, spaceID, workspaceID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

type SpaceInput struct {
	Name        *string
	Description *string
	Slug        *string
	Logo        *string
	Visibility  *string
	DefaultRole *string
	Settings    json.RawMessage
}

func (repository *Repository) CreateSpace(ctx context.Context, workspaceID, userID string, input SpaceInput) (domain.Space, error) {
	id, err := newUUID()
	if err != nil {
		return domain.Space{}, err
	}
	memberID, _ := newUUID()
	name := fallbackPointer(input.Name, "Untitled")
	slug := fallbackPointer(input.Slug, slugify(name))
	_, err = repository.db.Exec(ctx, `
INSERT INTO spaces (id, name, description, slug, logo, visibility, default_role, creator_id, workspace_id, settings)
VALUES ($1, $2, $3, $4, $5, COALESCE($6, 'private'), COALESCE($7, 'writer'), $8, $9, COALESCE($10::jsonb, '{}'::jsonb))`,
		id, name, input.Description, slug, input.Logo, input.Visibility, input.DefaultRole, userID, workspaceID, nullableJSON(input.Settings))
	if err != nil {
		return domain.Space{}, err
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO space_members (id, user_id, space_id, role, added_by_id) VALUES ($1, $2, $3, 'admin', $2)`, memberID, userID, id)
	if err != nil {
		return domain.Space{}, err
	}
	return repository.SpaceByID(ctx, id, workspaceID, userID)
}

func (repository *Repository) UpdateSpace(ctx context.Context, id, workspaceID, userID string, input SpaceInput) (domain.Space, error) {
	_, err := repository.db.Exec(ctx, `
UPDATE spaces SET name = COALESCE($3, name), description = COALESCE($4, description),
  slug = COALESCE($5, slug), logo = COALESCE($6, logo), visibility = COALESCE($7, visibility),
  default_role = COALESCE($8, default_role), settings = COALESCE($9::jsonb, settings), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID, input.Name, input.Description, input.Slug, input.Logo, input.Visibility, input.DefaultRole, nullableJSON(input.Settings))
	if err != nil {
		return domain.Space{}, err
	}
	return repository.SpaceByID(ctx, id, workspaceID, userID)
}

func (repository *Repository) DeleteSpace(ctx context.Context, id, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `UPDATE spaces SET deleted_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

const pageColumns = `
p.id::text, p.slug_id, p.title, p.icon, p.cover_photo, p.position,
COALESCE(p.content, '{"type":"doc","content":[]}'::jsonb), p.parent_page_id::text,
p.creator_id::text, p.last_updated_by_id::text, p.deleted_by_id::text,
p.space_id::text, p.workspace_id::text, p.is_locked, COALESCE(p.is_base, false),
p.created_at, p.updated_at, p.deleted_at,
EXISTS(SELECT 1 FROM pages child WHERE child.parent_page_id = p.id AND child.deleted_at IS NULL),
creator.id::text, creator.name, creator.avatar_url,
editor.id::text, editor.name, editor.avatar_url,
deleter.id::text, deleter.name, deleter.avatar_url,
s.id::text, s.name, s.slug`

const pageJoins = `
LEFT JOIN users creator ON creator.id = p.creator_id
LEFT JOIN users editor ON editor.id = p.last_updated_by_id
LEFT JOIN users deleter ON deleter.id = p.deleted_by_id
JOIN spaces s ON s.id = p.space_id`

func scanPage(row rowScanner) (domain.Page, error) {
	var value domain.Page
	var creatorID, creatorName, creatorAvatar *string
	var editorID, editorName, editorAvatar *string
	var deleterID, deleterName, deleterAvatar *string
	var spaceID, spaceSlug string
	var spaceName *string
	err := row.Scan(
		&value.ID, &value.SlugID, &value.Title, &value.Icon, &value.CoverPhoto,
		&value.Position, &value.Content, &value.ParentPageID, &value.CreatorID,
		&value.LastUpdatedByID, &value.DeletedByID, &value.SpaceID, &value.WorkspaceID,
		&value.IsLocked, &value.IsBase, &value.CreatedAt, &value.UpdatedAt, &value.DeletedAt,
		&value.HasChildren, &creatorID, &creatorName, &creatorAvatar, &editorID,
		&editorName, &editorAvatar, &deleterID, &deleterName, &deleterAvatar,
		&spaceID, &spaceName, &spaceSlug,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Page{}, ErrNotFound
	}
	if err != nil {
		return domain.Page{}, err
	}
	if creatorID != nil {
		value.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
	}
	if editorID != nil {
		value.LastUpdatedBy = &domain.UserSummary{ID: *editorID, Name: editorName, AvatarURL: editorAvatar}
	}
	if deleterID != nil {
		value.DeletedBy = &domain.UserSummary{ID: *deleterID, Name: deleterName, AvatarURL: deleterAvatar}
	}
	value.Space = &domain.SpaceSummary{ID: spaceID, Name: spaceName, Slug: spaceSlug}
	return value, nil
}

func (repository *Repository) PageByID(ctx context.Context, id, slugID, workspaceID string, includeDeleted bool) (domain.Page, error) {
	return scanPage(repository.db.QueryRow(ctx, `
SELECT `+pageColumns+` FROM pages p `+pageJoins+`
WHERE p.workspace_id = $1
  AND (($2 <> '' AND p.id::text = $2) OR ($3 <> '' AND p.slug_id = $3))
  AND ($4 OR p.deleted_at IS NULL)`, workspaceID, id, slugID, includeDeleted))
}

type PageInput struct {
	Title        *string
	Icon         *string
	CoverPhoto   *string
	Position     *string
	ParentPageID *string
	SpaceID      *string
	IsLocked     *bool
	Content      json.RawMessage
}

func (repository *Repository) CreatePage(ctx context.Context, workspaceID, userID string, input PageInput) (domain.Page, error) {
	id, err := newUUID()
	if err != nil {
		return domain.Page{}, err
	}
	slugID, err := randomID(10)
	if err != nil {
		return domain.Page{}, err
	}
	title := fallbackPointer(input.Title, "Untitled")
	position := fallbackPointer(input.Position, fmt.Sprintf("%020d", time.Now().UnixNano()))
	content := input.Content
	if len(content) == 0 {
		content = json.RawMessage(`{"type":"doc","content":[]}`)
	}
	if input.SpaceID == nil || *input.SpaceID == "" {
		return domain.Page{}, errors.New("spaceId is required")
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO pages (id, slug_id, title, icon, cover_photo, position, content, parent_page_id, creator_id, last_updated_by_id, space_id, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $9, $10, $11)`,
		id, slugID, title, input.Icon, input.CoverPhoto, position, content, input.ParentPageID, userID, *input.SpaceID, workspaceID)
	if err != nil {
		return domain.Page{}, err
	}
	return repository.PageByID(ctx, id, "", workspaceID, false)
}

func (repository *Repository) UpdatePage(ctx context.Context, id, workspaceID, userID string, input PageInput) (domain.Page, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Page{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
INSERT INTO page_history (page_id, slug_id, title, content, icon, cover_photo, last_updated_by_id, contributor_ids, space_id, workspace_id)
SELECT id, slug_id, title, content, icon, cover_photo, COALESCE(last_updated_by_id, creator_id), ARRAY[COALESCE(last_updated_by_id, creator_id)], space_id, workspace_id
FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID)
	if err != nil {
		return domain.Page{}, err
	}
	result, err := tx.Exec(ctx, `
UPDATE pages SET title = COALESCE($4, title), icon = COALESCE($5, icon),
  cover_photo = COALESCE($6, cover_photo), position = COALESCE($7, position),
  parent_page_id = COALESCE($8, parent_page_id), is_locked = COALESCE($9, is_locked),
  content = COALESCE($10::jsonb, content), last_updated_by_id = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID, userID,
		input.Title, input.Icon, input.CoverPhoto, input.Position, input.ParentPageID,
		input.IsLocked, nullableJSON(input.Content))
	if err != nil {
		return domain.Page{}, err
	}
	if result.RowsAffected() == 0 {
		return domain.Page{}, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Page{}, err
	}
	return repository.PageByID(ctx, id, "", workspaceID, false)
}

func (repository *Repository) DeletePage(ctx context.Context, id, workspaceID, userID string, permanent bool) error {
	var result pgconnCommandTag
	var err error
	if permanent {
		result, err = repository.db.Exec(ctx, `DELETE FROM pages WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	} else {
		result, err = repository.db.Exec(ctx, `UPDATE pages SET deleted_at = now(), deleted_by_id = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID, userID)
	}
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

type pgconnCommandTag interface {
	RowsAffected() int64
}

func (repository *Repository) RestorePage(ctx context.Context, id, workspaceID string) (domain.Page, error) {
	result, err := repository.db.Exec(ctx, `UPDATE pages SET deleted_at = NULL, deleted_by_id = NULL, updated_at = now() WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return domain.Page{}, err
	}
	if result.RowsAffected() == 0 {
		return domain.Page{}, ErrNotFound
	}
	return repository.PageByID(ctx, id, "", workspaceID, false)
}

func (repository *Repository) MovePage(ctx context.Context, id, workspaceID string, parentPageID, position *string) error {
	_, err := repository.db.Exec(ctx, `UPDATE pages SET parent_page_id = $3, position = COALESCE($4, position), updated_at = now() WHERE id = $1 AND workspace_id = $2`, id, workspaceID, parentPageID, position)
	return err
}

func (repository *Repository) MovePageToSpace(ctx context.Context, id, workspaceID, spaceID string) error {
	_, err := repository.db.Exec(ctx, `
WITH RECURSIVE descendants AS (
  SELECT id FROM pages WHERE id = $1 AND workspace_id = $2
  UNION ALL SELECT p.id FROM pages p JOIN descendants d ON p.parent_page_id = d.id
)
UPDATE pages SET space_id = $3, updated_at = now() WHERE id IN (SELECT id FROM descendants)`, id, workspaceID, spaceID)
	return err
}

type PageListFilter struct {
	SpaceID      *string
	ParentPageID *string
	CreatorID    *string
	Deleted      bool
	Recent       bool
	Limit        int
}

func (repository *Repository) Pages(ctx context.Context, workspaceID string, filter PageListFilter) (domain.Pagination[domain.Page], error) {
	limit := normalizeLimit(filter.Limit)
	rows, err := repository.db.Query(ctx, `
SELECT `+pageColumns+` FROM pages p `+pageJoins+`
WHERE p.workspace_id = $1
  AND ($2::uuid IS NULL OR p.space_id = $2)
  AND ($3::uuid IS NULL OR p.parent_page_id = $3)
  AND ($4::uuid IS NULL OR p.creator_id = $4)
  AND (($5 AND p.deleted_at IS NOT NULL) OR (NOT $5 AND p.deleted_at IS NULL))
ORDER BY CASE WHEN $6 THEN p.updated_at END DESC, CASE WHEN NOT $6 THEN p.position END NULLS LAST, p.created_at
LIMIT $7`, workspaceID, filter.SpaceID, filter.ParentPageID, filter.CreatorID, filter.Deleted, filter.Recent, limit)
	if err != nil {
		return domain.Pagination[domain.Page]{}, err
	}
	defer rows.Close()
	items := make([]domain.Page, 0)
	for rows.Next() {
		item, scanErr := scanPage(rows)
		if scanErr != nil {
			return domain.Pagination[domain.Page]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) Breadcrumbs(ctx context.Context, pageID, workspaceID string) ([]domain.Page, error) {
	rows, err := repository.db.Query(ctx, `
WITH RECURSIVE ancestors AS (
  SELECT p.*, 0 AS depth FROM pages p WHERE p.id = $1 AND p.workspace_id = $2 AND p.deleted_at IS NULL
  UNION ALL
  SELECT parent.*, child.depth + 1 FROM pages parent JOIN ancestors child ON child.parent_page_id = parent.id WHERE parent.deleted_at IS NULL
)
SELECT `+pageColumns+` FROM ancestors p `+pageJoins+` ORDER BY p.depth DESC`, pageID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Page, 0)
	for rows.Next() {
		item, scanErr := scanPage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func page[T any](items []T, limit int) domain.Pagination[T] {
	return domain.Pagination[T]{Items: items, Meta: domain.PaginationMeta{Limit: normalizeLimit(limit)}}
}

func fallbackPointer(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	return value
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	lastDash := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
			lastDash = false
		} else if !lastDash && result.Len() > 0 {
			result.WriteByte('-')
			lastDash = true
		}
	}
	cleaned := strings.Trim(result.String(), "-")
	if cleaned == "" {
		cleaned = "space"
	}
	suffix, _ := randomID(4)
	return cleaned + "-" + strings.ToLower(suffix)
}

func randomID(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer)[:size], nil
}

func newUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
