package http

import (
	"context"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
)

const (
	aiEmbeddingPollInterval = 3 * time.Second
	aiEmbeddingChunkRunes   = 1800
	aiEmbeddingChunkOverlap = 200
)

// startAIEmbeddingWorker is intentionally enabled only when an embedding
// model is configured. Without one, the normal PostgreSQL text search remains
// the zero-configuration fallback and no durable queue rows are created.
func (handler *Handler) startAIEmbeddingWorker() {
	if !handler.semanticIndexEnabled() || handler.repository == nil {
		return
	}
	go func() {
		process := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			jobs, err := handler.repository.ClaimAIEmbeddingJobs(ctx, 10)
			if err != nil {
				return
			}
			for _, job := range jobs {
				handler.processAIEmbeddingJob(job)
			}
		}
		process()
		ticker := time.NewTicker(aiEmbeddingPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				process()
			}
		}
	}()
}

func (handler *Handler) EnqueueAIPageEmbedding(workspaceID, pageID string) {
	if !handler.semanticIndexEnabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = handler.repository.EnqueueAIPageEmbedding(ctx, workspaceID, pageID)
	}()
}

func (handler *Handler) enqueueAIAttachmentEmbedding(workspaceID, attachmentID string) {
	if !handler.semanticIndexEnabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = handler.repository.EnqueueAIAttachmentEmbedding(ctx, workspaceID, attachmentID)
	}()
}

func (handler *Handler) semanticIndexEnabled() bool {
	if handler == nil || handler.aiProvider == nil || !handler.aiProvider.EmbeddingConfigured() {
		return false
	}
	// pgvector installations can use the same durable JSONB fallback. The
	// remote Turbopuffer driver is intentionally not claimed until a dedicated
	// Go adapter is implemented.
	return handler.aiVectorDriver == "" || handler.aiVectorDriver == "postgres" || handler.aiVectorDriver == "jsonb" || handler.aiVectorDriver == "pgvector"
}

func (handler *Handler) processAIEmbeddingJob(job postgres.AIEmbeddingJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var source postgres.AIEmbeddingSource
	var err error
	if job.PageID != nil {
		source, err = handler.repository.AIEmbeddingPageSource(ctx, *job.PageID, job.WorkspaceID)
	} else if job.AttachmentID != nil {
		source, err = handler.repository.AIEmbeddingAttachmentSource(ctx, *job.AttachmentID, job.WorkspaceID)
	} else {
		err = postgres.ErrInvalidInput
	}
	if err != nil {
		if err == postgres.ErrNotFound {
			_ = handler.repository.DeleteAIEmbeddings(ctx, job.WorkspaceID, job.PageID, job.AttachmentID)
			_ = handler.repository.CompleteAIEmbeddingJob(ctx, job.ID)
			return
		}
		_ = handler.repository.FailAIEmbeddingJob(ctx, job.ID, err.Error())
		return
	}

	text := strings.TrimSpace(source.Title)
	if source.Content != "" {
		if text != "" {
			text += "\n"
		}
		text += strings.TrimSpace(source.Content)
	}
	parts := postgresAIEmbeddingChunks(text)
	if len(parts) == 0 {
		if err = handler.repository.ReplaceAIEmbeddings(ctx, source, handler.aiProvider.EmbeddingModel(), nil); err != nil {
			_ = handler.repository.FailAIEmbeddingJob(ctx, job.ID, err.Error())
			return
		}
		_ = handler.repository.CompleteAIEmbeddingJob(ctx, job.ID)
		return
	}
	vectors, err := handler.aiProvider.Embed(ctx, parts)
	if err != nil {
		_ = handler.repository.FailAIEmbeddingJob(ctx, job.ID, err.Error())
		return
	}
	chunks := make([]postgres.AIEmbeddingChunk, 0, len(vectors))
	for _, vector := range vectors {
		if vector.Index < 0 || vector.Index >= len(parts) {
			continue
		}
		chunks = append(chunks, postgres.AIEmbeddingChunk{Index: vector.Index, Content: parts[vector.Index], Embedding: vector.Embedding})
	}
	if err = handler.repository.ReplaceAIEmbeddings(ctx, source, handler.aiProvider.EmbeddingModel(), chunks); err != nil {
		_ = handler.repository.FailAIEmbeddingJob(ctx, job.ID, err.Error())
		return
	}
	_ = handler.repository.CompleteAIEmbeddingJob(ctx, job.ID)
}

func postgresAIEmbeddingChunks(text string) []string {
	return postgresAIEmbeddingChunksWithLimits(text, aiEmbeddingChunkRunes, aiEmbeddingChunkOverlap)
}

// Kept as a small adapter so the chunking policy remains testable without
// exposing repository implementation details from the HTTP package.
func postgresAIEmbeddingChunksWithLimits(text string, maxRunes, overlap int) []string {
	return postgres.AIEmbeddingChunks(text, maxRunes, overlap)
}
