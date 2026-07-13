# ADR-0016: Surface Prunings (July 13 Batch)

- **Status**: Accepted (Franco, July 13, 2026 — decisions D4–D9)
- **Date**: 2026-07-13
- **Related**: `docs/ship-spec.md` §2/§4/§6/§16/§18. The larger
  siblings from the same review: ADR-0012 (backups), ADR-0013
  (topology), ADR-0014 (box config), ADR-0015 (preview capability).

Small decisions from the same product pass, recorded together so the
"not Y" survives. Zero users: every removal is outright, no shims.

## D4 — bare `secret set` follows branch=env

The old rule (bare form sets **prod**) made secrets the only verb
family that ignored branch=env: on a feature branch every verb
targeted your preview except this one, which silently mutated
production. Bare form now targets the current branch's env; prod is
set from the production branch — the same way prod is deployed.
**Not** a `--prod` flag: the branch already says it.

## D5 — `@secret:NAME` removed

Aliasing existed for var-name ≠ store-name mismatches, which cannot
exist yet (no users, no legacy names). One rule: the secret is named
after the var. Cost measured before deciding: ~10 lines of code — the
deletion is about grammar count, not code. Two vars sharing one value
set it twice, accepted. Returns in an hour if ever missed.

## D6 — `--include-dotenv` removed

A hidden flag that packed `.env` files into the release artifact —
bypassing ship's own exclusion rule, landing secrets on the box disk
under `releases/`. The sanctioned path already exists
(`secret set --from .env`). A flag that defeats our own guardrail is
a future hunter finding; deleting it buys the honest sentence
"`.env` never leaves your machine".

## D7 — init starter templates removed

`init` generated hello-world apps (python by default, php, hono, a
static index.html). A starter catalogue is a different product with
infinite surface (why hono and not fastify/rails/axum?) and every
template is a Dockerfile maintained against upstream images. ship
deploys the app you have. **Not** removed: the stack-detection TODO
(§2) — writing a correct Dockerfile for an *existing* app is the
valuable half and stays on the roadmap.

## D8 — `box ls` → `box apps`; status/apps/doctor stay three verbs

`ls` named nothing ("list… directories?") and squats on the natural
future meaning "list my boxes" (multi-box, parked). Vercel-check:
noun-first (`vercel env ls`). **Not** the fold-everything-into-status
design a review proposed: status ("how are you", fast summary), apps
(the table), doctor ("examine yourself now", slow probe) are three
different questions; folding doctor in would make the daily question
slow. The real defect was plumbing — status fetched the full app list
and doctor JSON to print counts — fixed as plumbing, not UX.

## D9 — `box forget` stays hidden

Needed in exactly one situation (rebuilt box at the same IP trips the
host-key pin — which is also what MITM looks like, hence no
auto-heal). Discovery-by-remediation: the `host_key_changed` error
names it. Rare recovery actions live in error output, not in `--help`.
