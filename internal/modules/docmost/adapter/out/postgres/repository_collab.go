package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/yjs"

	"github.com/jackc/pgx/v5"
	"github.com/reearth/ygo/crdt"
)

// CollaborationStore is the persistence adapter used by the Go Yjs server.
// The room name is page.<page uuid>, matching the Docmost frontend.
type CollaborationStore struct {
	db *Repository
}

func (repository *Repository) CollaborationStore() *CollaborationStore {
	return &CollaborationStore{db: repository}
}

func pageIDFromRoom(room string) (string, error) {
	pageID := strings.TrimPrefix(room, "page.")
	if pageID == room || strings.TrimSpace(pageID) == "" {
		return "", fmt.Errorf("invalid collaboration room %q", room)
	}
	return pageID, nil
}

// LoadDoc returns a complete Yjs V1 state update. Existing pages created by
// the REST API only have Tiptap JSON, so they are converted on first access.
func (store *CollaborationStore) LoadDoc(room string) ([]byte, error) {
	pageID, err := pageIDFromRoom(room)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var state, content []byte
	err = store.db.db.QueryRow(ctx, `
SELECT ydoc, COALESCE(content, '{"type":"doc","content":[]}'::jsonb)
FROM pages WHERE id = $1 AND deleted_at IS NULL`, pageID).Scan(&state, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	doc, err := yjs.Load(state, content)
	if err != nil {
		return nil, fmt.Errorf("load page ydoc: %w", err)
	}
	return crdt.EncodeStateAsUpdateV1(doc, nil), nil
}

// StoreUpdate applies an incremental update and writes both the canonical Ydoc
// and the Tiptap JSON projection. The row lock serializes concurrent Go
// processes writing the same page.
func (store *CollaborationStore) StoreUpdate(room string, update []byte) error {
	pageID, err := pageIDFromRoom(room)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := store.db.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var state, content []byte
	err = tx.QueryRow(ctx, `
SELECT ydoc, COALESCE(content, '{"type":"doc","content":[]}'::jsonb)
FROM pages WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, pageID).Scan(&state, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	doc, err := yjs.Load(state, content)
	if err != nil {
		return fmt.Errorf("load page ydoc: %w", err)
	}
	if err = crdt.ApplyUpdateV1(doc, update, nil); err != nil {
		return fmt.Errorf("apply page update: %w", err)
	}
	jsonContent, textContent, err := yjs.JSON(doc)
	if err != nil {
		return fmt.Errorf("project page content: %w", err)
	}
	fullState := crdt.EncodeStateAsUpdateV1(doc, nil)
	_, err = tx.Exec(ctx, `
UPDATE pages SET ydoc = $2, content = $3::jsonb, text_content = $4, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`, pageID, fullState, jsonContent, textContent)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CollaborationPage returns the page's space and deletion state for the
// websocket authorization hooks.
func (repository *Repository) CollaborationPage(ctx context.Context, pageID, workspaceID string) (string, bool, error) {
	var spaceID string
	var deleted bool
	err := repository.db.QueryRow(ctx, `
SELECT space_id::text, deleted_at IS NOT NULL
FROM pages WHERE id = $1 AND workspace_id = $2`, pageID, workspaceID).Scan(&spaceID, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return spaceID, deleted, err
}
