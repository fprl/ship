# ADR-0017: Quality-Pass Decisions (July 14 Batch)

- **Status**: Accepted (Franco, July 14, 2026)
- **Date**: 2026-07-14
- **Related**: `docs/ship-spec.md` §2/§8/§19, ADR-0014 (box config),
  ADR-0016 (surface prunings).

Design calls from the post-D1–D13 code-quality review, recorded so the
"not Y" survives. The review's clear-cut correctness fixes (error
masking, partial-stop stranding, journal-before-write, snapshot
collisions, JSON null) ship in the same batch without ADR entries —
they have one right answer.

## `ship init` writes no placeholder route

`init` used to fabricate `<name>.example.com` into `[routes]` when
`--host` was absent. The documented flow (README, spec §8) is deploy
first → automatic sslip URL → add the real domain later; a placeholder
the user must edit before their first deploy works against the
product's first-run moment, and a forgotten placeholder becomes a
bogus route on the box. No `--host` now means no `[routes]` block —
the first deploy synthesizes the zero-DNS URL, and the deploy output
already prints the add-a-domain next-step. **Not** the alternative of
keeping the placeholder and rewriting the docs around it: the docs
describe the flow we believe in; the manifest was wrong, not the docs.
`--host` given → route written exactly as before.

## Box-config apply mode: field deleted, concept stays in the spec

ADR-0014 reserved a per-key apply mode (`none`/`converge`) as a schema
field. As shipped, nothing read the field: a future key declared
`converge` would validate, journal, and silently never converge — a
declared-but-unread policy field is a no-op trap, worse than absence.
The field, its type, and its consts are deleted; spec §19 keeps the
concept and commits the first converge key to adding the field
*together with* the plumbing that reads it. **Not** keeping the field
as documentation (code that promises behavior must have the behavior)
and **not** dropping the concept (RFD-0007's `harden.*`/`backup.*`
still want it). Zero users; re-adding is a ten-line diff.

## Journals record what happened: apply → journal, not journal → apply

The spec's terse `box config set` line originally read validate →
authorize → journal → apply — which is exactly the ordering that let a
failed write leave a journal entry recording a change that never
happened. The order is now apply → journal (still under the config
lock), and a journal-append failure after a successful apply is a
stderr warning, not a command failure — the same philosophy as the
deploy journal. **Not** journal-intent-plus-outcome records: two-phase
audit is real machinery for a compliance need ship does not have.

## Secrets reject embedded newlines at set time

The podman `--env-file` format cannot represent an embedded newline —
it terminates the entry, so a multiline value corrupts the env file at
the next deploy. The set path (helper-authoritative) now rejects values
with non-trailing newlines with a coded error and a remediation
(encode multi-line material, decode in the app). One trailing newline
stays legal and stored verbatim. **Not** switching container env
delivery off env-files to make multiline values representable: that
rewires how every container starts, for a value shape nobody needs
yet; if a real need appears, that is its own decision. **Not** silently
flattening at deploy time — storing what we cannot deliver is how the
old double-trim bug happened.

## Point-of-no-return failures degrade to warnings (applied to deploy)

The previous batch established the rule for data fork/reset/restore:
after the swap, failures are warnings, not false failure reports. This
pass applies the same rule to the remaining violators found by the
hunts: a deploy whose traffic has switched no longer reports a false
abort when the success-journal append fails, and `data fork`/`data
rm`/`preview share --rotate` no longer exit nonzero when only the
follow-up URL lookup — after the mutation landed — fails.

## Secret trailing newline: the client owns the single trim

Both the client and the helper trimmed one trailing newline from a
secret value, so a value intentionally ending in a newline (PEM keys)
could never be stored. The boundary is now: the client trims exactly
one trailing newline (the TTY/`echo` artifact, closest to where it is
introduced) and documents it; the helper stores its stdin verbatim.
**Not** trimming on both sides "defensively" — every role reaches the
helper through the ship client, and a storage layer that edits values
is how the PEM bug happened.
