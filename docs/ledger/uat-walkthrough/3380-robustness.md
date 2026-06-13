## #3380 ROBUSTNESS

**Goal:** the platform must not wound itself during provisioning, image rolls, wipes. 5 measured defects (A-E) + 3 new session findings (F-H), each with a **binary** verification event (PASS/FAIL, no interpretation). A section is done only when its event passes, never when its PR merges.

### A — Wipe must fail fast + report truthfully (#3140)
| A1-A5 | wipe a real env | `TofuDestroyed` honest (false → Errors[] + ProviderPurge is primary); `ProviderPurge["vpc_peerings"]` populated; peerings purged BEFORE VPC cascade (#3315); ≤6-pass loop → `ResidualOrphans==0`; **next wipe flips `wiped` AND AK/SK sweep returns 0 catalyst-* VPC/EIP/NAT/ECS (bastion excluded)** | `huawei/provider.go:292-352,923-933,1941-1996` | ☐ |

### B — Thin cloud-init: Flux owns everything after k3s (#3145, open)
| # | Event | Expect | Source | ☐ |
|---|---|---|---|---|
| B1 | `grep 'helm repo add cilium' cloudinit-control-plane.tftpl` | **GAP — imperative cilium/gw-CRD prelude STILL PRESENT (`:687`); fix removes it (Flux slot/dependsOn).** | — | ☐ |
| B2-B3 | PUT-kubeconfig early + IPv4-pin loop intact | 15-min budget respected; #3232 IPv4-pin survives any refactor | `phase1_watch.go:117-119`; `tftpl:663-687` | ☐ |
| B5 | fresh prov | cloud-init = k3s+PUT+flux-bootstrap only; cilium+gw-CRDs installed DECLARATIVELY by Flux | — | ☐ |

### C — Phase-1 watch + jobs survive mothership rolls (#3153)
| C1/C5 | roll catalyst-api mid-prov | `shouldResumePhase1` treats file-on-PVC as resumable; log `resuming phase 1 watch after pod restart`; jobs keep streaming (NOT hw124 "0 events at 40/56"); hw130 frozen rows flip terminal | `deployments.go:698-766` | ☐ |

### D — Kyverno-Enforce landmine inventory (#3268)
| # | Event | Expect | Source | ☐ |
|---|---|---|---|---|
| D1-D2 | `bash scripts/check-kyverno-proxy-images.sh` | exit 0; no raw `registry.k8s.io`/`docker.io` in bp-{mgmt,dmz,rtz}-vcluster | `check-kyverno-proxy-images.sh:102-228` | ☐ |
| D3 | bp-vllm images | **GAP — bp-vllm NOT in the render matrix; add to CHARTS** | `:102-106` | ☐ |
| D4 | hook Jobs carry `managed-by: flux` | sanctioned pass | `gitea sso-configure-oauth-job.yaml:38-79` | ☐ |
| D5 | sovereign-tls dependsOn scope | **GAP — no drift guard; manual diff (hw130 shared-PG stall delayed ALL TLS ~75min)** | `check-bootstrap-deps.sh` | ☐ |
| D7 | `kubectl get certificate powerdns-api-tls -n powerdns` | **GAP — Ready=False, diagnosis OPEN** | `11-powerdns.yaml` | ☐ |
| D8 | re-roll a previously-denied image | Scheduled/Running, no kyverno deny event | `kyverno-policies/chart/values.yaml:193-228` | ☐ |

### E — cilium-envoy xDS dead-leg on catalyst-api rolls (#3301)
| E2-E3 | stale-xDS detector + roll healthz | **GAP — NO detector found in-repo (Build item open); only the manual cp1 restart runbook exists.** Event: roll → 0 post-Ready 503, OR detector auto-heals <60s | DoD §E | ☐ |

### New session findings (folded in per DoD §5)
| # | Finding | Event | Expect | Source | ☐ |
|---|---|---|---|---|---|
| F | bp-cnpg slow-ghcr fetch wedge (#3409 area) | inspect cnpg/shared-PG slot retries | HR `timeout:15m` + install `retries:-1`/upgrade `retries:3` so a slow ghcr fetch retries not wedges | `16-cnpg.yaml:95-101`; `16a-...yaml:126-132` | ☐ |
| F3 | slot-drift guard | every `NN-*.yaml` in `kustomization.yaml` | `scripts/check-bootstrap-deps.sh` green | `kustomization.yaml:87` | ☐ |
| G | cloud-init kubeconfig-capture API false-negative | `GET /cloudinit-log` 404 truthfulness | **GAP — THIS SESSION: the kubeconfig WAS on the PVC (`/var/lib/catalyst/kubeconfigs/<id>.yaml`) but the API reported "not captured"/404, blinding the operator. The 404 must mean genuinely-absent, not a path false-negative.** | `cloudinit_log.go:152-160`; `kubeconfig` endpoint 409 | ☐ |
| H | SME→NATS cross-node datapath crashloop | publisher + NATS on different nodes | **GAP — THIS SESSION (hw133 root cause of the #3376 502): billing/domain/notification/tenant CrashLoop because `cp1 → worker:4222` times out (NATS healthy, NO netpol). The `platform/nats-jetstream/chart/templates/` dir has NO NetworkPolicy at all — add a cross-node client carve-out + verify the cross-node Cilium datapath.** | `platform/nats-jetstream/chart/templates/` (no netpol); graceful-degrade `sandbox_sessions_nats_test.go:149-156` | ☐ |

**Open Build items:** thin cloud-init (B1); bp-vllm proxy matrix (D3); sovereign-tls dependsOn guard (D5); powerdns-api-tls diagnosis (D7); envoy xDS detector (E2); cloud-init capture false-negative (G); NATS cross-node datapath/netpol (H).
