# 3648 — Topology is ONE canonical vocabulary, every app — UAT walk (web UI)

> **Ticket:** [#3648](https://github.com/openova-io/openova/issues/3648) · **Car:** T0 · **PR:** #3649 · **Train:** `train/hw150`
>
> **What this proves:** a Blueprint's `SupportedTopologies` is the single source of truth, offered
> identically in the **create wizard** AND the **AppDetail placement strip**, for **every** app — no
> dialect divergence, no header/strip contradiction, no `invalid-topology` error. Proven on three
> different Blueprints, not postgres alone.
>
> **Format law (founder, 2026-06-03):** every row is ONE UI action — *go to a URL → one click/type →
> one screen/state.* Routes/labels are quoted from the deployed console source
> (`products/catalyst/bootstrap/ui/src/`) with `file:line`. `grep`/`go test` are the Appendix, NOT
> acceptance.
>
> **Replace at walk time:** `<fqdn>` = hw150 FQDN (e.g. `hw150.omani.works`); `<JWT>` = the RS256
> handover token; `<region-a>`/`<region-b>` = the two region codes. Tick **☑** pass, **☒** fail.

**Sign-in (once).** Open `https://console.<fqdn>/auth/handover?token=<JWT>` → 302 to `/dashboard`,
signed in as **emrah.baysal** with NO login form (avatar **E** top-right).

---

## Section 1 — Create at a supported topology, for THREE different Blueprints (founder item #3)

**Proves:** the create flow canonicalises the editor dialect (`active-hotstandby → active-hot-standby`)
before validating against `SupportedTopologies`, so the selection is accepted for **every** stateful
app — the failure was platform-wide, not a postgres quirk. Route: catalog card → class page → **+ New
instance** (`AppDetail/InstancesSection.tsx:100`); the wizard topology `<select>` posts the canonical
value (`endpoint_handler.go` `chooseTopology`/`canonicalizeTopology`, #3649).

| Step | Go to (URL) | Do (click / type) | Expect (screen / state) | ☐ |
|---|---|---|---|---|
| 1.1 | `/catalog/bp-postgres` | **+ New instance** → Topology **active-hot-standby** → Org → ☑ `<region-a>` ☑ `<region-b>` → **Create** | install starts; **NO** `topology "active-hotstandby" not in supported [singleton active-hot-standby] (invalid-topology)` | ☐ |
| 1.2 | `/catalog/bp-grafana` | same (active-hot-standby, 2 regions) | install starts; no invalid-topology error | ☐ |
| 1.3 | `/catalog/bp-gitea` | same | install starts; no invalid-topology error | ☐ |
| 1.4 | `/catalog/bp-openbao` | **+ New instance** → Topology **active-passive** (its declared mode) → Create | accepted; openbao does NOT offer active-hot-standby (not in its `SupportedTopologies`) | ☐ |

## Section 2 — The AppDetail placement strip offers ONLY supported modes (founder item #2, raised 3×)

**Proves:** the strip (`widgets/topology/TopologyEditor.tsx`) is constrained to the Blueprint's declared
topologies (`supportedCanonical`, `:129-138`) which `AppDetail/TopologyTab.tsx:232-258` feeds in, so it
can never present a mode the header says is unsupported.

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/app/<a postgres instance>` → **Topology & DR** tab | read the header (`DeclaredTopologyPanel`) | header shows `Effective active-hot-standby · Supported active-hot-standby · singleton` | ☐ |
| 2.2 | (same tab) | inspect the **Change placement** strip below | it offers **only** `singleton` + `active-hot-standby`; **active-active is disabled/absent** — strip agrees with header (the 3×-reported contradiction is gone) | ☐ |
| 2.3 | `/app/<an openbao instance>` → Topology & DR | inspect the strip | offers only `singleton` + `active-passive`; no active-hot-standby/active-active | ☐ |

## Section 3 — Generality proof (the law holds with ZERO per-app code)

**Proves:** the behaviour is declaration-driven, not whittled per app.

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | `/catalog/<a Blueprint that declares active-active>` → New instance | open the topology select | **active-active is offered** (because declared) | ☐ |
| 3.2 | `/catalog/<a singleton-only Blueprint>` → New instance | open the topology select | only `singleton` is offered; the 2-region picker stays hidden | ☐ |
| 3.3 | repo (operator/reviewer) | `git grep -nE '"postgres"|active-hotstandby|single-region' core/ products/catalyst/bootstrap/api` on the create+validate path | no raw-dialect post, no app-name branch — every value flows through the canonicaliser | ☐ |

## Appendix — automated checks (NOT acceptance)

- `npx vitest run src/widgets/topology` → `TopologyEditor.test.tsx:56` "constrains the picker to the Blueprint supported topologies (#3648)" green (59 tests).
- `npx vitest run src/pages/sovereign/AppDetail` → `InstancesSection` create-flow tests green.
- backend: `endpoint_handler_test.go` `TestChooseTopology_CanonicalisesEditorDialect` green.
- `git grep` audit (DoD §9.7): every topology POST site routes through `canonicalizeTopology`/`canonicalizeMode`.
