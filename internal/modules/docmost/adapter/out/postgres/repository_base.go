package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
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

// BaseRowReferences contains the small set of users and pages needed to
// render person/page cells without making one request per cell. It mirrors
// the references object consumed by the separated frontend.
type BaseRowReferences struct {
	Users map[string]domain.UserSummary `json:"users"`
	Pages map[string]BasePageReference  `json:"pages"`
}

type BasePageReference struct {
	ID      string               `json:"id"`
	SlugID  string               `json:"slugId"`
	Title   *string              `json:"title"`
	Icon    *string              `json:"icon"`
	SpaceID string               `json:"spaceId"`
	Space   *domain.SpaceSummary `json:"space"`
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

// BaseRowReferences resolves the references used by the returned page of
// rows. Restricted pages are checked with the same access rules as the page
// picker, so a reference cannot reveal a page the current user cannot open.
func (repository *Repository) BaseRowReferences(ctx context.Context, workspaceID, userID string, rows []BaseRow) (BaseRowReferences, error) {
	references := BaseRowReferences{
		Users: make(map[string]domain.UserSummary),
		Pages: make(map[string]BasePageReference),
	}
	if len(rows) == 0 {
		return references, nil
	}

	propertiesByID := make(map[string]string)
	propertyRows, err := repository.db.Query(ctx, `
SELECT id::text, type FROM base_properties
WHERE workspace_id = $1 AND deleted_at IS NULL AND type IN ('person', 'page')`, workspaceID)
	if err != nil {
		return BaseRowReferences{}, err
	}
	for propertyRows.Next() {
		var id, propertyType string
		if err := propertyRows.Scan(&id, &propertyType); err != nil {
			propertyRows.Close()
			return BaseRowReferences{}, err
		}
		propertiesByID[id] = propertyType
	}
	if err := propertyRows.Err(); err != nil {
		propertyRows.Close()
		return BaseRowReferences{}, err
	}
	propertyRows.Close()

	userIDs := make(map[string]struct{})
	pageIDs := make(map[string]struct{})
	for _, row := range rows {
		addBaseReferenceID(userIDs, row.CreatorID)
		addBaseReferenceID(userIDs, row.LastUpdatedByID)
		var cells map[string]any
		if json.Unmarshal(row.Cells, &cells) != nil {
			continue
		}
		for propertyID, propertyType := range propertiesByID {
			values := baseValues(cells[propertyID])
			for _, value := range values {
				id, ok := value.(string)
				if !ok || !looksLikeUUID(id) {
					continue
				}
				if propertyType == "person" {
					userIDs[id] = struct{}{}
				} else {
					pageIDs[id] = struct{}{}
				}
			}
		}
	}

	if len(userIDs) > 0 {
		ids := baseReferenceIDs(userIDs)
		userRows, err := repository.db.Query(ctx, `
SELECT id::text, name, avatar_url FROM users
WHERE workspace_id = $1 AND id::text = ANY($2::text[])
  AND deleted_at IS NULL AND deactivated_at IS NULL`, workspaceID, ids)
		if err != nil {
			return BaseRowReferences{}, err
		}
		for userRows.Next() {
			var summary domain.UserSummary
			if err := userRows.Scan(&summary.ID, &summary.Name, &summary.AvatarURL); err != nil {
				userRows.Close()
				return BaseRowReferences{}, err
			}
			references.Users[summary.ID] = summary
		}
		if err := userRows.Err(); err != nil {
			userRows.Close()
			return BaseRowReferences{}, err
		}
		userRows.Close()
	}

	if len(pageIDs) > 0 {
		ids := baseReferenceIDs(pageIDs)
		pageRows, err := repository.db.Query(ctx, `
SELECT p.id::text, p.slug_id, p.title, p.icon, p.space_id::text,
       s.id::text, s.slug, s.name
FROM pages p LEFT JOIN spaces s ON s.id = p.space_id
WHERE p.workspace_id = $1 AND p.id::text = ANY($2::text[]) AND p.deleted_at IS NULL`, workspaceID, ids)
		if err != nil {
			return BaseRowReferences{}, err
		}
		for pageRows.Next() {
			var reference BasePageReference
			var spaceID, spaceSlug string
			var spaceName *string
			if err := pageRows.Scan(&reference.ID, &reference.SlugID, &reference.Title, &reference.Icon, &reference.SpaceID, &spaceID, &spaceSlug, &spaceName); err != nil {
				pageRows.Close()
				return BaseRowReferences{}, err
			}
			access, accessErr := repository.PageAccess(ctx, reference.ID, workspaceID, userID)
			if accessErr != nil || (access.HasRestriction && !access.CanAccess) {
				continue
			}
			reference.Space = &domain.SpaceSummary{ID: spaceID, Slug: spaceSlug, Name: spaceName}
			references.Pages[reference.ID] = reference
		}
		if err := pageRows.Err(); err != nil {
			pageRows.Close()
			return BaseRowReferences{}, err
		}
		pageRows.Close()
	}
	return references, nil
}

func addBaseReferenceID(ids map[string]struct{}, value *string) {
	if value != nil && looksLikeUUID(*value) {
		ids[*value] = struct{}{}
	}
}

func baseReferenceIDs(ids map[string]struct{}) []string {
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	return values
}

// BaseRowsWithOptions is the view-aware rows endpoint. View filters and sorts
// are stored as JSON in base_views, so evaluating them here keeps the API
// compatible with the client while allowing the storage model to evolve.
// The unfiltered path above remains the fast, indexed cursor query.
func (repository *Repository) BaseRowsWithOptions(ctx context.Context, pageID, workspaceID, cursor string, limit int, filterJSON, sortsJSON json.RawMessage) (domain.Pagination[BaseRow], error) {
	limit = normalizeLimit(limit)
	rows, err := repository.db.Query(ctx, `
SELECT id::text, page_id::text, COALESCE(cells, '{}'::jsonb), position, creator_id::text,
       last_updated_by_id::text, workspace_id::text, created_at, updated_at
FROM base_rows WHERE page_id = $1 AND workspace_id = $2 AND deleted_at IS NULL
ORDER BY position COLLATE "C", id LIMIT 10000`, pageID, workspaceID)
	if err != nil {
		return domain.Pagination[BaseRow]{}, err
	}
	defer rows.Close()
	items := make([]BaseRow, 0)
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
	filter, err := decodeBaseFilter(filterJSON)
	if err != nil {
		return domain.Pagination[BaseRow]{}, err
	}
	for i := len(items) - 1; i >= 0; i-- {
		if filter != nil && !baseFilterMatches(filter, items[i]) {
			items = append(items[:i], items[i+1:]...)
		}
	}
	sorts, err := decodeBaseSorts(sortsJSON)
	if err != nil {
		return domain.Pagination[BaseRow]{}, err
	}
	if len(sorts) > 0 {
		sort.SliceStable(items, func(i, j int) bool {
			return compareBaseRows(items[i], items[j], sorts) < 0
		})
	}
	offset := 0
	if strings.TrimSpace(cursor) != "" {
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return domain.Pagination[BaseRow]{}, ErrInvalidInput
		}
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	pageItems := items[offset:end]
	result := page(pageItems, limit)
	result.Meta.HasPrevPage = offset > 0
	result.Meta.HasNextPage = end < len(items)
	if result.Meta.HasNextPage {
		next := strconv.Itoa(end)
		result.Meta.NextCursor = &next
	}
	return result, nil
}

type baseFilterNode struct {
	PropertyID string
	Op         string
	Value      any
	Children   []baseFilterNode
}

type baseSort struct {
	PropertyID string
	Direction  string
}

func decodeBaseFilter(raw json.RawMessage) (*baseFilterNode, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wire struct {
		PropertyID string            `json:"propertyId"`
		Op         string            `json:"op"`
		Value      json.RawMessage   `json:"value"`
		Children   []json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, ErrInvalidInput
	}
	node := &baseFilterNode{PropertyID: strings.TrimSpace(wire.PropertyID), Op: strings.TrimSpace(wire.Op)}
	if len(wire.Value) > 0 && string(wire.Value) != "null" {
		if err := json.Unmarshal(wire.Value, &node.Value); err != nil {
			return nil, ErrInvalidInput
		}
	}
	for _, childRaw := range wire.Children {
		child, err := decodeBaseFilter(childRaw)
		if err != nil || child == nil {
			return nil, ErrInvalidInput
		}
		node.Children = append(node.Children, *child)
	}
	if len(node.Children) == 0 && node.PropertyID == "" && node.Op == "" {
		return nil, nil
	}
	if len(node.Children) == 0 && node.Op == "" {
		return nil, ErrInvalidInput
	}
	return node, nil
}

func decodeBaseSorts(raw json.RawMessage) ([]baseSort, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []baseSort
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, ErrInvalidInput
	}
	for i := range values {
		values[i].PropertyID = strings.TrimSpace(values[i].PropertyID)
		values[i].Direction = strings.ToLower(strings.TrimSpace(values[i].Direction))
		if values[i].PropertyID == "" || (values[i].Direction != "asc" && values[i].Direction != "desc") {
			return nil, ErrInvalidInput
		}
	}
	return values, nil
}

func baseFilterMatches(node *baseFilterNode, row BaseRow) bool {
	if len(node.Children) > 0 {
		if strings.ToLower(node.Op) == "or" {
			for i := range node.Children {
				if baseFilterMatches(&node.Children[i], row) {
					return true
				}
			}
			return false
		}
		for i := range node.Children {
			if !baseFilterMatches(&node.Children[i], row) {
				return false
			}
		}
		return true
	}
	var cells map[string]any
	if json.Unmarshal(row.Cells, &cells) != nil {
		return false
	}
	return baseValueMatches(baseRowValue(row, cells, node.PropertyID), node.Op, node.Value)
}

func baseRowValue(row BaseRow, cells map[string]any, propertyID string) any {
	if value, ok := cells[propertyID]; ok {
		return value
	}
	switch propertyID {
	case "createdAt":
		return row.CreatedAt
	case "lastEditedAt":
		return row.UpdatedAt
	case "lastEditedBy":
		if row.LastUpdatedByID != nil {
			return *row.LastUpdatedByID
		}
		if row.CreatorID != nil {
			return *row.CreatorID
		}
	}
	return nil
}

func baseValueMatches(actual any, operator string, expected any) bool {
	operator = strings.TrimSpace(operator)
	empty := baseValueEmpty(actual)
	switch operator {
	case "isEmpty":
		return empty
	case "isNotEmpty":
		return !empty
	}
	if empty {
		return false
	}
	actualText := strings.ToLower(strings.TrimSpace(fmt.Sprint(actual)))
	expectedText := strings.ToLower(strings.TrimSpace(fmt.Sprint(expected)))
	actualNumber, actualNumberOK := baseNumber(actual)
	expectedNumber, expectedNumberOK := baseNumber(expected)
	if operator == "any" || operator == "none" || operator == "all" {
		values := baseValues(actual)
		expectedValues := baseValues(expected)
		matches := 0
		for _, candidate := range expectedValues {
			for _, value := range values {
				if baseEqual(value, candidate) {
					matches++
					break
				}
			}
		}
		switch operator {
		case "any":
			return matches > 0
		case "none":
			return matches == 0
		default:
			return matches == len(expectedValues) && len(expectedValues) > 0
		}
	}
	switch operator {
	case "eq":
		if leftTime, leftOK := baseTime(actual); leftOK {
			if rightTime, rightOK := baseExpectedTime(expected); rightOK {
				return leftTime.Format("2006-01-02") == rightTime.Format("2006-01-02")
			}
		}
		return baseEqual(actual, expected)
	case "neq":
		if leftTime, leftOK := baseTime(actual); leftOK {
			if rightTime, rightOK := baseExpectedTime(expected); rightOK {
				return leftTime.Format("2006-01-02") != rightTime.Format("2006-01-02")
			}
		}
		return !baseEqual(actual, expected)
	case "contains":
		if values := baseValues(actual); len(values) > 1 {
			for _, value := range values {
				if baseEqual(value, expected) {
					return true
				}
			}
		}
		return strings.Contains(actualText, expectedText)
	case "ncontains":
		return !baseValueMatches(actual, "contains", expected)
	case "startsWith":
		return strings.HasPrefix(actualText, expectedText)
	case "endsWith":
		return strings.HasSuffix(actualText, expectedText)
	case "gt", "gte", "lt", "lte":
		if actualNumberOK && expectedNumberOK {
			return compareBaseNumbers(actualNumber, expectedNumber, operator)
		}
		return compareBaseStrings(actualText, expectedText, operator)
	case "before", "onOrBefore", "after", "onOrAfter":
		actualTime, actualOK := baseTime(actual)
		expectedTime, expectedOK := baseExpectedTime(expected)
		if !actualOK || !expectedOK {
			return false
		}
		switch operator {
		case "before":
			return actualTime.Before(expectedTime)
		case "onOrBefore":
			return !actualTime.After(expectedTime)
		case "after":
			return actualTime.After(expectedTime)
		default:
			return !actualTime.Before(expectedTime)
		}
	case "isWithin":
		if value, ok := expected.(map[string]any); ok {
			if mode, _ := value["mode"].(string); mode == "exact" {
				expectedTime, expectedOK := baseExpectedTime(value)
				actualTime, actualOK := baseTime(actual)
				return actualOK && expectedOK && actualTime.Format("2006-01-02") == expectedTime.Format("2006-01-02")
			}
			preset, _ := value["preset"].(string)
			start, end := baseDateRange(preset, time.Now())
			actualTime, actualOK := baseTime(actual)
			return actualOK && !actualTime.Before(start) && actualTime.Before(end)
		}
	}
	return false
}

func baseValueEmpty(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	if values, ok := value.([]any); ok {
		return len(values) == 0
	}
	return false
}

func baseValues(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return []any{value}
}

func baseEqual(left, right any) bool {
	if leftNumber, leftOK := baseNumber(left); leftOK {
		if rightNumber, rightOK := baseNumber(right); rightOK {
			return leftNumber == rightNumber
		}
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(left)), strings.TrimSpace(fmt.Sprint(right)))
}

func baseNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

func compareBaseNumbers(left, right float64, operator string) bool {
	switch operator {
	case "gt":
		return left > right
	case "gte":
		return left >= right
	case "lt":
		return left < right
	default:
		return left <= right
	}
}

func compareBaseStrings(left, right, operator string) bool {
	switch operator {
	case "gt":
		return left > right
	case "gte":
		return left >= right
	case "lt":
		return left < right
	default:
		return left <= right
	}
}

func baseTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case *time.Time:
		if typed != nil {
			return *typed, true
		}
		return time.Time{}, false
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func baseExpectedTime(value any) (time.Time, bool) {
	if object, ok := value.(map[string]any); ok {
		if date, dateOK := object["date"].(string); dateOK {
			return baseTime(date)
		}
		if preset, presetOK := object["preset"].(string); presetOK {
			return baseDateAnchor(preset, time.Now()), true
		}
	}
	return baseTime(value)
}

func baseDateRange(preset string, now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch preset {
	case "today":
		return start, start.AddDate(0, 0, 1)
	case "tomorrow":
		return start.AddDate(0, 0, 1), start.AddDate(0, 0, 2)
	case "yesterday":
		return start.AddDate(0, 0, -1), start
	case "pastWeek":
		return start.AddDate(0, 0, -7), start.AddDate(0, 0, 1)
	case "pastMonth":
		return start.AddDate(0, -1, 0), start.AddDate(0, 0, 1)
	case "pastYear":
		return start.AddDate(-1, 0, 0), start.AddDate(0, 0, 1)
	case "thisWeek":
		weekStart := start.AddDate(0, 0, -int((int(start.Weekday())+6)%7))
		return weekStart, weekStart.AddDate(0, 0, 7)
	case "thisMonth":
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return monthStart, monthStart.AddDate(0, 1, 0)
	case "thisYear":
		yearStart := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, now.Location())
		return yearStart, yearStart.AddDate(1, 0, 0)
	case "nextWeek":
		return start.AddDate(0, 0, 1), start.AddDate(0, 0, 8)
	case "nextMonth":
		return start.AddDate(0, 1, 0), start.AddDate(0, 1, 1)
	case "nextYear":
		return start.AddDate(1, 0, 0), start.AddDate(1, 0, 1)
	default:
		return start, start.AddDate(0, 0, 1)
	}
}

func baseDateAnchor(preset string, now time.Time) time.Time {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch preset {
	case "tomorrow":
		return start.AddDate(0, 0, 1)
	case "yesterday":
		return start.AddDate(0, 0, -1)
	case "oneWeekAgo":
		return start.AddDate(0, 0, -7)
	case "oneWeekFromNow":
		return start.AddDate(0, 0, 7)
	case "oneMonthAgo":
		return start.AddDate(0, -1, 0)
	case "oneMonthFromNow":
		return start.AddDate(0, 1, 0)
	default:
		return start
	}
}

func compareBaseRows(left, right BaseRow, sorts []baseSort) int {
	var leftCells, rightCells map[string]any
	_ = json.Unmarshal(left.Cells, &leftCells)
	_ = json.Unmarshal(right.Cells, &rightCells)
	for _, config := range sorts {
		leftValue := baseRowValue(left, leftCells, config.PropertyID)
		rightValue := baseRowValue(right, rightCells, config.PropertyID)
		if leftTime, leftOK := baseTime(leftValue); leftOK {
			if rightTime, rightOK := baseTime(rightValue); rightOK {
				result := 0
				switch {
				case leftTime.Before(rightTime):
					result = -1
				case leftTime.After(rightTime):
					result = 1
				}
				if result != 0 {
					if config.Direction == "desc" {
						return -result
					}
					return result
				}
				continue
			}
		}
		leftText := strings.ToLower(fmt.Sprint(leftValue))
		rightText := strings.ToLower(fmt.Sprint(rightValue))
		result := strings.Compare(leftText, rightText)
		if leftNumber, leftOK := baseNumber(leftValue); leftOK {
			if rightNumber, rightOK := baseNumber(rightValue); rightOK {
				switch {
				case leftNumber < rightNumber:
					result = -1
				case leftNumber > rightNumber:
					result = 1
				default:
					result = 0
				}
			}
		}
		if result != 0 {
			if config.Direction == "desc" {
				return -result
			}
			return result
		}
	}
	return strings.Compare(left.ID, right.ID)
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
