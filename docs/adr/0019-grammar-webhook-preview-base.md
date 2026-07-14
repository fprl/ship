# ADR-0019: One CLI Grammar, Webhook Naming, Preview Base, Member Onboarding

- **Status**: Accepted (Franco, July 14, 2026)
- **Date**: 2026-07-14
- **Related**: ADR-0018 (partially superseded: collection verb
  spellings), ADR-0014 (box config), spec §2/§3/§8/§13/§16/§17,
  RFD-0008 (member invite flow, sketched from this ADR). Shaped in a
  two-round adversarial design consult; both sides conceded points;
  every factual claim verified in code first.

## 1. One grammar: `resource + subverb`; a bare noun never lists

Three list idioms coexisted (bare plural `box apps`/`box members`/
`box approvals`; namespace+ls `secret ls`/`data ls`; and standalone
mutation verbs `box approve <id>`, `box rm <app>`). One rule now
covers the whole CLI:

```
ship box member ls [<box>] [--json]
ship box member add <key|path|https-url> <box>|[<box>] --name <n> [--role r]
ship box member rm <name> [<box>]
ship box app ls [<box>] [--json]
ship box app rm <app> [<box>] --confirm <app>
ship box approval ls [<box>] [--json]
ship box approval grant <id> [<box>]
ship secret ls|set|rm            (already conformed)
ship data ls|save|restore|fork|reset   (already conformed)
```

Deleted spellings: `box members`, `box apps`, `box approvals`,
`box approve`, `box rm`. This supersedes ADR-0018's bare-plural
collection forms — settled once, pre-1.0, while renames are free.
**Not** bare-plural-lists: users and agents had to memorize which
nouns list bare, which need `ls`, and which mutations are standalone
verbs; uniform prediction beats isolated elegance. `box rm` also
read as destroying the box; `box app rm` cannot.

**The boundary rule** (the sentence the grammar needs): top-level
workflow verbs operate on the deployment selected through the
repo/branch model — `ship`, `status`, `logs`, `why`, `rollback`,
`exec`, `rm`, `preview pin/unpin/share`, `data *`, `secret *` — while
`box <resource> <subverb>` families manage independently addressable
box collections (members, apps, approvals). `ship rm <branch>` stays:
it is Vercel's own verb (`vercel rm`), the argument is a branch like
every workflow verb, Production demands `--confirm`, and previews are
reaper-managed anyway. **Not** `ship env rm`: it would mint a sixth
product noun (repo, box, branch, snapshot, URL) and a one-command
resource family whose `ls` sibling is `status`.

## 2. The webhook is named `webhook`, at both scopes

A URL that receives event POSTs is a webhook everywhere the user has
ever seen one (Vercel, GitHub, Stripe, Slack); "notifications" is the
human-channel concept. `box notify <box> <url>` also read like an
immediate action. Renamed wholesale, zero users, no aliases:

- ship.toml app key: `notify =` → `webhook =` (app events:
  deploy_aborted, deploy_recovered, preview_reaped)
- verb: `ship box notify` → `ship box webhook` (box events:
  doctor_degraded, approval_requested)
- box-config key: `notify.url` → `webhook.url`
- prose: "notify events" → "webhook events" across spec/docs/errors

**Not** renamed: the two-scope event routing (§17) — app events to
the app's webhook, box events once to the box webhook — that split
exists so one disk spike cannot page N agents into the same incident,
and it is untouched. **Not** split naming (`webhook` verb over a
`notify.url` key): one mechanism, one name, or the rename is a net
loss.

## 3. `[preview]`: addressing policy on the user's own domain

```toml
[preview]
base    = "preview.example.com"   # default: <box-ip>.sslip.io
aliases = true                    # default: false
```

- Canonical preview host: `<app>-<branch-slug>-<id>.<base>` (shape
  unchanged from ADR-0018; one flat label, app never truncated).
- `aliases = true` additionally serves `<branch-slug>.<base>` — a
  stable per-branch address derived automatically from the branch
  name, rendered as one extra route in the same per-env Caddy
  fragment, guarded by the same capability, updated on each deploy,
  removed with the env. Alias collision (two branches, one slug, or
  any existing route/host on the box): the existing owner keeps it,
  the newcomer gets a warning and keeps its canonical URL. **Not**
  hand-picked alias names: a boolean that can grow into a map later
  beats a mapping nobody asked for yet.
- `base` is a bare DNS suffix: no scheme, path, port, credentials,
  wildcard prefix, or trailing dot; labels validated; the full
  generated host must fit DNS limits; lowercased.
- Production addressing is untouched (`[routes]` or `<app>.<ip>`
  synthesis). Omitting the table keeps today's sslip behavior
  exactly.
- Deliberately independent of `processes.<n>.preview = false`
  (runtime inclusion) and `[env.preview]` (env values): addressing
  policy must not alter what runs. **Not** a revival of the deleted
  D13 `[previews]` protection knobs — protection stays always-on and
  non-configurable.
- Why this beats hiding the IP any other way (consult, July 14): the
  IP is public via scanning/DNS/CT regardless; sslip cannot alias
  (the IP in the name IS its mechanism); a ship-run domain is
  platform machinery. One wildcard DNS record on the user's domain
  fixes aesthetics, CT branch-name leakage, and the sslip
  same-site cookie caveat (sslip.io is not on the Public Suffix
  List) in one move.

## 4. Member onboarding: show first, confirm content, name explicitly

`box member add <github-user>` fetched github.com/<user>.keys and
authorized every key on the account before showing anything — a typo
handed a stranger deploy access. New shape:

- `--name <n>` is mandatory everywhere; identity never derives from
  key comments, filenames, or git config (the "ship"-instead-of-
  "fprl" incident). `box setup` still enrolls the first member as
  owner and prints whom it named, loudly.
- Literal key material or a local file writes immediately — the owner
  supplied the exact bytes.
- Remote sources are raw HTTPS keys-URLs only
  (`https://github.com/alice.keys`, gitlab/codeberg/self-hosted
  equivalents — the .keys endpoint is a forge convention). The bare
  command FETCHES AND PRINTS the keys, fingerprints, source URL,
  proposed name and role, writes nothing, and emits the exact commit
  command: `--confirm <name>@sha256:<plan-digest>`. The digest binds
  box, source, name, role, and the sorted key material; confirm
  refetches and requires an exact match, so what was reviewed is
  byte-identical to what installs (closes the fetch-changed-under-
  you gap). HTTPS enforced across redirects; bounded size/time/key
  count; atomic write of the confirmed set.
- **Not** `github:alice` shortcuts: for a command that grants deploy
  access, the URL is the provenance — explicit fetch beats saved
  keystrokes, works for every keys-serving host, and adds no scheme
  registry. **Not** an invite/join flow yet: invitations are
  unauthenticated bearer credentials that CREATE identity — opposite
  trust assumptions from approvals, and they need a new enrollment
  transport; sketched as RFD-0008, demand-driven, post-1.0.

## 5. Small ratified riders

- `box status` gains a `members: N (M owners)` line — the one thing
  the at-a-glance box picture was missing. Members otherwise stay
  OUT of box config: config holds settings; members are principals —
  the trust root config authorization is checked against (folding
  them in would be circular, and key material cannot ride a generic
  setter safely, per ADR-0014's own rule).
- `ship exec -- <cmd>` consumes the `--` separator instead of passing
  it to the container (bug found in the v0.5.0 real-box gate).
