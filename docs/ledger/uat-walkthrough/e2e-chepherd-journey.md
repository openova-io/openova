# UAT walkthrough — e2e Chepherd north-star journey (UAT.md rows 216-223 + MCP 212-213)

**Ticket:** #3988 (+ #3376 funnel, #3374 SSO, #4007 openova-mcp).
**What this proves:** the founder's north-star end-to-end — *a stranger with a coupon stands up
their own Org, logs in passwordless, installs chepherd as an Application, and chepherd's solo
agent creates more Applications for them via the RBAC-scoped openova MCP.*
**Gated on:** a fresh prov converged past storage (cycle-2c hw177) + `bp-agenity` in the catalog
(#4011) + `openova-mcp` deployed (on main) + the agent↔MCP wiring. Run AFTER the cycle-2c
foundation walk (see `../../sessions/2026-06-21/cycle2c-walk-runbook.md`).

A step flips its UAT.md row `✅` ONLY with a pasted live screenshot under
`docs/sessions/<date>/walk-<env>/evidence/`. No premature ✅ off an agent report or temp roll.

---

## A. Onboarding (rows 216-217)

**☐ A.1 (R216) — coupon → Org.** Open `https://marketplace.<fqdn>/redeem/?code=<CODE>` as a
stranger (no session). Redeem → pick plan/region → Launch. PASS = the provisioning timeline runs
to Done (Creating Org → manifests → vCluster → app → TLS → Health), and the Org appears ACTIVE in
`/organizations` backed by a real `vcluster` isolation (NOT a fake-green; the vCluster pods exist).
- Evidence: redeem screen + the timeline Done + the Org row ACTIVE.

**☐ A.2 (R217) — passwordless customer-user PIN login.** As the customer (e.g. `demo@openova.io`,
ANY domain — this is NOT the owner handover), hit the per-Org console → enter email → a 6-digit PIN
is emailed → enter it → lands signed-in, NO password field. PASS = signed-in as the customer with
their email in the avatar, their Org in the header.
- Evidence: PIN entry screen + the signed-in landing. (Verify the PIN was really emailed —
  IMAP `mail.openova.io:993`, per `reference_passwordless_pin_login_secret`.)

## B. chepherd as an Application (rows 218-220)

**☐ B.1 (R218) — installable from the catalog.** Per-Org console → Catalog → `bp-agenity` card is
present and installable into the Org's Environment. PASS = the card renders + the install wizard opens.

**☐ B.2 (R219) — provision → converges.** Install bp-agenity → its HelmRelease deploys, pods
Ready, the chepherd console is reachable at its per-Org FQDN. PASS = chepherd app card Running/Healthy
+ `Open` resolves to the live chepherd console (not 404/502/cert error).

**☐ B.3 (R220) — solo agent pre-configured.** Open chepherd → the solo agent is live, configured
with Claude Opus 4.7 + a working Anthropic token (the `externalsecret-anthropic` the chart wires).
PASS = the agent chat accepts input and responds (a trivial prompt round-trips).

## C. Chat-driven app creation via the openova MCP (rows 221-223, 212-213)

**☐ C.1 (R221) — chat → create.** In chepherd's agent chat: "create a <blueprint> application in my
org". The agent calls the openova MCP `create_application` tool → a new Application CR is created in
the CUSTOMER's Org. PASS = the agent reports success + an Application appears provisioning in the Org.

**☐ C.2 (R222) — converges + appears.** The agent-created Application converges (HR Ready, pods up)
and appears in the user's `/apps`. PASS = the new app card Running/Healthy in the Org, chat-driven
end-to-end (no operator console action).

**☐ C.3 (R223 + R212 + R213) — RBAC-scoped to the Org.** The agent's MCP token is Org-scoped:
- `list_applications` (R212) returns ONLY this Org's apps (a 2nd Org's apps are filtered out).
- cross-Org `get_application` (R213) → **403** (UI-parity).
- The agent CANNOT create/read/mutate in any other Org.
PASS = the agent, asked to touch another Org, is refused 403; `list_applications` shows only this
Org's set. (Verify against `openova-mcp/internal/identity` Org-claim enforcement, not just the chat.)

---

## Acceptance summary
The journey PASSES only when A.1→C.3 are each green on the SAME live customer Org on a converged
fresh prov, with pasted screenshots. Any step that needs an operator-console action to advance =
FAIL (the contract is zero-touch for the customer + chat-driven for app creation).
