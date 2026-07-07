# Django SQLite

> **Superseded:** This document describes the pre-ship surface and is not current.
> **Pending:** The Phase 3 rewrite will replace it; only broken commands are patched here.

Minimal Django app with SQLite under `/data` and a release command for
migrations.

Before deploying, edit `ship.toml`:

- set `box`
- set the route host

```bash
git init
git add .
git commit -m "initial ship app"
printf '%s' "$(openssl rand -hex 32)" | ship secret set DJANGO_SECRET_KEY
ship
curl https://django.example.com/health
```

`release` runs `python manage.py migrate --noinput` after the image is
built and before traffic moves to the new container. If migrations fail, the
old routed container stays active.
