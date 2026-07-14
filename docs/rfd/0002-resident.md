# RFD-0002: Resident — the box reports in, diagnoses, and fixes within bounds

**Status: draft, post-v1.** Builds on ship-spec §7 (deploy journal,
`notify`, doctor timer) and requires RFD-0003 (members). Does not modify
ship v1.

> Naming update July 14, 2026 (ADR-0019): `notify` is now `webhook`
> everywhere (manifest key, `ship box webhook`, config key
> `webhook.url`), and the approval verb is
> `ship box approval grant <id> [<box>]`. Commands quoted below keep
> their original spellings as a record of the design at the time.

> Update July 7, 2026: the §7 substrate is now built — journal, `why`,
> `notify` (four events with journal-grade payloads), doctor delta,
> `ship docs`. Still missing before a resident can exist: the
> `ship state --json` bundle below, and the agent role from RFD-0003.
> Do not point `notify` at an autonomous runner holding a full-access
> deploy key — that is the exact hole members closes.

## Naming

- **doctor** = sensors: deterministic checks with static remediations
  (exists today, upgraded in ship-spec §9).
- **resident** = the operator loop that acts on what sensors report:
  diagnoses, fixes what its role allows, escalates the rest with the
  diagnosis already done.

Doctor says "disk 94%: degraded." The resident says "disk hit 94% —
stale preview images; pruned them; now 61%; receipt in the journal."

## Principles

1. **No AI in the deploy hot path.** Deploys stay deterministic and
   tested (§5 contract). Intelligence lives in the judgment path:
   diagnosis, repair, review.
2. **No resident daemon.** Sensors fire on state *change* only (newly
   degraded check, crash loop, OOM kill, probe failure, disk watermark).
   A healthy box costs zero tokens.
3. **ship never embeds a model.** The brain is any runner that can
   receive a webhook and execute `ship` — a GitHub Action, an agent on a
   laptop, or an agent on the box. Swappable, no AI lock-in.
4. **The resident's memory is the box's journal**, not the agent's
   context. Stateless between incidents; any capable agent can pick up
   the next one cold.
5. **The resident never gets a shell.** It authenticates as a member
   with role `agent` (RFD-0003) and can only speak ship verbs — so it
   physically cannot drift host state that ship owns.

## Loop

```
sensor fires (state change)
  → notify POSTs the state bundle
    → runner wakes, acts via ship verbs as member role=agent
      → receipt lands in the journal (§7 attribution)
```

New surface needed: `ship state --json` — one diagnostic bundle
(journal tail, doctor results, last ~200 log lines, manifest, releases,
env list) so a webhook-woken agent needs zero discovery round-trips.
`ship status` stays the human summary; `state` is the machine dump.

## Escalation ladder

- **L0 — supervision.** systemd/podman restarts crashes; failed deploys
  never take traffic (existing engine). Most incidents end here and the
  resident never wakes.
- **L1 — within role.** Rollback, re-ship, pin, prune-via-verb: fix
  applied, receipt journaled, notify says what happened.
- **L2 — out-of-role ship verb.** Helper returns `approval_required`;
  notify carries the exact command and the diagnosis. A human approves
  one-shot (`ship approve <id>`) or runs it themselves.
- **L3 — outside ship's verb set.** The resident cannot act, by design.
  It files a proposal: the exact operator command, or a PR changing
  `ship.toml` / the Dockerfile — so fixes flow through the same door as
  deploys and persist instead of evaporating. Humans always keep
  break-glass SSH as the operator user (security-model.md keeps OpenSSH
  precisely as recovery access).
- **Promotion rule:** a recurring L2/L3 category becomes a new narrow
  helper verb (§12: add verbs rather than widening sudo). The escalation
  set shrinks release by release.

So "it broke and there's no sudo" resolves to: the resident becomes the
best pager message you ever got — diagnosis plus exact remediation, one
approval away — never a silent failure.

## Drift protection

Because the resident holds no shell, it cannot mutate ship-owned host
state out-of-band. Add a doctor check that hashes ship-written artifacts
(Caddy fragments, systemd units, sudoers grant) and flags foreign edits —
which also surfaces *human* SSH drift.

## v0 pilot

`notify` webhook → `claude -p` runner fed the payload → it runs
`ship why` / `ship logs` / `ship rollback` under an agent-role key. The
§9 agent-eval scenarios double as the resident's acceptance suite; same
rule as §9: when the resident fails a scenario, fix the error text or
the verb set, not the test.
