# BFSRM and Linux Dedicated Server Stats Setup

This project reads Battlefield 1942 XML event logs. It does not need a custom
game binary and it does not need to talk to BFSRM directly. BFSRM, BFSMD-style
service wrappers, and plain `bf1942_lnxded` installs all work if they run the
normal BF1942 dedicated server with DICE XML event logging enabled.

## Compatibility

`spawnpoint` is compatible with:

- BFSRM-style tools that manage the normal BF1942 dedicated server.
- Linux systemd services that start `bf1942_lnxded` or `start.sh`.
- Multi-instance wrappers where each instance has its own BF1942 install tree.

The required output is a completed XML file like:

```text
<game-dir>/mods/<mod>/logs/ev_<port>-YYYYMMDD_HHMM.xml
```

For the stock BF1942 mod on a default Linux install, that path is usually:

```text
/home/bf1942_user/bf1942/mods/bf1942/logs/
```

The DICE server README calls this the mod `Logs` directory. On Linux, the
verified dedicated server writes to lowercase `logs`, so create and watch that
directory.

## What the Manager Must Create

Current manager/setup scripts often configure the server and systemd unit but
miss the XML stats log directory and settings. Add these steps when creating a
server or instance:

```bash
GAME_DIR=/home/bf1942_user/bf1942
MOD=bf1942
RUN_AS=bf1942_user

install -d -o "$RUN_AS" -g "$RUN_AS" -m 0755 "$GAME_DIR/mods/$MOD/logs"
```

For multi-instance setups, change `GAME_DIR` to the instance root, for example:

```bash
GAME_DIR=/home/bf1942_user/instances/my-server
MOD=bf1942
RUN_AS=bf1942_user

install -d -o "$RUN_AS" -g "$RUN_AS" -m 0755 "$GAME_DIR/mods/$MOD/logs"
```

## Required ServerSettings.con Values

Enable XML event logging in:

```text
<game-dir>/mods/bf1942/settings/serversettings.con
```

Required:

```text
game.serverEventLogging 1
```

Recommended for live polling:

```text
game.serverEventLogCompression 0
```

Compression is supported by the worker for completed `.zxml` files, but
compressed logs flush in deferred blocks and are not a good choice for near
real-time development monitoring.

If the keys already exist, update them. If they are missing, append them:

```bash
SETTINGS="$GAME_DIR/mods/$MOD/settings/serversettings.con"

grep -qi '^game\.serverEventLogging' "$SETTINGS" \
  && sed -i 's/^game\.serverEventLogging.*/game.serverEventLogging 1/i' "$SETTINGS" \
  || printf '\ngame.serverEventLogging 1\n' >> "$SETTINGS"

grep -qi '^game\.serverEventLogCompression' "$SETTINGS" \
  && sed -i 's/^game\.serverEventLogCompression.*/game.serverEventLogCompression 0/i' "$SETTINGS" \
  || printf 'game.serverEventLogCompression 0\n' >> "$SETTINGS"
```

Restart the game server after changing these values.

## Worker Configuration

Point the Spawnpoint worker at the BF1942 XML log directory:

```bash
DATABASE_URL='postgres://spawnpoint:spawnpoint@localhost:5432/spawnpoint?sslmode=disable' \
ARCHIVE_DIR=/root/spawnpoint/data/raw-logs \
/root/spawnpoint/bin/spawnpoint-worker \
  -watch \
  -interval 15s \
  /home/bf1942_user/bf1942/mods/bf1942/logs
```

For a BFSRM/BFSMD instance, replace the final path with that instance's log
directory:

```bash
/home/bf1942_user/instances/my-server/mods/bf1942/logs
```

The included systemd template uses the default standalone path:

```text
deployments/systemd/spawnpoint-worker.service
```

For multiple servers, create one worker service per log directory or run one
worker command with multiple log directories:

```bash
/root/spawnpoint/bin/spawnpoint-worker -watch \
  /home/bf1942_user/instances/server-a/mods/bf1942/logs \
  /home/bf1942_user/instances/server-b/mods/bf1942/logs
```

## Shipping to Someone Else's Stats Site

Everything above assumes the worker runs next to its own PostgreSQL database.
If this box instead needs to send its logs to a *central* spawnpoint site
run by someone else, don't set `DATABASE_URL` at all — the worker also has an
HTTPS upload mode for exactly this case (`-upload-url` / `-upload-key`, see
the main [README](../README.md#multiple-game-servers)). The site owner
creates a server entry and an ingest API key for you; you never get
database, SSH, or admin access to their box, and they can revoke the key at
any time without touching yours.

The site owner should be able to hand you
[`deployments/scripts/remote-worker-setup.sh`](../deployments/scripts/remote-worker-setup.sh)
to automate this end: it builds the worker, creates an unprivileged system
user, verifies the URL/key work before installing anything, and sets up one
hardened systemd service per game server instance on this box.

## Verification

After restart, the server should create files similar to:

```text
ev_14567-20260702_1938.xml
```

Check for completed rounds:

```bash
grep -R '<bf:roundstats' /home/bf1942_user/bf1942/mods/bf1942/logs
```

Check that the worker can parse the files:

```bash
/root/spawnpoint/bin/spawnpoint-worker -parse-only \
  /home/bf1942_user/bf1942/mods/bf1942/logs
```

Check stored data:

```bash
psql 'postgres://spawnpoint:spawnpoint@localhost:5432/spawnpoint?sslmode=disable' \
  -c 'select count(*) from raw_log_files; select count(*) from events; select count(*) from round_player_stats;'
```

## Notes for BFSRM Tool Authors

When adding Spawnpoint support to a BFSRM-style manager, the manager should:

1. Create `<game-dir>/mods/<mod>/logs` with ownership matching the BF1942
   process user.
2. Set `game.serverEventLogging 1`.
3. Prefer `game.serverEventLogCompression 0` for live polling.
4. Restart the BF1942 process after settings changes.
5. Configure `spawnpoint-worker` to watch that log directory.

No RCON command is required for stats capture. RCON can be used for admin
features, but the stats pipeline is file-based and starts when the dedicated
server writes XML logs.
