# Astro Static

Static-only Astro app. ship serves the generated `dist/` directory directly
through Caddy.

Before deploying, set `box` and the route host in `ship.toml`, then build the
site:

```bash
npm install --no-package-lock && npm run build
```

```bash
git add . && git commit -m "initial ship app"
ship
```

`ship.toml`:

- `box` is the deploy SSH target for the VPS.
- There are no container processes.
- `[routes]` maps `site.example.com` to the static `dist` directory.
- Build output is uploaded with the release; it does not need to be committed.
