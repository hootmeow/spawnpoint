-- Store only a SHA-256 hash of each server ingest API key at rest, so a leaked
-- database dump or backup no longer exposes usable upload credentials. Existing
-- worker keys keep working unchanged: the server hashes any presented key and
-- compares it to this column. The raw keys are hashed in place and then cleared.

create extension if not exists pgcrypto;

alter table servers add column if not exists api_key_sha256 text;

update servers
   set api_key_sha256 = encode(digest(api_key, 'sha256'), 'hex')
 where api_key is not null
   and api_key_sha256 is null;

-- Drop the plaintext keys now that their hashes are stored.
update servers set api_key = null where api_key is not null;

create unique index if not exists servers_api_key_sha256_key
    on servers (api_key_sha256);
