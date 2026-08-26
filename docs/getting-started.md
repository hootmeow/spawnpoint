# Guided setup

A single, no-branching walkthrough for the most common setup:

> **One Linux box, one BF1942 server, a public stats site.**

If that's you, follow these steps top to bottom — every command is copy-paste,
and each step tells you what success looks like. If your situation is different
(Docker, a remote game server, an existing PostgreSQL), the
[README chooser](../README.md#get-started--pick-your-situation) points you to
the right guide instead.

You'll need about 15 minutes and a server you can `sudo` on.

---

## Step 1 — Install the prerequisites

The installer needs **Go 1.22+**, the **PostgreSQL client**, plus **git** and
**curl**. On Ubuntu/Debian:

```bash
# git, curl, PostgreSQL client
sudo apt update && sudo apt install -y git curl postgresql-client

# Go 1.22+ (Ubuntu's apt Go is often too old, so install the official build).
# This grabs the current stable release automatically.
GO_VER=$(curl -sSL https://go.dev/VERSION?m=text | head -1)
curl -sSL "https://go.dev/dl/${GO_VER}.linux-amd64.tar.gz" | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null
export PATH=$PATH:/usr/local/go/bin
```

On Rocky/Alma/RHEL, swap the first line for
`sudo dnf install -y git curl postgresql`. On ARM servers, replace
`linux-amd64` with `linux-arm64`.

**✓ Check:** both print a version.

```bash
go version     # go version go1.22.x ...
psql --version # psql (PostgreSQL) 1x.x
```

> Don't have PostgreSQL *itself* yet? That's fine — the installer offers to
> install and configure it for you in the next step.

---

## Step 2 — Clone and run the installer

```bash
git clone https://github.com/hootmeow/spawnpoint
cd spawnpoint
sudo ./setup.sh
```

`setup.sh` is interactive and safe to re-run. For the common case, answer the
prompts like this:

| Prompt | Pick |
| --- | --- |
| **Database** | *Standalone* — let it create a local PostgreSQL database (it will offer to install PostgreSQL if you don't have it). |
| **BF1942 log source** | *A BF1942/BFV server on this machine* — it scans for the server's `.../mods/*/logs` directory and lists what it finds; pick yours. |
| **Admin area** | *Yes, generate an admin token* — it prints a strong token and saves it to `.env`. **Copy it somewhere safe** — it's your only admin login. |
| **Site settings** | Accept the defaults (port `8080`, etc.) unless you have a reason to change them. |
| **systemd services** | *Yes* — it installs and starts `spawnpoint-api` and `spawnpoint-worker` so they run in the background and survive reboots. |

**✓ Check:** the installer ends with a summary and the services are running.

```bash
systemctl is-active spawnpoint-api spawnpoint-worker
# active
# active
```

---

## Step 3 — Open the site

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz   # 200
```

Then open **`http://<your-server-ip>:8080`** in a browser. You'll see the
dashboard. It's mostly empty until the first round finishes — that's expected.

**✓ Check (admin):** go to `http://<your-server-ip>:8080/admin/login`, paste the
admin token from Step 2 into the **Admin token** form, and you're in. The
**Status** page shows ingest health. Then create your personal account under
**Admin → Users** (make it an `admin`) — day-to-day sign-ins should use that
account; the token is your break-glass backup.

---

## Step 4 — Confirm stats are flowing

The worker watches your log directory and ingests each round's `.xml` log the
moment the round **finishes** (a log mid-round is skipped until it's complete).
So: play or let a round complete on your BF1942 server, then check:

```bash
# recent worker activity
journalctl -u spawnpoint-worker -n 20 --no-pager
```

**✓ Check:** you see a line like `... status=parsed rounds=1 events=…`, and the
player/leaderboard pages start filling in. The `/admin` → **Status** page also
shows the last successful ingest time.

> Nothing showing up? Make sure your BF1942 server actually writes XML event
> logs — that's a server-side setting, not something this app controls. See
> [BFSRM & Linux Dedicated Server Stats Setup](bfsrm-linux-stats-setup.md).

---

## Step 5 — (Optional) Take it public

Steps 1–4 give you a working site on `http://<ip>:8080`. That's perfect for a
LAN or a quick look, but **don't expose port 8080 to the internet directly** —
it's plain HTTP, so the admin token would travel in the clear.

To go public safely:

1. Put **TLS** in front with a reverse proxy — the
   [Nginx + Cloudflare guide](nginx-cloudflare-reverse-proxy.md) is a
   copy-paste walkthrough.
2. Add `SECURE_COOKIES=1` to `.env` (so the admin cookie is HTTPS-only) and set
   `HOST=127.0.0.1` (so the app is only reachable through the proxy).
3. Set `PUBLIC_BASE_URL=https://your-domain` for link previews, and
   `PRIVACY_CONTACT=` for the takedown contact on `/privacy`.
4. Restart: `sudo systemctl restart spawnpoint-api`.

The full list is the [Going live checklist](../README.md#going-live-public-deployment),
and the [Production Runbook](production.md) covers backups and monitoring.

---

## Updating later

```bash
cd spawnpoint
sudo ./update.sh
```

It moves to the newest release, rebuilds, applies any new database migrations,
and restarts the services. Safe to re-run.

---

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `Go 1.22+ is required` | Your Go is too old — redo the Go install in Step 1 (`apt`'s Go is often older than 1.22). |
| `Cannot connect to <DATABASE_URL>` | PostgreSQL isn't running or the URL is wrong. Re-run `setup.sh` and pick *Standalone* to have it set the database up. |
| Site loads but stays empty | No round has finished yet, or the server isn't writing XML logs. Check Step 4 and the [server-side logging setup](bfsrm-linux-stats-setup.md). |
| Admin login rejects the token | Use the exact token from `.env` (`grep ADMIN_TOKEN .env`). It's entered in the login form, never as a URL parameter. |
| Service won't start | `journalctl -u spawnpoint-api -n 50 --no-pager` shows why. |

Still stuck? The [README](../README.md) has the full reference, and every
`.env` option is documented in its [Configuration](../README.md#configuration-env)
table.
