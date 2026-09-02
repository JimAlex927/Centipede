package postgres

import (
	"context"
	"errors"
	"fmt"

	"centipede/internal/modules/vocabulary/application"
	"centipede/internal/modules/vocabulary/domain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (repository *Repository) Create(ctx context.Context, entry domain.Entry) (domain.Entry, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return domain.Entry{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row := tx.QueryRow(ctx, `
		INSERT INTO vocabulary_entries (user_id, language_tag, original_text, normalized_text, lemma, definition, notes, status, source_book, source_chapter, source_location)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at
	`, entry.UserID, entry.LanguageTag, entry.OriginalText, entry.NormalizedText, entry.Lemma, entry.Definition, entry.Notes, entry.Status, entry.Source.Book, entry.Source.Chapter, entry.Source.Location)
	if err := row.Scan(&entry.ID, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
		return domain.Entry{}, err
	}
	for _, item := range entry.Contexts {
		if _, err := tx.Exec(ctx, `INSERT INTO vocabulary_contexts (entry_id, context_text, source_location) VALUES ($1, $2, $3)`, entry.ID, item.Text, item.Location); err != nil {
			return domain.Entry{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Entry{}, err
	}
	return entry, nil
}

func (repository *Repository) List(ctx context.Context, filter application.Filter) ([]domain.Entry, error) {
	conditions := []string{"user_id = $1"}
	args := []any{filter.UserID}
	if filter.Language != "" {
		args = append(args, filter.Language)
		conditions = append(conditions, fmt.Sprintf("language_tag = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}
	if filter.Query != "" {
		args = append(args, filter.Query)
		placeholder := fmt.Sprintf("$%d", len(args))
		conditions = append(conditions, fmt.Sprintf("(original_text ILIKE '%%' || %s || '%%' OR definition ILIKE '%%' || %s || '%%')", placeholder, placeholder))
	}
	args = append(args, filter.Limit, filter.Offset)
	limitPlaceholder := fmt.Sprintf("$%d", len(args)-1)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))
	query := fmt.Sprintf(`
		SELECT id, user_id, language_tag, original_text, normalized_text, COALESCE(lemma, ''), definition, notes, status,
		       source_book, source_chapter, source_location, created_at, updated_at
		FROM vocabulary_entries
		WHERE %s
		ORDER BY updated_at DESC, id DESC
		LIMIT %s OFFSET %s
	`, joinConditions(conditions), limitPlaceholder, offsetPlaceholder)
	rows, err := repository.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]domain.Entry, 0)
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entry.Contexts, err = repository.contexts(ctx, entry.ID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (repository *Repository) FindByID(ctx context.Context, userID, entryID int64) (domain.Entry, error) {
	row := repository.pool.QueryRow(ctx, `
		SELECT id, user_id, language_tag, original_text, normalized_text, COALESCE(lemma, ''), definition, notes, status,
		       source_book, source_chapter, source_location, created_at, updated_at
		FROM vocabulary_entries WHERE id = $1 AND user_id = $2
	`, entryID, userID)
	entry, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Entry{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Entry{}, err
	}
	entry.Contexts, err = repository.contexts(ctx, entry.ID)
	return entry, err
}

func (repository *Repository) UpdateStatus(ctx context.Context, userID, entryID int64, status string) (domain.Entry, error) {
	row := repository.pool.QueryRow(ctx, `
		UPDATE vocabulary_entries SET status = $3, updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, language_tag, original_text, normalized_text, COALESCE(lemma, ''), definition, notes, status,
		          source_book, source_chapter, source_location, created_at, updated_at
	`, entryID, userID, status)
	entry, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Entry{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Entry{}, err
	}
	entry.Contexts, err = repository.contexts(ctx, entry.ID)
	return entry, err
}

func (repository *Repository) contexts(ctx context.Context, entryID int64) ([]domain.Context, error) {
	rows, err := repository.pool.Query(ctx, `SELECT id, context_text, source_location FROM vocabulary_contexts WHERE entry_id = $1 ORDER BY created_at, id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	contexts := make([]domain.Context, 0)
	for rows.Next() {
		var item domain.Context
		if err := rows.Scan(&item.ID, &item.Text, &item.Location); err != nil {
			return nil, err
		}
		contexts = append(contexts, item)
	}
	return contexts, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanEntry(row rowScanner) (domain.Entry, error) {
	var entry domain.Entry
	err := row.Scan(&entry.ID, &entry.UserID, &entry.LanguageTag, &entry.OriginalText, &entry.NormalizedText, &entry.Lemma, &entry.Definition, &entry.Notes, &entry.Status, &entry.Source.Book, &entry.Source.Chapter, &entry.Source.Location, &entry.CreatedAt, &entry.UpdatedAt)
	return entry, err
}

func joinConditions(conditions []string) string {
	result := ""
	for index, condition := range conditions {
		if index > 0 {
			result += " AND "
		}
		result += condition
	}
	return result
}
