# Astro Static

Real static-only Astro app. `simple-vps` serves `dist/`; Astro builds that
directory before deploy.

Before deploying, edit `simple-vps.toml`:

- set `[env.production].server`
- set `[routes.site].host`

```bash
npm install
npm run build
git init
git add .
git commit -m "initial simple-vps app"
simple-vps check --env production
simple-vps setup --env production
simple-vps deploy --env production
curl https://site.example.com/
```

For static-only apps, simple-vps deploys the generated output. It does not run
`npm run build` for you.
