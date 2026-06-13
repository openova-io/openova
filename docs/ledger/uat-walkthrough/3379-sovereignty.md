## #3379 SOVEREIGNTY

**Acceptance:** on a freshly **handed-over** Sovereign, the cutover engine auto-fires, all steps reach SUCCEEDED, the **10-minute deny-egress hold holds green**, `cutoverComplete=true` is stamped, and a fresh pod pulls from local Harbor while egress is still denied.

> **⚠ Three load-bearing realities:**
> 1. **11 steps, not the ADR's 8** (status ConfigMap hard-codes `totalSteps:"11"`, `09-cutover-status-configmap.yaml:46`).
> 2. **The "deny-egress NetworkPolicy" is a CIDR block, not FQDN** (Cilium 1.16 can't FQDN-egressDeny). Step 08 resolves github.com/ghcr.io/harbor.openova.io/xpkg.upbound.io → /32s + applies `CiliumClusterwideNetworkPolicy egressDeny: toCIDRSet` — ONLY when `egressTest.enforceCIDRBlock: true` (the overlay sets it; **chart default false = denies nothing = invalid proof**).
> 3. **`cutoverComplete=true` is set ONLY after all 11 steps succeed** (`cutover.go:1085-1092`; failure returns at `:1064-1082`).

### A. Pre-cutover dormant + 425 gate
| A1-A7 | kubectl/API | HR Ready (disableWait), 11 step ConfigMaps, status seeded `cutoverComplete:false`; registries v1 (mothership); Flux source still GitHub; **bootstrap auto-trigger gets HTTP 425 `handover-incomplete`** (archive not sealed); marker absent | `06a-...yaml:21-23`; `cutover_internal.go:442-453,166-178` | ☐ |

### B. Handover → engine auto-fires (gate skipped)
| B1-B3 | API | mother POSTs phase-0 archive → child seals `secret/catalyst/tofu-phase0-archive` → `fireCutoverEngine(...,"handover")` skips 425 → `cutoverStartedAt` set | `handover.go:368,406,425`; `cutover.go:1015-1019` | ☐ |

### C. The 11 tether-pivot steps (engine sorts by cutover-order)
| step | TETHER | Expect `step.<name>.result=success` | Source | ☐ |
|---|---|---|---|---|
| 01 gitea-mirror | git github→local Gitea | push OK + mirror-resync SEVERED (CronJob deleted, stays gone) | `01-...yaml:32,195-253` | ☐ |
| 02-03 harbor | local Harbor proxy projects + prewarm | projects exist on `harbor-core.harbor` | `02/03-...yaml` | ☐ |
| 04 registry-pivot | containerd mothership→local Harbor on every node | `registries.yaml`=v2 all nodes | `04-...daemonset.yaml:13-272` | ☐ |
| 05 flux-gitrepository | GitRepository → local Gitea | `gitrepository.openova.url`=local | `05-...yaml:21,56-71` | ☐ |
| 06 helmrepository-patches | 38 OCI HRs ghcr→local; **STRIPS ghcr.io/harbor.openova.io/xpkg auth from ghcr-pull** | all HR URLs local; `ghcr-pull` keys `registry.<fqdn>` only | `06-...yaml:228-296,356-464` | ☐ |
| 07 catalyst-api-env | gitops repo + PIN/JWT issuers → local; FATAL if `consolePublicURL`=example.local | no example.local residue (guards #3039 half-pivot) | `07-...yaml:225-270` | ☐ |
| 08 egress-block-test | **the 10-min hold** (see D) | — | `08-...yaml` | ☐ |
| 09 gitea-token-mint | local Gitea API token for SME | success | `values.yaml:585-608` | ☐ |
| 10 vcluster-registry | vcluster+sso-bridge image hosts → harbor.<fqdn>; asserts zero residual | success | `10-...yaml:316-347` | ☐ |
| 11 crossplane-provider | Provider packages xpkg→harbor.<fqdn>/proxy-xpkg | success | `11-...yaml:205,292-294` | ☐ |

### D. THE PROOF — step 08 hold + cutoverComplete + independence
| # | Action | Expect | Source | ☐ |
|---|---|---|---|---|
| D1 | `kubectl get hr ... -o jsonpath='{.spec.values.egressTest.enforceCIDRBlock}'` | `true` (else step 08 denies nothing = invalid proof) | `06a-...yaml:497-520` | ☐ |
| D3 | `kubectl get ccnp cutover-egress-block` DURING the window | live policy `egressDeny: toCIDRSet` of the /32s | `08-...yaml:119-178` | ☐ |
| D5 | watch the 600s window | `no new regressions during 600s window — sovereignty proof PASSED` | `08-...yaml:226-238` | ☐ |
| D6 | `kubectl get secret ghcr-pull ... \| jq .auths` | keys ONLY `registry.<fqdn>` (no ghcr.io/harbor.openova.io) — no half-pivot | DoD-2 | ☐ |
| D7 | `kubectl get cm ...status -o jsonpath='{.data.cutoverComplete}'` | `true`, `progressPercent:100` (only after all 11) | `cutover.go:1085-1092` | ☐ |
| D8 | sweep all Flux sources | ZERO external refs (github.com/ghcr.io/harbor.openova.io/xpkg) | DoD-3 | ☐ |
| D9 | re-deny egress, `kubectl run probe --image=registry.<fqdn>/...` | Pod Running — pulled from local Harbor with mothership egress black-holed | DoD-5 | ☐ |

### E. Post-cutover + F. failure-shape gates
| E1/E3 | `flux reconcile`; PIN silent-SSO | pulls local; JWT `iss`=`console.<fqdn>` (landed-logged-in, not a 302) | ADR-0002 §4.2; `07-...yaml:246-270` | ☐ |
| F1-F3 | assert ABSENT | step-07 FATAL (#3039); mirror-resync revert (hw86); enforce-mode silent fallback | — | ☐ |

**Status:** cutover has **never fired** (no env handed over) — the entire walk is unexecuted. **Divergences:** 11 steps not 8; CIDR-deny not FQDN; enforce must be true.
