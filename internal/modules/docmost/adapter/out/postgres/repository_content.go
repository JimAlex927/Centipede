package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

type duplicatePageRow struct {
	ID           string
	SlugID       string
	Title        *string
	Icon         *string
	CoverPhoto   *string
	Position     *string
	Content      json.RawMessage
	ParentPageID *string
	Depth        int
}

func (repository *Repository) DuplicatePage(ctx context.Context, pageID, targetSpaceID, workspaceID, userID string) (domain.Page, []string, error) {
	rows, err := repository.db.Query(ctx, `
WITH RECURSIVE tree AS (
  SELECT id, slug_id, title, icon, cover_photo, position, content, parent_page_id, 0 AS depth
  FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  UNION ALL
  SELECT p.id, p.slug_id, p.title, p.icon, p.cover_photo, p.position, p.content, p.parent_page_id, tree.depth + 1
  FROM pages p JOIN tree ON p.parent_page_id = tree.id WHERE p.deleted_at IS NULL
)
SELECT id::text, slug_id, title, icon, cover_photo, position,
       COALESCE(content, '{"type":"doc","content":[]}'::jsonb), parent_page_id::text, depth
FROM tree ORDER BY depth, position NULLS LAST, id`, pageID, workspaceID)
	if err != nil {
		return domain.Page{}, nil, err
	}
	defer rows.Close()
	pages := make([]duplicatePageRow, 0)
	for rows.Next() {
		var item duplicatePageRow
		if err = rows.Scan(&item.ID, &item.SlugID, &item.Title, &item.Icon, &item.CoverPhoto, &item.Position, &item.Content, &item.ParentPageID, &item.Depth); err != nil {
			return domain.Page{}, nil, err
		}
		pages = append(pages, item)
	}
	if err = rows.Err(); err != nil {
		return domain.Page{}, nil, err
	}
	if len(pages) == 0 {
		return domain.Page{}, nil, ErrNotFound
	}

	idMap := make(map[string]string, len(pages))
	slugMap := make(map[string]string, len(pages))
	for _, source := range pages {
		newID, uuidErr := newUUID()
		if uuidErr != nil {
			return domain.Page{}, nil, uuidErr
		}
		newSlug, slugErr := randomID(10)
		if slugErr != nil {
			return domain.Page{}, nil, slugErr
		}
		idMap[source.ID] = newID
		slugMap[source.SlugID] = newSlug
	}

	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return domain.Page{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	root := pages[0]
	childIDs := make([]string, 0, len(pages)-1)
	for _, source := range pages {
		newID := idMap[source.ID]
		var parentID *string
		if source.ID == root.ID {
			if targetSpaceID == "" {
				parentID = source.ParentPageID
			}
		} else if source.ParentPageID != nil {
			if mapped, ok := idMap[*source.ParentPageID]; ok {
				parentID = &mapped
			}
		}
		title := source.Title
		if source.ID == root.ID && targetSpaceID == "" {
			value := "Copy of Untitled"
			if source.Title != nil && strings.TrimSpace(*source.Title) != "" {
				value = "Copy of " + *source.Title
			}
			title = &value
		}
		position := source.Position
		if source.ID == root.ID {
			value := fmt.Sprintf("%020d", time.Now().UnixNano())
			position = &value
		}
		content := remapDuplicatedContent(source.Content, idMap, slugMap)
		spaceID := targetSpaceID
		if spaceID == "" {
			var lookupErr error
			lookupErr = tx.QueryRow(ctx, `SELECT space_id::text FROM pages WHERE id = $1`, source.ID).Scan(&spaceID)
			if lookupErr != nil {
				return domain.Page{}, nil, lookupErr
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO pages
(id, slug_id, title, icon, cover_photo, position, content, parent_page_id, creator_id, last_updated_by_id, space_id, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $9, $10, $11)`,
			newID, slugMap[source.SlugID], title, source.Icon, source.CoverPhoto, position, content, parentID, userID, spaceID, workspaceID)
		if err != nil {
			return domain.Page{}, nil, err
		}
		if source.ID != root.ID {
			childIDs = append(childIDs, newID)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Page{}, nil, err
	}
	result, err := repository.PageByID(ctx, idMap[root.ID], "", workspaceID, false)
	return result, childIDs, err
}

func remapDuplicatedContent(raw json.RawMessage, idMap, slugMap map[string]string) json.RawMessage {
	var document any
	if json.Unmarshal(raw, &document) != nil {
		return raw
	}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			typeName, _ := node["type"].(string)
			if attrs, ok := node["attrs"].(map[string]any); ok {
				if typeName == "mention" && attrs["entityType"] == "page" {
					if oldID, ok := attrs["entityId"].(string); ok {
						if newID, found := idMap[oldID]; found {
							attrs["entityId"] = newID
						}
					}
					if oldSlug, ok := attrs["slugId"].(string); ok {
						if newSlug, found := slugMap[oldSlug]; found {
							attrs["slugId"] = newSlug
						}
					}
				}
				if typeName == "transclusionReference" {
					if oldID, ok := attrs["sourcePageId"].(string); ok {
						if newID, found := idMap[oldID]; found {
							attrs["sourcePageId"] = newID
						}
					}
				}
				if href, ok := attrs["href"].(string); ok {
					for oldSlug, newSlug := range slugMap {
						href = strings.ReplaceAll(href, oldSlug, newSlug)
					}
					attrs["href"] = href
				}
			}
			if marks, ok := node["marks"].([]any); ok {
				filtered := marks[:0]
				for _, mark := range marks {
					entry, _ := mark.(map[string]any)
					if entry["type"] != "comment" {
						filtered = append(filtered, mark)
					}
				}
				node["marks"] = filtered
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		return raw
	}
	return encoded
}

func (repository *Repository) BacklinkCount(ctx context.Context, pageID, workspaceID, userID string, workspaceAdmin bool) (int64, int64, error) {
	var incoming, outgoing int64
	err := repository.db.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE b.target_page_id = $1),
  count(*) FILTER (WHERE b.source_page_id = $1)
FROM backlinks b
JOIN pages p ON p.id = CASE WHEN b.target_page_id = $1 THEN b.source_page_id ELSE b.target_page_id END
WHERE b.workspace_id = $2 AND p.workspace_id = $2 AND (b.target_page_id = $1 OR b.source_page_id = $1) AND p.deleted_at IS NULL
AND `+strings.NewReplacer("$8", "$3", "$9", "$4").Replace(pageListAccessSQL),
		pageID, workspaceID, userID, workspaceAdmin).Scan(&incoming, &outgoing)
	return incoming, outgoing, err
}

func (repository *Repository) BacklinkPages(ctx context.Context, pageID, workspaceID, userID, direction string, workspaceAdmin bool, limit int) (domain.Pagination[domain.BacklinkPage], error) {
	if direction != "incoming" && direction != "outgoing" {
		return domain.Pagination[domain.BacklinkPage]{}, ErrInvalidInput
	}
	rows, err := repository.db.Query(ctx, `
SELECT p.id::text, p.slug_id, p.title, p.icon, p.space_id::text, p.updated_at,
       s.id::text, s.name, s.slug
FROM backlinks b
JOIN pages p ON p.id = CASE WHEN $4 = 'incoming' THEN b.source_page_id ELSE b.target_page_id END
JOIN spaces s ON s.id = p.space_id
WHERE b.workspace_id = $2
  AND (($4 = 'incoming' AND b.target_page_id = $1) OR ($4 = 'outgoing' AND b.source_page_id = $1))
  AND p.deleted_at IS NULL
  AND p.workspace_id = $2
  AND `+strings.NewReplacer("$8", "$3", "$9", "$5").Replace(pageListAccessSQL)+`
ORDER BY p.updated_at DESC, p.id DESC LIMIT $6`, pageID, workspaceID, userID, direction, workspaceAdmin, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.BacklinkPage]{}, err
	}
	defer rows.Close()
	items := make([]domain.BacklinkPage, 0)
	for rows.Next() {
		var item domain.BacklinkPage
		var space domain.SpaceSummary
		if err = rows.Scan(&item.ID, &item.SlugID, &item.Title, &item.Icon, &item.SpaceID, &item.UpdatedAt, &space.ID, &space.Name, &space.Slug); err != nil {
			return domain.Pagination[domain.BacklinkPage]{}, err
		}
		item.Space = &space
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

const pageHistoryColumns = `
h.id::text, h.page_id::text, h.slug_id, h.title, h.content, h.slug, h.icon,
h.cover_photo, h.version, h.last_updated_by_id::text,
COALESCE(h.contributor_ids, '{}'::uuid[]), h.space_id::text, h.workspace_id::text,
h.created_at, h.updated_at, editor.id::text, editor.name, editor.avatar_url`

func scanPageHistory(row rowScanner) (domain.PageHistory, error) {
	var history domain.PageHistory
	var editorID, editorName, editorAvatar *string
	err := row.Scan(&history.ID, &history.PageID, &history.SlugID, &history.Title,
		&history.Content, &history.Slug, &history.Icon, &history.CoverPhoto,
		&history.Version, &history.LastUpdatedByID, &history.ContributorIDs,
		&history.SpaceID, &history.WorkspaceID, &history.CreatedAt, &history.UpdatedAt,
		&editorID, &editorName, &editorAvatar)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PageHistory{}, ErrNotFound
	}
	if err == nil && editorID != nil {
		history.LastUpdatedBy = &domain.UserSummary{ID: *editorID, Name: editorName, AvatarURL: editorAvatar}
	}
	return history, err
}

func (repository *Repository) PageHistory(ctx context.Context, pageID, workspaceID string, limit int) (domain.Pagination[domain.PageHistory], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+pageHistoryColumns+`
FROM page_history h LEFT JOIN users editor ON editor.id = h.last_updated_by_id
WHERE h.page_id = $1 AND h.workspace_id = $2 ORDER BY h.id DESC LIMIT $3`, pageID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.PageHistory]{}, err
	}
	defer rows.Close()
	items := make([]domain.PageHistory, 0)
	for rows.Next() {
		item, scanErr := scanPageHistory(rows)
		if scanErr != nil {
			return domain.Pagination[domain.PageHistory]{}, scanErr
		}
		item.Content = nil
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) PageHistoryByID(ctx context.Context, historyID, workspaceID string) (domain.PageHistory, error) {
	return scanPageHistory(repository.db.QueryRow(ctx, `SELECT `+pageHistoryColumns+`
FROM page_history h LEFT JOIN users editor ON editor.id = h.last_updated_by_id
WHERE h.id = $1 AND h.workspace_id = $2`, historyID, workspaceID))
}
