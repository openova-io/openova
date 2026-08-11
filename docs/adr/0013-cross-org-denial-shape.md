# ADR-0013 — Cross-Organization denial shape: 403 on assertion, not-found on lookup

- **Status**: Accepted
- **Date**: 2026-08-11
- **Context issues**: [#6122](https://github.com/openova-io/openova/issues/6122), [#5522](https://github.com/openova-io/openova/issues/5522), [#3988](https://github.com/openova-io/openova/issues/3988)
- **UAT row**: 213
- **Supersedes**: nothing. Records a choice `de1cbec54` made in code without adjudicating it.

## Context

`bp-openova-mcp` refuses cross-Organization requests in two different shapes, and
until now nothing said whether that was a design or a drift.

Measured on hw293.omantel.biz (dep `a0077ba47e3720e5`), 2026-08-11, under a genuine
Org-scoped bearer (`tier: org-admin`, `org_id: hw293walkone`):

| call | cross-Org answer |
|---|---|
| `get_application("uatm4-agenity")` — an Application of `hw293walktwo` | `-32000` `application "uatm4-agenity" not found in organization "hw293walkone"` |
| `create_application(..., organization: "hw293walktwo")` | `-32003 forbidden`, `status: 403`, nothing created |

UAT row 213 asserted **403 (UI-parity)** for the read. Before `de1cbec54` (#5522)
it was 403: the Org branch fetched by name on the deployment-addressed seam and
rejected a foreign namespace with `ErrForbidden`. #5522 re-routed Org-context
reads onto the own-org seam `GET /api/v1/org/applications`, which is
namespace-confined server-side, and from inside that estate a foreign name is
simply absent. The contract changed; the row did not, and the in-code comments
went on claiming "exact parity" with the write path for a release.

Controls establishing that the refusal is real and not an artifact (same minutes,
same instance): the own-Org `get_application` succeeds; the same name under its
rightful owner's identity succeeds; the mirror direction
(`hw293walktwo` → `hw293walkone`) gives the same not-found; and the build
demonstrably *can* emit 403, because the cross-Org create does.

## Decision

**The code is right. The read stays not-found, the write stays 403, and the rule
that makes them coherent is written down here.**

The axis is **not** read-vs-write. It is **what the denial is a denial of**:

- **Deny-by-ASSERTION → `-32003` / 403.** The caller supplied the Organization
  in the request (`organization: hw293walktwo`). Refusing discloses nothing the
  caller had not already asserted, so the honest, actionable answer is "you may
  not act on the Organization you named". `create_application` is this case, and
  it refuses locally — no request reaches the catalyst-api.
- **Deny-by-LOOKUP → `-32000` / not found.** The caller supplied a *name* and the
  answer is the result of a search. Here the shape of the refusal **is** the
  disclosure. `get_application` is this case.

A future tool inherits its shape from which of the two it is, not from whether it
reads or writes.

## Why not restore 403 on the read

1. **It would be an existence oracle.** To answer 403 rather than not-found, the
   MCP must first establish that the Application exists in *some other*
   Organization. That is a Sovereign-wide probe the Org caller is not entitled to
   make — `orgSafePathPrefixes` in
   `products/catalyst/bootstrap/api/internal/handler/org_scope.go` is a
   deny-by-default allowlist whose entire purpose is to refuse it. The 403 would
   therefore be built on top of a capability #4110/#5522 removed on purpose, and
   it would answer, for any name an attacker cares to try, "does some other
   Organization on this Sovereign run an app called *Y*?" The measured symmetry in
   both directions today is precisely the property an oracle destroys.

2. **"UI-parity" — the clause's own justification — argues the same way once
   checked.** An Org-scoped console session's only Application surface is
   `/api/v1/org/applications`, the sole Application entry on that allowlist
   (`org_scope.go`, `/api/v1/org/apps`). The deployment-addressed
   `/api/v1/sovereigns/{id}/applications/{name}` seam is 403'd at the **path**,
   before any Application name is considered. So the console never emits a
   *resource-level* 403 about another Organization's Application — it simply does
   not have that Application in the only list it can read. The row's parenthetical
   was reading a path-level 403 as a resource-level one. Faithful UI-parity is
   not-found.

3. **Server-side confinement is a stronger guarantee than a client-side check.**
   The pre-#5522 403 came from the MCP filtering a Sovereign-wide response it had
   already received. The current seam never puts another Organization's rows in
   the response at all. Reintroducing the 403 means reintroducing the wider read
   in order to reject it — trading a structural guarantee for a filtered one.

The cost of this decision is honestly stated: an Org agent that mistypes a name
and an Org agent that reaches for a neighbour's Application get the same answer,
so the MCP cannot tell a user "that exists but is not yours". That is the point.
`create_application` retains the precise 403 for the case where precision is free.

## Consequences

- UAT row 213's assertion is corrected to the not-found contract, with the
  create-path 403 named as the cross-Org authorization signal. **The row's Result
  and Walk cells are untouched** — adjudication changes the assertion, never the
  verdict.
- `products/openova-mcp/internal/tools/catalogue.go` no longer claims "exact
  parity" between the two paths; it states this rule and points here.
- Pinned at the wire by
  `products/openova-mcp/cmd/openova-mcp/cross_org_denial_shape_213_test.go`,
  which asserts the numeric codes (`-32000` vs `-32003`) that row 213 is actually
  measured with, plus an own-Org control on the same transport and bearer.
  `TestGetApplicationOrgNameOutsideOwnEstateNotFound` additionally asserts the
  denial *class*: its former message-text-only assertion stayed green against a
  one-word change that moved the wire answer to 403.
- Any new Org-scoped MCP tool must classify itself as deny-by-assertion or
  deny-by-lookup and take that shape.
