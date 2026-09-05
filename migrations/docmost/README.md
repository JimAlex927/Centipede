# Docmost fresh-database baseline

This schema-only baseline was generated from the Docmost source migrations through
`20260825T022612-oauth`, using PostgreSQL 17.11. It contains no application rows,
credentials, or Kysely migration bookkeeping. Requires PostgreSQL 17+ with the
bundled `unaccent` and `pg_trgm` extensions available.

Use **only on an empty database**. Existing Docmost installations must not execute
the baseline SQL directly. If the existing database has already been migrated to
the same Docmost schema, use the explicit adoption check instead.
The directory is separate from Centipede's own application migrations deliberately.

After selecting the intended database in an isolated configuration:

```powershell
go run ./cmd/migrate -dir migrations/docmost
go run ./cmd/api
```

For an existing installation, first take a database backup and then run:

```powershell
go run ./cmd/migrate -dir migrations/docmost -check-existing
```

The read-only preflight checks the required Docmost tables, columns, extensions,
functions and page-search trigger without changing the database. If it passes,
record the baseline with:

```powershell
go run ./cmd/migrate -dir migrations/docmost -adopt-existing
```

The adoption command validates the core Docmost tables, columns, extensions,
functions and page-search trigger, then only records the baseline in
`schema_migrations`; it does not run `CREATE`, `ALTER`, or data-changing SQL.
An older or incomplete schema is rejected and must be upgraded separately
before starting Go.

The Go migration runner records the baseline and skips it on subsequent runs.
No Node runtime is needed to apply it. The source-side `go-test-migrate.ts` script
is only a development reference generator for comparing the upstream schema.

This does not imply feature parity: collaboration, realtime delivery, import/export,
mail workflows and full page-level permissions still require migration work.
