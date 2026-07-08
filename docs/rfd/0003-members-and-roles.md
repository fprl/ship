# RFD-0003: Members — humans and agents as identities with roles

**Status: draft, post-v1.** Extends the shipped `ship member add|ls|rm` identity surface
and §7 attribution. Prerequisite for RFD-0002 (resident). Does not modify
ship v1.

> Update July 7, 2026: §7 attribution is built (the journal records SSH
> key comment + git author on every mutation that writes one). This RFD
> is the first promotion candidate after v1 ships — and becomes urgent
> the day anyone wires `notify` to an agent runner, which must not
> happen on a full-access key.

## Model

A **member** is a name + SSH public key + role. Members is the surface;
capabilities are the plumbing. No policy DSL exists anywhere.

```
ship member add cami --key <path|github-user> --role shipper
ship member add claude --role agent
ship member ls [--json]
ship member rm <name>
```

The identity surface (`member add|ls|rm`, July 8) shipped ahead of this
RFD with every member implicitly full-access; this RFD adds `--role` and
enforcement on top of it (no-compat rule, ship-spec §12).
Consistent with the §12 tie-breaker: Vercel's surface is members with
roles, not policy files.

## Roles (fixed bundles, exactly three)

- **owner** — everything: box init/doctor, member management, secrets,
  save/restore, `rm` prod.
- **shipper** — the daily loop: ship prod + previews, status/logs/why,
  rollback, secret set, pin/unpin, rm previews. Not: member management,
  restore, rm prod, box init.
- **agent** — previews and reads: ship/rollback/pin on preview envs;
  status/logs/why/docs/state everywhere. Not: prod mutations, secret
  read *or* set, rm, save/restore, shell. Anything outside the role
  returns `approval_required` (plus a notify event) — never a silent
  denial, so an agent always knows the exact next step.

## Enforcement

- **Identity:** per-member `authorized_keys` entries on the deploy user;
  the key comment carries the member name; the helper receives it and
  journals it — §7 attribution already specs "who shipped."
- **Role checks live in the helper** (server-side). Client-side checks
  are UX sugar only; a modified client changes nothing.
- **The gap to close honestly:** today the deploy user has a shell
  (rsync upload + `sudo helper`), and a shell cannot be role-limited.
  Target: agent-role keys get forced-command entries —
  `command="ship-helper serve --member <name>",restrict` — so the key
  physically cannot open a shell and uploads become a stream inside the
  serve protocol (the git-over-SSH shape). Sequence the work by trust
  tier: agent keys first (the untrusted tier, and previews-only traffic
  is the narrower protocol); owner/shipper humans keep plain keys until
  the serve protocol has earned it, because their trust model is
  "teammate," not "process."
- **Privilege model unchanged:** the helper remains the only privileged
  surface; sudoers stays as-is (§12).

## Approvals

`ship approve <id>` grants the requested action one-shot (optionally
time-boxed), recorded in the journal. Requested via the
`approval_required` error + notify event; consumed by RFD-0002's L2.

## Receipts

Every mutating verb is journaled with member identity (extends the §7
deploy journal to secrets, rm, pin, restore). A later `ship log` read
verb renders box-wide who-did-what-when. `ship status` already shows
"shipped by <member>."
