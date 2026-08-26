# Reverse Proxy: Nginx + Cloudflare Origin Certificate

This is the recommended way to put this site on the public internet: Cloudflare
in front (DDoS/bot protection, caching, and the certificate your visitors'
browsers see), Nginx on the origin box terminating that connection and
proxying to the Go API process, which is bound to loopback only and otherwise
unreachable.

```
Browser --HTTPS(public CA cert)--> Cloudflare edge --HTTPS(Origin cert)--> Nginx --HTTP--> spawnpoint-api (127.0.0.1:8080)
```

Every hop between the browser and your server is encrypted. The last hop
(Nginx to the Go process) is plain HTTP over loopback on the same machine,
which is fine — it never leaves the box.

This doc assumes you already have the app installed (`setup.sh` or Docker;
see the [README](../README.md)) and a domain added to a Cloudflare account.

## 1. DNS in Cloudflare

Add an A (or AAAA) record for your domain pointing at the server's public IP,
with the proxy status **"Proxied"** (orange cloud), not "DNS only" (grey
cloud). This is what puts Cloudflare in the request path at all — DNS-only
records skip it entirely, and none of the protections below apply.

## 2. Choose Full (Strict) — not Flexible

Cloudflare → your domain → **SSL/TLS → Overview**, set the encryption mode to
**Full (strict)**.

- **Flexible** terminates TLS at Cloudflare and talks to your origin over
  **plain HTTP**. Don't use this: this site has an admin login that posts a
  token, and Flexible mode would send that token in cleartext from
  Cloudflare's edge to your server, defeating the entire point of HTTPS on a
  site with a credential.
- **Full** encrypts edge-to-origin but accepts any certificate, including an
  expired or self-signed one — a network position between Cloudflare and
  your origin could still present a fake cert and go unnoticed.
- **Full (strict)** encrypts edge-to-origin *and* validates the origin's
  certificate. This is the correct setting, and it's what the Cloudflare
  Origin Certificate in the next step is designed for.

## 3. Generate a Cloudflare Origin Certificate

Cloudflare → your domain → **SSL/TLS → Origin Server → Create Certificate**.
Defaults are fine (RSA 2048, list your domain and `*.yourdomain.com`, 15-year
validity). This gives you a certificate + private key pair that:

- Is trusted by Cloudflare (satisfies Full (strict)) — but is **not** a
  publicly trusted certificate, so it will show a warning to anyone who hits
  your origin IP directly, bypassing Cloudflare. That's expected and part of
  why step 6 below matters.
- Doesn't need Certbot/renewal automation for 15 years.

Save both blocks it gives you on the server:

```bash
sudo install -d -m 0750 /etc/nginx/ssl
sudo tee /etc/nginx/ssl/cloudflare-origin.pem >/dev/null   # paste the certificate
sudo tee /etc/nginx/ssl/cloudflare-origin.key >/dev/null   # paste the private key
sudo chmod 600 /etc/nginx/ssl/cloudflare-origin.key
```

(If you'd rather have a publicly trusted cert on the origin too — useful if
you ever pause Cloudflare temporarily — Let's Encrypt via Certbot works
alongside this; just point `ssl_certificate`/`ssl_certificate_key` at its
files instead. The Origin Certificate above is simpler and sufficient as long
as Cloudflare stays in front.)

## 4. Install Nginx and configure the site

```bash
sudo apt install nginx   # or your distro's equivalent
```

`/etc/nginx/sites-available/spawnpoint`:

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name stats.example.com;
    return 301 https://$host$request_uri;
}

server {
    # Combined listen+http2 syntax for compatibility with the nginx that
    # ships in current LTS distros (e.g. 1.24 on Ubuntu 24.04); nginx 1.25.1+
    # also accepts this, just with a harmless deprecation warning in favor of
    # a separate `http2 on;` line if you're on a newer build.
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name stats.example.com;

    ssl_certificate     /etc/nginx/ssl/cloudflare-origin.pem;
    ssl_certificate_key /etc/nginx/ssl/cloudflare-origin.key;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # Real visitor IP + the Cloudflare-only allowlist both come from
    # /etc/nginx/conf.d/cloudflare.conf, created in step 5. No `include` line
    # needed here: Debian/Ubuntu's default nginx.conf already includes
    # everything in conf.d/*.conf at the http level (check yours has
    # `include /etc/nginx/conf.d/*.conf;` inside its `http {}` block if
    # you're on a different distro) — and it has to be included there, not
    # re-included inside this server block, since one of the directives it
    # defines (`geo`) is only valid at the http level.

    # Matches MAX_LOG_FILE_BYTES (default 100MB) — worker -upload-url ships
    # raw logs here via /api/ingest/upload. Nginx's own default (1MB) would
    # reject those uploads before the app ever sees them.
    client_max_body_size 100m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        # $proxy_add_x_forwarded_for appends the real client IP (once realip
        # has rewritten $remote_addr below) rather than replacing the whole
        # header, so the app's remoteIP() — which trusts the LAST hop, not
        # the first — sees the correct value even if a client tries to
        # prepend a fake one of their own.
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        # Required: the app's admin session cookie only gets the Secure
        # flag, and it only sends HSTS, when it can tell the original
        # request was HTTPS — which it can only do via this header, since
        # the connection it actually sees is plain HTTP from Nginx.
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 5s;
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;
    }
}
```

```bash
sudo ln -s /etc/nginx/sites-available/spawnpoint /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
```

At this point the site works over HTTPS but accepts direct connections from
anyone, not just Cloudflare — step 5 closes that.

## 5. Restrict the origin to Cloudflare only

Anyone who discovers your server's real IP (e.g. from old DNS history, a
misconfigured subdomain, or just internet-wide scanning) can otherwise hit it
directly, skipping Cloudflare's proxying, caching, and bot/DDoS protection
entirely — and, until you fix this, seeing a certificate warning (since the
Origin Certificate isn't publicly trusted) rather than being blocked outright.

Generate the Cloudflare-IPs include, which does two things: tells Nginx to
trust `CF-Connecting-IP` for the real visitor address (only from these
source IPs — anyone else's copy of that header is just an ordinary,
spoofable request header), and builds the allowlist the next block uses to
reject direct connections from anywhere else:

```bash
{
  echo "# Regenerate from https://www.cloudflare.com/ips/ periodically."
  for ip in $(curl -fsS https://www.cloudflare.com/ips-v4); do
    echo "set_real_ip_from $ip;"
  done
  for ip in $(curl -fsS https://www.cloudflare.com/ips-v6); do
    echo "set_real_ip_from $ip;"
  done
  echo "real_ip_header CF-Connecting-IP;"
} | sudo tee /etc/nginx/conf.d/cloudflare.conf >/dev/null
sudo nginx -t && sudo systemctl reload nginx
```

`set_real_ip_from` alone doesn't *block* non-Cloudflare traffic, it only
trusts their IP for the real-IP rewrite — for the actual blocking, add an
allowlist to the same file:

```bash
{
  echo
  echo "geo \$cloudflare_ip {"
  echo "    default 0;"
  for ip in $(curl -fsS https://www.cloudflare.com/ips-v4) $(curl -fsS https://www.cloudflare.com/ips-v6); do
    echo "    $ip 1;"
  done
  echo "}"
} | sudo tee -a /etc/nginx/conf.d/cloudflare.conf >/dev/null
```

...and inside the `server { listen 443 ssl; ... }` block from step 4, add:

```nginx
    if ($cloudflare_ip = 0) {
        return 403;
    }
```

Then:

```bash
sudo nginx -t && sudo systemctl reload nginx
```

(Alternative to all of this: Cloudflare's **Authenticated Origin Pulls**
feature — SSL/TLS → Origin Server → toggle it on — has Cloudflare present a
client certificate on every edge-to-origin request, and you configure Nginx
to require and verify it. That's mutual-TLS instead of an IP allowlist; more
robust against Cloudflare's IP ranges changing, but more moving parts to set
up. The IP allowlist above is simpler and enough for most self-hosted setups
— just remember to re-run the fetch periodically, or put it in a monthly
cron job, since Cloudflare's ranges do occasionally change.)

## 6. Configure the app and firewall

```bash
# in .env
HOST=127.0.0.1
PORT=8080
PUBLIC_BASE_URL=https://stats.example.com
```

`HOST=127.0.0.1` means the Go process itself only accepts loopback
connections — it's unreachable from the network even if someone bypasses
Nginx's own firewall rule in step 5. Restart the service after changing it:

```bash
sudo systemctl restart spawnpoint-api
```

Then close the app's port at the OS firewall too (defense in depth — belt
and suspenders with `HOST=127.0.0.1`):

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw deny 8080/tcp
```

## 7. Cloudflare edge settings

- **SSL/TLS → Edge Certificates**: turn on **Always Use HTTPS** (redirects
  any stray `http://` request at the edge) and set **Minimum TLS Version** to
  **1.2**.
- **Security → Bots**: enable **Bot Fight Mode** (free tier) — cheap,
  meaningful protection against the generic scraping/credential-stuffing bots
  that hit any public site.
- **Caching → Cache Rules**: this site's pages are already marked
  `Cache-Control: private` (see `apps/api/main.go`), which tells Cloudflare
  not to cache them by default — you don't need to configure anything here.
  If you ever add a **"Cache Everything"** page rule for performance, exclude
  `/admin/*` and `/api/*` explicitly first; caching either at a shared edge
  would be a real problem (stale admin state, or a cached ingest response
  replayed to the wrong caller).

## 8. Verify

```bash
curl -sI https://stats.example.com/ | grep -i "strict-transport-security\|content-security-policy"
curl -s https://stats.example.com/healthz
```

- `Strict-Transport-Security` should be present now (it wasn't reachable
  before, since the app only sends it when it can tell the request was
  HTTPS — see the `X-Forwarded-Proto` note in step 4).
- Confirm real visitor IPs show up correctly: `journalctl -u
  spawnpoint-api -f` while browsing the site from your own machine should
  log *your* IP, not Cloudflare's or `127.0.0.1`.
- Try hitting the server's bare IP directly
  (`curl -k https://<origin-ip>/ -H "Host: stats.example.com"`). Port 443 has
  to stay open to the public internet for Cloudflare to reach it at all, so
  the TLS handshake will still succeed — but the request itself should come
  back `403` from step 5's IP check, confirming a direct visitor (or a bot
  scanning IP ranges) can't skip Cloudflare and hit the app straight through.
- [SSL Labs' test](https://www.ssllabs.com/ssltest/) against your domain
  (tests the Cloudflare edge, which is what visitors actually see) should
  come back A or A+ once Full (strict) + a modern minimum TLS version are set.

## Troubleshooting

- **522/524 (connection timed out)**: Nginx isn't running, isn't listening on
  443, or the origin's firewall is blocking Cloudflare's IPs instead of the
  public internet's. Check `systemctl status nginx` and the ufw rules above.
- **525/526 (SSL handshake / invalid cert)**: the Origin Certificate isn't
  installed correctly, or SSL/TLS mode is set to Full (strict) but the
  origin is presenting a self-signed/expired cert instead of Cloudflare's.
  Re-check step 3/4.
- **Redirect loop**: usually SSL/TLS mode set to Flexible while the origin
  itself also redirects HTTP→HTTPS (each side thinks the other should have
  handled it). Full (strict), as configured above, avoids this.
- **Admin login redirects work but the cookie never sticks / login loops**:
  `X-Forwarded-Proto` isn't reaching the app, so `requestIsHTTPS()` returns
  false and the session cookie's `Secure` flag combined with an actual HTTPS
  connection from the browser confuses it. Confirm the `proxy_set_header
  X-Forwarded-Proto $scheme;` line from step 4 is present and Nginx was
  reloaded.
