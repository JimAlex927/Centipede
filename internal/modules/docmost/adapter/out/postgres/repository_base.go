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

type Base struct {
	ID                string         `json:"id"`
	SlugID            string         `json:"slugId"`
	Name              string         `json:"name"`
	Description       *string        `json:"description,omitempty"`
	Icon              *string        `json:"icon,omitempty"`
	PageID            string         `json:"pageId"`
	SpaceID           string         `json:"spaceId"`
	WorkspaceID       string         `json:"workspaceId"`
	CreatorID         *string        `json:"creatorId"`
	Properties        []BaseProperty `json:"properties"`
	Views             []BaseView     `json:"views"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	BaseSchemaVersion int            `json:"baseSchemaVersion"`
}

type BaseProperty struct {
	ID                 string          `json:"id"`
	PageID             string          `json:"pageId"`
	Name               string          `json:"name"`
	Type               string          `json:"type"`
	Position           string          `json:"position"`
	TypeOptions        json.RawMessage `json:"typeOptions"`
	PendingType        *string         `json:"pendingType"`
	PendingTypeOptions json.RawMessage `json:"pendingTypeOptions,omitempty"`
	IsPrimary          bool            `json:"isPrimary"`
	WorkspaceID        string          `json:"workspaceId"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type BaseRow struct {
	ID              string          `json:"id"`
	PageID          string          `json:"pageId"`
	Cells           json.RawMessage `json:"cells"`
	Position        string          `json:"position"`
	CreatorID       *string         `json:"creatorId"`
	LastUpdatedByID *string         `json:"lastUpdatedById"`
	WorkspaceID     string          `json:"workspaceId"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type BaseView struct {
	ID          string          `json:"id"`
	PageID      string          `json:"pageId"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Position    string          `json:"position"`
	Config      json.RawMessage `json:"config"`
	WorkspaceID string          `json:"workspaceId"`
	CreatorID   *string         `json:"creatorId"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func (repository *Repository) BaseByPage(ctx context.Context, pageID, workspaceID string) (Base, error) {
	page, err := repository.PageByID(ctx, pageID, pageID, workspaceID, false)
	if err != nil {
		return Base{}, err
	}
	if !page.IsBase {
		return Base{}, ErrNotFound
	}
	var schemaVersion int
	if err = repository.db.QueryRow(ctx, `SELECT COALESCE(base_schema_version, 0) FROM pages WHERE id = $1 AND workspace_id = $2`, page.ID, workspaceID).Scan(&schemaVersion); err != nil {
		return Base{}, err
	}
	properties, err := repository.BaseProperties(ctx, page.ID, workspaceID)
	if err != nil {
		return Base{}, err
	}
	views, err := repository.BaseViews(ctx, page.ID, workspaceID)
	if err != nil {
		return Base{}, err
	}
	name := "Untitled"
	if page.Title != nil && strings.TrimSpace(*page.Title) != "" {
		name = *page.Title
	}
	return Base{ID: page.ID, SlugID: page.SlugID, Name: name, Icon: page.Icon, PageID: page.ID, SpaceID: page.SpaceID, WorkspaceID: page.WorkspaceID, CreatorID: page.CreatorID, Properties: properties, Views: views, CreatedAt: page.CreatedAt, UpdatedAt: page.UpdatedAt, BaseSchemaVersion: schemaVersion}, nil
}

func (repository *Repository) Bases(ctx context.Context, workspaceID, spaceID, cursor string, limit int) (domain.Pagination[Base], error) {
	limit = normalizeLimit(limit)
	rows, err := repository.db.Query(ctx, `
SELECT id::text FROM pages
WHERE workspace_id = $1 AND space_id = $2 AND is_base = true AND deleted_at IS NULL
  AND ($3 = '' OR id::text > $3)
ORDER BY id LIMIT $4`, workspaceID, spaceID, cursor, limit+1)
	if err != nil {
		return domain.Pagination[Base]{}, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return domain.Pagination[Base]{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[Base]{}, err
	}
	hasNext := len(ids) > limit
	if hasNext {
		ids = ids[:limit]
	}
	items := make([]Base, 0, len(ids))
	for _, id := range ids {
		item, itemErr := repository.BaseByPage(ctx, id, workspaceID)
		if itemErr != nil {
			return domain.Pagination[Base]{}, itemErr
		}
		items = append(items, item)
	}
	result := page(items, limit)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = cursor != ""
	if hasNext && len(items) > 0 {
		next := items[len(items)-1].ID
		result.Meta.NextCursor = &next
	}
	return result, nil
}

func (repository *Repository) SetPageAsBase(ctx context.Context, pageID, workspaceID string, isBase bool) error {
	result, err := repository.db.Exec(ctx, `UPDATE pages SET is_base = $3, base_schema_version = CASE WHEN $3 THEN GREATEST(base_schema_version, 1) ELSE base_schema_version END, updated_at = now() WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, pageID, workspaceID, isBase)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) EnsureBaseDefaults(ctx context.Context, pageID, workspaceID, creatorID, template string) error {
	properties, err := repository.BaseProperties(ctx, pageID, workspaceID)
	if err != nil {
		return err
	}
	if len(properties) == 0 {
		if _, err = repository.CreateBaseProperty(ctx, BaseProperty{
			ID: "title", PageID: pageID, Name: "Name", Type: "text", Position: "a0",
			TypeOptions: json.RawMessage(`{"richText": false}`), IsPrimary: true, WorkspaceID: workspaceID,
		}); err != nil {
			return err
		}
	}
	views, err := repository.BaseViews(ctx, pageID, workspaceID)
	if err != nil {
		return err
	}
	if len(views) == 0 {
		viewType := "table"
		if template == "kanban" {
			viewType = "kanban"
		}
		if _, err = repository.CreateBaseView(ctx, pageID, workspaceID, creatorID, "All items", viewType, "a0", json.RawMessage(`{}`)); err != nil {
			return err
		}
	}
	return nil
}

func (repository *Repository) BaseProperties(ctx context.Context, pageID, workspaceID string) ([]BaseProperty, error) {
	rows, err := repository.db.Query(ctx, `
SELECT id, page_id::text, name, type, position, COALESCE(type_options, '{}'::jsonb),
       pending_type, COALESCE(pending_type_options, '{}'::jsonb), is_primary,
       workspace_id::text, created_at, updated_at
FROM base_properties WHERE page_id = $1 AND workspace_id = $2 AND deleted_at IS NULL
ORDER BY position COLLATE "C", id`, pageID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BaseProperty, 0)
	for rows.Next() {
		var item BaseProperty
		if err := rows.Scan(&item.ID, &item.PageID, &item.Name, &item.Type, &item.Position, &item.TypeOptions, &item.PendingType, &item.PendingTypeOptions, &item.IsPrimary, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) CreateBaseProperty(ctx context.Context, property BaseProperty) (BaseProperty, error) {
	if property.ID == "" {
		id, err := randomID(10)
		if err != nil {
			return BaseProperty{}, err
		}
		property.ID = id
	}
	if property.Position == "" {
		property.Position = fmt.Sprintf("%020d", time.Now().UnixNano())
	}
	if len(property.TypeOptions) == 0 {
		property.TypeOptions = json.RawMessage(`{}`)
	}
	_, err := repository.db.Exec(ctx, `
INSERT INTO base_properties (id, page_id, name, type, position, type_options, is_primary, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)`, property.ID, property.PageID, property.Name, property.Type, property.Position, property.TypeOptions, property.IsPrimary, property.WorkspaceID)
	if err != nil {
		return BaseProperty{}, err
	}
	return repository.BasePropertyByID(ctx, property.PageID, property.ID, property.WorkspaceID)
}

func (repository *Repository) BasePropertyByID(ctx context.Context, pageID, propertyID, workspaceID string) (BaseProperty, error) {
	var item BaseProperty
	err := repository.db.QueryRow(ctx, `
SELECT id, page_id::text, name, type, position, COALESCE(type_options, '{}'::jsonb),
       pending_type, COALESCE(pending_type_options, '{}'::jsonb), is_primary,
       workspace_id::text, created_at, updated_at
FROM base_properties WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL`, pageID, propertyID, workspaceID).Scan(
		&item.ID, &item.PageID, &item.Name, &item.Type, &item.Position, &item.TypeOptions, &item.PendingType, &item.PendingTypeOptions, &item.IsPrimary, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BaseProperty{}, ErrNotFound
	}
	return item, err
}

func (repository *Repository) UpdateBaseProperty(ctx context.Context, pageID, propertyID, workspaceID string, name, propertyType *string, typeOptions json.RawMessage) (BaseProperty, error) {
	result, err := repository.db.Exec(ctx, `
UPDATE base_properties SET name = COALESCE($4, name), type = COALESCE($5, type),
  type_options = COALESCE($6::jsonb, type_options), updated_at = now()
WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL`, pageID, propertyID, workspaceID, name, propertyType, nullableJSON(typeOptions))
	if err != nil {
		return BaseProperty{}, err
	}
	if result.RowsAffected() == 0 {
		return BaseProperty{}, ErrNotFound
	}
	return repository.BasePropertyByID(ctx, pageID, propertyID, workspaceID)
}

func (repository *Repository) DeleteBaseProperty(ctx context.Context, pageID, propertyID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `UPDATE base_properties SET deleted_at = now(), updated_at = now() WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL AND is_primary = false`, pageID, propertyID, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) ReorderBaseProperty(ctx context.Context, pageID, propertyID, workspaceID, position string) error {
	result, err := repository.db.Exec(ctx, `UPDATE base_properties SET position = $4, updated_at = now() WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL`, pageID, propertyID, workspaceID, position)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) BaseRows(ctx context.Context, pageID, workspaceID, cursor string, limit int) (domain.Pagination[BaseRow], error) {
	limit = normalizeLimit(limit)
	rows, err := repository.db.Query(ctx, `
SELECT id::text, page_id::text, COALESCE(cells, '{}'::jsonb), position, creator_id::text,
       last_updated_by_id::text, workspace_id::text, created_at, updated_at
FROM base_rows WHERE page_id = $1 AND workspace_id = $2 AND deleted_at IS NULL
  AND ($3 = '' OR id::text > $3) ORDER BY id LIMIT $4`, pageID, workspaceID, cursor, limit+1)
	if err != nil {
		return domain.Pagination[BaseRow]{}, err
	}
	defer rows.Close()
	items := make([]BaseRow, 0, limit)
	for rows.Next() {
		var item BaseRow
		if err := rows.Scan(&item.ID, &item.PageID, &item.Cells, &item.Position, &item.CreatorID, &item.LastUpdatedByID, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return domain.Pagination[BaseRow]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[BaseRow]{}, err
	}
	hasNext := len(items) > limit
	if hasNext {
		items = items[:limit]
	}
	result := page(items, limit)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = cursor != ""
	if hasNext && len(items) > 0 {
		next := items[len(items)-1].ID
		result.Meta.NextCursor = &next
	}
	return result, nil
}

func (repository *Repository) BaseRowByID(ctx context.Context, pageID, rowID, workspaceID string) (BaseRow, error) {
	var item BaseRow
	err := repository.db.QueryRow(ctx, `SELECT id::text, page_id::text, COALESCE(cells, '{}'::jsonb), position, creator_id::text, last_updated_by_id::text, workspace_id::text, created_at, updated_at FROM base_rows WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL`, pageID, rowID, workspaceID).Scan(&item.ID, &item.PageID, &item.Cells, &item.Position, &item.CreatorID, &item.LastUpdatedByID, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BaseRow{}, ErrNotFound
	}
	return item, err
}

func (repository *Repository) CreateBaseRow(ctx context.Context, pageID, workspaceID, creatorID, position string, cells json.RawMessage) (BaseRow, error) {
	id, err := newUUID()
	if err != nil {
		return BaseRow{}, err
	}
	if position == "" {
		position = fmt.Sprintf("%020d", time.Now().UnixNano())
	}
	if len(cells) == 0 {
		cells = json.RawMessage(`{}`)
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO base_rows (id, page_id, cells, position, creator_id, last_updated_by_id, workspace_id) VALUES ($1, $2, $3::jsonb, $4, $5, $5, $6)`, id, pageID, cells, position, creatorID, workspaceID)
	if err != nil {
		return BaseRow{}, err
	}
	return repository.BaseRowByID(ctx, pageID, id, workspaceID)
}

func (repository *Repository) UpdateBaseRow(ctx context.Context, pageID, rowID, workspaceID, userID string, cells json.RawMessage, position *string) (BaseRow, error) {
	result, err := repository.db.Exec(ctx, `UPDATE base_rows SET cells = COALESCE($5::jsonb, cells), position = COALESCE($6, position), last_updated_by_id = $4, updated_at = now() WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL`, pageID, rowID, workspaceID, userID, nullableJSON(cells), position)
	if err != nil {
		return BaseRow{}, err
	}
	if result.RowsAffected() == 0 {
		return BaseRow{}, ErrNotFound
	}
	return repository.BaseRowByID(ctx, pageID, rowID, workspaceID)
}

func (repository *Repository) DeleteBaseRows(ctx context.Context, pageID, workspaceID string, rowIDs []string) error {
	result, err := repository.db.Exec(ctx, `UPDATE base_rows SET deleted_at = now(), updated_at = now() WHERE page_id = $1 AND workspace_id = $2 AND id = ANY($3::uuid[]) AND deleted_at IS NULL`, pageID, workspaceID, rowIDs)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) ReorderBaseRow(ctx context.Context, pageID, rowID, workspaceID, position string) error {
	result, err := repository.db.Exec(ctx, `UPDATE base_rows SET position = $4, updated_at = now() WHERE page_id = $1 AND id = $2 AND workspace_id = $3 AND deleted_at IS NULL`, pageID, rowID, workspaceID, position)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) BaseViews(ctx context.Context, pageID, workspaceID string) ([]BaseView, error) {
	rows, err := repository.db.Query(ctx, `SELECT id::text, page_id::text, name, type, position, COALESCE(config, '{}'::jsonb), workspace_id::text, creator_id::text, created_at, updated_at FROM base_views WHERE page_id = $1 AND workspace_id = $2 ORDER BY position COLLATE "C", id`, pageID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BaseView, 0)
	for rows.Next() {
		var item BaseView
		if err := rows.Scan(&item.ID, &item.PageID, &item.Name, &item.Type, &item.Position, &item.Config, &item.WorkspaceID, &item.CreatorID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) CreateBaseView(ctx context.Context, pageID, workspaceID, creatorID, name, viewType, position string, config json.RawMessage) (BaseView, error) {
	id, err := newUUID()
	if err != nil {
		return BaseView{}, err
	}
	if viewType == "" {
		viewType = "table"
	}
	if position == "" {
		position = fmt.Sprintf("%020d", time.Now().UnixNano())
	}
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	_, err = repository.db.Exec(ctx, `INSERT INTO base_views (id, page_id, name, type, position, config, workspace_id, creator_id) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)`, id, pageID, name, viewType, position, config, workspaceID, creatorID)
	if err != nil {
		return BaseView{}, err
	}
	return repository.BaseViewByID(ctx, pageID, id, workspaceID)
}

func (repository *Repository) BaseViewByID(ctx context.Context, pageID, viewID, workspaceID string) (BaseView, error) {
	var item BaseView
	err := repository.db.QueryRow(ctx, `SELECT id::text, page_id::text, name, type, position, COALESCE(config, '{}'::jsonb), workspace_id::text, creator_id::text, created_at, updated_at FROM base_views WHERE page_id = $1 AND id = $2 AND workspace_id = $3`, pageID, viewID, workspaceID).Scan(&item.ID, &item.PageID, &item.Name, &item.Type, &item.Position, &item.Config, &item.WorkspaceID, &item.CreatorID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BaseView{}, ErrNotFound
	}
	return item, err
}

func (repository *Repository) UpdateBaseView(ctx context.Context, pageID, viewID, workspaceID string, name, viewType *string, config json.RawMessage, position *string) (BaseView, error) {
	result, err := repository.db.Exec(ctx, `UPDATE base_views SET name = COALESCE($4, name), type = COALESCE($5, type), config = COALESCE($6::jsonb, config), position = COALESCE($7, position), updated_at = now() WHERE page_id = $1 AND id = $2 AND workspace_id = $3`, pageID, viewID, workspaceID, name, viewType, nullableJSON(config), position)
	if err != nil {
		return BaseView{}, err
	}
	if result.RowsAffected() == 0 {
		return BaseView{}, ErrNotFound
	}
	return repository.BaseViewByID(ctx, pageID, viewID, workspaceID)
}

func (repository *Repository) DeleteBaseView(ctx context.Context, pageID, viewID, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `DELETE FROM base_views WHERE page_id = $1 AND id = $2 AND workspace_id = $3`, pageID, viewID, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
