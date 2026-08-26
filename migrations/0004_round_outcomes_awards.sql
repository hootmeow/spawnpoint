-- Round outcomes, time played, and per-round medals.

alter table rounds add column if not exists victory_type integer;
alter table round_player_stats add column if not exists play_seconds integer;

create table if not exists round_awards (
  id uuid primary key,
  round_id uuid not null references rounds(id) on delete cascade,
  category text not null,
  player_id uuid not null references players(id),
  value integer not null,
  created_at timestamptz not null default now(),
  unique (round_id, category)
);

create index if not exists idx_round_awards_player on round_awards (player_id, category);

-- Backfill medals for rounds ingested before this table existed. Winner per
-- category: highest value, ties broken by round score then slot for
-- determinism. Ingest keeps this up to date for new rounds.
do $$
declare
  cat text;
  col text;
begin
  for cat, col in select * from (values
    ('kills', 'kills'), ('score', 'score'), ('captures', 'captures'),
    ('heals', 'heals'), ('repairs', 'repairs'), ('deaths', 'deaths')
  ) as c(cat, col)
  loop
    execute format($f$
      insert into round_awards (id, round_id, category, player_id, value)
      select gen_random_uuid(), round_id, %L, player_id, %I from (
        select distinct on (round_id) round_id, player_id, %I
        from round_player_stats
        where %I > 0
        order by round_id, %I desc, score desc, slot_id asc nulls last
      ) winners
      on conflict (round_id, category) do nothing
    $f$, cat, col, col, col, col);
  end loop;
end
$$;
