# Architecture

## First Milestone

The first version should prove that historical parsing works.

```text
Battlefield XML/ZXML logs
  -> parser worker
  -> normalized events
  -> PostgreSQL raw event tables
  -> compatibility projections
  -> API/UI
```

## Why Historical Parsing First

Live dashboards are useful, but they depend on correct parser semantics. The legacy select(bf) parser has domain-specific behavior for player slot reuse, keyhash identity, bots, malformed logs, typo fields like `vechicle`, round restarts, and final `roundstats`.

The first hard requirement is to parse old logs and match legacy totals.

## Initial Components

- API: health checks and future REST endpoints.
- Worker: log parsing entrypoint.
- Parser: streaming Battlefield XML parser.
- Database: PostgreSQL schema for servers, raw files, games, rounds, players, identities, events, and round stats.
- Fixtures: real logs and expected summaries.

## Near-Term Decisions

- Add `.zxml` decompression support.
- Add source file timestamp extraction from `ev_<port>-YYYYMMDD_HHMM.xml`.
- Add parser summary output.
- Add compatibility comparison against legacy select(bf) tables.

