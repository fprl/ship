# Plain PHP

> **Superseded:** This document describes the pre-ship surface and is not current.
> **Pending:** The Phase 3 rewrite will replace it; only broken commands are patched here.

Small PHP app that exposes HTTP directly from the container.

```bash
git init
git add .
git commit -m "initial ship app"
printf '%s' 'change-me' | ship secret set APP_SECRET
ship
```

This uses PHP's built-in server to keep the example tiny. For Laravel,
Symfony, or anything real, keep the same `ship.toml` shape but use a
production HTTP-serving image such as FrankenPHP, RoadRunner, or Apache.
ship only needs the container to listen on the configured internal port.

## Redis and Postgres

ship v1 does not provision Redis or Postgres.

Use a managed service or a database you operate separately, then pass
connection strings as secrets:

```toml
[vars]
DATABASE_URL = "@secret:DATABASE_URL"
REDIS_URL = "@secret:REDIS_URL"
```

```bash
printf '%s' "$DATABASE_URL" | ship secret set DATABASE_URL
printf '%s' "$REDIS_URL" | ship secret set REDIS_URL
```

For single-node apps, SQLite and uploads belong under `/data`; ship
mounts `/data` into container apps and includes it in backup/restore.
