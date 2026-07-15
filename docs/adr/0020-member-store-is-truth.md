# ADR-0020: The Member Store Is the Identity Truth

- **Status**: Proposed (compat/quality sweep, July 14–15, 2026;
  pending Franco's review)
- **Date**: 2026-07-15
- **Related**: ADR-0019 §member onboarding ("identity never derives
  from comments/filenames/git config except the blessed `box setup`
  default"), spec §16/§17.

## Context

Until this change, a public key sitting in the deploy user's
`authorized_keys` with no record in `members.json` still worked:
`EffectiveMemberRecords` fabricated a record on the fly — name taken
from the key comment, role guessed from key count (owner when it was
the only parseable key, shipper otherwise) — and the auth path,
`member ls`, renderers, and `box update`'s converge all consumed that
fabrication. That is precisely the comment-derived identity ADR-0019
banned, surviving in the read path. It also made `box update` adopt
unknown keys into the store with invented identities.

## Decision

`members.json` is the authoritative register of who can use the box.
`authorized_keys` is a rendered artifact: it carries the key material
and the forced-command line, but contributes no identity.

- A fingerprint without a store record does not authorize
  (`member_unknown`), is not listed by `member ls`, and is dropped
  from the file the next time it is rendered (member add/rm, or
  `box update` converge — which now actually re-renders the file, as
  its doc comment always claimed).
- Fabrication is deleted everywhere: records come from the store or
  from the explicit enrollment overrides only.
- The blessed `box setup` default is unchanged: setup writes explicit
  records for the keys it enrolls (single-key → owner).
- Half-enrollment (crash between the two writes) stays self-healing:
  the retry carries its own name and role and rewrites both files.
  `member add` writes the record BEFORE the key line (senior-review
  fix): a crash leaves a record without access, never an unrecorded
  line that sshd would still honor. `member rm` keeps the opposite
  order — access is revoked before identity.
- `member rm` refuses to remove the last *recorded* member; stray
  unrecorded lines no longer count toward that guard (the old count
  could leave a box whose only remaining "member" was a dead stray
  line).

## Precision worth stating

The store is the *identity* truth, not a full mirror: key material
lives only in `authorized_keys` (records are fingerprint → name/role).
Rendering is therefore store ∩ file — a record whose key line is gone
cannot be resurrected from the store; re-enroll it. Full `box setup`
keeps merge semantics for the file (it enrolls what it was given and
does not canonicalize strays); add/rm and converge do canonicalize.

## Consequences

- Recovery from a wrong `members.json` is root + `ship box setup`
  (re-enrollment), same as every other box-state repair. For that to
  be true (senior-review fixes): setup enrolls every key it was
  GIVEN, whether or not its line already exists in the file; an
  unreadable/invalid store is rebuilt at setup with a loud warning
  (converge/`box update` still fails loudly — unattended runs never
  wipe the register); and the setup role default is computed from the
  provided keys, not from whatever lines sit in the file — existing
  record's role wins, else owner for a single provided key, else
  shipper.
- Rendering is fully canonical: lines the parser cannot read
  (comments, unsupported key types) are dropped too, not preserved.
- The unread `AuthorizedKey.Options` field went with the fabrication;
  rendered lines are fully regenerated from the role.
