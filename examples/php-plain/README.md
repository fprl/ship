# Plain PHP

Small PHP app that exposes HTTP directly from the container.

```bash
git init
git add .
git commit -m "initial simple-vps app"
simple-vps check --env production
simple-vps setup --env production
printf '%s' 'change-me' | simple-vps secret set APP_SECRET --env production
simple-vps deploy --env production
```

This uses PHP's built-in server to keep the example tiny. For Laravel,
Symfony, or anything real, keep the same `simple-vps.toml` shape but use a
production HTTP-serving image such as FrankenPHP, RoadRunner, or Apache.
simple-vps only needs the container to listen on the configured internal port.

## Redis and Postgres

simple-vps v1 does not provision Redis or Postgres.

Use a managed service or a database you operate separately, then pass
connection strings as secrets:

```toml
[vars]
DATABASE_URL = "@secret:DATABASE_URL"
REDIS_URL = "@secret:REDIS_URL"
```

```bash
printf '%s' "$DATABASE_URL" | simple-vps secret set DATABASE_URL --env production
printf '%s' "$REDIS_URL" | simple-vps secret set REDIS_URL --env production
```

For single-node apps, SQLite and uploads belong under `/data`; simple-vps
mounts `/data` into container apps and includes it in backup/restore.
