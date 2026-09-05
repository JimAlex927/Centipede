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

func TestSyncBacklinksIntegration(t *testing.T) {
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
CREATE TEMP TABLE pages(id uuid, slug_id text, workspace_id uuid, deleted_at timestamptz);
CREATE TEMP TABLE backlinks(
  id uuid, source_page_id uuid, target_page_id uuid,
  workspace_id uuid, UNIQUE(source_page_id, target_page_id)
);
INSERT INTO pages VALUES
 ('00000000-0000-0000-0000-000000000001', 'source', '00000000-0000-0000-0000-000000000002', NULL),
 ('00000000-0000-0000-0000-000000000011', 'linked', '00000000-0000-0000-0000-000000000002', NULL),
 ('00000000-0000-0000-0000-000000000012', 'obsolete', '00000000-0000-0000-0000-000000000002', NULL);
INSERT INTO backlinks(source_page_id, target_page_id, workspace_id)
VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000012', '00000000-0000-0000-0000-000000000002');
`); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"entityType":"page","entityId":"00000000-0000-0000-0000-000000000011"}},{"type":"text","marks":[{"type":"link","attrs":{"internal":true,"href":"/p/linked"}}]}]}]}`)
	if err := New(pool).SyncBacklinks(ctx, "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", content); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM backlinks WHERE source_page_id = '00000000-0000-0000-0000-000000000001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backlink sync kept %d links, want 1", count)
	}
	var targetID string
	if err := pool.QueryRow(ctx, `SELECT target_page_id::text FROM backlinks WHERE source_page_id = '00000000-0000-0000-0000-000000000001'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if targetID != "00000000-0000-0000-0000-000000000011" {
		t.Fatalf("backlink sync resolved target %q", targetID)
	}
}

func TestSessionActivityIntegration(t *testing.T) {
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
CREATE TEMP TABLE users(id uuid, workspace_id uuid, last_active_at timestamptz);
CREATE TEMP TABLE user_sessions(
  id uuid, user_id uuid, workspace_id uuid, last_active_at timestamptz,
  revoked_at timestamptz, expires_at timestamptz
);
INSERT INTO users VALUES
 ('00000000-0000-0000-0000-000000000011', '00000000-0000-0000-0000-000000000002', now() - interval '1 day');
INSERT INTO user_sessions VALUES
 ('00000000-0000-0000-0000-000000000021', '00000000-0000-0000-0000-000000000011', '00000000-0000-0000-0000-000000000002', now() - interval '1 day', NULL, now() + interval '1 day');
`); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	active, err := repository.SessionActive(ctx, "00000000-0000-0000-0000-000000000021", "00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000002")
	if err != nil || !active {
		t.Fatalf("active session check = %v, %v", active, err)
	}
	var sessionActivity, userActivity time.Time
	if err := pool.QueryRow(ctx, `SELECT last_active_at FROM user_sessions`).Scan(&sessionActivity); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_active_at FROM users`).Scan(&userActivity); err != nil {
		t.Fatal(err)
	}
	if time.Since(sessionActivity) > time.Minute || time.Since(userActivity) > time.Minute {
		t.Fatalf("session activity was not refreshed: session=%v user=%v", sessionActivity, userActivity)
	}
}

func TestPageUpdateNotificationsIntegration(t *testing.T) {
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
  id uuid, workspace_id uuid, space_id uuid, deleted_at timestamptz
);
CREATE TEMP TABLE spaces(
  id uuid, deleted_at timestamptz, visibility text
);
CREATE TEMP TABLE users(
  id uuid, workspace_id uuid, deleted_at timestamptz,
  deactivated_at timestamptz, settings jsonb
);
CREATE TEMP TABLE watchers(
  user_id uuid, workspace_id uuid, page_id uuid, space_id uuid,
  muted_at timestamptz
);
CREATE TEMP TABLE space_members(
  space_id uuid, user_id uuid, group_id uuid, deleted_at timestamptz
);
CREATE TEMP TABLE group_users(group_id uuid, user_id uuid);
CREATE TEMP TABLE notifications(
  id text DEFAULT 'notification', user_id uuid, workspace_id uuid,
  type text, actor_id uuid, page_id uuid, space_id uuid,
  created_at timestamptz DEFAULT now()
);
INSERT INTO pages VALUES
  ('00000000-0000-0000-0000-000000000101',
   '00000000-0000-0000-0000-000000000102',
   '00000000-0000-0000-0000-000000000103', NULL);
INSERT INTO spaces VALUES
  ('00000000-0000-0000-0000-000000000103', NULL, 'public');
INSERT INTO users VALUES
  ('00000000-0000-0000-0000-000000000111',
   '00000000-0000-0000-0000-000000000102', NULL, NULL, '{}'::jsonb);
INSERT INTO watchers VALUES
  ('00000000-0000-0000-0000-000000000111',
   '00000000-0000-0000-0000-000000000102',
   '00000000-0000-0000-0000-000000000101', NULL, NULL);
`); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	actorID := "00000000-0000-0000-0000-000000000112"
	deliveries, err := repository.CreatePageUpdateNotifications(
		ctx,
		"00000000-0000-0000-0000-000000000101",
		"00000000-0000-0000-0000-000000000102",
		actorID,
		[]string{actorID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].UserID != "00000000-0000-0000-0000-000000000111" {
		t.Fatalf("page update deliveries = %#v, want one watcher delivery", deliveries)
	}
	second, err := repository.CreatePageUpdateNotifications(
		ctx,
		"00000000-0000-0000-0000-000000000101",
		"00000000-0000-0000-0000-000000000102",
		actorID,
		[]string{actorID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("page update cooldown delivered %d duplicate notifications", len(second))
	}
}
