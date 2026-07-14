# ADR-0018: CLI Taxonomy, Approval Addressing, and Generated URLs

- **Status**: Accepted (Franco, July 14, 2026)
- **Date**: 2026-07-14
- **Related**: spec §5/§8/§13/§16/§19, ADR-0014 (box config),
  ADR-0016 (surface prunings), ADR-0017 (quality pass). Shaped in an
  adversarial design consult (two independent reviewers converging
  over four rounds); every factual claim verified in code before
  acceptance.

Eight decisions, ratified together. Zero users: renames are outright,
no aliases, no migrations.

## 1. Approvals live on the box, so the verbs move there

`ship approve` was a project command (resolved the box from
ship.toml), which made the one person who most needs it — an approver
outside the requester's checkout — unable to run it. New surface,
following the existing object-first `box rm <app> [<box>]` shape:

```
ship box approvals [<box>] [--json]     list pending
ship box approve <id> [<box>]           grant one
```

Top-level `ship approve` is deleted. Splitting list from grant kills
the two-optional-positionals ambiguity structurally. Every
remediation that names an approval prints the FULL resolved command
(`next: ship box approve abc123 203.0.113.7`) — the approver's real
workflow is pasting from chat. **Not** `--box` flags (second spelling
for addressing) and **not** a shorter project-scoped form kept as
sugar (two spellings for one verb).

To make those remediations truthful, the helper must know the box's
client-routable address: `box setup` records the address the client
used, box-side, next to members/approvals state; notifications stop
deriving a name from `os.Hostname()` (a Hetzner-internal hostname no
laptop can dial).

## 2. Generated URLs are app-first; env words never appear

The synthesized host was `<env>.<ip-dashes>.sslip.io`. Every app's
production env shares one name, so a second routeless production app
on a box synthesized the IDENTICAL host — a guaranteed collision on a
core topology (verified in routes.go: the env name was the only
distinguishing label). The app name is the box-global identity, so it
becomes the first label:

```
production:  <app>.<ip-dashes>.sslip.io
preview:     <app>-<branch-slug>-<id>.<ip-dashes>.sslip.io
```

One flat label for previews, sized deterministically: the app name is
never truncated; `slug_budget = min(28, 57 - len(app))` (app max 41 →
slug keeps ≥16 chars); the persisted 4-char id disambiguates; the
collision retry keys on the FINAL host label, not the env name (two
long slugs can truncate identically). **Not** the nested shape
`<app>.<slug>-<id>.<base>`: a standard wildcard certificate covers
exactly one label, so nested previews could never ride the planned
wildcard-base (`*.preview.example.com`, spec §8 steady state) without
changing shape — the exact URL instability this ADR ends. sslip
multi-label resolution was confirmed empirically before rejecting
nested on futures, not on feasibility.

`box apps` shows the class (Production / branch) and the canonical
URL; internal env names stay out of human output.

## 3. The production env is named `production`

The env NAME (`prod`) and the authorization CLASS (`production`)
forced users to hold two words for one singleton concept
(`env=prod class=production` in approval rows). Users never type the
env name (branch=env) and — after §2 — URLs never contain it, so the
rename is free and internal: directories, snapshot filenames, JSON
`env` fields, approval summaries. **Not** kept as `prod` for
brevity: brevity that nobody types buys nothing.

## 4. Member verbs move under box

Same disease as approvals (manifest-only resolver on box-global
state), same cure, same shape:

```
ship box member add <key> [<box>] [--role owner|shipper|agent]
ship box members [<box>] [--json]
ship box member rm <name> [<box>]
```

Singular `member` for operations, plural `members` for the
collection — mirroring `box apps`/`box approvals`. Role stays a flag:
optional policy with a sensible default, not identity.

## 5. `data rm` → `data reset`

Every other `rm` destroys an addressed object (`ship rm <branch>`
destroys the environment). `data rm` empties the env's `/data`
contents and restarts containers — the helper function was already
named `resetAppData`. The verb now says what it does.

## 6. `box notify` stays — with a fence

Challenged from both sides (delete the sugar / delete the generic
config), it survives on the D11 rationale: the pager is the one
setting every user must touch in minute one, and it deserves the
memorable spelling. The fence this ADR adds: **dedicated sugar
requires unusually high product importance — never one verb per
config key.** `box config` remains the only generic settings surface.

## 7. No sticky box context

Considered and rejected: a kubectl-style `ship use <box>` selector.
ship is a directory-context tool — ship.toml IS the context,
committed to the repo, traveling with the code (the gh model: infer
from the checkout, explicit argument elsewhere). A sticky pointer is
competing invisible state that outlives terminals and rebuilt boxes,
and approvals are exactly where the target must be visible in the
pasted command. Box ALIASES also rejected: a second mutable namespace
solving an undemonstrated typing problem — remediations are pasted,
not typed. `ship boxes` stays parked as a plain future list verb,
never a resolver.

## 8. Approval grants get integrity guards

Found while dogfooding the smoke teardown: (a) the approver's role
was never checked against the action's required role, so a shipper
could grant owner-gated actions; (b) self-approval was not rejected,
so the minting member could grant their own request — together
reducing owner-only actions to "shipper + two commands"; (c) the
grant inherited the request's original expiry, so approving near the
deadline left a useless retry window. Now: the required role is
recorded at mint, granting requires approver role ≥ required and a
different member, and a grant refreshes the expiry window. Approval
summaries become verb-first human sentences (env-destroy rows carried
no verb at all). **Not** blocking the agent flow: agent mints, human
grants — unchanged, that is the feature.
