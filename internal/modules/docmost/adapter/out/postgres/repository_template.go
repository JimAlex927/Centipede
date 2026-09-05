package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

const templateFields = `
t.id::text, COALESCE(t.title, ''), t.description, t.content, t.icon,
t.space_id::text, t.workspace_id::text, t.creator_id::text,
t.last_updated_by_id::text, t.created_at, t.updated_at,
u.id::text, u.name, u.avatar_url`

func scanTemplate(row rowScanner) (domain.Template, error) {
	var value domain.Template
	var creatorID, creatorName, creatorAvatar *string
	err := row.Scan(
		&value.ID, &value.Title, &value.Description, &value.Content, &value.Icon,
		&value.SpaceID, &value.WorkspaceID, &value.CreatorID, &value.LastUpdatedByID,
		&value.CreatedAt, &value.UpdatedAt,
		&creatorID, &creatorName, &creatorAvatar,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Template{}, ErrNotFound
	}
	if err != nil {
		return domain.Template{}, err
	}
	if creatorID != nil {
		value.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
	}
	return value, nil
}

// TemplateByID returns only templates visible to the viewer. Global templates
// are visible to every workspace member; space templates follow the same
// public/member/group access rules as the rest of Docmost.
func (repository *Repository) TemplateByID(ctx context.Context, id, workspaceID, viewerID string, admin, includeContent bool) (domain.Template, error) {
	content := "NULL::jsonb"
	if includeContent {
		content = "t.content"
	}
	return scanTemplate(repository.db.QueryRow(ctx, `
SELECT `+strings.Replace(templateFields, "t.content", content, 1)+`
FROM templates t
LEFT JOIN users u ON u.id = t.creator_id
WHERE t.id = $1 AND t.workspace_id = $2 AND t.deleted_at IS NULL
  AND (
    t.space_id IS NULL OR $3 OR EXISTS (
      SELECT 1 FROM spaces s
      WHERE s.id = t.space_id AND s.workspace_id = t.workspace_id AND s.deleted_at IS NULL
        AND (s.visibility = 'public' OR EXISTS (
          SELECT 1 FROM space_members sm
          WHERE sm.space_id = s.id AND sm.deleted_at IS NULL
            AND (sm.user_id = $4 OR sm.group_id IN (
              SELECT gu.group_id FROM group_users gu WHERE gu.user_id = $4
            ))
        ))
    )
  )`, id, workspaceID, admin, viewerID))
}

type templateCursor struct {
	Title string `json:"title"`
	ID    string `json:"id"`
}

func encodeTemplateCursor(value templateCursor) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeTemplateCursor(value string) (templateCursor, error) {
	if strings.TrimSpace(value) == "" {
		return templateCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return templateCursor{}, ErrInvalidInput
	}
	var cursor templateCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Title == "" && cursor.ID == "" || !looksLikeUUID(cursor.ID) {
		return templateCursor{}, ErrInvalidInput
	}
	return cursor, nil
}

func (repository *Repository) Templates(ctx context.Context, workspaceID, viewerID string, admin bool, spaceID, cursor string, limit int) (domain.Pagination[domain.Template], error) {
	pageSize := normalizeLimit(limit)
	position, err := decodeTemplateCursor(cursor)
	if err != nil {
		return domain.Pagination[domain.Template]{}, err
	}
	rows, err := repository.db.Query(ctx, `
SELECT `+strings.Replace(templateFields, "t.content", "NULL::jsonb", 1)+`
FROM templates t
LEFT JOIN users u ON u.id = t.creator_id
WHERE t.workspace_id = $1 AND t.deleted_at IS NULL
  AND ($2 = '' OR t.space_id = NULLIF($2, '')::uuid)
  AND (
    t.space_id IS NULL OR $3 OR EXISTS (
      SELECT 1 FROM spaces s
      WHERE s.id = t.space_id AND s.workspace_id = t.workspace_id AND s.deleted_at IS NULL
        AND (s.visibility = 'public' OR EXISTS (
          SELECT 1 FROM space_members sm
          WHERE sm.space_id = s.id AND sm.deleted_at IS NULL
            AND (sm.user_id = $4 OR sm.group_id IN (
              SELECT gu.group_id FROM group_users gu WHERE gu.user_id = $4
            ))
        ))
    )
  )
  AND ($5 = '' OR (lower(COALESCE(t.title, '')), t.id) > (lower($6), $7::uuid))
ORDER BY lower(COALESCE(t.title, '')), t.id
LIMIT $8`, workspaceID, spaceID, admin, viewerID, cursor, position.Title, position.ID, pageSize+1)
	if err != nil {
		return domain.Pagination[domain.Template]{}, err
	}
	defer rows.Close()
	items := make([]domain.Template, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanTemplate(rows)
		if scanErr != nil {
			return domain.Pagination[domain.Template]{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Pagination[domain.Template]{}, err
	}
	hasNext := len(items) > pageSize
	if hasNext {
		items = items[:pageSize]
	}
	result := page(items, pageSize)
	result.Meta.HasNextPage = hasNext
	result.Meta.HasPrevPage = cursor != ""
	if hasNext && len(items) > 0 {
		next, encodeErr := encodeTemplateCursor(templateCursor{Title: items[len(items)-1].Title, ID: items[len(items)-1].ID})
		if encodeErr != nil {
			return domain.Pagination[domain.Template]{}, encodeErr
		}
		result.Meta.NextCursor = &next
	}
	return result, nil
}

type TemplateCreateInput struct {
	Title       string
	Description *string
	Icon        *string
	SpaceID     *string
	Content     json.RawMessage
}

func (repository *Repository) CreateTemplate(ctx context.Context, workspaceID, creatorID string, input TemplateCreateInput) (domain.Template, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return domain.Template{}, ErrInvalidInput
	}
	id, err := newUUID()
	if err != nil {
		return domain.Template{}, err
	}
	_, err = repository.db.Exec(ctx, `
INSERT INTO templates (id, title, description, content, icon, space_id, workspace_id, creator_id, last_updated_by_id, text_content)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $8, $9)`, id, title, input.Description, nullableJSON(input.Content), input.Icon, input.SpaceID, workspaceID, creatorID, jsonTextContent(input.Content))
	if err != nil {
		return domain.Template{}, err
	}
	return repository.TemplateByID(ctx, id, workspaceID, creatorID, true, true)
}

type TemplateUpdateInput struct {
	Title          *string
	SetTitle       bool
	Description    *string
	SetDescription bool
	Icon           *string
	SetIcon        bool
	SpaceID        *string
	SetSpaceID     bool
	Content        json.RawMessage
	SetContent     bool
}

func (repository *Repository) UpdateTemplate(ctx context.Context, id, workspaceID, userID string, input TemplateUpdateInput) (domain.Template, error) {
	sets := make([]string, 0, 6)
	args := []any{id, workspaceID, userID}
	add := func(expression string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf(expression, len(args)))
	}
	if input.SetTitle {
		if input.Title == nil || strings.TrimSpace(*input.Title) == "" {
			return domain.Template{}, ErrInvalidInput
		}
		value := strings.TrimSpace(*input.Title)
		add("title = $%d", value)
	}
	if input.SetDescription {
		add("description = $%d", input.Description)
	}
	if input.SetIcon {
		add("icon = $%d", input.Icon)
	}
	if input.SetSpaceID {
		add("space_id = $%d::uuid", input.SpaceID)
	}
	if input.SetContent {
		add("content = $%d::jsonb", nullableJSON(input.Content))
		add("text_content = $%d", jsonTextContent(input.Content))
	}
	if len(sets) == 0 {
		return domain.Template{}, ErrInvalidInput
	}
	args = append(args, id, workspaceID)
	query := `UPDATE templates SET ` + strings.Join(sets, ", ") + fmt.Sprintf(`, last_updated_by_id = $3, updated_at = now()
WHERE id = $%d AND workspace_id = $%d AND deleted_at IS NULL`, len(args)-1, len(args))
	// The final two workspace arguments are intentionally duplicated in the
	// SQL so dynamic SET expressions never alter the scope predicates.
	_, err := repository.db.Exec(ctx, query, args...)
	if err != nil {
		return domain.Template{}, err
	}
	return repository.TemplateByID(ctx, id, workspaceID, userID, true, true)
}

func (repository *Repository) DeleteTemplate(ctx context.Context, id, workspaceID string) error {
	result, err := repository.db.Exec(ctx, `DELETE FROM templates WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
