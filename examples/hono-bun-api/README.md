# Hono Bun API

Minimal Bun/Hono container API with a `/health` probe.

Before deploying, set `box` and the route host in `ship.toml`.

```bash
git add . && git commit -m "initial ship app"
ship
```

`ship.toml`:

- `box` is the deploy SSH target for the VPS.
- `probe = "/health"` gates traffic on the API health endpoint.
- `[processes].web` runs `bun run src/server.ts` on port `3000`.
- `[routes]` sends `api.example.com` to `web`.
