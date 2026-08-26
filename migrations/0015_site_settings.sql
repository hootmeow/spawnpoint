-- Site-wide toggles operators can flip from the admin panel without
-- redeploying. This is a single-row table: settings always has exactly one
-- row, so pages read/write it without needing a key column.
--
-- chat_log_public: shows/hides the public /chat page. Added after the
-- pre-launch privacy review noted there was no way to take the chat log
-- down quickly (e.g. a player posting something abusive or doxxing
-- themselves) short of editing the database directly.

create table if not exists site_settings (
  singleton boolean primary key default true check (singleton),
  chat_log_public boolean not null default true,
  updated_at timestamptz not null default now()
);

insert into site_settings (singleton) values (true) on conflict (singleton) do nothing;
