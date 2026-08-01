# Promotion spec — the 8 REPO-class ⚠️ rows (2026-08-01)

Eight UAT rows were walked this session and stamped **⚠️ partial**, not ✅. Each one has a proven
half and an unproven half, and each already carries a `NOT PROVEN HERE:` clause naming exactly what
is missing.

This file turns those clauses into **the command that promotes each row**, so the walk after hw292
is mechanical rather than exploratory. Nobody should have to re-derive what "finish R6" means.

## Why they are ⚠️ and not ✅

Every row asserts a **conjunction**: a static property AND a runtime outcome.

| row | proven now (static) | unproven (runtime) |
|---|---|---|
| R3 | `plane-isolation/values.yaml:131` admits `sso-bridge` into openbao's ingress allow-list | the dial actually succeeds |
| R4 | `sso-bridge/networkpolicy.yaml:13-14` permits egress to keycloak **and** openbao, both ports | the reconciler tick actually reaches both |
| R6 | `postgres/networkpolicy.yaml` admits all four declared consumer classes | an app dial and the CNPG operator probe succeed |
| R8 | sync-Job emits `username`+`password`; GitRepository attaches `secretRef` | source-controller completes a clone |
| R9 | producer writes `sso/powerdns-admin`; consumer reads the same key into `OIDC_OAUTH_SECRET` | the OIDC round-trip completes |
| R10 | org-controller ClusterRole carries `update`+`patch` on `organizations` | per-Org provisioning reaches Ready |
| R14 | `HandleListOrganizations` merges store **and** live CRs | a live directory returns CR-backed Orgs |
| R18 | `decidePubkeyPublish` returns `preserveInjected` on a Sovereign (5 branches tested) | a live reconcile leaves the Secret untouched across restarts |

Marking any of these ✅ today would assert runtime behaviour nobody has observed — the
declared-vs-actual defect filed four times this session (#5542, #5545, #5515, #5558).

## The promotion command per row

Run on a converged Sovereign. Each promotes its row to ✅ **only** on the stated pass condition;
anything else is ❌ with the output pasted into the evidence cell.

```bash
# R3 — sso-bridge reaches openbao through the default-deny
kubectl -n sso-bridge exec deploy/sso-bridge -- \
  wget -qO- --timeout=5 http://openbao.openbao.svc:8200/v1/sys/health
# PASS: HTTP 200/429/473 body returned.  FAIL: timeout or "Policy denied".

# R4 — the reconciler's own egress reaches BOTH admin endpoints
kubectl -n sso-bridge exec deploy/sso-bridge -- sh -c '
  wget -qO- --timeout=5 http://keycloak.keycloak.svc:8080/realms/master >/dev/null && echo KC-OK;
  wget -qO- --timeout=5 http://openbao.openbao.svc:8200/v1/sys/health   >/dev/null && echo BAO-OK'
# PASS: both KC-OK and BAO-OK. Checking only one proves nothing — #4437 was a
# single-port gap that looked fine from the other side.

# R6 — a real consumer dial AND the operator probe
kubectl -n keycloak exec deploy/keycloak -- \
  sh -c 'nc -zv shared-pg-rw.shared-data.svc 5432'
kubectl -n cnpg-system logs deploy/cnpg-controller-manager --tail=50 | grep -i "shared-pg"
# PASS: nc succeeds AND no "Instance Status Extraction Error" for shared-pg.

# R8 — source-controller actually clones with the seeded credentials
kubectl -n flux-system get gitrepository openova-catalog-sovereign \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
# PASS: Ready=True with an artifact revision. FAIL: 401 / "authentication required".

# R9 — the PDA OIDC round-trip completes
#   browser: https://powerdns-admin.<sovereign-fqdn>/ -> sign in -> lands authenticated
kubectl -n powerdns-admin get secret pda-sso-oidc-credentials \
  -o jsonpath='{.data.OIDC_OAUTH_SECRET}' | base64 -d | wc -c
# PASS: non-zero length AND the browser lands signed-in (both halves, per #5416 —
# a populated Secret with a broken round-trip is the failure mode).

# R10 — per-Org provisioning reaches Ready
kubectl get organizations -A \
  -o custom-columns=NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status
# PASS: at least one Organization Ready=True (was create-only, never reached Ready).

# R14 — the live directory returns CR-backed Orgs
curl -s -H "Cookie: <owner-session>" https://console.<fqdn>/api/v1/organizations | jq '.items|length'
kubectl get organizations -A --no-headers | wc -l
# PASS: the API count is >= the CR count. A CRD-absent cluster degrades to
# store-only with only a Warn log (organization_provisioning.go:820-825), so the
# comparison — not the API alone — is the assertion.

# R18 — the injected handover key survives a reconcile
kubectl -n catalyst get secret catalyst-handover-jwt-public -o jsonpath='{.data.public\.jwk}' | sha256sum
kubectl -n catalyst rollout restart deploy/catalyst-api && kubectl -n catalyst rollout status deploy/catalyst-api
kubectl -n catalyst get secret catalyst-handover-jwt-public -o jsonpath='{.data.public\.jwk}' | sha256sum
# PASS: the two hashes MATCH. A changed hash means the local signer clobbered the
# mothership-injected key and every inbound owner-handover JWT will 401 (#4450).
```

## Ordering

R8 and R10 first — a Sovereign that cannot clone (R8) or reach Ready (R10) blocks the rest. R3/R4
next (they gate SSO). R6, R9, R14, R18 are independent afterwards.

## Blocked by, not by effort

Every command above needs a converged Sovereign. As of 2026-08-01 there is none: hw291 is wiped,
hw292 unfired, and the mothership `catalyst` namespace sits at zero pods with the console 503 and
zero Catalyst CRDs (three readings across ~3h; see
`docs/sessions/2026-08-01/mothership-catalyst-scaled-to-zero.md` and
`scripts/check-uat-walkability.sh`).
