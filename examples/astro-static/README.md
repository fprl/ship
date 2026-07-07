# Astro Static

> **Superseded:** This document describes the pre-ship surface and is not current.
> **Pending:** The Phase 3 rewrite will replace it; only broken commands are patched here.

Real static-only Astro app. `simple-vps` serves `dist/`; Astro builds that
directory before deploy.

Before deploying, edit `ship.toml`:

- set `box`
- set the route host

```bash
npm install
npm run build
git init
git add .
git commit -m "initial simple-vps app"
ship
curl https://site.example.com/
```

For static-only apps, simple-vps deploys the generated output. It does not run
`npm run build` for you.
