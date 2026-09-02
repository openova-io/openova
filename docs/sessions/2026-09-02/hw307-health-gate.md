# hw307 health gate — 2026-09-02T21:25:00Z

Point-in-time stability record for `hw307.omani.works` (dep `9a1f230f320d7ff9`), taken read-only after the
evening train merged. Every line below is a command output, not a summary of one.

## The three gate checks

| check | region A (`me-east-215-a`) | region B (`me-east-215-b`) |
|---|---|---|
| `kubectl get nodes` | **5/5 Ready** — cp1 + 4 workers | **6/6 Ready** — cp1 + 5 workers |
| `kubectl get svc -A \| grep -i nodeport` | **no match** | **no match** |
| helm hook Jobs (`helm.sh/hook` annotated) | 2, failed 0, incomplete 0 | 2, failed 0, incomplete 0 |

Service inventory behind the NodePort result — the rule is zero `type: NodePort`, and the count is what
proves the query was not vacuous:

```
region A   ClusterIP 183   LoadBalancer 2   NodePort 0
region B   ClusterIP 155   LoadBalancer 2   NodePort 0
```

### One correction, recorded rather than hidden

The first pass of the hook audit reported `powerdns/powerdns-zone-bootstrap failed` in region B. **That was
a defect in the audit, not in the cluster.** A Kubernetes Job can carry `failed` and `succeeded`
simultaneously when a pod fails and a retry succeeds, and the script tested `failed` first:

```
succeeded=1  failed=1  completions=1  completionTime=2026-09-02T14:07:14Z
SuccessCriteriaMet=True   Complete=True
```

The Job completed. Any future hook audit must read `Complete` / `SuccessCriteriaMet`, never the raw
`failed` counter.

## Live surfaces at the same moment

```
console      200 200 200 200        registry   200 200 200 200
marketplace  200 200 200 200        harbor     301 301 301 301
grafana      302 302 302 302        auth       302 302 302 302
pdns-admin   302 302 302 302        newapi     200 200 200 200
chargeback   404 200 404 404 200 404 200 404 200 200      <- #6827, ~50% on one VIP
```

Four consecutive samples per host, because a single probe cannot distinguish 100% from 50% — the lesson
#6827 exists to record.

## Workload state

- **HelmReleases** — region A 64 Ready + 4 suspended-by-design; region B 60 Ready + 8 suspended-by-design.
- **org-services** — 14/14 Running 1/1, recovered at 21:2xZ after the `ferretdb/ferretdb:1.24` proxy-cache
  entry was repaired; the tier had been down since provisioning (#6803, third instance).
- **Application CRs** — 7, every one carrying a placement from the canonical enum (UAT G10).
- **Deployment records on the mothership** — 2: hw307 `ready`, hw306 `wiped`. One active environment.

## On "applying accumulated fixes from main"

Nothing to apply by hand, and applying by hand would be the defect. hw307 is **pre-cutover**, so its Flux
tracks `github.com/openova-io/openova` `main` directly and it upgrades itself: observed four times this
evening, umbrella `1.4.1634 → 1.4.1637 → 1.4.1641/1643 → 1.4.1647`, each landing as
`Helm upgrade succeeded for release catalyst-system/catalyst-platform`. Tonight's fixes reached it that
way — #6806 verified in the running console bundle, #6807 verified by calling the Sovereign's own reconcile
endpoint, #6820 verified by chargeback installing.

The cost of that automation is recorded in #6825: each self-upgrade takes the console and API 503 for 2–3
minutes, and the frequency tracks merge cadence.
