# Import graph + ownership map (reshaped ship)

Status: draft contract. This is the data the step-4 import analyzer and
the CI ownership diff will encode. Kept as a table here so the rule set is
reviewable before it becomes code; the analyzer is the enforcement, this
is the specification.

## Package roles

| Role | Import path (target) | May import |
|------|----------------------|------------|
| kernel | `kernel/…` | stdlib + its own adapter interfaces only; **no domain package** |
| activation-records | `activationrecords/…` | kernel |
| apps | `modules/apps/…` | kernel, activation-records, provider interfaces |
| provider interface | `kernel/provider` (build/runtime/proxy) | kernel |
| infra adapter | `adapters/{build,podman,caddy,host}/…` | kernel, its provider interface; may use raw exec |
| module (teal) | `modules/<name>/…` | kernel + typed capabilities only |
| module definition | `modules/<name>/definition` | kernel types only (imported by the registry) |
| registry / builtin | `builtin/…` | module **definition** packages only |

## The rules the analyzer enforces

1. `modules/<a>` may not import `modules/<b>` for any a ≠ b
   (teal↔teal banned, including apps↔other-module except the sanctioned
   apps→provider-interface path).
2. A domain module (`modules/<name>` that is not an infra adapter) may not
   import `adapters/…` and may not import `os/exec` or any raw-exec
   helper. Raw exec is allowed only inside `adapters/…` and `kernel/…`.
3. Only `activationrecords/…` may import the pointer/journal/outcome
   format types with write access; other readers use its exported read
   API. (Enforced by keeping the writers in `activationrecords/internal`.)
4. Nothing may import `modules/<name>/internal/…` except `modules/<name>`
   itself (compiler-enforced by Go's internal rule; the analyzer also
   asserts it so a mistaken `replace` cannot mask it).
5. `builtin/…` imports only `modules/*/definition`, never module
   implementations.
6. `kernel/…` imports no domain package (no activationrecords, apps, or
   modules) — the mechanism cannot learn meaning.

A violation is a failed `go test` (the analyzer is an archtest-style
plain test), not just a lint warning.

## Ownership map (CI diff)

A change on a feature branch may modify only:

- `modules/<name>/…` (its own ownership directory), and
- `builtin/<name>.go` (its one composition file), and
- `tests/fake-vps/<name>_smoke_test.go` and any
  `tests/agent-evals/<name>_*.go` it registers.

Anything else — another module's files, `kernel/…`, `activationrecords/…`,
`modules/apps/…`, shared schemas, the registry, shared fixtures, the
manifest grammar, `go.mod`, or docs — requires an explicit
**architecture waiver** label on the change. The CI diff reads each
module's declared ownership paths from its Definition and fails an
unwaived cross-owner edit.

Waivers are legitimate for: a genuine new kernel capability, a
manifest-grammar change, a release dependency, an ADR/decision record, or
a migration. They are not for reaching into another module because it was
convenient.

## Why "promote, don't import"

If module A needs a fact owned by module B, importing B couples them and
reintroduces the collision the reshape removes. The rule is to move the
shared fact **down** — into the kernel as a typed capability, or into
activation-records if it is deploy memory — so both A and B depend on the
lower layer, never on each other. If a proposed feature cannot be built
without a teal→teal import, that is the signal a new kernel capability is
required, and it gets designed as one.
