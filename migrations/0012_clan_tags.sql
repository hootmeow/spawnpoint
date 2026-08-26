-- Clan/tag grouping. clan_tag is extracted from the handful of nickname
-- conventions BF1942/BFV clans actually use ([TAG]Name, Name-TAG-, =TAG=Name,
-- {TAG}Name, at either end of the name) and normalized to uppercase so
-- differently-cased tags group together. Ingestion (ensurePlayerIdentity /
-- createPlayerRow in internal/storage/postgres/store.go) keeps this in sync
-- going forward using the same rules, implemented once in Go as
-- domain.ExtractClanTag; this backfill mirrors that logic in SQL for
-- players that already existed before this migration ran.
alter table players add column if not exists clan_tag text;
create index if not exists idx_players_clan_tag on players (clan_tag) where clan_tag is not null;

update players p
set clan_tag = upper(x.tag)
from (
	select id,
		coalesce(
			(regexp_match(display_name, '^\[([A-Za-z0-9_]{2,6})\]'))[1],
			(regexp_match(display_name, '\[([A-Za-z0-9_]{2,6})\]$'))[1],
			(regexp_match(display_name, '^-([A-Za-z0-9_]{2,6})-'))[1],
			(regexp_match(display_name, '-([A-Za-z0-9_]{2,6})-$'))[1],
			(regexp_match(display_name, '^=([A-Za-z0-9_]{2,6})='))[1],
			(regexp_match(display_name, '=([A-Za-z0-9_]{2,6})=$'))[1],
			(regexp_match(display_name, '^\{([A-Za-z0-9_]{2,6})\}'))[1],
			(regexp_match(display_name, '\{([A-Za-z0-9_]{2,6})\}$'))[1]
		) as tag
	from players
) x
where p.id = x.id and x.tag is not null and p.clan_tag is null;
