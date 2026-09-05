package migrations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, pool *pgxpool.Pool, directory string) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}
	filenames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			filenames = append(filenames, entry.Name())
		}
	}
	sort.Strings(filenames)

	for _, filename := range filenames {
		var applied bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, filename).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, filename))
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, filename); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// AdoptExisting records a Docmost baseline for a database that was already
// migrated by Docmost. It deliberately never executes the baseline SQL: the
// baseline contains CREATE statements and is only safe for an empty database.
// The caller must opt into this path explicitly.
func AdoptExisting(ctx context.Context, pool *pgxpool.Pool, directory, version string) error {
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("migration version is required")
	}
	if _, err := os.Stat(filepath.Join(directory, version)); err != nil {
		return fmt.Errorf("read adoption baseline %s: %w", version, err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	missing := make([]string, 0)
	tables := make([]string, 0, len(docmostAdoptionSchema))
	for table := range docmostAdoptionSchema {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		columns := docmostAdoptionSchema[table]
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`, table).Scan(&exists); err != nil {
			return fmt.Errorf("check table %s: %w", table, err)
		}
		if !exists {
			missing = append(missing, table)
			continue
		}
		for _, column := range columns {
			if err := pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
			)`, table, column).Scan(&exists); err != nil {
				return fmt.Errorf("check column %s.%s: %w", table, column, err)
			}
			if !exists {
				missing = append(missing, table+"."+column)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("existing database is missing Docmost schema elements: %s", strings.Join(missing, ", "))
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, version); err != nil {
		return fmt.Errorf("record adopted baseline: %w", err)
	}
	return nil
}

var docmostAdoptionSchema = map[string][]string{
	"attachments":                  {"file_path", "workspace_id"},
	"comments":                     {"page_id", "workspace_id"},
	"file_tasks":                   {"workspace_id", "status"},
	"groups":                       {"workspace_id"},
	"group_users":                  {"group_id", "user_id"},
	"labels":                       {"workspace_id"},
	"notifications":                {"user_id", "workspace_id"},
	"page_access":                  {"page_id"},
	"page_history":                 {"page_id", "content"},
	"page_labels":                  {"page_id", "label_id"},
	"page_permissions":             {"page_access_id"},
	"page_transclusion_references": {"source_page_id"},
	"page_transclusions":           {"page_id"},
	"pages":                        {"content", "workspace_id", "space_id"},
	"shares":                       {"page_id", "workspace_id"},
	"space_members":                {"space_id", "user_id"},
	"spaces":                       {"workspace_id"},
	"user_sessions":                {"user_id", "workspace_id"},
	"user_tokens":                  {"user_id", "workspace_id"},
	"users":                        {"workspace_id", "email"},
	"watchers":                     {"user_id", "page_id"},
	"workspaces":                   {"hostname"},
}
