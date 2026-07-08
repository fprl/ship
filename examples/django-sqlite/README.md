# Django SQLite

Minimal Django app with SQLite under `/data` and migrations run as the release
command.

Before deploying, set `box` and the route host in `ship.toml`, then store the
required Django secret:

```bash
printf '%s' "$DJANGO_SECRET_KEY" | ship secret set DJANGO_SECRET_KEY
```

```bash
git add . && git commit -m "initial ship app"
ship
```

`ship.toml`:

- `box` is the deploy SSH target for the VPS.
- `release = "python manage.py migrate --noinput"` runs before traffic moves.
- `[env]` puts SQLite at `/data/db.sqlite3` and references `DJANGO_SECRET_KEY`.
- `[processes].web` serves Django on port `8000`.
- `[routes]` sends `django.example.com` to `web`.
