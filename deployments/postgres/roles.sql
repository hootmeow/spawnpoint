-- Run as a PostgreSQL administrator after applying migrations.
-- Replace passwords before use.

create role spawnpoint_api login password 'replace-api-password';
create role spawnpoint_worker login password 'replace-worker-password';
create role spawnpoint_admin login password 'replace-admin-password';

grant connect on database spawnpoint to spawnpoint_api, spawnpoint_worker, spawnpoint_admin;

grant usage on schema public to spawnpoint_api, spawnpoint_worker, spawnpoint_admin;

grant select on all tables in schema public to spawnpoint_api;
grant select on all sequences in schema public to spawnpoint_api;

grant select, insert, update on
  organizations,
  servers,
  raw_log_files,
  games,
  rounds,
  players,
  player_identities,
  player_aliases,
  events,
  round_player_stats,
  ingest_jobs
to spawnpoint_worker;

grant delete on
  raw_log_files,
  games,
  rounds,
  events,
  round_player_stats
to spawnpoint_worker;

grant select, insert, update, delete on all tables in schema public to spawnpoint_admin;
grant all privileges on all sequences in schema public to spawnpoint_admin;

alter default privileges in schema public grant select on tables to spawnpoint_api;
alter default privileges in schema public grant select, insert, update on tables to spawnpoint_worker;
alter default privileges in schema public grant select, insert, update, delete on tables to spawnpoint_admin;
