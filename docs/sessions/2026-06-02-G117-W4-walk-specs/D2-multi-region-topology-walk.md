# G117 W4.D2 — Multi-region topology walk (auto-default + region-kill drill on fresh prov)

> **Goal**: Prove a fresh multi-region Sovereign auto-defaults to `multi-region` topology variants per locked decision #7 (`len(Sovereign.spec.regions) > 1`), and that region-kill triggers the active-hot-standby failover chain end-to-end.
>
> **Pillar**: 2 (multi-region BCP topology choice at signup) + 3 (two independent CNPG clusters + region-kill failover)
>
> **Cited memory**: `feedback_multiregion_hcs_mimic_violation_2026_05_28.md`, `feedback_six_pillars_target_state_no_workaround.md`

## Pre-flight env state

| # | State | How to verify |
|---|---|---|
| P-1 | Fresh prov via canonical `POST /sovereign/api/v1/deployments` with `regions: [{code: "fsn1-a"}, {code: "fsn1-b"}]` (or HCS equivalent `[{code: "me-east-215-a"}, {code: "me-east-215-b"}]` per memory pin — MUST be 2 codes, even if same physical AZ) | `kubectl get sovereign -o yaml \| yq '.spec.regions \| length'` returns `2` |
| P-2 | `Phase 1 b` complete: both regions' mgmt+rtz+dmz clusters Ready | `kubectl --kubeconfig=<each-region-cluster> get nodes` returns `Ready` on all |
| P-3 | `bp-cnpg-pair` reconciled on the cnpg pair across region A + region B | `kubectl get cluster.postgresql.cnpg.io -A` shows `<org>-pgpair-a` Primary in regionA and `<org>-pgpair-b` Standby in regionB |
| P-4 | Cilium ClusterMesh established | `cilium clustermesh status --context=regionA` shows regionB connected |
| P-5 | Tier-1 SSO walk D1 already PASSED on this prov (sanity baseline) | `docs/ledger/TRUST.md` shows VERIFIED-PASS for D1 on this `t<NN>` |

## Walk procedure

### Step 1 — Verify auto-default topology resolution

| # | Action | Expected | Probe |
|---|---|---|---|
| 1.1 | As sovereign-admin, navigate console catalog → grafana → click `+ New instance` | Dialog appears with topology picker; **pre-selected value MUST be `active-hot-standby`** (the multi-region default per Blueprint.spec.topology.defaults.multi-region) | Screenshot showing pre-selection |
| 1.2 | Inspect the resolved topology in the request payload | DevTools Network tab shows `POST /apps/instances` body with `topology: "active-hot-standby"` if user accepted the default OR with explicit override if changed | HAR |
| 1.3 | Submit with the default | Application CR created with `spec.topology: "active-hot-standby"` | `kubectl get application <id> -o yaml \| yq '.spec.topology'` returns `"active-hot-standby"` |
| 1.4 | Verify HR fan-out per W2.C1 deliverable | 2 HRs created: one on mgmt-A with `catalyst.openova.io/role=active`, one on mgmt-B with `role=passive` | `kubectl get hr -A -l catalyst.openova.io/app=<name>` shows 2 rows with distinct cluster annotations |
| 1.5 | Both HRs reconcile to Ready | Active first (must complete before passive starts per W2.C1 acceptance) | `kubectl get hr -A -l catalyst.openova.io/app=<name> --watch` |

### Step 2 — Verify multi-region default is overridable

| # | Action | Expected | Probe |
|---|---|---|---|
| 2.1 | Create a second grafana instance, explicitly pick `singleton` from the dropdown | Dialog accepts; warning banner appears: "Singleton topology means region-kill will lose this instance — confirm?" | Screenshot |
| 2.2 | Submit | Application CR with `spec.topology: "singleton"`; 1 HR on mgmt-A only | `kubectl get hr ... \| wc -l` returns 1 |
| 2.3 | Try to override with invalid value via direct curl | API returns 400 with `code: invalid-topology` | `curl -X POST ... -d '{"topology":"bogus"}' -w "%{http_code}"` returns `400` |

### Step 3 — Region-kill drill (the punchline)

| # | Action | Expected | Probe |
|---|---|---|---|
| 3.1 | Note current state: active grafana instance serving on `grafana.acme.t<NN>.omani.works` from mgmt-A. Curl baseline. | HTTP 200 from mgmt-A's Grafana pod | `curl -sf https://grafana.acme.t<NN>.omani.works/api/health -w "%{http_code}\n"` |
| 3.2 | Kill all nodes in region A: `for n in $(hcloud server list -o columns=name -l region=fsn1-a); do hcloud server poweroff $n; done` (or HCS equivalent) | All region-A Pods go NotReady within 30s | `kubectl --kubeconfig=<regionA> get nodes` returns timeout or NotReady |
| 3.3 | Continuum lease detects loss → flips DNS lua-record from regionA IP → regionB IP within ~60s | PowerDNS lua-record health-check fires; record flips | `dig +short grafana.acme.t<NN>.omani.works` returns regionB IP |
| 3.4 | bp-continuum issues CNPG `cnpg promote` on the regionB standby | regionB Postgres becomes Primary; WAL streaming flips direction | `kubectl --kubeconfig=<regionB> get cluster <org>-pgpair-b -o yaml \| yq '.status.currentPrimary'` returns regionB pod name |
| 3.5 | bp-continuum unsuspends the passive Grafana HR on mgmt-B; helm-controller reconciles it to active role | HR labels flip from `role=passive` → `role=active`; existing Grafana pod (already running) starts serving traffic | `kubectl get hr ... -o yaml \| yq '.metadata.labels."catalyst.openova.io/role"'` returns `active` |
| 3.6 | Curl Grafana from outside | HTTP 200 from mgmt-B's Grafana pod within ~90s of region-kill | `curl -sf https://grafana.acme.t<NN>.omani.works/api/health -w "%{http_code}\n"` returns `200` |
| 3.7 | RTO measured | < 2 minutes wall-clock from region-kill to first successful 200 on the new active | timestamp diff |
| 3.8 | Application.status.perCluster reflects new state | mgmt-A row `Status=Failed,Cluster=mgmt-A,Reason=ClusterUnreachable`; mgmt-B row `Status=Ready,Role=active` (was passive) | `kubectl get application <id> -o yaml \| yq '.status.perCluster'` |
| 3.9 | Tier-1 SSO still works | Click Launch on Grafana in Catalyst console; silent-SSO completes in <500ms via mgmt-B | Per D1 probes |

### Step 4 — Region restore + return-to-active

| # | Action | Expected | Probe |
|---|---|---|---|
| 4.1 | Power region-A nodes back on | Nodes Ready within 60s | `kubectl get nodes` returns Ready |
| 4.2 | bp-cnpg-pair starts replicating regionB → regionA (WAL streaming inverted) | regionA pod transitions Primary → Standby | `kubectl --kubeconfig=<regionA> get cluster <org>-pgpair-a -o yaml \| yq '.status.currentPrimary'` |
| 4.3 | Grafana HR on mgmt-A re-reconciles to `role=passive` (NOT auto-fail-back to active per active-hot-standby semantics) | mgmt-A HR Ready, role=passive | `kubectl get hr ... -o yaml \| yq '.metadata.labels."catalyst.openova.io/role"'` returns `passive` |
| 4.4 | Operator-initiated fail-back via console "Promote region A" button (G117.6 ships this UI affordance) | mgmt-A becomes active; mgmt-B becomes passive; DNS lua-record flips back | Same probes as Step 3 reversed |

## Self-verification curl probes

```bash
# Probe 1 — Sovereign reports 2 regions
kubectl get sovereign t<NN> -o jsonpath='{.spec.regions[*].code}'
# Expect: fsn1-a fsn1-b

# Probe 2 — Grafana Application has 2 HRs across regions
kubectl get hr -A -l catalyst.openova.io/app=acme-grafana \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.labels.catalyst\.openova\.io/role}{"\t"}{.spec.kubeConfig.secretRef.name}{"\n"}{end}'
# Expect 2 rows; one active mgmt-A; one passive mgmt-B

# Probe 3 — Pre-kill baseline (run before region-kill)
HOSTNAME=grafana.acme.t<NN>.omani.works
for i in $(seq 1 5); do
  curl -sf https://$HOSTNAME/api/health -w "$(date +%H:%M:%S) %{http_code} %{time_total}s\n"
  sleep 1
done

# Probe 4 — Kill region A
for n in $(hcloud server list -o columns=name -l region=fsn1-a -o noheader); do
  hcloud server poweroff $n &
done
wait
echo "region A killed at $(date +%H:%M:%S)"

# Probe 5 — Continuum DNS flip detection (poll dig for IP change)
INITIAL_IP=$(dig +short $HOSTNAME | head -1)
echo "initial IP: $INITIAL_IP"
while true; do
  CURR=$(dig +short $HOSTNAME | head -1)
  if [ "$CURR" != "$INITIAL_IP" ]; then
    echo "DNS flipped to $CURR at $(date +%H:%M:%S)"
    break
  fi
  sleep 5
done

# Probe 6 — First successful 200 after flip
while true; do
  CODE=$(curl -sf -o /dev/null -w "%{http_code}" https://$HOSTNAME/api/health 2>/dev/null || echo "fail")
  if [ "$CODE" = "200" ]; then
    echo "first 200 at $(date +%H:%M:%S)"
    break
  fi
  sleep 2
done

# Probe 7 — RTO compute (subtract Step 4 timestamp from Step 6 timestamp)
# Expect: <120s
```

## Evidence on TRUST.md format

```markdown
| 2026-06-0X | t<NN>.omani.works | Multi-region topology auto-default + region-kill drill | VERIFIED-PASS | g117-w4-d2 | Phase 1b 2 regions; auto-default=active-hot-standby; region-kill RTO <120s; SSO still works on standby | screenshots/g117-w4-d2-{pre-kill,dns-flip,post-flip,sso-on-standby}.png | <walker> |
```

## Expected HTTP codes summary

| Probe | Method | URL | Expected code |
|---|---|---|---|
| Topology dialog GET | GET | `/api/catalog/grafana` | 200 with `defaultTopology: "active-hot-standby"` |
| Create instance | POST | `/api/apps/instances` | 201 |
| Create instance with invalid topology | POST | `/api/apps/instances` (body has bogus topology) | 400 |
| Grafana health pre-kill | GET | `https://grafana.acme.t<NN>.../api/health` | 200 (from mgmt-A) |
| Grafana health during-kill | GET | same | 502/503 then 200 (after RTO) |
| Grafana health post-restore | GET | same | 200 |

## Screenshot capture points

| # | Filename pattern | Moment |
|---|---|---|
| 1 | `<date>-g117-w4-d2-topology-dialog-multiregion-default.png` | Step 1.1 |
| 2 | `<date>-g117-w4-d2-hr-fanout-2-clusters.png` | Step 1.4 (terminal showing 2 HRs across clusters) |
| 3 | `<date>-g117-w4-d2-pre-region-kill-baseline.png` | Step 3.1 (curl loop output) |
| 4 | `<date>-g117-w4-d2-dns-flip-detected.png` | Step 3.3 (dig output showing IP change) |
| 5 | `<date>-g117-w4-d2-post-kill-application-status.png` | Step 3.8 (`kubectl get application -o yaml \| yq .status`) |
| 6 | `<date>-g117-w4-d2-grafana-back-up-on-mgmt-b.png` | Step 3.6 (browser tab showing Grafana) |
| 7 | `<date>-g117-w4-d2-sso-still-works-post-failover.png` | Step 3.9 |
| 8 | `<date>-g117-w4-d2-region-a-restored.png` | Step 4.1 |

## Failure-mode triage

- **Pre-selected topology = singleton on multi-region prov**: locked decision #7 not honored — check `Sovereign.spec.regions` count detection in catalyst-api `GET /catalog/<bp>`
- **Only 1 HR created**: W2.C1 fan-out broken; check application-controller logs
- **DNS lua-record never flips**: PowerDNS health-check config missing — bp-powerdns may be missing the lua-record snippet from `bp-continuum`
- **CNPG promote doesn't fire**: continuum lease lost the lock — check `kubectl get lease -n catalyst-continuum`
- **RTO > 2min**: profile each step (DNS TTL, helm-controller wake interval, cnpg promote latency)
- **SSO breaks after failover**: per-realm broker URL hard-coded to mgmt-A — must use Sovereign FQDN per PR #2729
