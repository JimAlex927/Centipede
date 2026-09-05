package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollaborationStoreSaveVersionIntegration(t *testing.T) {
	url := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
CREATE TEMP TABLE pages(
  id uuid, slug_id text, title text, content jsonb, icon text, cover_photo text,
  last_updated_by_id uuid, creator_id uuid, space_id uuid, workspace_id uuid,
  deleted_at timestamptz
);
CREATE TEMP TABLE page_history(
  page_id uuid, slug_id text, title text, content jsonb, icon text, cover_photo text,
  last_updated_by_id uuid, contributor_ids uuid[], space_id uuid, workspace_id uuid
);
INSERT INTO pages VALUES
 ('00000000-0000-0000-0000-000000000001', 'slug', 'Page', '{"type":"doc","content":[]}',
  NULL, NULL, '00000000-0000-0000-0000-000000000011',
  '00000000-0000-0000-0000-000000000011',
  '00000000-0000-0000-0000-000000000003',
  '00000000-0000-0000-0000-000000000002', NULL);
`); err != nil {
		t.Fatal(err)
	}

	store := New(pool).CollaborationStore()
	room := "page.00000000-0000-0000-0000-000000000001"
	if version, err := store.SaveVersion(ctx, room, "auto"); err != nil || version != 1 {
		t.Fatalf("first collaboration version = %d, %v", version, err)
	}
	if version, err := store.SaveVersion(ctx, room, "auto"); err != nil || version != 0 {
		t.Fatalf("duplicate collaboration version = %d, %v", version, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pages SET content = '{"type":"doc","content":[{"type":"paragraph"}]}'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if version, err := store.SaveVersion(ctx, room, "auto"); err != nil || version != 1 {
		t.Fatalf("changed collaboration version = %d, %v", version, err)
	}
}
