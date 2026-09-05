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

func TestPagePermissionMutationsIntegration(t *testing.T) {
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
CREATE TEMP TABLE pages(id uuid, parent_page_id uuid, workspace_id uuid, deleted_at timestamptz, slug_id text, title text, icon text, is_base boolean, space_id uuid);
CREATE TEMP TABLE page_access(id uuid, page_id uuid UNIQUE, workspace_id uuid, space_id uuid, access_level text, creator_id uuid, updated_at timestamptz);
CREATE TEMP TABLE page_permissions(id uuid, page_access_id uuid, user_id uuid, group_id uuid, role text, added_by_id uuid, created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now(), UNIQUE(page_access_id, user_id), UNIQUE(page_access_id, group_id));
CREATE TEMP TABLE users(id uuid, name text, email text, avatar_url text, workspace_id uuid, deleted_at timestamptz);
CREATE TEMP TABLE groups(id uuid, name text, is_default boolean, workspace_id uuid, deleted_at timestamptz);
CREATE TEMP TABLE group_users(group_id uuid);
INSERT INTO pages VALUES ('00000000-0000-0000-0000-000000000001', NULL, '00000000-0000-0000-0000-000000000002', NULL, 'slug', 'Page', NULL, false, '00000000-0000-0000-0000-000000000003');
INSERT INTO users VALUES ('00000000-0000-0000-0000-000000000011', 'Alice', 'alice@example.com', NULL, '00000000-0000-0000-0000-000000000002', NULL), ('00000000-0000-0000-0000-000000000012', 'Bob', 'bob@example.com', NULL, '00000000-0000-0000-0000-000000000002', NULL);
INSERT INTO groups VALUES ('00000000-0000-0000-0000-000000000021', 'Editors', false, '00000000-0000-0000-0000-000000000002', NULL);
`); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	pageID := "00000000-0000-0000-0000-000000000001"
	workspaceID := "00000000-0000-0000-0000-000000000002"
	user1ID := "00000000-0000-0000-0000-000000000011"
	user2ID := "00000000-0000-0000-0000-000000000012"
	groupID := "00000000-0000-0000-0000-000000000021"
	if err := repository.RestrictPage(ctx, pageID, workspaceID, user1ID); err != nil {
		t.Fatal(err)
	}
	restriction, err := repository.PageRestriction(ctx, pageID, workspaceID)
	if err != nil || !restriction.Direct || restriction.RestrictionID == "" {
		t.Fatalf("unexpected restriction: %+v, %v", restriction, err)
	}
	if err := repository.AddPagePermissions(ctx, pageID, workspaceID, user1ID, "writer", []string{user1ID, user2ID}, []string{groupID}); err != nil {
		t.Fatal(err)
	}
	reader := "reader"
	if err := repository.UpdatePagePermissionRole(ctx, pageID, workspaceID, reader, &user2ID, nil); err != nil {
		t.Fatal(err)
	}
	members, err := repository.PagePermissionMembers(ctx, pageID, workspaceID, "", 10)
	if err != nil || len(members.Items) != 3 {
		t.Fatalf("unexpected members: %+v, %v", members, err)
	}
	if err := repository.RemovePagePermissions(ctx, pageID, workspaceID, []string{user2ID}, []string{groupID}); err != nil {
		t.Fatal(err)
	}
	members, err = repository.PagePermissionMembers(ctx, pageID, workspaceID, "", 10)
	if err != nil || len(members.Items) != 1 || members.Items[0].Role != "writer" {
		t.Fatalf("unexpected remaining members: %+v, %v", members, err)
	}
	if err := repository.RemovePageRestriction(ctx, pageID, workspaceID); err != nil {
		t.Fatal(err)
	}
	restriction, err = repository.PageRestriction(ctx, pageID, workspaceID)
	if err != nil || restriction.Direct || restriction.Inherited {
		t.Fatalf("restriction was not removed: %+v, %v", restriction, err)
	}
}
