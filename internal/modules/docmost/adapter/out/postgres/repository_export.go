package postgres

import (
	"context"
	"strings"

	"centipede/internal/modules/docmost/domain"
)

func (repository *Repository) ExportPageTree(ctx context.Context, pageID, workspaceID, viewerID string, viewerAdmin, includeChildren bool) ([]domain.Page, error) {
	tree := `
WITH RECURSIVE tree AS (
  SELECT p.id, p.parent_page_id, 0 AS depth, ARRAY[p.id] AS visited
  FROM pages p
  WHERE p.id = $1 AND p.workspace_id = $2 AND p.deleted_at IS NULL
  UNION ALL
  SELECT child.id, child.parent_page_id, tree.depth + 1, tree.visited || child.id
  FROM pages child JOIN tree ON child.parent_page_id = tree.id
  WHERE $5 AND child.workspace_id = $2 AND child.deleted_at IS NULL
    AND NOT child.id = ANY(tree.visited)
)
SELECT ` + pageColumns + `
FROM pages p JOIN tree ON tree.id = p.id ` + pageJoins + `
WHERE ` + stringsReplacePageAccess("$8", "$3", "$9", "$4") + `
ORDER BY tree.depth, p.position NULLS LAST, p.created_at, p.id`
	rows, err := repository.db.Query(ctx, tree, pageID, workspaceID, viewerID, viewerAdmin, includeChildren)
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

func (repository *Repository) ExportSpacePages(ctx context.Context, spaceID, workspaceID, viewerID string, viewerAdmin bool) ([]domain.Page, error) {
	query := `SELECT ` + pageColumns + ` FROM pages p ` + pageJoins + `
WHERE p.space_id = $1 AND p.workspace_id = $2 AND p.deleted_at IS NULL
  AND ` + stringsReplacePageAccess("$8", "$3", "$9", "$4") + `
ORDER BY p.position NULLS LAST, p.created_at, p.id`
	rows, err := repository.db.Query(ctx, query, spaceID, workspaceID, viewerID, viewerAdmin)
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

func stringsReplacePageAccess(fromViewer, viewer, fromAdmin, admin string) string {
	return strings.NewReplacer(fromViewer, viewer, fromAdmin, admin).Replace(pageListAccessSQL)
}
