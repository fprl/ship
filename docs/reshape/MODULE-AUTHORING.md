# Module authoring (reshaped ship)

Status: draft corpus. This is the normative contract for building a
module. It is written to be read by a coding agent as its whole brief.
Everything mechanically enforceable here is also enforced by the compiler,
the import analyzer, the CI ownership diff, and the conformance harnesses
— this document is their prose face, and it must not drift from them.

## What a module is

A module is one capability that is not the kernel, not activation-records,
and not apps. It lives in one ownership directory and is wired in by one
composition file. Adding a module adds files; it edits nothing shared.

```
modules/<name>/
  definition.go        exported Definition (the only exported package surface)
  internal/…           implementation — compiler-hidden from every other module
  README.md            interface, invariants, state, dependencies, failures
builtin/<name>.go      the one composition file that registers the constructor
tests/fake-vps/<name>_smoke_test.go   self-registering smoke case (test-only)
```

You may add `modules/<name>/client` and `modules/<name>/host` packages
when import cycles or portability require it. You may not add anything
outside your ownership paths.

## The Definition

A module contributes an immutable `Definition`, assembled by a
constructor, carrying:

- **Identity** — a stable module id and its ownership paths.
- **Operations** — zero or more remote verbs. Each operation owns its
  fixed server path and exposure, a typed request schema, the required
  authorization class and how to extract its target, the helper handler,
  the client command adapter, its docs, and its expected error codes.
  This single definition is the authority the registry reads to generate
  the command catalogue, the sudoers grant, and the client renderer — so
  a verb is added in one place, not seven.
- **Resources** — desired host state backed by a primitive (a rendered
  config file, a systemd unit/timer, a sysusers/tmpfiles entry). Each
  resource owns observe/diff/apply and therefore yields a drift check for
  free.
- **Checks** — semantic health that cannot be derived from configuration
  (freshness, integrity, reachability).
- **Phase participation** — optional, narrowly typed. A module may
  participate in a named `apps` deploy phase (e.g. contribute environment
  variables, or copy data on preview create / purge on env destroy).
  There is no generic `Hook(event)`; participation is always a typed
  interface tied to a specific phase with a specific failure meaning.

A module returns only the parts it has. It does not implement a
seven-method interface full of empty slices.

## Rules the machine enforces

1. **Ownership.** A change may touch only your `modules/<name>/…`
   directory, your `builtin/<name>.go`, and your self-registering
   smoke/eval files. Touching anything else fails the CI ownership diff
   and needs an explicit architecture waiver. Waivers exist for genuine
   new kernel capabilities, manifest-grammar changes, and ADRs — not for
   convenience.
2. **No sibling imports.** Your implementation is under
   `modules/<name>/internal/…`; the compiler forbids another module from
   importing it. You may import the kernel and the typed capabilities it
   hands you. You may not import another module or the infrastructure
   adapters. A cross-module need is promoted to a kernel capability, not
   imported.
3. **No raw shell in domain modules.** You receive typed capabilities.
   Raw command execution is reserved for the infrastructure adapters
   (build, runtime, proxy, host, and future resource backends). If a
   domain module reaches for `exec`, shell strings become the real
   undocumented interface — the analyzer rejects it.
4. **Declare permissions; do not enforce them.** Every client-exposed
   operation carries its required authorization class. The kernel checks
   it before your handler runs. An operation missing authorization
   metadata is rejected at registry freeze.
5. **Render for primitives; retire cleanly.** Prefer a rendered config
   file + a systemd unit over Go supervision/scheduling. "Uninstall" does
   not exist (modules are compiled in); **resource retirement** does —
   stop, disable, delete owned files, daemon-reload, verify absence — for
   upgrades that drop a resource.

## Registration

Feature packages have no import side effects. The one composition file
`builtin/<name>.go` registers your constructor; a controlled `init()`
there collects it. The root sorts, validates, and **freezes** an
immutable registry at startup. The registry validates: no duplicate or
prefix-ambiguous command paths, every client operation has authorization
metadata, every ownership path is disjoint. Runtime- or config-driven
command registration is forbidden.

## Host converge order

Host resources converge in four fixed phases with deterministic id order
within each: **bootstrap → trust → runtime → schedules**. There is no
dependency solver and no cross-module ordering. If your module needs to
run after another module, that is a design smell — the shared prerequisite
belongs in a lower phase or in the kernel.

## Testing contract

- **Collocate** unit and invariant tests as `_test.go` beside the code.
- **Conformance harnesses** for a shared interface live with the
  interface owner (`kernel/contracttest`, `activationrecords/contracttest`)
  and export reusable test functions; both the real adapter and its fake
  invoke the same functions, so a fake can never drift from the contract.
- **Smoke/eval** cases self-register from your own files; you never edit
  the shared smoke or eval runner.
- Test infrastructure never appears in the production Definition
  interface.

## Definition of done for a module

Builds; `go test ./...` green (the import analyzer runs as a plain test,
so boundary violations fail here); its resources retire cleanly; its
operations carry authorization metadata; its docs and error codes
register; and the arc it lands in ends at or below its starting
production line count.
