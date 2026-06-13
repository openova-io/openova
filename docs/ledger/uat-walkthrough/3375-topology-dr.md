## #3375 TOPOLOGY/DR

**Law:** every blueprint defines its DR perfectly, continuum executes it, agreed apps proven by region-kill. Source of truth = `docs/sessions/2026-06-02-per-blueprint-topology-audit.md` (promoted to `docs/topology-matrix.md`). Acceptance = a witnessed continuum-driven promote with zero data loss + DNS flip + lands-working on a **fresh 2-region zero-touch prov** — never kubectl surgery, never a wire-proof. **Precondition: the prov is genuinely 2-region AND the §6 cross-region machinery is overlay-ENABLED (all flags default-OFF; a single-region prov renders NONE of it = the PR #1599 anti-pattern).**

### Block A — Registry & schema convergence (DoD-1/2/3/4 + G115)
| # | Action | Do | Expect | Source | ☐ |
|---|---|---|---|---|---|
| A1-A4 | shell | `docs/topology-matrix.md` | ~90 rows, no blank class; 14 CLASS-B rows; 4 ROW-AMENDMENTS for founder | `docs/topology-matrix.md:57-187` | ☐ |
| A6 | shell | `go test ./core/controllers/internal/clusterregistry/...` | `CanonicalIDStrings()`=6 IDs; `Parse("mgmt-B")`→{mgmt,B} | `clusterregistry/registry.go:129-183` | ☐ |
| A7-A9 | shell | `KubeConfigSecretFor` wired + live resolve | seam populated by `clusterResolver().SecretFor`; same-region HR → real `vc-*` secret; cross-region split-side = no kubeConfig (IaC-pure) | `application_controller.go:842,925`; `registry.go:255-269` | ☐ |
| A10-A11 | console | App Detail → Topology tab | DR section renders for active-hotstandby. **GAP — `callerTier` not threaded `AppDetail.tsx:715-719`→TopologyTab→DRSection; live Switchover button shows disabled "Owner tier required".** | `TopologyTab.tsx:252`; `DRSection.tsx:118,130` | ☐ |
| A12 | browser | `grafana.<sov>` datasources + dashboards (G115) | non-empty (not the empty hw86 state) | `grafana/chart/templates/datasource-configmaps.yaml` | ☐ |

### Block B — cnpg-pair region-kill, CONTINUUM-DRIVEN (the §6.1 proven row)
| # | Action | Do | Expect (RTO/RPO) | Source | ☐ |
|---|---|---|---|---|---|
| B2 | shell | `kubectl get cluster <pair>-primary -o jsonpath='{.spec.postgresql.synchronous}'` | sync `remote_apply` (zero-tx-loss) | `primary-cluster.yaml:111-125` | ☐ |
| B4 | shell | `psql -c "SELECT sync_state FROM pg_stat_replication"` | one row `sync` BEFORE the kill | `primary-cluster.yaml:81-125` | ☐ |
| B8 | shell | seed canary table + write loop on `db.<org>.<sov>` | replicated to region-B; loop running | — | ☐ |
| B9-B10 | console | DR section → **Switchover…** → confirm dialog (7 steps) | POST `/continuums/{name}/switchover` (NOT kubectl); CR patched | `SwitchoverDialog.tsx:108-212`; `continuum.go:292,462-469` | ☐ |
| B11-B14 | console | watch StatusPanel steps 1-7 | validate-lease → cordon → drain-http → **flip-dns** (lua-record, region-B first) → swap-lease → uncordon (promote) → audit | `sequence.go:301-476`; `dns/lua.go:133-318` | ☐ |
| B15 | shell | MEASURE RTO (confirm→FailedOver+writes) | **<60s promote, <5s write disruption** (hw128 baseline 3s) | `ARCHITECTURE.md:874` | ☐ |
| B16 | shell | MEASURE RPO on new primary `SELECT count(*) FROM dr_canary` | **RPO=0 — every committed write present** | sync proof B2 | ☐ |
| B18 | client | write + read against `db.<org>.<sov>` post-flip | connects region-B, works (not just wire-proven) | acceptance bar | ☐ |

### Block C — rejoin without split-brain (DoD-9)
| C2-C3 | shell | bring region-A back; `psql -c "SELECT pg_is_in_recovery()"` | `t` (follower, not 2nd primary); exactly ONE lease holder | `sequence.go:404-441` | ☐ |

### Block D/E — gitea + openbao region-kill (DoD-7/8)
| # | Action | Expect | Source | ☐ |
|---|---|---|---|---|
| D2/D4 | shell | gitea blob mirror running (`weed filer.remote.sync`); blob present in BOTH regions before the kill | `objectstorage-mirror.yaml:39-108` | ☐ |
| D6 | git | post-kill: clone returns repo+blob, new push succeeds within RTO | DoD-7 | ☐ |
| E2/E5 | shell | openbao snapshot CronJobs (save/fetch to S3); **KV reads UNINTERRUPTED on replica during kill** | `snapshot-replication.yaml:135-299` | ☐ |
| E4 | shell | promote: `vault operator raft transition-to-primary` | **GAP candidate — promotion delegated to bp-continuum; if no wiring AND no runbook → NO MECHANISM FOUND.** | `snapshot-replication.yaml:26-28` | ☐ |

### Block F — console truth + evidence
| F1 | console | Topology tab post-walk shows converged declaration (active=region-B, Phase=FailedOver) | `StatusPanel.tsx:72,152-163` | ☐ |

**Gaps:** (1) console callerTier not threaded (Switchover disabled); (2) openbao promotion half possibly unwired; (3) switchover authz HTTP 200 not 403; (4) all cross-region machinery default-OFF — needs a genuine 2-region overlay-enabled prov.
