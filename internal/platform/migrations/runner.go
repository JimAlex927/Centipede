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

// UpgradeExisting brings an already populated Docmost database up to the
// schema required by the Go service. It deliberately skips the fresh-database
// baseline: executing that file against an existing installation would fail
// on duplicate tables and is not an upgrade operation.
//
// The compatibility files are additive and idempotent. Each file is recorded
// only after its transaction commits, so an interrupted upgrade can be safely
// retried. The baseline is recorded only after the final read-only validation
// succeeds.
func UpgradeExisting(ctx context.Context, pool *pgxpool.Pool, directory, baselineVersion string) error {
	if strings.TrimSpace(baselineVersion) == "" {
		return fmt.Errorf("migration baseline version is required")
	}
	for _, filename := range []string{"000002_docmost_compatibility.sql", "000003_docmost_late_tables.sql"} {
		if _, err := os.Stat(filepath.Join(directory, filename)); err != nil {
			return fmt.Errorf("read compatibility migration %s: %w", filename, err)
		}
	}
	if err := ensureExistingDocmostCore(ctx, pool); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, filename := range []string{"000002_docmost_compatibility.sql", "000003_docmost_late_tables.sql"} {
		var applied bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, filename).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, filename))
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, filename); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", filename, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", filename, err)
		}
	}

	if err := ValidateExisting(ctx, pool); err != nil {
		return fmt.Errorf("validate upgraded Docmost schema: %w", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, baselineVersion); err != nil {
		return fmt.Errorf("record adopted baseline: %w", err)
	}
	return nil
}

func ensureExistingDocmostCore(ctx context.Context, pool *pgxpool.Pool) error {
	for _, table := range []string{"workspaces", "users", "groups", "spaces", "pages", "attachments"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`, table).Scan(&exists); err != nil {
			return fmt.Errorf("check Docmost core table %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("existing database is not a Docmost installation: missing core table %s", table)
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
	if err := ValidateExisting(ctx, pool); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, version); err != nil {
		return fmt.Errorf("record adopted baseline: %w", err)
	}
	return nil
}

// ValidateExisting verifies that an already migrated Docmost database has the
// structures required by the Go service. It is deliberately read-only and can
// be used as a preflight check before adopting the schema bookkeeping.
func ValidateExisting(ctx context.Context, pool *pgxpool.Pool) error {
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
	missing, err := missingDocmostObjects(ctx, pool)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("existing database is missing Docmost schema objects: %s", strings.Join(missing, ", "))
	}
	return nil
}

func missingDocmostObjects(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	missing := make([]string, 0)
	for _, extension := range []string{"pg_trgm", "unaccent"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_extension WHERE extname = $1
		)`, extension).Scan(&exists); err != nil || !exists {
			if err != nil {
				return nil, fmt.Errorf("check extension %s: %w", extension, err)
			}
			missing = append(missing, "extension "+extension)
		}
	}

	for _, function := range []struct {
		name string
		args string
	}{
		{name: "f_unaccent", args: "text"},
		{name: "gen_uuid_v7", args: ""},
		{name: "pages_tsvector_trigger", args: ""},
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public'
			  AND p.proname = $1
			  AND pg_get_function_identity_arguments(p.oid) = $2
		)`, function.name, function.args).Scan(&exists); err != nil || !exists {
			if err != nil {
				return nil, fmt.Errorf("check function public.%s: %w", function.name, err)
			}
			label := "function public." + function.name + "("
			if function.args != "" {
				label += function.args
			}
			missing = append(missing, label+")")
		}
	}

	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname = 'pages'
		  AND t.tgname = 'pages_tsvector_update'
		  AND NOT t.tgisinternal
	)`).Scan(&exists); err != nil || !exists {
		if err != nil {
			return nil, fmt.Errorf("check page search trigger: %w", err)
		}
		missing = append(missing, "trigger public.pages.pages_tsvector_update")
	}
	return missing, nil
}

var docmostAdoptionSchema = map[string][]string{
	"ai_chat_messages":             {"chat_id", "workspace_id", "role", "content", "metadata"},
	"ai_chats":                     {"workspace_id", "creator_id", "title"},
	"api_keys":                     {"name", "creator_id", "workspace_id", "expires_at"},
	"attachments":                  {"file_name", "file_path", "type", "page_id", "space_id", "workspace_id"},
	"audit":                        {"workspace_id", "event", "resource_type", "created_at"},
	"auth_accounts":                {"user_id", "provider_user_id", "auth_provider_id", "workspace_id"},
	"auth_providers":               {"name", "type", "workspace_id", "is_enabled", "settings"},
	"backlinks":                    {"source_page_id", "target_page_id", "workspace_id"},
	"base_properties":              {"page_id", "name", "type", "type_options", "workspace_id"},
	"base_rows":                    {"page_id", "cells", "workspace_id"},
	"base_views":                   {"page_id", "name", "type", "config", "workspace_id"},
	"billing":                      {"workspace_id", "status", "currency"},
	"comments":                     {"content", "page_id", "workspace_id", "updated_at"},
	"favorites":                    {"user_id", "type", "workspace_id"},
	"file_tasks":                   {"file_name", "file_path", "workspace_id", "status"},
	"groups":                       {"name", "workspace_id"},
	"group_users":                  {"group_id", "user_id"},
	"labels":                       {"name", "workspace_id"},
	"notifications":                {"user_id", "workspace_id", "data"},
	"oauth_authorization_codes":    {"code_hash", "client_id", "user_id", "workspace_id", "expires_at"},
	"oauth_clients":                {"name", "redirect_uris", "grant_types", "scopes", "workspace_id"},
	"oauth_grants":                 {"user_id", "client_id", "workspace_id", "scopes"},
	"oauth_tokens":                 {"grant_id", "workspace_id", "access_token_jti", "scopes", "access_expires_at"},
	"page_access":                  {"page_id", "space_id", "workspace_id", "access_level"},
	"page_history":                 {"page_id", "content", "workspace_id"},
	"page_labels":                  {"page_id", "label_id"},
	"page_permissions":             {"page_access_id", "role"},
	"page_transclusion_references": {"reference_page_id", "source_page_id", "transclusion_id"},
	"page_transclusions":           {"page_id", "content", "transclusion_id"},
	"page_verifications":           {"page_id", "workspace_id", "status", "mode"},
	"page_verifiers":               {"page_verification_id", "user_id", "is_primary"},
	"pages":                        {"slug_id", "content", "workspace_id", "space_id", "updated_at"},
	"shares":                       {"key", "page_id", "workspace_id"},
	"space_members":                {"space_id", "role"},
	"spaces":                       {"name", "slug", "workspace_id", "settings"},
	"scim_tokens":                  {"name", "token_hash", "workspace_id", "is_enabled"},
	"templates":                    {"title", "content", "space_id", "workspace_id", "text_content"},
	"user_mfa":                     {"user_id", "method", "is_enabled", "workspace_id"},
	"user_sessions":                {"user_id", "workspace_id", "expires_at"},
	"user_tokens":                  {"token", "type", "user_id"},
	"users":                        {"name", "email", "workspace_id", "password"},
	"watchers":                     {"user_id", "space_id", "workspace_id", "type"},
	"workspace_invitations":        {"email", "token", "workspace_id"},
	"workspaces":                   {"name", "hostname", "settings"},
}
