# ADR 0001: Centipede uses a modular monolith

## Status

Accepted.

## Decision

Centipede is deployed as one API process while business capabilities are split into feature-owned modules. Each module owns its domain rules, application ports, adapters, and database tables.

Allmacht is a separate identity provider. Centipede maps an external `(issuer, subject)` to a local application user and never reads Allmacht tables.

## Dependency rules

- Domain depends only on the Go standard library.
- Application depends on domain and consumer-owned interfaces.
- Gin and pgx stay in adapters or platform packages.
- Modules do not import another module's adapter packages.
- Cross-module behavior uses explicit contracts or application events.

## Extraction triggers

A module is physically extracted only after it needs independent scaling, release ownership, security isolation, or a distinct operational lifecycle.
