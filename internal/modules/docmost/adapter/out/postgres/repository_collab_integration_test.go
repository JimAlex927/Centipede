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

func TestSessionRetentionIntegration(t *testing.T) {
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
CREATE TEMP TABLE user_sessions(
  id uuid, user_id uuid, last_active_at timestamptz, created_at timestamptz,
  revoked_at timestamptz, expires_at timestamptz
);
INSERT INTO user_sessions
SELECT md5(n::text)::uuid, '00000000-0000-0000-0000-000000000011'::uuid,
       now() - (n || ' seconds')::interval, now() - (n || ' seconds')::interval,
       NULL, now() + interval '1 day'
FROM generate_series(1, 26) AS n;
`); err != nil {
		t.Fatal(err)
	}
	if err := New(pool).TrimExcessSessions(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 25 {
		t.Fatalf("session retention kept %d sessions, want 25", count)
	}
}
