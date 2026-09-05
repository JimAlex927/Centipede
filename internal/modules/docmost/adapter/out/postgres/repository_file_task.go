package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

const fileTaskColumns = `
f.id::text, f.type, f.source, f.status, f.file_name, f.file_path,
COALESCE(f.file_size, 0), f.file_ext, f.error_message, f.creator_id::text,
f.page_id::text, f.space_id::text, f.workspace_id::text,
COALESCE(f.metadata, '{}'::jsonb), f.created_at, f.updated_at, f.deleted_at`

type FileTaskInput struct {
	ID, Type, Source, FileName, FilePath, FileExt string
	FileSize                                      int64
	CreatorID, SpaceID, WorkspaceID               string
	Status                                        string
}

func scanFileTask(row rowScanner) (domain.FileTask, error) {
	var task domain.FileTask
	err := row.Scan(
		&task.ID, &task.Type, &task.Source, &task.Status, &task.FileName,
		&task.FilePath, &task.FileSize, &task.FileExt, &task.ErrorMessage,
		&task.CreatorID, &task.PageID, &task.SpaceID, &task.WorkspaceID,
		&task.Metadata, &task.CreatedAt, &task.UpdatedAt, &task.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FileTask{}, ErrNotFound
	}
	return task, err
}

func (repository *Repository) FileTaskByID(ctx context.Context, taskID, workspaceID string) (domain.FileTask, error) {
	return scanFileTask(repository.db.QueryRow(ctx, `SELECT `+fileTaskColumns+`
FROM file_tasks f
WHERE f.id = $1 AND f.workspace_id = $2`, taskID, workspaceID))
}

func (repository *Repository) CreateFileTask(ctx context.Context, input FileTaskInput) (domain.FileTask, error) {
	_, err := repository.db.Exec(ctx, `INSERT INTO file_tasks
(id, type, source, status, file_name, file_path, file_size, file_ext, creator_id, space_id, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		input.ID, input.Type, input.Source, input.Status, input.FileName, input.FilePath,
		input.FileSize, input.FileExt, input.CreatorID, input.SpaceID, input.WorkspaceID)
	if err != nil {
		return domain.FileTask{}, err
	}
	return repository.FileTaskByID(ctx, input.ID, input.WorkspaceID)
}

func (repository *Repository) UpdateFileTaskStatus(ctx context.Context, taskID, workspaceID, status, errorMessage string) error {
	_, err := repository.db.Exec(ctx, `UPDATE file_tasks
SET status = $3, error_message = NULLIF($4, ''), updated_at = now()
WHERE id = $1 AND workspace_id = $2`, taskID, workspaceID, status, errorMessage)
	return err
}

func (repository *Repository) FileTasks(ctx context.Context, workspaceID, userID, cursor, beforeCursor string, limit int) (domain.Pagination[domain.FileTask], error) {
	limit = normalizeLimit(limit)
	args := []any{workspaceID, userID}
	whereCursor := ""
	if cursor != "" {
		value, err := decodeFileTaskCursor(cursor)
		if err != nil {
			return domain.Pagination[domain.FileTask]{}, ErrInvalidInput
		}
		whereCursor = " AND f.id < $3"
		args = append(args, value)
	} else if beforeCursor != "" {
		value, err := decodeFileTaskCursor(beforeCursor)
		if err != nil {
			return domain.Pagination[domain.FileTask]{}, ErrInvalidInput
		}
		whereCursor = " AND f.id > $3"
		args = append(args, value)
	}
	args = append(args, limit+1)

	order := "DESC"
	if beforeCursor != "" && cursor == "" {
		order = "ASC"
	}
	rows, err := repository.db.Query(ctx, `SELECT `+fileTaskColumns+`
FROM file_tasks f
WHERE f.workspace_id = $1
  AND f.space_id IS NOT NULL
  AND f.space_id IN (
    SELECT sm.space_id FROM space_members sm
    WHERE sm.user_id = $2 AND sm.deleted_at IS NULL
    UNION
    SELECT sm.space_id FROM space_members sm
    JOIN group_users gu ON gu.group_id = sm.group_id
    WHERE gu.user_id = $2 AND sm.deleted_at IS NULL
  )`+whereCursor+`
ORDER BY f.id `+order+`
	LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return domain.Pagination[domain.FileTask]{}, err
	}
	defer rows.Close()
	items := make([]domain.FileTask, 0, limit)
	for rows.Next() {
		item, scanErr := scanFileTask(rows)
		if scanErr != nil {
			return domain.Pagination[domain.FileTask]{}, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return domain.Pagination[domain.FileTask]{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if order == "ASC" {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	var prevCursor, nextCursor *string
	if cursor != "" && len(items) > 0 {
		value := encodeFileTaskCursor(items[0].ID)
		prevCursor = &value
	}
	if hasMore && len(items) > 0 {
		value := encodeFileTaskCursor(items[len(items)-1].ID)
		nextCursor = &value
	}
	return domain.Pagination[domain.FileTask]{Items: items, Meta: domain.PaginationMeta{
		Limit: limit, HasNextPage: hasMore, HasPrevPage: cursor != "", NextCursor: nextCursor, PrevCursor: prevCursor,
	}}, nil
}

func encodeFileTaskCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(url.Values{"id": []string{id}}.Encode()))
}

func decodeFileTaskCursor(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	parsed, err := url.ParseQuery(string(decoded))
	if err != nil || len(parsed.Get("id")) == 0 || strings.TrimSpace(parsed.Get("id")) != parsed.Get("id") {
		return "", ErrInvalidInput
	}
	return parsed.Get("id"), nil
}
