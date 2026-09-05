package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

type PageRestriction struct {
	Direct        bool
	Inherited     bool
	RestrictionID string
	InheritedFrom *domain.PageSummary
}

// PageRestriction describes direct and nearest inherited restrictions without
// changing the database. The recursive query is cycle-safe for corrupted page
// trees, matching the access checks used elsewhere in the Go backend.
func (repository *Repository) PageRestriction(ctx context.Context, pageID, workspaceID string) (PageRestriction, error) {
	rows, err := repository.db.Query(ctx, `
WITH RECURSIVE ancestors AS (
  SELECT p.id, p.parent_page_id, 0 AS depth, ARRAY[p.id] AS visited
  FROM pages p
  WHERE p.id = $1 AND p.workspace_id = $2 AND p.deleted_at IS NULL
  UNION ALL
  SELECT p.id, p.parent_page_id, a.depth + 1, a.visited || p.id
  FROM pages p JOIN ancestors a ON a.parent_page_id = p.id
  WHERE p.workspace_id = $2 AND p.deleted_at IS NULL AND NOT p.id = ANY(a.visited)
)
SELECT pa.id::text, a.depth, p.id::text, p.slug_id, p.title, p.icon,
       COALESCE(p.is_base, false), p.space_id::text
FROM ancestors a
JOIN page_access pa ON pa.page_id = a.id
JOIN pages p ON p.id = a.id
ORDER BY a.depth ASC`, pageID, workspaceID)
	if err != nil {
		return PageRestriction{}, err
	}
	defer rows.Close()

	result := PageRestriction{}
	var foundPage bool
	for rows.Next() {
		var restrictionID, pageID, slugID, spaceID string
		var depth int
		var title, icon *string
		var isBase bool
		if err := rows.Scan(&restrictionID, &depth, &pageID, &slugID, &title, &icon, &isBase, &spaceID); err != nil {
			return PageRestriction{}, err
		}
		foundPage = true
		if depth == 0 {
			result.Direct = true
			result.RestrictionID = restrictionID
			continue
		}
		if !result.Inherited {
			result.Inherited = true
			result.InheritedFrom = &domain.PageSummary{ID: pageID, SlugID: slugID, Title: title, Icon: icon, IsBase: isBase, SpaceID: spaceID}
		}
	}
	if err := rows.Err(); err != nil {
		return PageRestriction{}, err
	}
	if !foundPage {
		var exists bool
		if err := repository.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL)`, pageID, workspaceID).Scan(&exists); err != nil {
			return PageRestriction{}, err
		}
		if !exists {
			return PageRestriction{}, ErrNotFound
		}
	}
	return result, nil
}

func (repository *Repository) RestrictPage(ctx context.Context, pageID, workspaceID, creatorID string) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	result, err := repository.db.Exec(ctx, `
INSERT INTO page_access (id, page_id, workspace_id, space_id, access_level, creator_id)
SELECT $1, p.id, p.workspace_id, p.space_id, 'restricted', $4
FROM pages p
WHERE p.id = $2 AND p.workspace_id = $3 AND p.deleted_at IS NULL
ON CONFLICT (page_id) DO UPDATE SET updated_at = now()`, id, pageID, workspaceID, creatorID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) RemovePageRestriction(ctx context.Context, pageID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `DELETE FROM page_access WHERE page_id = $1 AND workspace_id = $2`, pageID, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err := repository.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL)`, pageID, workspaceID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func (repository *Repository) pageAccessID(ctx context.Context, pageID, workspaceID string) (string, error) {
	var id string
	err := repository.db.QueryRow(ctx, `SELECT id::text FROM page_access WHERE page_id = $1 AND workspace_id = $2`, pageID, workspaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func (repository *Repository) AddPagePermissions(ctx context.Context, pageID, workspaceID, addedByID, role string, userIDs, groupIDs []string) error {
	if role != "reader" && role != "writer" {
		return fmt.Errorf("role must be reader or writer")
	}
	accessID, err := repository.pageAccessID(ctx, pageID, workspaceID)
	if err != nil {
		return err
	}
	for _, userID := range uniqueIDs(userIDs) {
		id, idErr := newUUID()
		if idErr != nil {
			return idErr
		}
		result, execErr := repository.db.Exec(ctx, `
INSERT INTO page_permissions (id, page_access_id, user_id, role, added_by_id)
SELECT $1, $2, u.id, $3, $4 FROM users u
WHERE u.id = $5 AND u.workspace_id = $6 AND u.deleted_at IS NULL
ON CONFLICT (page_access_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = now()`, id, accessID, role, addedByID, userID, workspaceID)
		if execErr != nil {
			return execErr
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	for _, groupID := range uniqueIDs(groupIDs) {
		id, idErr := newUUID()
		if idErr != nil {
			return idErr
		}
		result, execErr := repository.db.Exec(ctx, `
INSERT INTO page_permissions (id, page_access_id, group_id, role, added_by_id)
SELECT $1, $2, g.id, $3, $4 FROM groups g
WHERE g.id = $5 AND g.workspace_id = $6 AND g.deleted_at IS NULL
ON CONFLICT (page_access_id, group_id) DO UPDATE SET role = EXCLUDED.role, updated_at = now()`, id, accessID, role, addedByID, groupID, workspaceID)
		if execErr != nil {
			return execErr
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	return nil
}

func (repository *Repository) RemovePagePermissions(ctx context.Context, pageID, workspaceID string, userIDs, groupIDs []string) error {
	accessID, err := repository.pageAccessID(ctx, pageID, workspaceID)
	if err != nil {
		return err
	}
	if ids := uniqueIDs(userIDs); len(ids) > 0 {
		if _, err = repository.db.Exec(ctx, `DELETE FROM page_permissions WHERE page_access_id = $1 AND user_id = ANY($2::uuid[])`, accessID, ids); err != nil {
			return err
		}
	}
	if ids := uniqueIDs(groupIDs); len(ids) > 0 {
		if _, err = repository.db.Exec(ctx, `DELETE FROM page_permissions WHERE page_access_id = $1 AND group_id = ANY($2::uuid[])`, accessID, ids); err != nil {
			return err
		}
	}
	return nil
}

func (repository *Repository) UpdatePagePermissionRole(ctx context.Context, pageID, workspaceID, role string, userID, groupID *string) error {
	if role != "reader" && role != "writer" {
		return fmt.Errorf("role must be reader or writer")
	}
	accessID, err := repository.pageAccessID(ctx, pageID, workspaceID)
	if err != nil {
		return err
	}
	var result pgconnCommandTag
	if userID != nil {
		result, err = repository.db.Exec(ctx, `UPDATE page_permissions SET role = $3, updated_at = now() WHERE page_access_id = $1 AND user_id = $2`, accessID, *userID, role)
	} else if groupID != nil {
		result, err = repository.db.Exec(ctx, `UPDATE page_permissions SET role = $3, updated_at = now() WHERE page_access_id = $1 AND group_id = $2`, accessID, *groupID, role)
	} else {
		return ErrInvalidInput
	}
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) PagePermissionMembers(ctx context.Context, pageID, workspaceID string, limit int) (domain.Pagination[domain.PagePermissionMember], error) {
	accessID, err := repository.pageAccessID(ctx, pageID, workspaceID)
	if err != nil {
		return domain.Pagination[domain.PagePermissionMember]{}, err
	}
	rows, err := repository.db.Query(ctx, `
SELECT pp.role, pp.created_at, u.id::text, COALESCE(u.name, ''), u.email, u.avatar_url,
       NULL::text, NULL::bigint, false
FROM page_permissions pp JOIN users u ON u.id = pp.user_id
WHERE pp.page_access_id = $1 AND u.deleted_at IS NULL
UNION ALL
SELECT pp.role, pp.created_at, g.id::text, g.name, NULL::text, NULL::text,
       'group', (SELECT count(*) FROM group_users gu WHERE gu.group_id = g.id), g.is_default
FROM page_permissions pp JOIN groups g ON g.id = pp.group_id
WHERE pp.page_access_id = $1 AND g.deleted_at IS NULL
ORDER BY 7 DESC NULLS LAST, 4, 3
LIMIT $2`, accessID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.PagePermissionMember]{}, err
	}
	defer rows.Close()
	items := make([]domain.PagePermissionMember, 0)
	for rows.Next() {
		var item domain.PagePermissionMember
		var email, kind *string
		if err := rows.Scan(&item.Role, &item.CreatedAt, &item.ID, &item.Name, &email, &item.AvatarURL, &kind, &item.MemberCount, &item.IsDefault); err != nil {
			return domain.Pagination[domain.PagePermissionMember]{}, err
		}
		if kind == nil {
			item.Type = "user"
			item.Email = derefString(email)
		} else {
			item.Type = "group"
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func uniqueIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
