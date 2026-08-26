create table organizations (
  id uuid primary key,
  name text not null,
  created_at timestamptz not null default now()
);

create table servers (
  id uuid primary key,
  organization_id uuid not null references organizations(id),
  name text not null,
  game text not null check (game in ('bf1942', 'bfvietnam')),
  address text,
  port integer,
  created_at timestamptz not null default now()
);

create table raw_log_files (
  id uuid primary key,
  server_id uuid not null references servers(id),
  original_name text not null,
  content_sha256 bytea not null unique,
  storage_uri text not null,
  compressed boolean not null default false,
  status text not null,
  observed_start_at timestamptz,
  parsed_at timestamptz,
  created_at timestamptz not null default now()
);

create table games (
  id uuid primary key,
  server_id uuid not null references servers(id),
  raw_log_file_id uuid references raw_log_files(id),
  mod_id text,
  map_id text,
  map_name text,
  game_mode text,
  started_at timestamptz not null,
  server_settings jsonb not null default '{}'::jsonb,
  legacy_game_id bigint,
  unique (server_id, raw_log_file_id)
);

create table rounds (
  id uuid primary key,
  game_id uuid not null references games(id),
  round_index integer not null,
  started_at timestamptz,
  ended_at timestamptz,
  end_type text check (end_type in ('regular', 'restart', 'forced', 'unknown')),
  winning_team_id integer,
  start_tickets jsonb,
  end_tickets jsonb,
  legacy_round_id bigint,
  unique (game_id, round_index)
);

create table players (
  id uuid primary key,
  organization_id uuid not null references organizations(id),
  display_name text not null,
  created_at timestamptz not null default now()
);

create table player_identities (
  id uuid primary key,
  player_id uuid not null references players(id),
  identity_type text not null check (identity_type in ('bf_keyhash', 'nickname_lan', 'discord', 'steam', 'manual')),
  identity_value text not null,
  verified boolean not null default false,
  created_at timestamptz not null default now(),
  unique (identity_type, identity_value)
);

create table player_aliases (
  id uuid primary key,
  player_id uuid not null references players(id),
  alias text not null,
  first_seen_at timestamptz,
  last_seen_at timestamptz,
  times_seen bigint not null default 0,
  unique (player_id, alias)
);

create table events (
  id uuid primary key,
  organization_id uuid not null references organizations(id),
  server_id uuid not null references servers(id),
  game_id uuid references games(id),
  round_id uuid references rounds(id),
  raw_log_file_id uuid references raw_log_files(id),
  sequence_no bigint not null,
  event_type text not null,
  raw_event_name text not null,
  occurred_at timestamptz,
  timestamp_seconds double precision not null,
  actor_player_id uuid references players(id),
  target_player_id uuid references players(id),
  actor_slot_id integer,
  target_slot_id integer,
  weapon_code text,
  vehicle_code text,
  kit_code text,
  chat_text text,
  params jsonb not null default '{}'::jsonb,
  unique (raw_log_file_id, sequence_no)
);

create table round_player_stats (
  id uuid primary key,
  round_id uuid not null references rounds(id),
  player_id uuid not null references players(id),
  slot_id integer,
  is_bot boolean,
  team_id integer,
  score integer not null default 0,
  kills integer not null default 0,
  deaths integer not null default 0,
  teamkills integer not null default 0,
  captures integer not null default 0,
  attacks integer not null default 0,
  defences integer not null default 0,
  objectives integer not null default 0,
  objective_teamkills integer not null default 0,
  heals integer not null default 0,
  self_heals integer not null default 0,
  repairs integer not null default 0,
  other_repairs integer not null default 0,
  rank_in_round integer,
  source text not null default 'roundstats',
  unique (round_id, player_id)
);

create index idx_servers_organization on servers (organization_id);
create index idx_raw_log_files_server_created on raw_log_files (server_id, created_at desc);
create index idx_games_server_started on games (server_id, started_at desc);
create index idx_rounds_game_index on rounds (game_id, round_index);
create index idx_player_identities_value on player_identities (identity_type, identity_value);
create index idx_events_round_sequence on events (round_id, sequence_no);
create index idx_events_type_time on events (event_type, occurred_at desc);
create index idx_events_actor_time on events (actor_player_id, occurred_at desc);
create index idx_events_params_gin on events using gin (params);
create index idx_round_player_stats_player on round_player_stats (player_id);
create index idx_round_player_stats_round_slot on round_player_stats (round_id, slot_id);
create index idx_round_player_stats_score on round_player_stats (round_id, score desc);
