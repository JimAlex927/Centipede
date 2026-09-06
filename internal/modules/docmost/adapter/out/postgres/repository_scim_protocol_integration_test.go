package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpdateSCIMUserReactivationHonorsLicenseSeatLimit(t *testing.T) {
	databaseURL := os.Getenv("DOCMOST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DOCMOST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(databaseURL)
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
CREATE TEMP TABLE workspaces(id uuid, deleted_at timestamptz);
CREATE TEMP TABLE users(
  id uuid, name text, email text, email_verified_at timestamptz, password text,
  role text, workspace_id uuid, settings jsonb, has_generated_password boolean,
  scim_external_id text, deactivated_at timestamptz, deleted_at timestamptz,
  created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now()
);
INSERT INTO workspaces VALUES ('00000000-0000-0000-0000-000000000002', NULL);
INSERT INTO users(id, name, email, workspace_id, scim_external_id, deactivated_at)
VALUES
  ('00000000-0000-0000-0000-000000000011', 'Inactive', 'inactive@example.com', '00000000-0000-0000-0000-000000000002', 'scim-inactive', now()),
  ('00000000-0000-0000-0000-000000000012', 'Active', 'active@example.com', '00000000-0000-0000-0000-000000000002', 'scim-active', NULL);
`); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	active := true
	if _, err := repository.UpdateSCIMUser(ctx, "scim-inactive", "00000000-0000-0000-0000-000000000002", SCIMUserInput{
		UserName: "inactive@example.com", Active: &active,
	}, 2); err != nil {
		t.Fatalf("reactivation with an available seat failed: %v", err)
	}

	if _, err := pool.Exec(ctx, `
UPDATE users SET deactivated_at = now() WHERE scim_external_id = 'scim-inactive';
INSERT INTO users(id, name, email, workspace_id, scim_external_id, deactivated_at)
VALUES ('00000000-0000-0000-0000-000000000013', 'Another', 'another@example.com', '00000000-0000-0000-0000-000000000002', 'scim-another', NULL);
`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateSCIMUser(ctx, "scim-inactive", "00000000-0000-0000-0000-000000000002", SCIMUserInput{
		UserName: "inactive@example.com", Active: &active,
	}, 2); !errors.Is(err, ErrLicenseSeatsExceeded) {
		t.Fatalf("reactivation at the seat limit returned %v, want ErrLicenseSeatsExceeded", err)
	}
}
