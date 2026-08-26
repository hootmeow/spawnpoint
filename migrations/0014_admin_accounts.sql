-- Named admin accounts, server-side sessions, and an audit log.
--
-- Before this migration the single ADMIN_TOKEN was the whole auth story:
-- anyone holding it was "the admin", sessions were stateless signed cookies,
-- and revoking one person meant rotating the token for everyone. This adds:
--
--   admin_users     named accounts with a bcrypt password hash and a role
--                   ('admin' = everything; 'moderator' = player moderation,
--                   identity tools, and dashboards, but no server registry,
--                   ingest keys, or user management).
--   admin_sessions  server-side sessions (cookie carries a random token,
--                   only its SHA-256 is stored) so individual sessions can
--                   be revoked and disabling a user takes effect immediately.
--                   user_id NULL marks a break-glass ADMIN_TOKEN session.
--   admin_audit_log append-only record of who did what in the admin area.
--
-- ADMIN_TOKEN keeps working as a break-glass credential and as the bootstrap
-- path for creating the first account (sign in with it, then Admin > Users).

create extension if not exists pgcrypto;

create table if not exists admin_users (
  id uuid primary key default gen_random_uuid(),
  username text not null unique,
  password_hash text not null,
  role text not null default 'moderator' check (role in ('admin', 'moderator')),
  disabled boolean not null default false,
  created_at timestamptz not null default now(),
  last_login_at timestamptz
);

create table if not exists admin_sessions (
  id uuid primary key default gen_random_uuid(),
  token_sha256 text not null unique,
  -- NULL = session opened with the break-glass ADMIN_TOKEN, not an account.
  user_id uuid references admin_users(id) on delete cascade,
  role text not null check (role in ('admin', 'moderator')),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  last_seen_at timestamptz not null default now(),
  remote_ip text,
  user_agent text
);

create index if not exists idx_admin_sessions_user on admin_sessions (user_id);
create index if not exists idx_admin_sessions_expires on admin_sessions (expires_at);

create table if not exists admin_audit_log (
  id bigserial primary key,
  at timestamptz not null default now(),
  actor text not null,
  remote_ip text,
  action text not null,
  target text,
  detail text
);

create index if not exists idx_admin_audit_at on admin_audit_log (at desc);
