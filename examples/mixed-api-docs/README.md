# Mixed API And Docs

Container app with a static `/docs` route in the same release.

Before deploying, edit `simple-vps.toml`:

- set `[env.production].server`
- set both route hosts

```bash
git init
git add .
git commit -m "initial simple-vps app"
simple-vps check --env production
simple-vps setup --env production
simple-vps deploy --env production
curl https://mixed.example.com/health
curl https://mixed.example.com/docs
```

Rollback and restore move the web process and `docs-dist/` snapshot together.
