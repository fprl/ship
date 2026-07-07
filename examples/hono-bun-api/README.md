# Hono Bun API

> **Superseded:** This document describes the pre-ship surface and is not current.
> **Pending:** The Phase 3 rewrite will replace it; only broken commands are patched here.

Minimal container app example.

Before deploying, edit `ship.toml`:

- set `box`
- set the route host

```bash
git init
git add .
git commit -m "initial ship app"
ship
curl https://api.example.com/health
```
