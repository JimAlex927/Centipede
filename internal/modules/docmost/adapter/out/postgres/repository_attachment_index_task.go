package postgres

import (
	"context"

	"centipede/internal/modules/docmost/domain"
)

// ProcessingAttachmentIndexTasks returns durable attachment-index jobs that
// can be resumed after a Go process restart. The attachment id is stored in
// file_tasks.source so re-indexing the same attachment can create a fresh job
// without colliding with a previous completed job.
func (repository *Repository) ProcessingAttachmentIndexTasks(ctx context.Context, limit int) ([]domain.FileTask, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := repository.db.Query(ctx, `SELECT `+fileTaskColumns+`
FROM file_tasks f
WHERE f.type = 'attachment-index' AND f.status = 'processing'
  AND f.source IS NOT NULL AND f.source <> '' AND f.deleted_at IS NULL
ORDER BY f.created_at ASC, f.id ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.FileTask, 0)
	for rows.Next() {
		item, scanErr := scanFileTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
