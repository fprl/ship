# RFD-0011: Unit Management, Pods, and Process Connectivity (parked)

- **Status**: Parked (Franco + sol, July 17, 2026 — captured from the
  ADR-0023 primitives audit and the pods evaluation so the reasoning
  survives until the need is real)
- **Related**: ADR-0023 (primitives audit verdicts), ADR-0022
  (convergence model), docs/ship-spec.md theses.

## 1. Quadlet migration (parked design)

Move container supervision to systemd via Quadlet, ONE migration for
everything: per-process `.container` units as the convergence unit —
ship renders "pointer → derived units", systemd owns start/restart/
logs — and the Caddy container's hand-written unit replaced in the same
move. Deliberately not done in the v0.9.0 arc: Quadlet cannot express
ship's rollout choreography (web overlaps old+new during the route
switch; workers must never overlap), so ship remains the orchestrator
and Quadlet is a supervision layer, not a subsystem replacement. Two
separate unit migrations were rejected; do it once or not at all.

## 2. Pods: evaluated and rejected as a default shape

Verdict (sol, 4-point evaluation, July 17): no current ship shape
warrants pods. A pod is an atomic namespace group — one IP, shared
localhost. Breaks for ship: two processes of one app may both bind the
same internal port today (own namespaces make that legal; one pod makes
the manifest invalid); the deploy choreography cannot use the pod as
the rollout unit (overlap-for-web, no-overlap-for-workers); pods add an
object class (infra containers, pod state, pod GC) while their sales
pitch — no manual networks, compose replacement — solves problems
ship's per-app network already dissolved invisibly. Kamal is not a
precedent: Docker has no pods; Kamal is per-container + proxy +
network, i.e. ship's own shape. IF ship ever grows explicit sidecar
co-location (processes that intentionally share localhost/lifecycle
and have unique ports), an opt-in `.pod` per activation inside the
Quadlet design is the place — never the default, never the rollout
unit.

## 3. Process connectivity growth path: the ingress is the mesh

Today ship injects SHIP_URL/SHIP_ENV/SHIP_BRANCH/SHIP_RELEASE and
offers NO process-to-process endpoint — the Heroku/12-factor stance
(processes coordinate via backing services), and nothing has needed
more. When an app legitimately needs web → internal-process HTTP, the
answer is routes, not networking: an internal route through Caddy is
stable across deploys BY CONSTRUCTION (Caddy always names the selected
container; the handoff window that breaks DNS aliases and pod-local
addressing is already solved there), TLS-consistent, and observable.
Sketch: a route marked internal in ship.toml (not publicly served, or
served on an internal hostname) + injected `SHIP_ROUTE_<NAME>_URL` per
route. Vercel-grade connect-by-URL UX, ~zero new machinery. Needs a
design pass on auth (internal routes must not be reachable from
outside) before building. Do not build ahead of a real app needing it.
