# Django SQLite

Minimal Django app with SQLite under `/data` and a release command for
migrations.

Before deploying, edit `simple-vps.toml`:

- set `[env.production].server`
- set `[routes.app].host`

```bash
git init
git add .
git commit -m "initial simple-vps app"
simple-vps check --env production
simple-vps setup --env production
printf '%s' "$(openssl rand -hex 32)" | simple-vps secret set DJANGO_SECRET_KEY --env production
simple-vps deploy --env production
curl https://django.example.com/health
```

`[deploy].release` runs `python manage.py migrate --noinput` after the image is
built and before traffic moves to the new container. If migrations fail, the
old routed container stays active.
