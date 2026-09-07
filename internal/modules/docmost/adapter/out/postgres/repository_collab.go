package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"centipede/internal/modules/docmost/adapter/yjs"

	"github.com/jackc/pgx/v5"
	"github.com/reearth/ygo/crdt"
)

// CollaborationStore is the persistence adapter used by the Go Yjs server.
// The room name is page.<page uuid>, matching the Docmost frontend.
type CollaborationStore struct {
	db *Repository

	mu           sync.Mutex
	contributors map[string]map[string]struct{}
	onVersion    func(context.Context, string, string, []string, []byte)
	onUpdate     func(context.Context, string, string)
}

func (repository *Repository) CollaborationStore() *CollaborationStore {
	return &CollaborationStore{db: repository, contributors: make(map[string]map[string]struct{})}
}

func (store *CollaborationStore) AddContributor(room, userID string) {
	if strings.TrimSpace(room) == "" || strings.TrimSpace(userID) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.contributors[room] == nil {
		store.contributors[room] = make(map[string]struct{})
	}
	store.contributors[room][userID] = struct{}{}
}

func (store *CollaborationStore) consumeContributors(room string) []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	set := store.contributors[room]
	delete(store.contributors, room)
	result := make([]string, 0, len(set))
	for userID := range set {
		result = append(result, userID)
	}
	return result
}

func (store *CollaborationStore) SetVersionCallback(callback func(context.Context, string, string, []string, []byte)) {
	store.mu.Lock()
	store.onVersion = callback
	store.mu.Unlock()
}

func (store *CollaborationStore) SetUpdateCallback(callback func(context.Context, string, string)) {
	store.mu.Lock()
	store.onUpdate = callback
	store.mu.Unlock()
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
	// StoreUpdate persists ydoc as a complete Yjs V1 state update. Reusing it
	// avoids rebuilding the CRDT from the Tiptap JSON projection and encoding
	// the whole document again every time a cold room is opened. The fallback
	// remains necessary for pages created before collaboration initialized ydoc.
	if len(state) > 0 {
		return state, nil
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
	var workspaceID string
	err = tx.QueryRow(ctx, `
SELECT ydoc, COALESCE(content, '{"type":"doc","content":[]}'::jsonb), workspace_id::text
FROM pages WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, pageID).Scan(&state, &content, &workspaceID)
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
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// Backlinks are derived data. A failed refresh must not turn a successful
	// Yjs persistence write into a client-visible collaboration failure; the
	// next persistence flush will retry the idempotent rebuild.
	_ = store.db.SyncBacklinks(ctx, pageID, workspaceID, jsonContent)
	// Transclusion sources and references are also derived from the projected
	// Tiptap document. Keep their indexes in sync with collaboration saves so
	// freshly edited blocks are visible without a Node/Bull worker.
	_ = store.db.SyncTransclusions(ctx, pageID, workspaceID, jsonContent)
	store.mu.Lock()
	callback := store.onUpdate
	store.mu.Unlock()
	if callback != nil {
		callback(ctx, pageID, workspaceID)
	}
	return nil
}

// SaveVersion implements ygo's optional VersionableAdapter. Hocuspocus used
// to enqueue this work separately after a collaboration persistence flush;
// keeping it in the persistence adapter gives the standalone Go server the
// same user-visible page history without a Redis/Bull worker.
//
// The latest history content check makes the operation idempotent. ygo may
// call this hook after a flush that only changed awareness or after another
// writer has already captured the same state.
func (store *CollaborationStore) SaveVersion(ctx context.Context, room, _ string) (int64, error) {
	pageID, err := pageIDFromRoom(room)
	if err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var inserted int64
	var workspaceID, spaceID string
	var snapshot []byte
	err = store.db.db.QueryRow(ctx, `
INSERT INTO page_history
  (page_id, slug_id, title, content, icon, cover_photo, last_updated_by_id,
   contributor_ids, space_id, workspace_id)
SELECT p.id, p.slug_id, p.title, p.content, p.icon, p.cover_photo,
       COALESCE(p.last_updated_by_id, p.creator_id),
       ARRAY[COALESCE(p.last_updated_by_id, p.creator_id)], p.space_id, p.workspace_id
FROM pages p
WHERE p.id = $1 AND p.deleted_at IS NULL
  AND NOT (
    NOT EXISTS (
      SELECT 1 FROM page_history first_history
      WHERE first_history.page_id = p.id
    )
    AND p.content->>'type' = 'doc'
    AND jsonb_typeof(p.content->'content') = 'array'
    AND jsonb_array_length(p.content->'content') = 1
    AND p.content->'content'->0->>'type' = 'paragraph'
    AND (
      p.content->'content'->0->'content' IS NULL
      OR (
        jsonb_typeof(p.content->'content'->0->'content') = 'array'
        AND jsonb_array_length(p.content->'content'->0->'content') = 0
      )
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM page_history h
    WHERE h.page_id = p.id
      AND h.content IS NOT DISTINCT FROM p.content
  )

RETURNING 1, workspace_id::text, space_id::text, content`, pageID).Scan(&inserted, &workspaceID, &spaceID, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		// Match the Node history worker: a first empty paragraph is not a
		// meaningful version, and contributors from that transient save must
		// not leak into the next real edit.
		store.consumeContributors(room)
		return 0, nil
	}
	if err == nil && inserted > 0 {
		actors := store.consumeContributors(room)
		// Node adds users who edited a page to its watcher list while saving
		// collaboration history. Keep that behavior in the durable Go path;
		// notification delivery alone is not enough because later edits would
		// otherwise omit those contributors.
		_ = store.db.AddPageWatchers(ctx, actors, pageID, spaceID, workspaceID)
		store.mu.Lock()
		callback := store.onVersion
		store.mu.Unlock()
		if callback != nil {
			callback(ctx, pageID, workspaceID, actors, snapshot)
		}
	}
	return inserted, err
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
