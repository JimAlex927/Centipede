# Centipede engineering rules

- Use PowerShell 7 or newer on Windows.
- Keep Centipede as a modular monolith: one deployable API, feature-first modules.
- A module owns its domain, application use cases, adapters, and database tables.
- Domain packages depend only on the Go standard library.
- Application packages depend on domain types and consumer-owned ports.
- Gin, pgx, HTTP, and PostgreSQL belong in adapters or platform packages.
- Modules communicate through explicit public contracts, never by importing another module's adapters.
- Allmacht owns authentication. Centipede owns application users and business authorization.
- Never query or reference Allmacht database tables from Centipede.
- `cmd` is only a composition root and contains no business rules.
