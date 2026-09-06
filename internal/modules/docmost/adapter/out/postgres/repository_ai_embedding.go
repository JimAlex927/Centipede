package postgres

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AIEmbeddingJob is a durable semantic-indexing task. The source columns are
// mutually exclusive, matching the database check constraint.
type AIEmbeddingJob struct {
	ID           string
	WorkspaceID  string
	PageID       *string
	AttachmentID *string
	Attempts     int
}

type AIEmbeddingSource struct {
	WorkspaceID  string
	PageID       *string
	AttachmentID *string
	Title        string
	Content      string
}

type AIEmbeddingChunk struct {
	Index     int
	Content   string
	Embedding []float32
}

type AIEmbeddingMatch struct {
	PageID       string
	AttachmentID *string
	Title        string
	SlugID       string
	SpaceSlug    string
	Content      string
	ChunkIndex   int
	Score        float32
}

func (repository *Repository) EnqueueAIPageEmbedding(ctx context.Context, workspaceID, pageID string) error {
	return repository.enqueueAIEmbedding(ctx, workspaceID, &pageID, nil)
}

func (repository *Repository) EnqueueAIAttachmentEmbedding(ctx context.Context, workspaceID, attachmentID string) error {
	return repository.enqueueAIEmbedding(ctx, workspaceID, nil, &attachmentID)
}

func (repository *Repository) enqueueAIEmbedding(ctx context.Context, workspaceID string, pageID, attachmentID *string) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	query := `
INSERT INTO ai_embedding_jobs (id, workspace_id, page_id, attachment_id, status, attempts, next_run_at)
VALUES ($1, $2, $3, $4, 'pending', 0, now())
	`
	if pageID != nil {
		query += `ON CONFLICT (workspace_id, page_id) WHERE page_id IS NOT NULL DO UPDATE
SET status = 'pending', attempts = 0, next_run_at = now(), error_message = NULL, updated_at = now()`
	} else {
		query += `ON CONFLICT (workspace_id, attachment_id) WHERE attachment_id IS NOT NULL DO UPDATE
SET status = 'pending', attempts = 0, next_run_at = now(), error_message = NULL, updated_at = now()`
	}
	_, err = repository.db.Exec(ctx, query, id, workspaceID, pageID, attachmentID)
	return err
}

// ClaimAIEmbeddingJobs atomically reserves work for one process. Stale jobs
// are recoverable after a crash without requiring Redis or a second worker.
func (repository *Repository) ClaimAIEmbeddingJobs(ctx context.Context, limit int) ([]AIEmbeddingJob, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
WITH picked AS (
  SELECT id FROM ai_embedding_jobs
  WHERE (status = 'pending' OR (status = 'processing' AND updated_at < now() - interval '10 minutes'))
    AND next_run_at <= now()
  ORDER BY created_at, id
  LIMIT $1
  FOR UPDATE SKIP LOCKED
)
UPDATE ai_embedding_jobs j
SET status = 'processing', updated_at = now()
FROM picked
WHERE j.id = picked.id
RETURNING j.id::text, j.workspace_id::text, j.page_id::text, j.attachment_id::text, j.attempts`, limit)
	if err != nil {
		return nil, err
	}
	jobs := make([]AIEmbeddingJob, 0, limit)
	for rows.Next() {
		var job AIEmbeddingJob
		if err := rows.Scan(&job.ID, &job.WorkspaceID, &job.PageID, &job.AttachmentID, &job.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (repository *Repository) CompleteAIEmbeddingJob(ctx context.Context, jobID string) error {
	_, err := repository.db.Exec(ctx, `UPDATE ai_embedding_jobs SET status = 'success', error_message = NULL, updated_at = now() WHERE id = $1`, jobID)
	return err
}

func (repository *Repository) FailAIEmbeddingJob(ctx context.Context, jobID string, message string) error {
	_, err := repository.db.Exec(ctx, `
UPDATE ai_embedding_jobs
SET attempts = attempts + 1,
    status = CASE WHEN attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END,
    next_run_at = CASE WHEN attempts + 1 >= 5 THEN now() ELSE now() + LEAST((attempts + 1) * interval '1 minute', interval '30 minutes') END,
    error_message = LEFT(NULLIF($2, ''), 1000), updated_at = now()
WHERE id = $1`, jobID, message)
	return err
}

func (repository *Repository) AIEmbeddingPageSource(ctx context.Context, pageID, workspaceID string) (AIEmbeddingSource, error) {
	var source AIEmbeddingSource
	var id string
	err := repository.db.QueryRow(ctx, `SELECT id::text, workspace_id::text, COALESCE(title, ''), COALESCE(text_content, '') FROM pages WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL`, pageID, workspaceID).Scan(&id, &source.WorkspaceID, &source.Title, &source.Content)
	if err != nil {
		if err == pgx.ErrNoRows {
			return AIEmbeddingSource{}, ErrNotFound
		}
		return AIEmbeddingSource{}, err
	}
	source.PageID = &id
	return source, nil
}

func (repository *Repository) AIEmbeddingAttachmentSource(ctx context.Context, attachmentID, workspaceID string) (AIEmbeddingSource, error) {
	var source AIEmbeddingSource
	var id string
	err := repository.db.QueryRow(ctx, `SELECT a.id::text, a.workspace_id::text, COALESCE(a.file_name, ''), COALESCE(a.text_content, '') FROM attachments a WHERE a.id = $1 AND a.workspace_id = $2 AND a.type = 'file' AND a.deleted_at IS NULL`, attachmentID, workspaceID).Scan(&id, &source.WorkspaceID, &source.Title, &source.Content)
	if err != nil {
		if err == pgx.ErrNoRows {
			return AIEmbeddingSource{}, ErrNotFound
		}
		return AIEmbeddingSource{}, err
	}
	source.AttachmentID = &id
	return source, nil
}

func (repository *Repository) ReplaceAIEmbeddings(ctx context.Context, source AIEmbeddingSource, model string, chunks []AIEmbeddingChunk) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return ErrInvalidInput
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM ai_embeddings WHERE workspace_id = $1 AND model_name = $2 AND (($3::uuid IS NOT NULL AND page_id = $3) OR ($4::uuid IS NOT NULL AND attachment_id = $4))`, source.WorkspaceID, model, source.PageID, source.AttachmentID); err != nil {
		return err
	}
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.Content) == "" || len(chunk.Embedding) == 0 {
			continue
		}
		id, idErr := newUUID()
		if idErr != nil {
			return idErr
		}
		vector, marshalErr := json.Marshal(chunk.Embedding)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO ai_embeddings (id, workspace_id, page_id, attachment_id, model_name, chunk_index, content, embedding) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`, id, source.WorkspaceID, source.PageID, source.AttachmentID, model, chunk.Index, chunk.Content, vector); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (repository *Repository) DeleteAIEmbeddings(ctx context.Context, workspaceID string, pageID, attachmentID *string) error {
	_, err := repository.db.Exec(ctx, `DELETE FROM ai_embeddings WHERE workspace_id = $1 AND (($2::uuid IS NOT NULL AND page_id = $2) OR ($3::uuid IS NOT NULL AND attachment_id = $3))`, workspaceID, pageID, attachmentID)
	return err
}

func (repository *Repository) SemanticSearch(ctx context.Context, workspaceID, model string, query []float32, spaceID *string, limit int, viewerID string, viewerAdmin bool) ([]AIEmbeddingMatch, error) {
	if len(query) == 0 || strings.TrimSpace(model) == "" {
		return []AIEmbeddingMatch{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := repository.db.Query(ctx, `
SELECT COALESCE(e.page_id, a.page_id)::text, e.attachment_id::text,
       COALESCE(p.title, ''), COALESCE(p.slug_id, ''), COALESCE(s.slug, ''),
       e.chunk_index, e.content, e.embedding
FROM ai_embeddings e
LEFT JOIN attachments a ON a.id = e.attachment_id
JOIN pages p ON p.id = COALESCE(e.page_id, a.page_id)
JOIN spaces s ON s.id = p.space_id
WHERE e.workspace_id = $1 AND e.model_name = $2
  AND p.deleted_at IS NULL AND ($3::uuid IS NULL OR p.space_id = $3)
  AND `+strings.NewReplacer("$8", "$4", "$9", "$5").Replace(pageListAccessSQL)+`
ORDER BY e.updated_at DESC`, workspaceID, model, spaceID, viewerID, viewerAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]AIEmbeddingMatch, 0)
	for rows.Next() {
		var item AIEmbeddingMatch
		var rawVector []byte
		if err := rows.Scan(&item.PageID, &item.AttachmentID, &item.Title, &item.SlugID, &item.SpaceSlug, &item.ChunkIndex, &item.Content, &rawVector); err != nil {
			return nil, err
		}
		var vector []float32
		if err := json.Unmarshal(rawVector, &vector); err != nil {
			continue
		}
		item.Score = cosineSimilarity(query, vector)
		if item.Score > 0 {
			results = append(results, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(results, func(left, right int) bool { return results[left].Score > results[right].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func cosineSimilarity(left, right []float32) float32 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		dot += float64(left[index]) * float64(right[index])
		leftNorm += float64(left[index]) * float64(left[index])
		rightNorm += float64(right[index]) * float64(right[index])
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	score := dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	return float32(score)
}

func AIEmbeddingChunks(content string, maxRunes, overlap int) []string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) == 0 {
		return nil
	}
	if maxRunes <= 0 {
		maxRunes = 1800
	}
	if overlap < 0 || overlap >= maxRunes {
		overlap = 200
	}
	step := maxRunes - overlap
	chunks := make([]string, 0, (len(runes)+step-1)/step)
	for start := 0; start < len(runes); start += step {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(runes) {
			break
		}
	}
	return chunks
}
