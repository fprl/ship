# RFD-0005: Sleeping previews — serverless economics on a box you own

**Status: draft, post-v1.** Slotted for the agentic wave in RFD-0004's
July 12 triage, but like every RFD it is unscheduled until promoted to
a spec section with acceptance criteria. Requires preview protection
(v0.4.0, redesigned as one capability — spec §15). Does not touch
prod, ever.

## Pitch

An agent pushes five branches to a €4 VPS and every preview gets a URL —
and costs ~zero RAM until a human (or agent) actually looks at it. The
platforms call this scale-to-zero and meter it; here it's a systemd
feature from 2010 on hardware you own.

## Why scheduled (not someday)

The 1–5 team never felt preview RAM pressure at human pace. The agent
fleet changes the arithmetic: previews-per-box is now bounded by agent
enthusiasm, not by teammate count. Franco's trigger argument (July 12):
five agent branches on a cheap box is the *target* workload — so this
ships with the wave that creates the workload, not after users complain.

## Mechanism (old primitive, zero new verbs)

- Each preview web process gets a **systemd socket unit** owning its
  port; `systemd-socket-proxyd` (or podman's native socket activation)
  forwards to the container, starting it on first connection.
- **Idle timeout** stops the container (`--exit-idle-time` on the
  proxyd side); state is on disk (`/data`), so stop/start is safe by
  the data doctrine (RFD-0001).
- Caddy config unchanged — it proxies to the socket whether or not the
  container is warm. First request pays a cold start; everything after
  is normal.
- Surface: a manifest knob only (name pinned at spec promotion, e.g.
  `[previews] sleep = "10m"` — the behavior section §15 introduces;
  NOT `[env.preview]`, which is a pure env-var overlay). No new verbs,
  no new daemon. `ship status` shows `sleeping` honestly as a process
  state.

## Hard dependency: Preview Protection

Public sslip URLs get crawled; crawlers are traffic; traffic keeps
previews awake. Protection rejects unauthenticated requests **at Caddy,
before the backend socket is touched**, so a protected preview actually
sleeps. Unprotected previews with `sleep` set would thrash awake — the
spec section should refuse or warn on that combination.

## Open questions (answer at spec promotion)

- **Cold-start UX:** first hit blocks a few seconds. Acceptable, or does
  Caddy serve a minimal "waking up" interstitial? (Lean: block; measure
  first.)
- **Probes:** deploy-time probe runs while warm, then the preview may
  sleep; doctor must not wake previews on its timer.
- **Workers:** socket activation wakes on *inbound* traffic; a preview's
  queue worker has none. v0 likely: only `web` processes sleep; workers
  in sleeping previews are stopped with them and wake together.
- **Wake attribution:** journal the wake (who/what woke it) — cheap and
  in character.

## Acceptance sketch

Fake-vps: preview with `sleep=1s` idles → container exits → HTTP request
→ 200 within budget → journal records wake; prod never gains a socket
unit; doctor timer wakes nothing; unprotected+sleep combination warns.
