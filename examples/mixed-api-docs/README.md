# Mixed API And Docs

> **Superseded:** This document describes the pre-ship surface and is not current.
> **Pending:** The Phase 3 rewrite will replace it; only broken commands are patched here.

Container app with a static `/docs` route in the same release.

Before deploying, edit `ship.toml`:

- set `box`
- set both route hosts

```bash
git init
git add .
git commit -m "initial simple-vps app"
ship
curl https://mixed.example.com/health
curl https://mixed.example.com/docs
```

Rollback and restore move the web process and `docs-dist/` snapshot together.
