# G117 W4.D4 — Multi-instance walk (3 grafanas in one Environment, 3 distinct topologies, 3 isolated storages)

> **Goal**: Prove an Org can run multiple instances of the same Blueprint in one Environment, each with its own topology, namespace, persistent storage, dashboards, and endpoint — with no cross-instance contamination.
>
> **Pillar**: 1 (marketplace + onboarding for multi-instance) + 2 (multi-region topology per-instance choice)
>
> **Cited memory**: `feedback_chart_version_collision_serialize_or_rebase.md`, `feedback_six_pillars_target_state_no_workaround.md`

## Pre-flight env state

| # | State | How to verify |
|---|---|---|
| P-1 | Fresh prov (single-region acceptable; multi-region preferred for diversity of topology choices) | Standard checks |
| P-2 | Org `acme` exists with Environment `acme-prod`; D1 SSO walk PASSED | TRUST.md |
| P-3 | `Blueprint.spec.multiInstance.enabled: true` on bp-grafana (W2.C2 ships this default) | `kubectl get blueprint bp-grafana -o yaml \| yq '.spec.multiInstance'` returns `{enabled: true, maxPerOrg: 10, isolationLevel: "namespace"}` |
| P-4 | application-controller from W2.C1 + W2.C2 deployed | image SHA matches the merged-main PR for both slots |

## Walk procedure

### Step 1 — Create 3 grafana instances with 3 different topologies

| # | Action | Expected | Probe |
|---|---|---|---|
| 1.1 | Open catalog drill-down for grafana: `/catalog/grafana` | Shows `instanceCount: 0` initially | Screenshot |
| 1.2 | Click `+ New instance`. Name=`obs-prod`, topology=`singleton`. Submit. | Application CR created in namespace `acme`; HR placed on mgmt-A; PVC `obs-prod-storage` (5Gi default) created | `kubectl -n acme get application obs-prod` |
| 1.3 | Wait for Ready (<3min). | Application.status.state=Ready | `kubectl -n acme wait --for=condition=Ready application obs-prod --timeout=180s` |
| 1.4 | Click `+ New instance` again. Name=`obs-staging`, topology=`active-hot-standby` (multi-region) OR `singleton` if single-region prov. Submit. | Second Application CR; depending on topology either 1 or 2 HRs | `kubectl -n acme get applications` shows 2 rows |
| 1.5 | Wait for Ready. | Application.status.state=Ready | wait |
| 1.6 | Click `+ New instance` a third time. Name=`obs-experimental`, topology=`active-active` (if Blueprint supports it; if not, fallback `singleton`). Submit. | Third Application CR | `kubectl -n acme get applications` shows 3 rows |
| 1.7 | Verify catalog drill-down updates | `instanceCount: 3` reflected in the catalog page header | Screenshot of `/catalog/grafana` showing badge `3 instances` |

### Step 2 — Verify namespace + storage isolation

| # | Action | Expected | Probe |
|---|---|---|---|
| 2.1 | List namespaces for the Org | 3 distinct namespaces with the multi-instance naming template: `acme-obs-prod-1`, `acme-obs-staging-1`, `acme-obs-experimental-1` (or per W2.C2's chosen template) — each labeled `catalyst.openova.io/org=acme, app=<name>, instance=<id>` | `kubectl get ns -l catalyst.openova.io/org=acme` |
| 2.2 | List PVCs across the 3 namespaces | Each has its own PVC named per the Helm release; sizes match values overrides | `kubectl get pvc -A -l catalyst.openova.io/org=acme` |
| 2.3 | List Endpoints across the 3 instances | 3 distinct hostnames: `obs-prod.acme.t<NN>.omani.works`, `obs-staging.acme.t<NN>.omani.works`, `obs-experimental.acme.t<NN>.omani.works` | `kubectl get applications -A -o yaml \| yq '.items[].status.endpoints[].hostname'` |
| 2.4 | Curl each Endpoint | All 3 return 200 from their respective Grafana pods (distinct underlying state) | `for url in <3 URLs>; do curl -sf $url/api/health -w "$url: %{http_code}\n"; done` |

### Step 3 — Verify dashboards diverge

| # | Action | Expected | Probe |
|---|---|---|---|
| 3.1 | Open `obs-prod` Grafana via Launch button. Create a new dashboard named `Production-Metrics`. | Saved in obs-prod's DB only | Grafana UI screenshot |
| 3.2 | Open `obs-staging` Grafana via Launch button. List dashboards. | `Production-Metrics` is NOT visible (correct — isolated storage) | Grafana UI screenshot showing empty dashboards |
| 3.3 | Open `obs-experimental` Grafana via Launch button. List dashboards. | `Production-Metrics` is NOT visible | Grafana UI screenshot |
| 3.4 | Verify per-instance Grafana DB on the PVC level | `kubectl exec -n acme-obs-prod-1 deploy/grafana -- ls /var/lib/grafana/grafana.db` exists with size > 0; same for the other two with DIFFERENT modification times | exec output |

### Step 4 — Verify Ingress + cert isolation

| # | Action | Expected | Probe |
|---|---|---|---|
| 4.1 | List HTTPRoutes for the Org | 3 distinct HTTPRoutes, one per instance, with non-overlapping hostnames | `kubectl get httproutes -A -l catalyst.openova.io/org=acme` |
| 4.2 | List Certificates | 3 distinct Certificate CRs; each Ready=True | `kubectl get certificates -A -l catalyst.openova.io/org=acme` |
| 4.3 | TLS verify per-instance | Each cert presents the correct SAN | `for url in <3>; do openssl s_client -connect <host>:443 -servername <host> </dev/null 2>/dev/null \| openssl x509 -noout -text \| grep -A1 "Subject Alt"; done` |

### Step 5 — Verify multi-instance gate (negative test)

| # | Action | Expected | Probe |
|---|---|---|---|
| 5.1 | Try to create a 4th instance with name `obs-prod` (collision with existing) | API returns 409 `Error{code: "name-collision"}`; UI surfaces clearly | Toast/screenshot |
| 5.2 | Set `Blueprint.spec.multiInstance.maxPerOrg: 3` via Sovereign-admin. Try to create a 4th with unique name. | 409 `Error{code: "max-per-org-exceeded"}` | curl response |
| 5.3 | Restore `maxPerOrg: 10`. Disable `multiInstance.enabled: false`. Try to create a 4th. | 409 `Error{code: "multi-instance-disabled"}` per OpenAPI contract | curl response |
| 5.4 | Re-enable `multiInstance.enabled: true`. Confirm 4th can now be created. | 201 | curl response |

### Step 6 — Verify per-Org realm SSO works for each instance

| # | Action | Expected | Probe |
|---|---|---|---|
| 6.1 | If W2.C4 deployed (per-Org realm Tier-3): each instance's Endpoint redirect_uri MUST be registered in the per-Org realm `acme`'s Grafana client | `curl -sf -H "Authorization: Bearer $KC_ADMIN" .../admin/realms/acme/clients?clientId=grafana \| jq '.[0].redirectUris'` includes ALL 3 URIs | Otherwise, instances share `sovereign` realm Grafana client (test that all 3 redirect_uris are in there) |
| 6.2 | Sign in via SSO at obs-prod | Silent SSO works | D1 probes per URL |
| 6.3 | Sign in via SSO at obs-staging | Silent SSO works | same |
| 6.4 | Sign in via SSO at obs-experimental | Silent SSO works | same |

### Step 7 — Delete one instance; verify isolation under teardown

| # | Action | Expected | Probe |
|---|---|---|---|
| 7.1 | Click Delete on `obs-experimental` in console | Confirmation dialog; on confirm: cascade uninstall fires | Screenshot |
| 7.2 | Wait for cleanup (<2min). | All HRs for `obs-experimental` removed; namespace pruned (if isolation=namespace); KC client redirect_uri pruned; cert revoked + Certificate deleted; HTTPRoute deleted; PowerDNS A record deleted | `kubectl get ns acme-obs-experimental-1` returns NotFound after the prune; same for HR/cert/HTTPRoute |
| 7.3 | Curl the deleted hostname | NXDOMAIN or 404 | dig + curl |
| 7.4 | Verify the OTHER two instances unaffected | `obs-prod` + `obs-staging` still serve 200; their dashboards intact | re-run Step 3 verification |

## Self-verification curl probes

```bash
# Probe 1 — Create 3 instances via API
ORG_TOKEN=$(curl -sf -H "..." | jq -r .token)
for i in 1 2 3; do
  case $i in
    1) NAME=obs-prod TOP=singleton ;;
    2) NAME=obs-staging TOP=active-hot-standby ;;
    3) NAME=obs-experimental TOP=active-active ;;
  esac
  curl -X POST -H "Authorization: Bearer $ORG_TOKEN" \
    -d "{\"blueprint\":\"grafana\",\"org\":\"acme\",\"name\":\"$NAME\",\"topology\":\"$TOP\"}" \
    https://api.t<NN>.omani.works/catalyst/v1/apps/instances \
    -w "$NAME: %{http_code}\n"
done
# Expect: 3× HTTP 201

# Probe 2 — Confirm 3 distinct namespaces with correct labels
kubectl get ns -l catalyst.openova.io/org=acme -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.labels.catalyst\.openova\.io/instance}{"\n"}{end}'
# Expect: 3 rows with 3 distinct instance IDs

# Probe 3 — Confirm 3 distinct Grafana DB files
for ns in $(kubectl get ns -l catalyst.openova.io/org=acme -o name | cut -d/ -f2); do
  kubectl exec -n $ns deploy/grafana -- stat -c "%n %s %y" /var/lib/grafana/grafana.db 2>/dev/null
done
# Expect: 3 distinct outputs

# Probe 4 — Cross-instance dashboard isolation (HTTP API check)
for ns in <3 NS>; do
  URL=$(kubectl get application -n acme -l catalyst.openova.io/instance=$ns -o jsonpath='{.items[0].status.endpoints[0].hostname}')
  curl -sf -H "Authorization: Bearer $GRAFANA_KEY_$ns" https://$URL/api/search?type=dash-db | jq 'length'
done
# Expect: instance 1 → 1 (Production-Metrics); instance 2 → 0; instance 3 → 0

# Probe 5 — Negative test: max-per-org gate
kubectl patch blueprint bp-grafana --type=merge -p '{"spec":{"multiInstance":{"maxPerOrg":3}}}'
curl -X POST -H "Authorization: Bearer $ORG_TOKEN" \
  -d '{"blueprint":"grafana","org":"acme","name":"obs-over-limit","topology":"singleton"}' \
  https://api.t<NN>.omani.works/catalyst/v1/apps/instances \
  -w "\nHTTP %{http_code}\n"
# Expect: HTTP 409 with body code: max-per-org-exceeded

# Probe 6 — Delete one instance
APP_ID=$(kubectl get application -n acme obs-experimental -o jsonpath='{.metadata.uid}')
curl -X DELETE -H "Authorization: Bearer $ORG_TOKEN" \
  https://api.t<NN>.omani.works/catalyst/v1/apps/$APP_ID \
  -w "HTTP %{http_code}\n"
# Expect: HTTP 202

# Probe 7 — Confirm cleanup
sleep 120
kubectl get ns acme-obs-experimental-1 2>&1
# Expect: Error from server (NotFound)
kubectl get certificate -A -l catalyst.openova.io/app=obs-experimental 2>&1
# Expect: No resources found
dig +short obs-experimental.acme.t<NN>.omani.works
# Expect: empty
```

## Evidence on TRUST.md format

```markdown
| 2026-06-0X | t<NN>.omani.works | 3-instance Grafana per Environment with isolated storage + dashboards | VERIFIED-PASS | g117-w4-d4 | 3 namespaces, 3 PVCs, 3 distinct dashboards, 3 certs, 3 endpoints, 3 SSO redirects; gates 409 on name-collision/maxPerOrg/multiInstance-disabled | screenshots/g117-w4-d4-{catalog-3-instances,namespace-list,dashboard-isolation-{1,2,3},delete-cleanup}.png | <walker> |
```

## Expected HTTP codes summary

| Probe | Method | URL | Expected code |
|---|---|---|---|
| Create instance | POST | `/api/apps/instances` × 3 | 201 each |
| Catalog drill-down post-create | GET | `/api/catalog/grafana` | 200 with `instanceCount: 3` |
| Name collision | POST | same with existing name | 409 |
| MaxPerOrg exceeded | POST | 4th when max=3 | 409 |
| MultiInstance disabled | POST | when enabled=false | 409 |
| Delete instance | DELETE | `/api/apps/<id>` | 202 |
| Per-instance health | GET | each Endpoint `/api/health` | 200 |

## Screenshot capture points

| # | Filename pattern | Moment |
|---|---|---|
| 1 | `<date>-g117-w4-d4-catalog-drill-3-instances.png` | Step 1.7 |
| 2 | `<date>-g117-w4-d4-namespace-list-3-distinct.png` | Step 2.1 |
| 3 | `<date>-g117-w4-d4-grafana-obs-prod-dashboard.png` | Step 3.1 |
| 4 | `<date>-g117-w4-d4-grafana-obs-staging-no-dashboard.png` | Step 3.2 |
| 5 | `<date>-g117-w4-d4-grafana-obs-experimental-no-dashboard.png` | Step 3.3 |
| 6 | `<date>-g117-w4-d4-name-collision-409.png` | Step 5.1 |
| 7 | `<date>-g117-w4-d4-max-per-org-409.png` | Step 5.2 |
| 8 | `<date>-g117-w4-d4-multi-instance-disabled-409.png` | Step 5.3 |
| 9 | `<date>-g117-w4-d4-delete-cleanup-other-two-unaffected.png` | Step 7.4 |

## Failure-mode triage

- **Catalog drill-down shows wrong instanceCount**: catalyst-api `GET /catalog/{bp}` not querying live Application CRs; check W2.C2
- **Namespaces overlap**: namingTemplate broken; check W2.C2 admission code
- **Dashboards leak between instances**: PVC binding wrong; check Helm release values vs PVC selector
- **409 not returned on collision**: admission webhook not wired or gate logic flawed
- **Delete leaves orphan namespace/cert/HTTPRoute**: organization-controller finalizer missing pieces
- **SSO works for instance 1 but not 2-3**: KC client redirect_uri only includes first; check bp-sso-bridge
