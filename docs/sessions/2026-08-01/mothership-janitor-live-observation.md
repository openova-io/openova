# Live mothership janitor observation (2026-08-01)

**Surface**: mothership `catalyst-api-7c558f4d98-89scx`, ns `catalyst`, via `~/.kube/config`
**Access**: READ-ONLY (`kubectl logs`, `kubectl exec … cat`). No mutations, no restarts, no patches.
**Node**: `vmi3116389` Ready, k3s v1.34.4+k3s1, 152d uptime.

## Finding A — #5545 CONFIRMED LIVE IN PRODUCTION (declared-vs-actual)

The janitor pass at `2026-08-01T07:29:52Z` emitted, per resource:

```
[JANITOR] sweep orphan OBS bucket catalyst-hw40-omani-works-4a2beaa4:
  would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)
```

…61 times, plus keypair equivalents — every one explicitly **log-only, nothing acted on**. The
pass summary for that same sweep reads:

```json
{"msg":"[JANITOR] pass complete","durationMs":7242,"destructive":false,
 "reaped":0,"reapErrors":0,"failedInfraPreserved":0,
 "orphanKubeconfigsDeleted":0,"orphanTofuWorkdirsDeleted":0,"orphanEIPsDeleted":0,
 "orphanKeypairsDeleted":2,"orphanVPCsDeleted":0,"orphanEVSDeleted":0,
 "orphanOBSBucketsDeleted":61}
```

`destructive: false` and every line says "would-reap", yet the summary reports
**`orphanOBSBucketsDeleted: 61`** and **`orphanKeypairsDeleted: 2`**. Nothing was deleted. The
summary keys assert 63 deletions that did not happen.

This is **issue #5545**, filed earlier this session from source reading, now confirmed on the live
production mothership. The fix (mode-dependent key naming — `orphan<X>WouldReap` when log-only,
`orphan<X>Deleted` only when destructive) is authored in
`products/catalyst/bootstrap/api/internal/handler/janitor.go` with a mode-sensitivity test in
`janitor_wouldreap_5545_test.go`, and is unmerged.

Operational significance: anyone reading this summary — a dashboard, an alert, an operator — would
conclude 63 orphaned cloud resources had been cleaned up. They are all still there. The janitor has
never run destructively (no `CATALYST_JANITOR_DESTRUCTIVE` env var is set on the Deployment).

## Finding B — UAT row R1 is VACUOUS on the current env, not "unobservable"

R1 asserts: *"catalyst-api orphan-sweep no longer reaps a `ready` Sovereign — denylist inversion
(`case "wiped": eligible; default: protect`) holds on the live env."*

Its recorded evidence says the janitor is *"not observable from child kubeconfig —
mothership-scope"*. **That reason is now stale.** The janitor is a mothership catalyst-api loop and
is directly observable from the mothership kubeconfig, which is what was used here.

The real blocker is different, and it is vacuity. The deployment registry currently holds:

```
/var/lib/catalyst/deployments/
  2c2d746b578c636b.json   → status: "wiped"
  org-tenant/  sme-tenant/  user-provision/     (templates, not deployments)
```

Exactly **one** deployment record, and its status is `wiped` — which under the denylist inversion
is the *eligible* case, not the protected one. There is **no `ready` Sovereign in the registry at
all**, so the sweep has nothing to protect and `reaped: 0` cannot distinguish "the denylist held"
from "there was nothing to evaluate".

Stamping R1 green on `reaped: 0` would be the consistent-with-≠-evidence-for error: the observation
is equally explained by the hypothesis being true and by the test being empty. R1 therefore stays
⛔ and needs a `ready` Sovereign — i.e. hw292 — to be testable at all.

The janitor also logs **no per-deployment protection decisions**. Only three message shapes appear
across 24h: `pass complete`, `sweep orphan <resource>`, and `sweep orphan OBS bucket`. Even with a
ready Sovereign present, a future walker could not confirm the denylist from logs alone — the
protect branch is silent. Adding a log line on the protect path would make R1 walkable; that is a
prerequisite worth noting for whoever fixes #5545, since it touches the same function.

## What was NOT concluded

- R1 not stamped ✅ (vacuous — see above).
- No claim that the denylist is broken. It may well be correct; this env cannot tell.
- No mutation of any kind was performed on the mothership.
