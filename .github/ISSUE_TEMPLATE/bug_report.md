---
name: Bug report
about: Something is broken or behaving unexpectedly
labels: bug
---

**What happened?**

**What did you expect instead?**

**How did you install?** (setup.sh / Docker Compose / manual)

**Versions**

- App version (`bin/spawnpoint-api -version` or the `/admin` overview):
- OS / PostgreSQL version:

**Logs**

```
journalctl -u spawnpoint-api -n 50 --no-pager   # or `docker compose logs api`
```
