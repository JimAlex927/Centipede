package postgres

import (
	"context"
	"errors"

	"centipede/internal/modules/docmost/domain"

	"github.com/jackc/pgx/v5"
)

const attachmentColumns = `
a.id::text, a.file_name, a.file_path, COALESCE(a.file_size, 0), a.file_ext,
a.mime_type, a.type, a.creator_id::text, a.page_id::text, a.space_id::text,
a.workspace_id::text, a.created_at, a.updated_at, a.deleted_at`

func scanAttachment(row rowScanner) (domain.Attachment, error) {
	var attachment domain.Attachment
	err := row.Scan(&attachment.ID, &attachment.FileName, &attachment.FilePath,
		&attachment.FileSize, &attachment.FileExt, &attachment.MimeType, &attachment.Type,
		&attachment.CreatorID, &attachment.PageID, &attachment.SpaceID,
		&attachment.WorkspaceID, &attachment.CreatedAt, &attachment.UpdatedAt, &attachment.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Attachment{}, ErrNotFound
	}
	return attachment, err
}

func (repository *Repository) AttachmentByID(ctx context.Context, attachmentID, workspaceID string) (domain.Attachment, error) {
	return scanAttachment(repository.db.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM attachments a WHERE a.id = $1 AND a.workspace_id = $2 AND a.deleted_at IS NULL`, attachmentID, workspaceID))
}

func (repository *Repository) AttachmentsByIDs(ctx context.Context, attachmentIDs []string, workspaceID string) ([]domain.Attachment, error) {
	if len(attachmentIDs) == 0 {
		return []domain.Attachment{}, nil
	}
	rows, err := repository.db.Query(ctx, `SELECT `+attachmentColumns+`
FROM attachments a
WHERE a.workspace_id = $1 AND a.id::text = ANY($2) AND a.type = 'file' AND a.deleted_at IS NULL
ORDER BY a.id`, workspaceID, attachmentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Attachment, 0, len(attachmentIDs))
	for rows.Next() {
		item, scanErr := scanAttachment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type AttachmentInput struct {
	ID, FileName, FilePath, FileExt, MimeType, Type string
	FileSize                                        int64
	CreatorID, WorkspaceID                          string
	PageID, SpaceID                                 *string
}

func (repository *Repository) AttachmentByPublicImageID(ctx context.Context, attachmentID string) (domain.Attachment, error) {
	return scanAttachment(repository.db.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM attachments a
JOIN workspaces w ON w.id = a.workspace_id AND w.deleted_at IS NULL
WHERE a.id::text = $1 AND a.type IN ('avatar', 'space-icon', 'workspace-icon')
AND a.mime_type IN ('image/png', 'image/jpeg', 'image/gif', 'image/webp')
AND a.deleted_at IS NULL`, attachmentID))
}

func (repository *Repository) AttachmentByPublicImagePath(ctx context.Context, attachmentType, fileName, workspaceID string) (domain.Attachment, error) {
	return scanAttachment(repository.db.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM attachments a
JOIN workspaces w ON w.id = a.workspace_id AND w.deleted_at IS NULL
WHERE a.type = $1 AND a.file_name = $2 AND a.workspace_id = $3
AND a.mime_type IN ('image/png', 'image/jpeg', 'image/gif', 'image/webp')
AND a.deleted_at IS NULL`, attachmentType, fileName, workspaceID))
}

func (repository *Repository) CreateAttachment(ctx context.Context, input AttachmentInput) (domain.Attachment, error) {
	_, err := repository.db.Exec(ctx, `INSERT INTO attachments
(id, file_name, file_path, file_size, file_ext, mime_type, type, creator_id, page_id, space_id, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, input.ID, input.FileName,
		input.FilePath, input.FileSize, input.FileExt, input.MimeType, input.Type,
		input.CreatorID, input.PageID, input.SpaceID, input.WorkspaceID)
	if err != nil {
		return domain.Attachment{}, err
	}
	return repository.AttachmentByID(ctx, input.ID, input.WorkspaceID)
}

func (repository *Repository) UpdateAttachmentSize(ctx context.Context, attachmentID, pageID, workspaceID, extension string, size int64) (domain.Attachment, error) {
	result, err := repository.db.Exec(ctx, `UPDATE attachments SET file_size = $5, updated_at = now()
WHERE id = $1 AND page_id = $2 AND workspace_id = $3 AND lower(file_ext) = lower($4) AND type = 'file' AND deleted_at IS NULL`,
		attachmentID, pageID, workspaceID, extension, size)
	if err != nil {
		return domain.Attachment{}, err
	}
	if result.RowsAffected() == 0 {
		return domain.Attachment{}, ErrNotFound
	}
	return repository.AttachmentByID(ctx, attachmentID, workspaceID)
}

func (repository *Repository) DeleteAttachment(ctx context.Context, attachmentID, workspaceID string) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM attachments WHERE id = $1 AND workspace_id = $2`, attachmentID, workspaceID)
	return err
}

func (repository *Repository) PageAttachments(ctx context.Context, pageID, workspaceID string, limit int) (domain.Pagination[domain.Attachment], error) {
	rows, err := repository.db.Query(ctx, `SELECT `+attachmentColumns+`, u.id::text, u.name, u.avatar_url
FROM attachments a LEFT JOIN users u ON u.id = a.creator_id
WHERE a.page_id = $1 AND a.workspace_id = $2 AND a.type = 'file' AND a.deleted_at IS NULL
ORDER BY a.created_at DESC LIMIT $3`, pageID, workspaceID, normalizeLimit(limit))
	if err != nil {
		return domain.Pagination[domain.Attachment]{}, err
	}
	defer rows.Close()
	items := make([]domain.Attachment, 0)
	for rows.Next() {
		var item domain.Attachment
		var creatorID, creatorName, creatorAvatar *string
		if err = rows.Scan(&item.ID, &item.FileName, &item.FilePath, &item.FileSize,
			&item.FileExt, &item.MimeType, &item.Type, &item.CreatorID, &item.PageID,
			&item.SpaceID, &item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt,
			&item.DeletedAt, &creatorID, &creatorName, &creatorAvatar); err != nil {
			return domain.Pagination[domain.Attachment]{}, err
		}
		if creatorID != nil {
			item.Creator = &domain.UserSummary{ID: *creatorID, Name: creatorName, AvatarURL: creatorAvatar}
		}
		items = append(items, item)
	}
	return page(items, limit), rows.Err()
}

func (repository *Repository) SetUserAvatar(ctx context.Context, userID, workspaceID, url string) error {
	_, err := repository.db.Exec(ctx, `UPDATE users SET avatar_url = NULLIF($3, ''), updated_at = now() WHERE id = $1 AND workspace_id = $2`, userID, workspaceID, url)
	return err
}

func (repository *Repository) SetSpaceLogo(ctx context.Context, spaceID, workspaceID, url string) error {
	_, err := repository.db.Exec(ctx, `UPDATE spaces SET logo = NULLIF($3, ''), updated_at = now() WHERE id = $1 AND workspace_id = $2`, spaceID, workspaceID, url)
	return err
}

func (repository *Repository) SetWorkspaceLogo(ctx context.Context, workspaceID, url string) error {
	_, err := repository.db.Exec(ctx, `UPDATE workspaces SET logo = NULLIF($2, ''), updated_at = now() WHERE id = $1`, workspaceID, url)
	return err
}

func (repository *Repository) RemoveIconAttachments(ctx context.Context, attachmentType, workspaceID, userID string, spaceID *string) ([]string, error) {
	rows, err := repository.db.Query(ctx, `DELETE FROM attachments
WHERE type = $1 AND workspace_id = $2 AND deleted_at IS NULL
  AND ($1 <> 'avatar' OR creator_id = $3)
  AND ($1 <> 'space-icon' OR space_id = $4)
RETURNING file_path`, attachmentType, workspaceID, userID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make([]string, 0)
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}
