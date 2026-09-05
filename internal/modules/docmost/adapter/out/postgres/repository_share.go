package postgres

import (
	"context"
	"errors"
	"strings"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

const shareColumns = `
sh.id::text, sh.key, sh.page_id::text, COALESCE(sh.include_sub_pages, false),
COALESCE(sh.search_indexing, false), sh.creator_id::text, sh.space_id::text,
sh.workspace_id::text, sh.created_at, sh.updated_at, sh.deleted_at`

func scanShare(row rowScanner) (domain.Share, error) {
	var share domain.Share
	err := row.Scan(&share.ID, &share.Key, &share.PageID, &share.IncludeSubPages,
		&share.SearchIndexing, &share.CreatorID, &share.SpaceID, &share.WorkspaceID,
		&share.CreatedAt, &share.UpdatedAt, &share.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Share{}, ErrNotFound
	}
	return share, err
}

func (repository *Repository) ShareByID(ctx context.Context, shareID string) (domain.Share, error) {
	return scanShare(repository.db.QueryRow(ctx, `SELECT `+shareColumns+` FROM shares sh WHERE (sh.id::text = $1 OR lower(sh.key) = lower($1)) AND sh.deleted_at IS NULL`, shareID))
}

func (repository *Repository) ShareByPage(ctx context.Context, pageID, workspaceID string) (domain.Share, error) {
	return scanShare(repository.db.QueryRow(ctx, `SELECT `+shareColumns+` FROM shares sh WHERE sh.page_id = $1 AND sh.workspace_id = $2 AND sh.deleted_at IS NULL`, pageID, workspaceID))
}

func (repository *Repository) ShareForPage(ctx context.Context, pageID, workspaceID string) (domain.Share, error) {
	row := repository.db.QueryRow(ctx, `
WITH RECURSIVE hierarchy AS (
  SELECT p.id, p.slug_id, p.title, p.icon, p.parent_page_id, 0 AS level
  FROM pages p WHERE (p.id::text = $1 OR p.slug_id = $1) AND p.workspace_id = $2 AND p.deleted_at IS NULL
  UNION ALL
  SELECT parent.id, parent.slug_id, parent.title, parent.icon, parent.parent_page_id, hierarchy.level + 1
  FROM pages parent JOIN hierarchy ON hierarchy.parent_page_id = parent.id
  WHERE parent.deleted_at IS NULL AND parent.workspace_id = $2 AND hierarchy.level < 25
)
SELECT `+shareColumns+`, hierarchy.level,
       hierarchy.id::text, hierarchy.slug_id, hierarchy.title, hierarchy.icon, false, sh.space_id::text
FROM hierarchy JOIN shares sh ON sh.page_id = hierarchy.id
WHERE sh.workspace_id = $2 AND sh.deleted_at IS NULL
  AND (hierarchy.level = 0 OR COALESCE(sh.include_sub_pages, false))
ORDER BY hierarchy.level LIMIT 1`, pageID, workspaceID)
	var share domain.Share
	var shared domain.PageSummary
	err := row.Scan(&share.ID, &share.Key, &share.PageID, &share.IncludeSubPages,
		&share.SearchIndexing, &share.CreatorID, &share.SpaceID, &share.WorkspaceID,
		&share.CreatedAt, &share.UpdatedAt, &share.DeletedAt, &share.Level,
		&shared.ID, &shared.SlugID, &shared.Title, &shared.Icon, &shared.IsBase, &shared.SpaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Share{}, ErrNotFound
	}
	if err == nil {
		share.SharedPage = &shared
	}
	return share, err
}

func (repository *Repository) SharingAllowed(ctx context.Context, workspaceID, spaceID string) (bool, error) {
	var allowed bool
	err := repository.db.QueryRow(ctx, `
SELECT NOT (COALESCE((w.settings->'sharing'->>'disabled')::boolean, false)
             OR COALESCE((s.settings->'sharing'->>'disabled')::boolean, false))
FROM workspaces w JOIN spaces s ON s.workspace_id = w.id
WHERE w.id = $1 AND s.id = $2 AND w.deleted_at IS NULL AND s.deleted_at IS NULL`, workspaceID, spaceID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return allowed, err
}

func (repository *Repository) PageHasRestrictedAncestor(ctx context.Context, pageID, workspaceID string) (bool, error) {
	var restricted bool
	err := repository.db.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
SELECT id, parent_page_id FROM pages WHERE id = $1 AND workspace_id = $2
UNION
SELECT p.id, p.parent_page_id FROM pages p JOIN ancestors a ON a.parent_page_id = p.id WHERE p.workspace_id = $2
)
SELECT EXISTS(SELECT 1 FROM ancestors a JOIN page_access pa ON pa.page_id = a.id)`, pageID, workspaceID).Scan(&restricted)
	return restricted, err
}

type PageAccessResult struct {
	HasRestriction bool
	CanAccess      bool
	CanEdit        bool
}

// PageAccess evaluates all restricted ancestors. A restriction on any
// ancestor denies traversal unless the user has a matching user/group entry;
// the nearest restricted ancestor determines the effective writer role.
func (repository *Repository) PageAccess(ctx context.Context, pageID, workspaceID, userID string) (PageAccessResult, error) {
	var result PageAccessResult
	var exists bool
	err := repository.db.QueryRow(ctx, `
WITH RECURSIVE ancestors AS (
  SELECT id, parent_page_id, 0 AS depth, ARRAY[id] AS visited
  FROM pages
  WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  UNION ALL
  SELECT p.id, p.parent_page_id, a.depth + 1, a.visited || p.id
  FROM pages p JOIN ancestors a ON a.parent_page_id = p.id
  WHERE p.workspace_id = $2 AND NOT p.id = ANY(a.visited)
)
SELECT
  EXISTS(SELECT 1 FROM ancestors),
  COUNT(pa.id) > 0,
  COALESCE(bool_and(pp.id IS NOT NULL) FILTER (WHERE pa.id IS NOT NULL), true),
  COALESCE((array_agg(pp.role ORDER BY a.depth ASC, pp.role DESC NULLS LAST)
    FILTER (WHERE pa.id IS NOT NULL))[1] = 'writer', false)
FROM ancestors a
LEFT JOIN page_access pa ON pa.page_id = a.id
LEFT JOIN page_permissions pp ON pp.page_access_id = pa.id
  AND (pp.user_id = $3 OR pp.group_id IN (
    SELECT gu.group_id FROM group_users gu WHERE gu.user_id = $3
  ))`, pageID, workspaceID, userID).Scan(&exists, &result.HasRestriction, &result.CanAccess, &result.CanEdit)
	if err != nil {
		return PageAccessResult{}, err
	}
	if !exists {
		return PageAccessResult{}, ErrNotFound
	}
	if !result.HasRestriction {
		result.CanAccess = true
		result.CanEdit = true
	}
	result.CanEdit = result.CanAccess && result.CanEdit
	return result, nil
}

func (repository *Repository) ShareContainsPage(ctx context.Context, share domain.Share, pageID string) (bool, error) {
	var contains bool
	err := repository.db.QueryRow(ctx, `WITH RECURSIVE target AS (
	  SELECT id, parent_page_id FROM pages
	  WHERE (id::text = $1 OR slug_id = $1) AND workspace_id = $2 AND deleted_at IS NULL
	), ancestors AS (
	  SELECT id, parent_page_id FROM target
	  UNION
	  SELECT p.id, p.parent_page_id FROM pages p JOIN ancestors a ON a.parent_page_id = p.id
	  WHERE p.workspace_id = $2 AND p.deleted_at IS NULL
	)
	SELECT EXISTS(SELECT 1 FROM ancestors WHERE id = $3)
	  AND ($4 OR EXISTS(SELECT 1 FROM target WHERE id = $3))`,
		pageID, share.WorkspaceID, share.PageID, share.IncludeSubPages).Scan(&contains)
	return contains, err
}

func (repository *Repository) CreateShare(ctx context.Context, pageID, spaceID, workspaceID, creatorID string, includeSubPages, searchIndexing bool) (domain.Share, error) {
	if existing, err := repository.ShareByPage(ctx, pageID, workspaceID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return domain.Share{}, err
	}
	key, err := randomID(12)
	if err != nil {
		return domain.Share{}, err
	}
	key = strings.ToLower(key)
	return scanShare(repository.db.QueryRow(ctx, `INSERT INTO shares
(key, page_id, include_sub_pages, search_indexing, creator_id, space_id, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+strings.ReplaceAll(shareColumns, "sh.", ""),
		key, pageID, includeSubPages, searchIndexing, creatorID, spaceID, workspaceID))
}

func (repository *Repository) UpdateShare(ctx context.Context, shareID, workspaceID string, includeSubPages, searchIndexing *bool) (domain.Share, error) {
	result, err := repository.db.Exec(ctx, `UPDATE shares SET include_sub_pages = COALESCE($3, include_sub_pages), search_indexing = COALESCE($4, search_indexing), updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, shareID, workspaceID, includeSubPages, searchIndexing)
	if err != nil {
		return domain.Share{}, err
	}
	if result.RowsAffected() == 0 {
		return domain.Share{}, ErrNotFound
	}
	return repository.ShareByID(ctx, shareID)
}

func (repository *Repository) DeleteShare(ctx context.Context, shareID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `DELETE FROM shares WHERE id = $1 AND workspace_id = $2`, shareID, workspaceID)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (repository *Repository) DeleteSharesByWorkspace(ctx context.Context, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM shares WHERE workspace_id = $1`, workspaceID)
	return err
}

func (repository *Repository) DeleteSharesBySpace(ctx context.Context, spaceID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM shares WHERE space_id = $1 AND workspace_id = $2`, spaceID, workspaceID)
	return err
}

func (repository *Repository) Shares(ctx context.Context, workspaceID, userID string, workspaceAdmin bool, limit int) (domain.Pagination[domain.Share], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+shareColumns+`,
  p.id::text, p.slug_id, p.title, p.icon, COALESCE(p.is_base, false), p.space_id::text,
  s.id::text, s.name, s.slug,
  creator.id::text, creator.name, creator.avatar_url
FROM shares sh JOIN spaces s ON s.id = sh.space_id JOIN pages p ON p.id = sh.page_id
LEFT JOIN users creator ON creator.id = sh.creator_id
WHERE sh.workspace_id = $1 AND sh.deleted_at IS NULL AND ($3 OR s.visibility = 'public'
  OR EXISTS (SELECT 1 FROM space_members sm WHERE sm.space_id = s.id AND sm.user_id = $2 AND sm.deleted_at IS NULL)
  OR EXISTS (SELECT 1 FROM space_members sm JOIN group_users gu ON gu.group_id = sm.group_id WHERE sm.space_id = s.id AND gu.user_id = $2 AND sm.deleted_at IS NULL))
ORDER BY sh.updated_at DESC, sh.id DESC LIMIT $4`, workspaceID, userID, workspaceAdmin, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Share]{}, err
	}
	defer rows.Close()
	items := make([]domain.Share, 0)
	for rows.Next() {
		var item domain.Share
		var pageSummary domain.PageSummary
		var spaceSummary domain.SpaceSummary
		var creatorID, creatorName, creatorAvatar *string
		if err = rows.Scan(&item.ID, &item.Key, &item.PageID, &item.IncludeSubPages,
			&item.SearchIndexing, &item.CreatorID, &item.SpaceID, &item.WorkspaceID,
			&item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
			&pageSummary.ID, &pageSummary.SlugID, &pageSummary.Title, &pageSummary.Icon, &pageSummary.IsBase, &pageSummary.SpaceID,
			&spaceSummary.ID, &spaceSummary.Name, &spaceSummary.Slug,
			&creatorID, &creatorName, &creatorAvatar); err != nil {
			return domain.Pagination[domain.Share]{}, err
		}
		item.Page = &pageSummary
		item.Space = &spaceSummary
		if creatorID != nil {
			item.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) SharedPageTree(ctx context.Context, share domain.Share) ([]domain.Page, error) {
	if !share.IncludeSubPages {
		return []domain.Page{}, nil
	}
	rows, err := repository.db.Query(ctx, `WITH RECURSIVE tree AS (
  SELECT id FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  UNION SELECT p.id FROM pages p JOIN tree ON p.parent_page_id = tree.id
  WHERE p.deleted_at IS NULL AND p.workspace_id = $2
  AND NOT EXISTS (SELECT 1 FROM page_access pa WHERE pa.page_id = p.id)
)
SELECT `+pageColumns+` FROM pages p `+pageJoins+` WHERE p.id IN (SELECT id FROM tree) AND p.id <> $1 ORDER BY p.position NULLS LAST, p.created_at`, share.PageID, share.WorkspaceID)
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
		item.Content = nil
		items = append(items, item)
	}
	return items, rows.Err()
}
