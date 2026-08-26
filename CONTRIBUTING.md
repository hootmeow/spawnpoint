# Contributing

Thanks for helping improve Spawnpoint!

## Getting started

- Go 1.22+ and a PostgreSQL 13+ database for full local runs; the parser and
  most unit tests need no database (`go test ./...`).
- `docs/architecture.md` explains how the pieces fit together;
  `deployments/compose/docker-compose.dev.yml` is a light dev stack.

## Pull requests

- Run `gofmt`, `go vet ./...`, and `go test ./...` before pushing — CI checks
  all three plus the Docker builds.
- Keep the zero-client-side-JavaScript constraint: pages are server-rendered
  HTML with inline SVG, and the CSP allows no scripts at all.
- New database changes go in a new `migrations/NNNN_name.sql` file (additive
  only — see the rollback note in README "Updating"), plus a probe entry in
  `setup.sh`'s `probe_for` so pre-installer databases skip it correctly.
- Parser changes should come with a test in
  `internal/parser/bflog/parser_test.go`; real-world log fixtures are welcome
  (see `fixtures/README.md`, sanitize nicknames/chat first).

## Bugs and ideas

Open a GitHub issue with reproduction steps (for bugs) or the problem you're
trying to solve (for features). For security issues, see [SECURITY.md](SECURITY.md).
