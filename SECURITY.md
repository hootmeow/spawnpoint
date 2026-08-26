# Security Policy

## Reporting a vulnerability

Please report security issues privately via
[GitHub Security Advisories](https://github.com/hootmeow/spawnpoint/security/advisories/new)
rather than opening a public issue. Include what you found, how to reproduce
it, and what an attacker could do with it. You'll get a response as soon as
possible, and a fix will be released before details are published.

## Scope notes for self-hosters

- `ADMIN_TOKEN` is the single admin credential. Keep it long and random, put
  TLS in front of the app, and set `SECURE_COOKIES=1` — see the
  [going-live checklist](README.md#going-live-public-deployment).
- Server ingest API keys are stored only as SHA-256 hashes; a leaked database
  or backup does not expose usable upload credentials.
- The app never trusts `X-Forwarded-For` / `CF-Connecting-IP` from direct
  internet connections; see the comments in `apps/api/main.go` (`remoteIP`)
  before changing proxy topology.
