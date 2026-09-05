package postgres

import (
	"centipede/internal/modules/docmost/domain"
	"context"
	"encoding/json"
	"strings"
)

type LabelPageFilter struct {
	LabelID string `json:"labelId"`
	Name    string `json:"name"`
	SpaceID string `json:"spaceId"`
	Query   string `json:"query"`
	Cursor  string `json:"cursor"`
	Limit   int    `json:"limit"`
}

const labelPageWhere = `p.workspace_id::text=$1 AND p.deleted_at IS NULL
 AND ($2='' OR p.space_id::text=$2)
 AND EXISTS (SELECT 1 FROM page_labels pl JOIN labels l ON l.id=pl.label_id
 WHERE pl.page_id=p.id AND l.workspace_id=p.workspace_id AND l.type='page'
 AND (($3<>'' AND l.id::text=$3) OR ($3='' AND l.name=$4)))`

func (repository *Repository) LabelUsage(ctx context.Context, workspaceID, userID string, admin bool, filter LabelPageFilter) (int, error) {
	var count int
	err := repository.db.QueryRow(ctx, `SELECT count(*) FROM pages p WHERE `+labelPageWhere+` AND `+
		strings.NewReplacer("$8", "$5", "$9", "$6").Replace(pageListAccessSQL),
		workspaceID, filter.SpaceID, filter.LabelID, strings.TrimSpace(filter.Name), userID, admin).Scan(&count)
	return count, err
}

func (repository *Repository) PagesByLabel(ctx context.Context, workspaceID, userID string, admin bool, filter LabelPageFilter) (domain.Pagination[map[string]any], error) {
	limit := normalizeLimit(filter.Limit)
	rows, err := repository.db.Query(ctx, `SELECT jsonb_build_object(
 'id',p.id,'slugId',p.slug_id,'title',p.title,'icon',p.icon,'spaceId',p.space_id,
 'createdAt',p.created_at,'updatedAt',p.updated_at,
 'space',(SELECT jsonb_build_object('id',s.id,'name',s.name,'slug',s.slug,'logo',s.logo) FROM spaces s WHERE s.id=p.space_id),
 'creator',(SELECT jsonb_build_object('id',u.id,'name',u.name,'avatarUrl',u.avatar_url) FROM users u WHERE u.id=p.creator_id),
 'labels',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',l.id,'name',l.name) ORDER BY l.name)
 FROM page_labels pl JOIN labels l ON l.id=pl.label_id WHERE pl.page_id=p.id AND l.workspace_id=p.workspace_id),'[]'::jsonb))
 FROM pages p WHERE `+labelPageWhere+` AND `+
		strings.NewReplacer("$8", "$5", "$9", "$6").Replace(pageListAccessSQL)+`
 AND ($7='' OR COALESCE(p.title,'') ILIKE '%' || $7 || '%')
 AND ($8='' OR p.id::text>$8) ORDER BY p.id LIMIT $9`,
		workspaceID, filter.SpaceID, filter.LabelID, strings.TrimSpace(filter.Name), userID, admin, filter.Query, filter.Cursor, limit+1)
	if err != nil {
		return domain.Pagination[map[string]any]{}, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return domain.Pagination[map[string]any]{}, err
		}
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			return domain.Pagination[map[string]any]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[map[string]any]{}, err
	}
	hasNext := len(items) > limit
	if hasNext {
		items = items[:limit]
	}
	result := page(items, limit)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = filter.Cursor != ""
	if hasNext {
		cursor := items[len(items)-1]["id"].(string)
		result.Meta.NextCursor = &cursor
	}
	return result, nil
}
