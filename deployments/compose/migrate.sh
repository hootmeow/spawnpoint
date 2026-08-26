#!/bin/sh
# Applies migrations/*.sql to $DATABASE_URL, skipping ones already recorded
# in schema_migrations. Run as the one-shot `migrate` service in
# docker-compose.prod.yml before api/worker start. Safe to re-run.
set -eu

: "${DATABASE_URL:?DATABASE_URL is required}"

until psql "$DATABASE_URL" -Atc "select 1" >/dev/null 2>&1; do
  echo "waiting for postgres..."
  sleep 1
done

psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 <<'SQL'
create table if not exists schema_migrations (
  version text primary key,
  applied_at timestamptz not null default now()
);
SQL

for file in /migrations/*.sql; do
  version="$(basename "$file" .sql)"
  applied=$(psql "$DATABASE_URL" -Atc "select 1 from schema_migrations where version = '$version'")
  if [ "$applied" = "1" ]; then
    echo "skip $version (recorded)"
    continue
  fi
  echo "apply $version"
  psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 -f "$file"
  psql "$DATABASE_URL" -Atc "insert into schema_migrations (version) values ('$version') on conflict do nothing" >/dev/null
done

echo "schema up to date"
