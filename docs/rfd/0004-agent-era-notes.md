# RFD-0004: Agent-era notes — the laptop-closed loop

**Status: notes, triaged July 12, 2026.** Imported from a cross-session
strategy conversation and triaged with Franco. This is a catalog and a
compass, not a commitment: `docs/ship-spec.md` stays authoritative, and
items here only become buildable by promotion to a spec section with
acceptance criteria (rfd/README.md rule).

## North star

Agentic dev: code with the laptop closed, across different harnesses.
Phone kicks off a task → a sandbox writes code → `ship` → preview URL →
agent iterates → notify pushes the URL to the phone → human reviews
there → sandbox dies, the preview survives on the box. **ship is the
destination, not the workshop** — the box is the persistent memory of an
amnesiac workflow.

## Strategic theses (condensed)

1. **Build ship; don't move to the CF stack.** PaaS/serverless sells
   ops-labor abstraction; AI collapsed the price of ops labor. What
   stays scarce is what ship is: determinism, guardrails, legible
   failure. Honest caveat: Workers+D1 wins for a single greenfield TS
   app at n=1. ship wins at n=many, any runtime, owned data.
2. **Cloudflare = commodity edges only.** R2 as backup target, Tunnel,
   DNS. Durable Objects is the only CF primitive with no box equivalent.
3. **The wedge is agents.** "The deploy target built for agents" — the
   agent-eval suite proving recovery is a moat; README should lead with
   it. Supporting moves: MCP adapter over the CLI, `ship box create`,
   the resident (RFD-0002).
4. **Design compass: agents invert the economics of old protocols.**
   Pull-based, file-based, text-based, signature-based primitives lost
   to dashboards for humans but are optimal for agents. Selection rule
   for everything below.
5. **Discipline:** adopt an old primitive only where it *replaces*
   surface; every new surface passes the same errcat + docs + agent-eval
   gate. Umbrella line: *"Everything the platforms meter, on one box you
   own."*

## The catalog

| # | Name | Old primitive | Surface cost |
|---|------|---------------|--------------|
| 1 | Sleeping Previews | systemd socket activation + socket-proxyd | runtime mode, zero verbs |
| 2 | State Sync | rsync + versioned flat-file contract | 1 verb, caps future verb growth |
| 3 | Agent Badges | OpenSSH CA certs (TTL, principals) | replaces standing agent keys |
| 4 | The Box Feed | Maildir outbox + Atom feed (RFC 5005) | replaces webhook retry engine |
| 5 | Rehearsals (`ship data diff`) | `VACUUM INTO` + `sqldiff` on cold files | 1 verb in `data` bucket |
| 6 | Provenance | git notes + box-state-as-git + bundle | internal upgrade |
| 7 | Disk Budgets | XFS project quotas + reflink | flips a setup default |
| 8 | Live Tail | SSE with Last-Event-ID | transport on existing verb |
| 9 | The Front Desk | RFC 8615 `/.well-known/ship` | 1 generated endpoint |
| 10 | Preview Protection + Share Links | Basic Auth + capability URLs | flips a default, adds share |
| 11 | The Sealed Journal | hash chain, signed head | schema bump, zero surface |

Resolutions worth keeping: Rehearsals never diffs live files (snapshot
both sides via `VACUUM INTO`, diff cold copies); Sleeping Previews only
works *with* Preview Protection (crawlers on public sslip URLs keep
previews awake); State Sync serves a curated export dir so secrets can
never enter a syncable tree.

## Cloud-agent findings (measured)

- From inside a real Claude Code cloud sandbox: **port 22 egress is
  blocked, 443 is open.** Pure-SSH ship cannot deploy from the exact
  environments the agent trend runs in, today.
- **The 443 door is still SSH**, dressed for the port that exits:
  cloudflared access, sslh/Caddy-layer4 muxing SSH+TLS on 443, or
  proxytunnel as ProxyCommand. Auth stays byte-identical (same sshd,
  keys, forced commands). Agents never get a shell regardless.
- **Credentials:** v1 is a preview-only role key safe to put in sandbox
  secrets (RFD-0003 substrate); endgame is keyless OIDC federation (box
  trusts GitHub's issuer, sandbox exchanges identity for a 30-minute
  branch-scoped badge). Prod is always human-gated.
- **Builds (ADR-0010 revisited):** the spike is the build step, never
  the run. The sandbox is an arch-matched, already-paid builder — heavy
  builds move there via registry-free `podman save | ssh | podman load`,
  shipping together with the 443 door. Pre-approved invisible defaults:
  sacrificial build cgroup, serial build queue.

## Triage — July 12, 2026

- **v0.4.0 (spec sections, build-ready):** Preview Protection + Share
  Links (#10); `ship box status` / `ship box update` (version skew made
  visible + one-command converge); notify split (app webhook for app
  events, box webhook for box events — one disk spike must wake one
  agent, not N).
- **v0.5.0:** the resident (RFD-0002) — `ship state --json`, drift
  doctor check, pilot.
- **v0.6.0 — the agentic wave:** 443 door + Sleeping Previews
  (RFD-0005). Franco's trigger argument, accepted: an agent pushing
  five branches to a cheap VPS is the *target* workload, not an edge
  case — RAM pain arrives with the first agent fleet, so this is
  scheduled, not someday.
- **Parked (this doc is their record):** State Sync, Agent Badges, Box
  Feed, Rehearsals, Provenance, Disk Budgets, Live Tail, Front Desk,
  Sealed Journal, MCP adapter, `box create`, sandbox-side builds.
- **Rejected for the record:** SMTP/XMPP/mDNS/SNMP as channels,
  dashboards, being a sandbox/CDE, multi-host, moving compute to
  Workers.

## Open, flagged honestly

- ~~Preview privacy **default flip** (protected-by-default)~~ —
  settled July 13: always protected, knob deleted, one capability
  token (D13, ADR-0015, spec §15).
- Swap-at-setup contradicts ADR-0010's "no build machinery" stance;
  adopting it means amending the ADR.
- Whether git notes push to origin by default; Front Desk per-box or
  per-app — unexamined.
