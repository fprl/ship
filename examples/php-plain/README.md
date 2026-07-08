# Plain PHP

Small PHP container app using PHP's built-in server for the example.

Before deploying, set `box` and the route host in `ship.toml`, then store the
required secret:

```bash
printf '%s' "$APP_SECRET" | ship secret set APP_SECRET
```

```bash
git add . && git commit -m "initial ship app"
ship
```

`ship.toml`:

- `box` is the deploy SSH target for the VPS.
- `[env]` sets `DATABASE_PATH` under `/data` and references `APP_SECRET`.
- `[processes].web` serves HTTP on port `8080`.
- `[routes]` sends `php.example.com` to `web`.
