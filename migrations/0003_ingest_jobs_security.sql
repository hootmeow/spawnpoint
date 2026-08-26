create table if not exists ingest_jobs (
  id uuid primary key,
  server_id uuid references servers(id),
  source_path text not null,
  original_name text not null,
  content_sha256 bytea,
  status text not null check (status in ('started', 'parsed', 'skipped', 'failed')),
  error_message text,
  file_size_bytes bigint,
  rounds integer not null default 0,
  events integer not null default 0,
  round_stats integer not null default 0,
  storage_uri text,
  started_at timestamptz not null default now(),
  finished_at timestamptz
);

create index if not exists idx_ingest_jobs_status_started on ingest_jobs (status, started_at desc);
create index if not exists idx_ingest_jobs_server_started on ingest_jobs (server_id, started_at desc);
create index if not exists idx_ingest_jobs_sha on ingest_jobs (content_sha256);
