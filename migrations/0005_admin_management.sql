-- Admin management: server registry fields and player moderation flags.
-- Additive and safe to run on existing installs.

-- Per-server ingest credentials (remote log upload) and live-query metadata.
alter table servers add column if not exists api_key text;
create unique index if not exists idx_servers_api_key on servers (api_key) where api_key is not null;
alter table servers add column if not exists query_port integer;
alter table servers add column if not exists enabled boolean not null default true;

-- Player moderation. hidden removes a player from public lists, search,
-- leaderboards, and awards (round history keeps the record). bot_override
-- forces bot/human classification when the logs get it wrong; null = trust
-- the logs.
alter table players add column if not exists hidden boolean not null default false;
alter table players add column if not exists bot_override boolean;
create index if not exists idx_players_hidden on players (hidden) where hidden;
