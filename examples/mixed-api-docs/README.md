# Mixed API And Docs

Container API plus a static `/docs` route deployed as one release.

Before deploying, set `box` and both route hosts in `ship.toml`.

```bash
git add . && git commit -m "initial ship app"
ship
```

`ship.toml`:

- `box` is the deploy SSH target for the VPS.
- `probe = "/health"` gates traffic on the API health endpoint.
- `[processes].web` serves the API on port `3000`.
- `[routes]` sends `/docs` to `docs-dist` and the host root to `web`.
- Rollback and restore move the API and static docs together.
