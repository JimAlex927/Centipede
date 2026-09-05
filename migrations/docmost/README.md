# Docmost fresh-database baseline

This schema-only baseline was generated from the Docmost source migrations through
`20260825T022612-oauth`, using PostgreSQL 17.11. It contains no application rows,
credentials, or Kysely migration bookkeeping. Requires PostgreSQL 17+ with the
bundled `unaccent` and `pg_trgm` extensions available.

Use **only on an empty database**. Existing Docmost installations need a separately
verified upgrade/adoption path; do not apply this baseline to an existing schema.
The directory is separate from Centipede's own application migrations deliberately.

After selecting the intended database in an isolated configuration:

```powershell
go run ./cmd/migrate -dir migrations/docmost
go run ./cmd/api
```

The Go migration runner records the baseline and skips it on subsequent runs.
No Node runtime is needed to apply it. The source-side `go-test-migrate.ts` script
is only a development reference generator for comparing the upstream schema.

This does not imply feature parity: collaboration, realtime delivery, import/export,
mail workflows and full page-level permissions still require migration work.
