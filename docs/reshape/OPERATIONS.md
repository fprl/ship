# Operations (reshaped ship)

Status: draft corpus. How the deploy transaction, failure classes,
recovery, boot, and garbage collection work. Normative for `apps` and
`activation-records`.

## The eight deploy phases

`apps` sequences every deploy through these phases in order. Each phase
has one pinned failure meaning. The Commit phase is the point of no
return: before it, every failure is `failed`; after it, every failure is
a committed outcome.

| # | Phase | What it does | Failure |
|---|-------|--------------|---------|
| 1 | **Validate** | Pure request/config/policy validation. | `failed`, no cleanup |
| 2 | **Prepare** | Create reversible artifact + activation candidates, resolve secrets, render proposed runtime and route state. | `failed`, compensate every owned candidate |
| 3 | **Verify** | Build/start the candidate, run the release command and health probe, and ask the proxy to *validate* the candidate route config without changing live traffic. | `failed`, clean candidates; old traffic stays authoritative |
| 4 | **Commit** | Kernel/activation-records only: atomically publish the activation pointer. No module participates. | before publish `failed`; after publish, all later failures are committed |
| 5 | **ConvergeRuntime** | Make processes/static runtime match the committed pointer (start new before stopping old). | `committed_unconverged` |
| 6 | **Route** | Proxy atomically applies the route config it validated in Verify. | `committed_unconverged` |
| 7 | **Finalize** | Append the terminal journal state and retire superseded runtime/candidates. Idempotent and retryable. | `committed_degraded` |
| 8 | **Notify** | Webhooks, progress, human output. | warning only — must never rewrite the recorded outcome |

Two subtleties that are part of the contract:

- **The proxy participates twice**: validate pre-commit (Verify), apply
  post-commit (Route). Validation before the pointer moves means a bad
  route config fails the deploy clean, with live routes untouched.
- **Finalize participants are tagged required or opportunistic.**
  Publishing the success journal and preventing duplicate workers are
  *required* — their failure is `committed_degraded`. Best-effort GC is
  *opportunistic* — its failure is a warning, never a degrade. One failed
  image prune must not mark a healthy deploy degraded.

## Failure classes (the outcome vocabulary)

`activation-records` owns these strings; they are a closed set.

- `failed` — strictly pre-commit. The old release still serves. Clean.
- `committed_unconverged` — the pointer moved; runtime/routes may be old
  or half-switched. Heals forward on the next convergence.
- `committed_degraded` — committed and converged, but a required
  finalizer did not complete. Serving, needs attention.
- `deployed` / `rolled_back` — terminal successful outcomes.
- `converged` — a no-op or repair convergence; not a lifecycle event.

`next:` remediation for any committed-but-not-clean outcome is
`ship converge`.

## Interruption recovery

A killed deploy lands in exactly one of three self-healing states,
decided by which side of the Commit phase it died on:

- **Before the pointer write** — nothing committed. Prepared candidates
  are removed by activation label; the old release keeps serving.
- **After the pointer write, before Finalize** — `committed_unconverged`.
  The pointer names the new artifact; any later verb, an explicit
  `ship converge`, or boot convergence heals forward. Never rolls back.
- **After Route, before the fragment/journal is durable** — live state is
  ahead of disk; the next convergence re-applies the identical candidate
  (a no-op) and re-publishes the record.

There is no rollback-on-failure path that could strand the operator on an
unexpected version.

## Boot convergence

A systemd oneshot ordered after the container runtime and proxy are up
enumerates every deployed environment and reconciles each to its
committed pointer. It reads desired state; it never replays the journal
to reconstruct runtime. Per-environment tolerance is typed:

- Permanent artifact-resolution failures (absent image/tuple/path/hash)
  are skipped with a "redeploy to heal" note — they do not fail the unit.
- Infrastructure failures (runtime down, I/O) aggregate into a non-zero
  exit so systemd retries.

One broken environment never blocks the others.

## Garbage collection

GC keeps the active artifact plus the newest N verified rollback
candidates (production deeper than preview), protects anything it cannot
verify and anything inside a short grace window, and refuses the whole
environment's sweep on any journal uncertainty. It shares the exact
candidate set with rollback, so the two can never disagree about what is
safe to keep. It never removes a running container. Physical image
removal untags every ship alias before removing by id.

GC runs after every successful deploy/rollback and on a timer. Its
failure is always opportunistic (a warning), never a deploy degrade.

## Rollback

Rollback selects the newest verified non-active candidate from the full
committed history, prints its identity before committing, re-resolves
current secrets into a fresh activation, and then runs normal
convergence. It reads the pointer only for identity, so it works even
when the currently-active artifact is itself broken. Static targets are
re-hashed immediately before commit.
