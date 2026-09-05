package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPagePermissionMembersCursorIntegration(t *testing.T) {
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
CREATE TEMP TABLE pages(id text, parent_page_id text, workspace_id text, deleted_at timestamptz, slug_id text, title text, icon text, is_base boolean, space_id text);
CREATE TEMP TABLE page_access(id text, page_id text, workspace_id text);
CREATE TEMP TABLE page_permissions(id text, page_access_id text, user_id text, group_id text, role text, created_at timestamptz);
CREATE TEMP TABLE users(id text, name text, email text, avatar_url text, deleted_at timestamptz);
CREATE TEMP TABLE groups(id text, name text, is_default boolean, deleted_at timestamptz);
CREATE TEMP TABLE group_users(group_id text);
INSERT INTO pages VALUES ('page', NULL, 'workspace', NULL, 'slug', 'Page', NULL, false, 'space');
INSERT INTO page_access VALUES ('access', 'page', 'workspace');
INSERT INTO users VALUES ('user-1', 'Alice', 'alice@example.com', NULL, NULL);
INSERT INTO groups VALUES ('group-1', 'Editors', false, NULL);
INSERT INTO page_permissions VALUES
 ('permission-user', 'access', 'user-1', NULL, 'reader', now()),
 ('permission-group', 'access', NULL, 'group-1', 'writer', now());
`); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	first, err := repository.PagePermissionMembers(ctx, "page", "workspace", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Type != "group" || !first.Meta.HasNextPage || first.Meta.NextCursor == nil {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second, err := repository.PagePermissionMembers(ctx, "page", "workspace", *first.Meta.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Type != "user" || second.Meta.HasNextPage {
		t.Fatalf("unexpected second page: %+v", second)
	}
}
