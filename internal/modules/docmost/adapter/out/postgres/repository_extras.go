package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

const groupColumns = `
g.id::text, g.name, g.description, g.is_default, COALESCE(g.is_external, false),
g.creator_id::text, g.workspace_id::text, g.created_at, g.updated_at,
(SELECT count(*) FROM group_users gu WHERE gu.group_id = g.id)`

func scanGroup(row rowScanner) (domain.Group, error) {
	var value domain.Group
	err := row.Scan(&value.ID, &value.Name, &value.Description, &value.IsDefault, &value.IsExternal,
		&value.CreatorID, &value.WorkspaceID, &value.CreatedAt, &value.UpdatedAt, &value.MemberCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Group{}, ErrNotFound
	}
	return value, err
}

func (repository *Repository) Groups(ctx context.Context, workspaceID string, limit int) (domain.Pagination[domain.Group], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+groupColumns+` FROM groups g WHERE g.workspace_id = $1 AND g.deleted_at IS NULL ORDER BY g.is_default DESC, g.name LIMIT $2`, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Group]{}, err
	}
	defer rows.Close()
	items := make([]domain.Group, 0)
	for rows.Next() {
		item, scanErr := scanGroup(rows)
		if scanErr != nil {
			return domain.Pagination[domain.Group]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) GroupByID(ctx context.Context, id, workspaceID string) (domain.Group, error) {
	return scanGroup(repository.db.QueryRow(ctx, `SELECT `+groupColumns+` FROM groups g WHERE g.id = $1 AND g.workspace_id = $2 AND g.deleted_at IS NULL`, id, workspaceID))
}

func (repository *Repository) CreateGroup(ctx context.Context, workspaceID, userID, name string, description *string, userIDs []string) (domain.Group, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Group{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := newUUID()
	if err != nil {
		return domain.Group{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO groups (id, name, description, is_default, creator_id, workspace_id) VALUES ($1, $2, $3, false, $4, $5)`, id, strings.TrimSpace(name), description, userID, workspaceID); err != nil {
		return domain.Group{}, err
	}
	for _, memberID := range userIDs {
		linkID, idErr := newUUID()
		if idErr != nil {
			return domain.Group{}, idErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO group_users (id, group_id, user_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, linkID, id, memberID); err != nil {
			return domain.Group{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Group{}, err
	}
	return repository.GroupByID(ctx, id, workspaceID)
}

func (repository *Repository) UpdateGroup(ctx context.Context, id, workspaceID string, name, description *string) (domain.Group, error) {
	_, err := repository.db.Exec(ctx, `UPDATE groups SET name = COALESCE($3, name), description = COALESCE($4, description), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL AND is_default = false`, id, workspaceID, name, description)
	if err != nil {
		return domain.Group{}, err
	}
	return repository.GroupByID(ctx, id, workspaceID)
}

func (repository *Repository) DeleteGroup(ctx context.Context, id, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `UPDATE groups SET deleted_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND is_default = false AND deleted_at IS NULL`, id, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) GroupMembers(ctx context.Context, groupID, workspaceID string, limit int) (domain.Pagination[domain.User], error) {
	rows, err := repository.db.Query(ctx, `SELECT
u.id::text, u.name, u.email, u.email_verified_at, u.password, u.avatar_url, u.role,
u.workspace_id::text, u.locale, u.timezone, COALESCE(u.settings, '{}'::jsonb),
u.last_active_at, u.last_login_at, u.deactivated_at, u.deleted_at, u.created_at,
u.updated_at, COALESCE(u.has_generated_password, false)
FROM users u JOIN group_users gu ON gu.user_id = u.id
WHERE gu.group_id = $1 AND u.workspace_id = $2 AND u.deleted_at IS NULL
ORDER BY u.name LIMIT $3`, groupID, workspaceID, normalizeLimit(limit))
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

func (repository *Repository) AddGroupUsers(ctx context.Context, groupID, workspaceID string, userIDs []string) error {
	for _, userID := range userIDs {
		id, err := newUUID()
		if err != nil {
			return err
		}
		if _, err = repository.db.Exec(ctx, `
INSERT INTO group_users (id, group_id, user_id)
SELECT $1, g.id, u.id FROM groups g JOIN users u ON u.workspace_id = g.workspace_id
WHERE g.id = $2 AND g.workspace_id = $3 AND u.id = $4
ON CONFLICT DO NOTHING`, id, groupID, workspaceID, userID); err != nil {
			return err
		}
	}
	return nil
}

func (repository *Repository) RemoveGroupUser(ctx context.Context, groupID, workspaceID, userID string) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM group_users gu USING groups g WHERE gu.group_id = g.id AND g.id = $1 AND g.workspace_id = $2 AND gu.user_id = $3 AND g.is_default = false`, groupID, workspaceID, userID)
	return err
}

const commentColumns = `
c.id::text, COALESCE(c.content, '{}'::jsonb), c.selection, c.type,
c.creator_id::text, c.page_id::text, c.parent_comment_id::text,
c.resolved_by_id::text, c.resolved_at, c.workspace_id::text,
c.created_at, c.updated_at, c.edited_at, c.deleted_at,
creator.id::text, creator.name, creator.avatar_url,
resolver.id::text, resolver.name, resolver.avatar_url`

func scanComment(row rowScanner) (domain.Comment, error) {
	var value domain.Comment
	var creatorID, creatorName, creatorAvatar *string
	var resolverID, resolverName, resolverAvatar *string
	err := row.Scan(&value.ID, &value.Content, &value.Selection, &value.Type, &value.CreatorID,
		&value.PageID, &value.ParentCommentID, &value.ResolvedByID, &value.ResolvedAt,
		&value.WorkspaceID, &value.CreatedAt, &value.UpdatedAt, &value.EditedAt,
		&value.DeletedAt, &creatorID, &creatorName, &creatorAvatar, &resolverID,
		&resolverName, &resolverAvatar)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Comment{}, ErrNotFound
	}
	if err == nil && creatorID != nil {
		value.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
	}
	if err == nil && resolverID != nil {
		value.ResolvedBy = &domain.UserSummary{ID: *resolverID, Name: resolverName, AvatarURL: resolverAvatar}
	}
	return value, err
}

const commentJoins = ` LEFT JOIN users creator ON creator.id = c.creator_id LEFT JOIN users resolver ON resolver.id = c.resolved_by_id `

func (repository *Repository) Comments(ctx context.Context, pageID, workspaceID string, limit int) (domain.Pagination[domain.Comment], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+commentColumns+` FROM comments c `+commentJoins+` WHERE c.page_id = $1 AND c.workspace_id = $2 AND c.deleted_at IS NULL ORDER BY c.created_at LIMIT $3`, pageID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Comment]{}, err
	}
	defer rows.Close()
	items := make([]domain.Comment, 0)
	for rows.Next() {
		item, scanErr := scanComment(rows)
		if scanErr != nil {
			return domain.Pagination[domain.Comment]{}, scanErr
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) CommentByID(ctx context.Context, id, workspaceID string) (domain.Comment, error) {
	return scanComment(repository.db.QueryRow(ctx, `SELECT `+commentColumns+` FROM comments c `+commentJoins+` WHERE c.id = $1 AND c.workspace_id = $2 AND c.deleted_at IS NULL`, id, workspaceID))
}

type CommentInput struct {
	PageID          string
	Content         json.RawMessage
	Selection       *string
	Type            *string
	ParentCommentID *string
	SpaceID         string
}

func (repository *Repository) CreateComment(ctx context.Context, workspaceID, userID string, input CommentInput) (domain.Comment, error) {
	id, err := newUUID()
	if err != nil {
		return domain.Comment{}, err
	}
	content := input.Content
	if len(content) == 0 {
		content = json.RawMessage(`{}`)
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO comments (id, content, selection, type, creator_id, page_id, parent_comment_id, workspace_id, space_id)
VALUES ($1, $2::jsonb, $3, COALESCE($4, 'page'), $5, $6, $7, $8, $9)`, id, content, input.Selection, input.Type, userID, input.PageID, input.ParentCommentID, workspaceID, input.SpaceID)
	if err != nil {
		return domain.Comment{}, err
	}
	return repository.CommentByID(ctx, id, workspaceID)
}

func (repository *Repository) UpdateComment(ctx context.Context, id, workspaceID, userID string, content json.RawMessage) (domain.Comment, error) {
	result, err := repository.db.Exec(ctx, `UPDATE comments SET content = $4::jsonb, last_edited_by_id = $3, edited_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND creator_id = $3 AND deleted_at IS NULL`, id, workspaceID, userID, content)
	if err != nil {
		return domain.Comment{}, err
	}
	if result.RowsAffected() == 0 {
		return domain.Comment{}, ErrNotFound
	}
	return repository.CommentByID(ctx, id, workspaceID)
}

func (repository *Repository) ResolveComment(ctx context.Context, id, workspaceID, userID string, resolved bool) (domain.Comment, error) {
	_, err := repository.db.Exec(ctx, `UPDATE comments SET resolved_at = CASE WHEN $4 THEN now() ELSE NULL END, resolved_by_id = CASE WHEN $4 THEN $3::uuid ELSE NULL END, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, id, workspaceID, userID, resolved)
	if err != nil {
		return domain.Comment{}, err
	}
	return repository.CommentByID(ctx, id, workspaceID)
}

func (repository *Repository) DeleteComment(ctx context.Context, id, workspaceID, userID string, admin bool) error {
	result, err := repository.db.Exec(ctx, `UPDATE comments SET deleted_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND ($3 OR creator_id = $4) AND deleted_at IS NULL`, id, workspaceID, admin, userID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) Labels(ctx context.Context, workspaceID, viewerID string, viewerAdmin bool, labelType, query, cursor string, limit int) (domain.Pagination[domain.Label], error) {
	limit = normalizeLimit(limit)
	if labelType == "" {
		labelType = "page"
	}
	rows, err := repository.db.Query(ctx, `SELECT l.id::text, l.name, l.type, l.workspace_id::text, l.created_at, l.updated_at
FROM labels l WHERE l.workspace_id = $1 AND l.type=$2
AND ($3='' OR l.name ILIKE '%' || $3 || '%') AND ($4='' OR l.id::text>$4)
AND EXISTS (SELECT 1 FROM page_labels pl JOIN pages p ON p.id=pl.page_id
 WHERE pl.label_id=l.id AND p.workspace_id=l.workspace_id AND p.deleted_at IS NULL AND `+
		strings.NewReplacer("$8", "$5", "$9", "$6").Replace(pageListAccessSQL)+`)
ORDER BY l.id LIMIT $7`, workspaceID, labelType, query, cursor, viewerID, viewerAdmin, limit+1)
	if err != nil {
		return domain.Pagination[domain.Label]{}, err
	}
	defer rows.Close()
	items := make([]domain.Label, 0)
	for rows.Next() {
		var item domain.Label
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return domain.Pagination[domain.Label]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[domain.Label]{}, err
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

func (repository *Repository) PageLabels(ctx context.Context, pageID, workspaceID string, limit int) (domain.Pagination[domain.Label], error) {
	rows, err := repository.db.Query(ctx, `SELECT l.id::text, l.name, l.type, l.workspace_id::text, l.created_at, l.updated_at FROM labels l JOIN page_labels pl ON pl.label_id = l.id JOIN pages p ON p.id = pl.page_id WHERE p.id = $1 AND p.workspace_id = $2 ORDER BY l.name LIMIT $3`, pageID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Label]{}, err
	}
	defer rows.Close()
	items := make([]domain.Label, 0)
	for rows.Next() {
		var item domain.Label
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return domain.Pagination[domain.Label]{}, err
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) AddPageLabels(ctx context.Context, pageID, workspaceID string, names []string) ([]domain.Label, error) {
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM pages WHERE id=$1 AND workspace_id=$2 AND deleted_at IS NULL FOR UPDATE`, pageID, workspaceID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	attached := make([]domain.Label, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, ErrInvalidInput
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		labelID, err := newUUID()
		if err != nil {
			return nil, err
		}
		linkID, err := newUUID()
		if err != nil {
			return nil, err
		}
		var label domain.Label
		if err = tx.QueryRow(ctx, `
WITH label AS (
  INSERT INTO labels (id, name, type, workspace_id) VALUES ($1, $2, 'page', $3)
  ON CONFLICT (workspace_id, type, name) DO UPDATE SET updated_at = labels.updated_at
  RETURNING *
), linked AS (
INSERT INTO page_labels (id, page_id, label_id)
SELECT $4, $5, id FROM label ON CONFLICT DO NOTHING
)
SELECT id::text,name,type,workspace_id::text,created_at,updated_at FROM label`, labelID, name, workspaceID, linkID, pageID).Scan(&label.ID, &label.Name, &label.Type, &label.WorkspaceID, &label.CreatedAt, &label.UpdatedAt); err != nil {
			return nil, err
		}
		attached = append(attached, label)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return attached, nil
}

func (repository *Repository) RemovePageLabel(ctx context.Context, pageID, labelID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM page_labels pl USING labels l WHERE pl.label_id = l.id AND pl.page_id = $1 AND pl.label_id = $2 AND l.workspace_id = $3`, pageID, labelID, workspaceID)
	return err
}

type FavoriteInput struct {
	Type       string
	PageID     *string
	SpaceID    *string
	TemplateID *string
}

// FavoriteTargetSpace validates that a favorite target belongs to the current
// workspace and returns the space whose permissions protect that target.
func (repository *Repository) FavoriteTargetSpace(ctx context.Context, workspaceID string, input FavoriteInput) (string, error) {
	var spaceID string
	var err error
	switch {
	case input.Type == "page" && input.PageID != nil && input.SpaceID == nil && input.TemplateID == nil:
		err = repository.db.QueryRow(ctx, `SELECT space_id::text FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, *input.PageID, workspaceID).Scan(&spaceID)
	case input.Type == "space" && input.SpaceID != nil && input.PageID == nil && input.TemplateID == nil:
		err = repository.db.QueryRow(ctx, `SELECT id::text FROM spaces WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, *input.SpaceID, workspaceID).Scan(&spaceID)
	case input.Type == "template" && input.TemplateID != nil && input.PageID == nil && input.SpaceID == nil:
		err = repository.db.QueryRow(ctx, `SELECT space_id::text FROM templates WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, *input.TemplateID, workspaceID).Scan(&spaceID)
	default:
		return "", ErrInvalidInput
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return spaceID, err
}

func (repository *Repository) AddFavorite(ctx context.Context, workspaceID, userID string, input FavoriteInput) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO favorites (id, user_id, page_id, space_id, template_id, type, workspace_id) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, id, userID, input.PageID, input.SpaceID, input.TemplateID, input.Type, workspaceID)
	return err
}

func (repository *Repository) RemoveFavorite(ctx context.Context, workspaceID, userID string, input FavoriteInput) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM favorites WHERE user_id = $1 AND workspace_id = $2 AND type = $3 AND ($4::uuid IS NULL OR page_id = $4) AND ($5::uuid IS NULL OR space_id = $5) AND ($6::uuid IS NULL OR template_id = $6)`, userID, workspaceID, input.Type, input.PageID, input.SpaceID, input.TemplateID)
	return err
}

func (repository *Repository) FavoriteIDs(ctx context.Context, workspaceID, userID, favoriteType string, spaceID *string) ([]string, error) {
	rows, err := repository.db.Query(ctx, `SELECT COALESCE(page_id, space_id, template_id)::text FROM favorites WHERE user_id = $1 AND workspace_id = $2 AND type = $3 AND ($4::uuid IS NULL OR space_id = $4) ORDER BY created_at`, userID, workspaceID, favoriteType, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	return items, rows.Err()
}

func (repository *Repository) Favorites(ctx context.Context, workspaceID, userID string, limit int) (domain.Pagination[domain.Favorite], error) {
	rows, err := repository.db.Query(ctx, `
SELECT f.id::text, f.user_id::text, f.page_id::text, f.space_id::text, f.template_id::text,
 f.type, f.workspace_id::text, f.created_at,
 p.id::text, p.slug_id, p.title, p.icon, COALESCE(p.is_base, false), p.space_id::text,
 s.id::text, s.name, s.slug
FROM favorites f LEFT JOIN pages p ON p.id = f.page_id LEFT JOIN spaces s ON s.id = f.space_id
WHERE f.user_id = $1 AND f.workspace_id = $2 ORDER BY f.created_at DESC LIMIT $3`, userID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Favorite]{}, err
	}
	defer rows.Close()
	items := make([]domain.Favorite, 0)
	for rows.Next() {
		var item domain.Favorite
		var pageID, pageSlugID, pageSpaceID, spaceID, spaceSlug *string
		var pageTitle, pageIcon, spaceName *string
		var pageIsBase *bool
		if err := rows.Scan(&item.ID, &item.UserID, &item.PageID, &item.SpaceID, &item.TemplateID, &item.Type, &item.WorkspaceID, &item.CreatedAt, &pageID, &pageSlugID, &pageTitle, &pageIcon, &pageIsBase, &pageSpaceID, &spaceID, &spaceName, &spaceSlug); err != nil {
			return domain.Pagination[domain.Favorite]{}, err
		}
		if pageID != nil && pageSlugID != nil && pageSpaceID != nil {
			item.Page = &domain.PageSummary{ID: *pageID, SlugID: *pageSlugID, Title: pageTitle, Icon: pageIcon, IsBase: pageIsBase != nil && *pageIsBase, SpaceID: *pageSpaceID}
		}
		if spaceID != nil && spaceSlug != nil {
			item.Space = &domain.SpaceSummary{ID: *spaceID, Name: spaceName, Slug: *spaceSlug}
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) SetWatcher(ctx context.Context, workspaceID, userID, spaceID string, pageID *string, watching bool) error {
	if !watching {
		_, err := repository.db.Exec(ctx, `DELETE FROM watchers WHERE user_id = $1 AND workspace_id = $2 AND space_id = $3 AND (($4::uuid IS NULL AND page_id IS NULL) OR page_id = $4)`, userID, workspaceID, spaceID, pageID)
		return err
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	watcherType := "space"
	if pageID != nil {
		watcherType = "page"
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO watchers (id, user_id, page_id, space_id, workspace_id, type, added_by_id) VALUES ($1, $2, $3, $4, $5, $6, $2) ON CONFLICT DO NOTHING`, id, userID, pageID, spaceID, workspaceID, watcherType)
	return err
}

// AddPageWatchers mirrors Docmost's collaboration history worker. Inserts are
// intentionally no-op on conflict so a user's explicit page mute is not
// silently removed by a later edit.
func (repository *Repository) AddPageWatchers(ctx context.Context, userIDs []string, pageID, spaceID, workspaceID string) error {
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		id, err := newUUID()
		if err != nil {
			return err
		}
		if _, err = repository.db.Exec(ctx, `
INSERT INTO watchers (id, user_id, page_id, space_id, workspace_id, type, added_by_id)
VALUES ($1, $2, $3, $4, $5, 'page', $2)
ON CONFLICT DO NOTHING`, id, userID, pageID, spaceID, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func (repository *Repository) WatchStatus(ctx context.Context, workspaceID, userID, spaceID string, pageID *string) (bool, error) {
	var watching bool
	err := repository.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM watchers WHERE user_id = $1 AND workspace_id = $2 AND space_id = $3 AND (($4::uuid IS NULL AND page_id IS NULL) OR page_id = $4))`, userID, workspaceID, spaceID, pageID).Scan(&watching)
	return watching, err
}

func (repository *Repository) WatchedSpaceIDs(ctx context.Context, workspaceID, userID string, limit int) (domain.Pagination[string], error) {
	rows, err := repository.db.Query(ctx, `SELECT space_id::text FROM watchers WHERE user_id = $1 AND workspace_id = $2 AND page_id IS NULL ORDER BY created_at DESC LIMIT $3`, userID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[string]{}, err
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return domain.Pagination[string]{}, err
		}
		items = append(items, id)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) Sessions(ctx context.Context, workspaceID, userID, currentSessionID string) ([]domain.Session, error) {
	rows, err := repository.db.Query(ctx, `SELECT id::text, device_name, geo_location, last_active_at, created_at FROM user_sessions WHERE user_id = $1 AND workspace_id = $2 AND revoked_at IS NULL AND expires_at > now() ORDER BY last_active_at DESC`, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Session, 0)
	for rows.Next() {
		var item domain.Session
		if err := rows.Scan(&item.ID, &item.DeviceName, &item.GeoLocation, &item.LastActiveAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.IsCurrentDevice = item.ID == currentSessionID
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) RevokeOtherSessions(ctx context.Context, workspaceID, userID, currentSessionID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND workspace_id = $2 AND id <> $3 AND revoked_at IS NULL`, userID, workspaceID, currentSessionID)
	return err
}

func (repository *Repository) SetWorkspaceMemberActive(ctx context.Context, workspaceID, userID string, active bool) error {
	var result pgconnCommandTag
	var err error
	if active {
		result, err = repository.db.Exec(ctx, `UPDATE users SET deactivated_at = NULL, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, userID, workspaceID)
	} else {
		result, err = repository.db.Exec(ctx, `UPDATE users SET deactivated_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, userID, workspaceID)
	}
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err == nil && !active {
		_, _ = repository.db.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, userID, workspaceID)
	}
	return err
}

func (repository *Repository) DeleteWorkspaceMember(ctx context.Context, workspaceID, userID string) error {
	result, err := repository.db.Exec(ctx, `UPDATE users SET deleted_at = now(), deactivated_at = now(), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, userID, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err == nil {
		_, _ = repository.db.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, userID, workspaceID)
	}
	return err
}

func (repository *Repository) ChangeWorkspaceMemberRole(ctx context.Context, workspaceID, userID, role string) error {
	result, err := repository.db.Exec(ctx, `UPDATE users SET role = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, userID, workspaceID, role)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) OwnerCount(ctx context.Context, workspaceID string) (int64, error) {
	var count int64
	err := repository.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE workspace_id = $1 AND role = 'owner' AND deleted_at IS NULL AND deactivated_at IS NULL`, workspaceID).Scan(&count)
	return count, err
}

func (repository *Repository) SpaceMembers(ctx context.Context, spaceID, workspaceID string, limit int) (domain.Pagination[domain.SpaceMember], error) {
	rows, err := repository.db.Query(ctx, `
SELECT member.id, member.name, member.email, member.avatar_url, member.type, member.role, member.is_default, member.member_count
FROM (
  SELECT u.id::text AS id, u.name, u.email, u.avatar_url, 'user'::text AS type, sm.role,
    false AS is_default, 0::bigint AS member_count, sm.created_at
  FROM space_members sm JOIN users u ON u.id = sm.user_id
  WHERE sm.space_id = $1 AND u.workspace_id = $2 AND sm.deleted_at IS NULL AND u.deleted_at IS NULL
  UNION ALL
  SELECT g.id::text, g.name, NULL::varchar, NULL::varchar, 'group'::text, sm.role,
    g.is_default, (SELECT count(*) FROM group_users gu WHERE gu.group_id = g.id), sm.created_at
  FROM space_members sm JOIN groups g ON g.id = sm.group_id
  WHERE sm.space_id = $1 AND g.workspace_id = $2 AND sm.deleted_at IS NULL AND g.deleted_at IS NULL
) member ORDER BY member.type DESC, member.name LIMIT $3`, spaceID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.SpaceMember]{}, err
	}
	defer rows.Close()
	items := make([]domain.SpaceMember, 0)
	for rows.Next() {
		var item domain.SpaceMember
		if err := rows.Scan(&item.ID, &item.Name, &item.Email, &item.AvatarURL, &item.Type, &item.Role, &item.IsDefault, &item.MemberCount); err != nil {
			return domain.Pagination[domain.SpaceMember]{}, err
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) AddSpaceMembers(ctx context.Context, spaceID, workspaceID, addedByID string, userIDs, groupIDs []string) error {
	for _, userID := range userIDs {
		id, err := newUUID()
		if err != nil {
			return err
		}
		_, err = repository.db.Exec(ctx, `
INSERT INTO space_members (id, user_id, space_id, role, added_by_id)
SELECT $1, u.id, s.id, 'writer', $4 FROM users u JOIN spaces s ON s.workspace_id = u.workspace_id
WHERE u.id = $2 AND s.id = $3 AND s.workspace_id = $5
ON CONFLICT (space_id, user_id) DO UPDATE SET deleted_at = NULL`, id, userID, spaceID, addedByID, workspaceID)
		if err != nil {
			return err
		}
	}
	for _, groupID := range groupIDs {
		id, err := newUUID()
		if err != nil {
			return err
		}
		_, err = repository.db.Exec(ctx, `
INSERT INTO space_members (id, group_id, space_id, role, added_by_id)
SELECT $1, g.id, s.id, 'writer', $4 FROM groups g JOIN spaces s ON s.workspace_id = g.workspace_id
WHERE g.id = $2 AND s.id = $3 AND s.workspace_id = $5
ON CONFLICT (space_id, group_id) DO UPDATE SET deleted_at = NULL`, id, groupID, spaceID, addedByID, workspaceID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (repository *Repository) RemoveSpaceMember(ctx context.Context, spaceID, workspaceID string, userID, groupID *string) error {
	_, err := repository.db.Exec(ctx, `UPDATE space_members sm SET deleted_at = now(), updated_at = now() FROM spaces s WHERE sm.space_id = s.id AND s.id = $1 AND s.workspace_id = $2 AND ($3::uuid IS NULL OR sm.user_id = $3) AND ($4::uuid IS NULL OR sm.group_id = $4)`, spaceID, workspaceID, userID, groupID)
	return err
}

func (repository *Repository) ChangeSpaceMemberRole(ctx context.Context, spaceID, workspaceID, role string, userID, groupID *string) error {
	_, err := repository.db.Exec(ctx, `UPDATE space_members sm SET role = $5, updated_at = now() FROM spaces s WHERE sm.space_id = s.id AND s.id = $1 AND s.workspace_id = $2 AND sm.deleted_at IS NULL AND ($3::uuid IS NULL OR sm.user_id = $3) AND ($4::uuid IS NULL OR sm.group_id = $4)`, spaceID, workspaceID, userID, groupID, role)
	return err
}

type PageSearchOptions struct {
	CreatorID *string
	LabelIDs  []string
	TitleOnly bool
	Offset    int
}

// SearchPages keeps the original Go call shape for AI/MCP callers. HTTP
// search uses SearchPagesWithOptions so the frontend's filters are preserved.
func (repository *Repository) SearchPages(ctx context.Context, workspaceID, query string, spaceID *string, limit int, viewerID string, viewerAdmin bool) ([]domain.SearchPage, error) {
	return repository.SearchPagesWithOptions(ctx, workspaceID, query, spaceID, limit, viewerID, viewerAdmin, PageSearchOptions{})
}

func (repository *Repository) SearchPagesWithOptions(ctx context.Context, workspaceID, query string, spaceID *string, limit int, viewerID string, viewerAdmin bool, options PageSearchOptions) ([]domain.SearchPage, error) {
	query = strings.TrimSpace(query)
	if options.Offset < 0 {
		options.Offset = 0
	}
	if options.Offset > 100000 {
		options.Offset = 100000
	}
	labelIDs := options.LabelIDs
	if labelIDs == nil {
		labelIDs = []string{}
	}
	if query == "" && options.CreatorID == nil && len(labelIDs) == 0 {
		return []domain.SearchPage{}, nil
	}
	titleLikeQuery := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(query)
	rows, err := repository.db.Query(ctx, `
SELECT p.id::text, p.title, p.icon, p.parent_page_id::text, p.slug_id, p.creator_id::text,
 p.created_at, p.updated_at,
 CASE WHEN $2 = '' OR $10 THEN 1::real ELSE similarity(COALESCE(p.title, ''), $2)::real END,
 CASE WHEN $2 = '' OR $10 THEN '' ELSE ts_headline('simple', COALESCE(p.text_content, ''), plainto_tsquery('simple', $2)) END,
 s.id::text, s.name, s.slug
FROM pages p JOIN spaces s ON s.id = p.space_id
WHERE p.workspace_id = $1 AND p.deleted_at IS NULL AND ($3::uuid IS NULL OR p.space_id = $3)
  AND `+strings.NewReplacer("$8", "$5", "$9", "$6").Replace(pageListAccessSQL)+`
  AND ($7::uuid IS NULL OR p.creator_id = $7)
  AND (COALESCE(array_length($8::uuid[], 1), 0) = 0 OR EXISTS (
    SELECT 1 FROM page_labels pl WHERE pl.page_id = p.id AND pl.label_id = ANY($8::uuid[])
  ))
  AND ($2 = '' OR ($10 AND COALESCE(p.title, '') ILIKE '%' || $11 || '%' ESCAPE E'\\')
       OR (NOT $10 AND (COALESCE(p.title, '') ILIKE '%' || $2 || '%' OR COALESCE(p.text_content, '') ILIKE '%' || $2 || '%')))
ORDER BY CASE WHEN $2 = '' OR $10 THEN p.updated_at END DESC, similarity(COALESCE(p.title, ''), $2) DESC
LIMIT $4 OFFSET $9`, workspaceID, query, spaceID, normalizeLimit(limit), viewerID, viewerAdmin, options.CreatorID, labelIDs, options.Offset, options.TitleOnly, titleLikeQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SearchPage, 0)
	for rows.Next() {
		var item domain.SearchPage
		var space domain.SpaceSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.Icon, &item.ParentPageID, &item.SlugID, &item.CreatorID, &item.CreatedAt, &item.UpdatedAt, &item.Rank, &item.Highlight, &space.ID, &space.Name, &space.Slug); err != nil {
			return nil, err
		}
		item.Space = &space
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) SearchAttachments(ctx context.Context, workspaceID, query string, spaceID *string, limit int, viewerID string, viewerAdmin bool) ([]domain.AttachmentSearch, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []domain.AttachmentSearch{}, nil
	}
	rows, err := repository.db.Query(ctx, `
SELECT a.id::text, a.file_name, a.page_id::text, a.creator_id::text,
 a.created_at, a.updated_at, similarity(COALESCE(a.file_name, ''), $2)::real,
 ts_headline('simple', COALESCE(a.text_content, ''), plainto_tsquery('simple', $2)),
 s.id::text, s.name, s.slug,
 p.id::text, p.slug_id, p.title, p.icon, COALESCE(p.is_base, false), p.space_id::text
FROM attachments a
JOIN pages p ON p.id = a.page_id
JOIN spaces s ON s.id = p.space_id
WHERE a.workspace_id = $1 AND a.type = 'file' AND a.deleted_at IS NULL
  AND p.deleted_at IS NULL AND ($3::uuid IS NULL OR p.space_id = $3)
  AND `+strings.NewReplacer("$8", "$4", "$9", "$5").Replace(pageListAccessSQL)+`
  AND (COALESCE(a.file_name, '') ILIKE '%' || $2 || '%'
       OR COALESCE(a.text_content, '') ILIKE '%' || $2 || '%')
ORDER BY similarity(COALESCE(a.file_name, ''), $2) DESC, a.updated_at DESC
LIMIT $6`, workspaceID, query, spaceID, viewerID, viewerAdmin, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.AttachmentSearch, 0)
	for rows.Next() {
		var item domain.AttachmentSearch
		var space domain.SpaceSummary
		var page domain.PageSummary
		if err := rows.Scan(&item.ID, &item.FileName, &item.PageID, &item.CreatorID, &item.CreatedAt, &item.UpdatedAt, &item.Rank, &item.Highlight,
			&space.ID, &space.Name, &space.Slug, &page.ID, &page.SlugID, &page.Title, &page.Icon, &page.IsBase, &page.SpaceID); err != nil {
			return nil, err
		}
		item.Space = &space
		item.Page = &page
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) SearchSharedPages(ctx context.Context, shareID, query string, limit int) ([]domain.SearchPage, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []domain.SearchPage{}, nil
	}
	rows, err := repository.db.Query(ctx, `
WITH RECURSIVE shared AS (
  SELECT sh.page_id, COALESCE(sh.include_sub_pages, false) AS include_sub_pages,
         sh.workspace_id
  FROM shares sh
  WHERE (sh.id::text = $1 OR lower(sh.key) = lower($1))
    AND sh.deleted_at IS NULL
), tree AS (
  SELECT p.id, p.parent_page_id, s.include_sub_pages, s.workspace_id
  FROM pages p JOIN shared s ON s.page_id = p.id
  WHERE p.workspace_id = s.workspace_id AND p.deleted_at IS NULL
  UNION ALL
  SELECT child.id, child.parent_page_id, tree.include_sub_pages, tree.workspace_id
  FROM pages child JOIN tree ON child.parent_page_id = tree.id
  WHERE tree.include_sub_pages AND child.workspace_id = tree.workspace_id
    AND child.deleted_at IS NULL
)
SELECT p.id::text, p.title, p.icon, p.parent_page_id::text, p.slug_id, p.creator_id::text,
 p.created_at, p.updated_at,
 similarity(COALESCE(p.title, ''), $2)::real,
 ts_headline('simple', COALESCE(p.text_content, ''), plainto_tsquery('simple', $2)),
 s.id::text, s.name, s.slug
FROM pages p
JOIN tree t ON t.id = p.id
JOIN spaces s ON s.id = p.space_id
WHERE p.deleted_at IS NULL
  AND (COALESCE(p.title, '') ILIKE '%' || $2 || '%'
       OR COALESCE(p.text_content, '') ILIKE '%' || $2 || '%')
  AND NOT EXISTS (
    WITH RECURSIVE ancestors AS (
      SELECT ap.id, ap.parent_page_id
      FROM pages ap WHERE ap.id = p.id AND ap.workspace_id = t.workspace_id
      UNION ALL
      SELECT parent.id, parent.parent_page_id
      FROM pages parent JOIN ancestors a ON a.parent_page_id = parent.id
      WHERE parent.workspace_id = t.workspace_id
    )
    SELECT 1 FROM ancestors a JOIN page_access pa ON pa.page_id = a.id
  )
ORDER BY similarity(COALESCE(p.title, ''), $2) DESC, p.updated_at DESC
LIMIT $3`, shareID, query, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SearchPage, 0)
	for rows.Next() {
		var item domain.SearchPage
		var space domain.SpaceSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.Icon, &item.ParentPageID, &item.SlugID, &item.CreatorID, &item.CreatedAt, &item.UpdatedAt, &item.Rank, &item.Highlight, &space.ID, &space.Name, &space.Slug); err != nil {
			return nil, err
		}
		item.Space = &space
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) Suggestions(ctx context.Context, workspaceID, query string, includeUsers, includeGroups, includePages bool, spaceID *string, limit int, viewerID string, viewerAdmin bool) (map[string]any, error) {
	result := map[string]any{}
	like := "%" + strings.TrimSpace(query) + "%"
	if includeUsers {
		rows, err := repository.db.Query(ctx, `SELECT `+userColumns+` FROM users WHERE workspace_id = $1 AND deleted_at IS NULL AND (name ILIKE $2 OR email ILIKE $2) ORDER BY name LIMIT $3`, workspaceID, like, normalizeLimit(limit))
		if err != nil {
			return nil, err
		}
		users := make([]domain.User, 0)
		for rows.Next() {
			user, scanErr := scanUser(rows)
			if scanErr != nil {
				rows.Close()
				return nil, scanErr
			}
			users = append(users, user)
		}
		rows.Close()
		result["users"] = users
	}
	if includeGroups {
		groups, err := repository.Groups(ctx, workspaceID, limit)
		if err != nil {
			return nil, err
		}
		filtered := make([]domain.Group, 0)
		for _, group := range groups.Items {
			if strings.Contains(strings.ToLower(group.Name), strings.ToLower(strings.TrimSpace(query))) {
				filtered = append(filtered, group)
			}
		}
		result["groups"] = filtered
	}
	if includePages {
		pages, err := repository.SearchPages(ctx, workspaceID, query, spaceID, limit, viewerID, viewerAdmin)
		if err != nil {
			return nil, err
		}
		result["pages"] = pages
	}
	return result, nil
}

func (repository *Repository) CleanupExpiredSessions(ctx context.Context) error {
	now := time.Now().UTC()
	_, err := repository.db.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at < $1 OR revoked_at < $2`, now, now.Add(-7*24*time.Hour))
	return err
}

// TrimExcessSessions mirrors Docmost's session retention policy. Keep the
// newest 25 active sessions for each user so repeated logins cannot grow the
// table without bound. Expired and revoked rows are handled separately by
// CleanupExpiredSessions.
func (repository *Repository) TrimExcessSessions(ctx context.Context) error {
	_, err := repository.db.Exec(ctx, `
DELETE FROM user_sessions
WHERE id IN (
  SELECT id FROM (
    SELECT id, row_number() OVER (
      PARTITION BY user_id ORDER BY last_active_at DESC NULLS LAST, created_at DESC, id DESC
    ) AS session_number
    FROM user_sessions
    WHERE revoked_at IS NULL AND expires_at > now()
  ) active_sessions
  WHERE session_number > 25
)`)
	return err
}
