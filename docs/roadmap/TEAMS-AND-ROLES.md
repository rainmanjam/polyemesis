# Teams and roles

**Status:** proposed, not started. **Effort: 20–30 days.** Zero new Go modules.
**This is not a feature with a risk section. It is a security boundary being
retrofitted, with a feature attached.**

---

## The crux: there is no authorization to extend

`requireAuth` **authenticates and nothing authorizes**.

`internal/api/api.go:512` calls `authenticate(r)`, puts a `*principal` into the
context, and calls `next`. There is no permission argument, no decision, and no
deny path other than 401. The `principal` struct carries a username and an
optional API token and nothing else — its own comment says:

> Both routes lead to the same **single administrator**; what differs is how the
> claim was made.

One `r.Group` wraps essentially the entire API. And the data layer bakes the
assumption in too: `GetUser()` takes **no id** — it is
`SELECT … FROM users ORDER BY id LIMIT 1`. Every caller of that implicitly means
"the one account".

So this is not "add a role column". It is introducing an authorization layer
where none exists, across ~120 route registrations, in a system whose every
handler currently assumes it is serving the owner.

## What should NOT be built

Stating this first, because it determines everything else.

1. **No org / team / workspace / tenant table.** The **source already is the
   tenancy unit**. A container above it adds a join to every query and a
   migration to every table, to serve zero self-hosted users who have more than
   one org in one binary. The church A/V team is one install with one source;
   the agency is one install with five. Both are served by scoping to sources.
2. **No role hierarchy, no separation-of-duty constraints.** NIST frames these
   as independent optional layers; flat RBAC alone is standards-conformant.
3. **No custom roles, no permission-matrix editor.** Three roles, compiled in.
   A custom-role UI makes the permission set *data*, which means a migration
   every time a permission constant is added.
4. **No per-user overrides and no `is_admin` bool beside the role.** NIST names
   this exact pattern the group-vs-role loophole that makes access review
   impossible.
5. **No policy engine, no external IdP.** OWASP does prefer ABAC/ReBAC over bare
   RBAC — the design answers that by mapping roles to *named permissions* and
   doing ownership checks separately, not by importing an engine. An IdP is
   modules plus an internet dependency on a box that must work on a dark LAN.
6. **No email invitations, password reset, or self-service signup.** There is no
   mail transport in this binary and adding one is a subsystem.
7. **No per-object ACLs.** Scope is the source, full stop.
8. **Do not role-scope the public playout origin.** Its users are viewers with
   no account, deliberately.
9. **Do not put the role in the JWT** — it must be revocable without waiting for
   expiry.
10. **Do not use the word "session".** It already means a recording session
    here, and `db.requireSession` already collides by name with
    `api.requireSession`. The new concept keeps the name the codebase chose:
    **`principal`**.

## The model

```
users.role         TEXT NOT NULL DEFAULT 'owner'   -- owner | operator | viewer
user_sources(user_id, source_id)                   -- scope; empty for owner
```

| Role | Scope | Can |
|---|---|---|
| `owner` | install-wide | everything: users, roles, settings, listeners, TLS, platform credentials, tokens |
| `operator` | sources in `user_sources` | everything about those sources: destinations, renditions, go-live, expert args, chat, clips, jobs |
| `viewer` | sources in `user_sources` | read those sources. No writes at all |

**One role per user, not one role per (user, source).** Someone needing operator
on A and viewer on B gets operator on both, or two accounts. Per-pair roles is
the first step of role explosion, and the agency case does not need it.

Permissions are Go constants, not rows — `PermSourceRead`, `PermDestControl`,
`PermExpertWrite`, `PermInstallWrite`, `PermUserWrite`, and so on — with a
compiled `map[Role]map[Perm]bool` built at init and read-only thereafter.

Two flags ride on the permission rather than the handler: `Scoped` (evaluated
against a source id resolved per route) and `SessionOnly` (never reachable by
API token — token minting, for the reason already documented).

**Deny by default:** a route registered without naming a permission must fail to
compile or fail a test, not default to open.

## Corrections carried from verification

The adversarial pass refuted four claims. Two matter to implementation.

**The route-census test does not work as designed.** The design proposed
`chi.Walk` over the real router, asserting `walked == registered + allowlist`
with a literal count, as the backstop that catches an unguarded route. Checked
against the vendored `go-chi/chi/v5@v5.3.1` (`tree.go:842`), **`chi.Walk` emits
one row per `(method, pattern)`, not one per registration** — so the arithmetic
is wrong and the literal is meaningless. The intent is right and it is the most
valuable test in the plan; the mechanism has to be rebuilt. Derive the expected
set from the permission registry and diff it against the walk, rather than
comparing counts.

**The "zero new modules" claim is at risk.** The design's break-glass path
(`-grant-owner <username>`) prompts for a password on the terminal, and there is
no `golang.org/x/term` in `go.mod` or `go.sum`. Either read from stdin without
echo suppression — bad for a password — or accept one new module and say so.

Two more were citation errors rather than design errors: the NIST RBAC0–3
layering and the "archived project" banner were attributed to the wrong pages of
`csrc.nist.gov` (they sit on the FAQ and a sibling page respectively), and the
module count was given as 9 direct + 20 indirect = 29, where the file actually
has **9 direct + 18 indirect = 27**.

That last one surfaced a real documentation bug, independent of this feature:
**`docs/DEPENDENCIES.md` opens with "Eight, deliberately"** and lists eight
direct modules. There are nine — `datarhei/gosrt` was added during the one-port
work and never recorded there. `docs/MODULES.md` correctly says nine.

## Test plan

The house style is measurement, and for a security boundary the measurement is
**exhaustive coverage of the decision, not spot checks**.

- **The permission matrix, executed.** For every route × every role × in-scope
  and out-of-scope source, issue the request and record the status. Assert the
  full grid against a golden table. A new route with no entry fails the build.
  This is the test that makes the retrofit reviewable, and it is itself a
  substantial build.
- **Deny by default.** Every registered route must appear in the permission
  registry; the diff is the assertion.
- **Revocation latency, measured.** Change a user's role and assert their next
  request is refused — with a real clock, because the ordering of
  `IssuedAt < sessions_valid_from` at one-second granularity is a genuine race
  the verifier flagged. Measure it; do not assume it.
- **Token narrowing.** A token minted by an operator must never exceed that
  operator's rights, and must shrink when their role shrinks.
- **The break-glass path.** Assert it works on a running install and that it is
  audited.
- **Audit completeness**, stated honestly: the design claimed deny-decision rows
  would equal 403 responses *exactly*, while its own write path buffers. Assert
  what a buffered writer can actually guarantee.

## Risks

1. **~120 routes, and the review burden exceeds the typing burden.** A missed
   route is a privilege escalation, and it will not fail any existing test.
2. **Migration must not lock anyone out** of a running install. The existing
   account becomes `owner`; `CreateUser`'s refuse-to-run-twice rule is currently
   the security control on the unauthenticated `/setup` route and must keep
   working unchanged.
3. **Session invalidation on privilege change** is where retrofits usually leak.
4. **Scope resolution per route** — every scoped handler must resolve *which*
   source it is acting on before deciding, and some currently do not know.
5. **`ListAll`-style queries** must be renamed and every call site audited, or a
   viewer sees another tenant's destinations through a list endpoint.
6. **This lands on top of a threat model that says the opposite.**
   [SECURITY.md](../../SECURITY.md) currently states "There is one user" as a
   design decision. Shipping this rewrites the threat model, and the doc must
   move with the code, not after it.

## Effort

20–30 days: schema and store 3 · principal resolution and revocation 2 · the
guard and the ~120-route retrofit 4 · object scoping plus `ListAll` call-site
audit 3 · token narrowing 2 · audit sink 2 · UI 4 · migration and break-glass 2 ·
the test matrix harness 5 · docs 1.

The range is wide because this is a security boundary: the review burden is the
long pole, not the typing.

---

## See also

- [ROADMAP](README.md)
- [../../SECURITY.md](../../SECURITY.md) — the threat model this rewrites
- [../COMPARISON.md](../COMPARISON.md) — where teams/roles sits against restream.io
