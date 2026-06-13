## #3373 PLACEMENT

**Law:** placement is INSTANCE data (`Application.spec.placement`), set by DATA not a hand-coded slot; ONE render mechanism = the Flux `spec.kubeConfig.secretRef` pivot; host = absolute minimum; Git is the truth (a direct Git edit of `spec.placement` is as valid as a console write); the advanced selector is generic (rendered from the blueprint declaration). **Live re-home of a running stateful instance is excluded (ticket §8); the Git-field semantics are in scope (DoD-7).**

### DoD-1 — Schema + CR field + controller honor placement
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 1.1 | shell | inspect `spec.placement` in the CRD | dual-form field (string DR-posture OR `{vcluster,regions,clusters,mode}`); `x-kubernetes-preserve-unknown-fields` | `products/catalyst/chart/crds/application.yaml:121-159` | ☐ |
| 1.3 | shell | inspect `status.placement` | object `vcluster` enum `[host,mgmt,dmz,rtz]` + `source` (`instance\|blueprint-default\|...`) | `application.yaml:426-451` | ☐ |
| 1.4-1.5 | shell | controller resolution + validation | order instance>blueprint-default>legacy; vcluster ∉ allowedPlacements → `markFailed(ReasonInvalid,...)` | `application_controller.go:706-729` | ☐ |
| 1.7 | shell | renderer pivot | stamps `spec.kubeConfig.secretRef {name,key:config}` (the ONE mechanism) | `fanout.go:269-293` | ☐ |
| 1.8 | **AUTOMATED** | `go test ./controllers/application/... -run Placement -race` | green | `fanout_test.go` | ☐ |

### DoD-2 — Provisioning advanced view: vCluster/regions/clusters selectors
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 2.4 | console + marketplace | search for a generic placement selector bound to `allowedPlacements` | **NO UI FOUND — gap. Zero rows. The default-vs-advanced screenshots DoD-2 demands are unsatisfiable today.** | (none in `core/console/src`, `core/marketplace/src`) | ☐ |
| 2.5 | API | `GET /catalog/{bp}` | carries `defaultPlacement`+`allowedPlacements[]` — the data a selector NEEDS exists server-side; only the UI is missing | `catalyst-catalog/internal/source/source.go:148-154,212-231` | ☐ |

### DoD-3 — Live walk: spawn an instance with NON-default placement → lands in chosen vCluster
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 3.1 | Git (no UI exists) | author an Application CR, set `spec.placement.vcluster` to a non-default allowed tier, commit | Flux reconciles, no admission reject | `platform/openbao/blueprint.yaml:21-23` | ☐ |
| 3.3 | shell | `kubectl get hr <inst-hr> -n <hostNs> -o jsonpath='{.spec.kubeConfig.secretRef.name}'` | `vc-<tier>` | `fanout.go:269-293`; `placement.yaml:57-72` | ☐ |
| 3.4 | shell | resources land INSIDE the vCluster inner namespace | Deploy/STS in vCluster, not host | `22-loki.yaml:80-130` | ☐ |
| 3.5 | console app detail | look for "vCluster: <tier>" placement field | **NO UI FOUND — gap. AppDetail.svelte has no `status.placement` renderer (its "vCluster" copy is tenant boilerplate).** | `AppDetail.svelte:330,353` | ☐ |

### DoD-4 — §4 table executed; conformance audit asserts actual==declared, zero undeclared host workloads
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 4.1 | shell | `cat clusters/_template/bootstrap-kit/placement.yaml` | single source-of-truth table (vclusters map + slots with target+justification) | `placement.yaml:47-203` | ☐ |
| 4.2 | **AUTOMATED** | `python3 scripts/audit-placement-conformance.py rendered` | exit 0 | `audit-placement-conformance.py:68-94` | ☐ |
| 4.3 | shell | `...py live --kubeconfig <kc>` | per-HR ✓ + `zero undeclared host workloads` | `audit-placement-conformance.py:107-163` | ☐ |
| 4.4 | shell | each §4 target row in its vCluster | each `vc-<target-tier>`. **GAP — several still `vcluster: host` (keycloak REVERTED post-outage); audit migration-gap list names them.** | `placement.yaml:153-203` | ☐ |
| 4.6 | shell | `...py rendered --strict-target` | **FAILS by design until active==target for every slot.** | `audit-placement-conformance.py:91-93` | ☐ |

### DoD-5 — 3 coupling fixes, proven on one re-homed app (gitea/harbor with working HTTPRoute + secrets)
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 5.2 | shell | vCluster CRD-sync set | `sync.fromHost.customResourceDefinitions` registers httproutes + externalsecrets inside the vCluster | `bp-mgmt-vcluster/chart/values.yaml:299-313` | ☐ |
| 5.5 | browser | open the re-homed app's public URL | serves via host Gateway → backendRef alias → in-vCluster pod | `bp-mgmt-vcluster/chart/values.yaml:299-307` | ☐ |
| 5.6 | shell | **OSS Pro-gating limitation row** | OSS syncer only (no loft-sh Pro integrated networking); a route-serving failure here = the known Pro gap, recorded not regressed | `bp-mgmt-vcluster/chart/values.yaml:299-313` | ☐ |

### DoD-6/7 — zero hand-coded placement; Git-is-truth
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| 6.1 | shell | `python3 scripts/render-slot-placement.py check` | PASSED; a hand-edited placement field FAILS (placement is DATA) | `render-slot-placement.py:100-200,466-484` | ☐ |
| 7.1-7.3 | Gitea repo | edit `spec.placement.vcluster` directly in Git, commit, `flux reconcile` | instance re-homes to the new tier, `source: instance`; works with Catalyst shut down | `application_controller.go:706-723`; `fanout.go:269-293` | ☐ |
| 7.5 | console | console reflects the new placement | **NO UI FOUND — gap. Only the kubectl half of Git-is-truth is walkable.** | (none) | ☐ |

**Gaps:** (1) NO placement selector UI (DoD-2); (2) NO console placement display (DoD-3); (3) NO console-reflects-new-placement (DoD-7); (4) §4 not fully executed (keycloak host); (5) OSS Pro-gating limits route-bearing re-home.
