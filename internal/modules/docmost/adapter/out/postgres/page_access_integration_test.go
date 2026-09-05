package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Temporary tables shadow application tables on this single test connection;
// the test never writes to the deployment's pages or permission records.
func TestPageAccessIntegration(t *testing.T) {
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
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE pages(id text, parent_page_id text, workspace_id text, deleted_at timestamptz);
 CREATE TEMP TABLE page_access(id text, page_id text);
 CREATE TEMP TABLE page_permissions(id text, page_access_id text, user_id text, group_id text, role text);
 CREATE TEMP TABLE group_users(user_id text, group_id text);
 INSERT INTO pages VALUES ('root',NULL,'workspace',NULL),('child','root','workspace',NULL);`)
	repo := New(pool)
	check := func(page, workspace string, want PageAccessResult, missing bool) {
		t.Helper()
		got, err := repo.PageAccess(ctx, page, workspace, "user")
		if missing {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected not found, got %v", err)
			}
			return
		}
		if err != nil || got != want {
			t.Fatalf("got %+v, %v; want %+v", got, err, want)
		}
	}
	check("missing", "workspace", PageAccessResult{}, true)
	check("child", "other", PageAccessResult{}, true)
	check("child", "workspace", PageAccessResult{CanAccess: true, CanEdit: true}, false)
	exec(`INSERT INTO page_access VALUES ('root-access','root');`)
	check("child", "workspace", PageAccessResult{HasRestriction: true}, false)
	exec(`INSERT INTO page_permissions VALUES ('permission','root-access','user',NULL,'reader');`)
	check("child", "workspace", PageAccessResult{HasRestriction: true, CanAccess: true}, false)
	exec(`INSERT INTO page_access VALUES ('child-access','child');
 INSERT INTO group_users VALUES ('user','group');
 INSERT INTO page_permissions VALUES ('group-permission','child-access',NULL,'group','writer');`)
	check("child", "workspace", PageAccessResult{HasRestriction: true, CanAccess: true, CanEdit: true}, false)
	exec(`DELETE FROM page_permissions WHERE id='permission';`)
	check("child", "workspace", PageAccessResult{HasRestriction: true}, false)
	exec(`UPDATE pages SET parent_page_id='child' WHERE id='root';`)
	check("child", "workspace", PageAccessResult{HasRestriction: true}, false)
	exec(`UPDATE pages SET deleted_at=now() WHERE id='child';`)
	check("child", "workspace", PageAccessResult{}, true)
}
