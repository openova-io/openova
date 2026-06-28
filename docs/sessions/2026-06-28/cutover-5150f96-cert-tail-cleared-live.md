# Cutover 5150f96 — first clean run clears the cert-tail live (2026-06-28)

Live evidence captured on the fresh single-region cutover prov `5150f96071638f1e`
(kv030704.omani.works), driving #3379 toward `cutoverComplete`. This is the
FIRST cutover to clear the cert-tail gate where 00720a7a + every prior run wedged.

## Step progression (engine `self-sovereign-cutover-status`)
- step-01 gitea-mirror ✅ · step-02 harbor-projects ✅ · step-03 harbor-prewarm ✅
  (#4581 504-retry) · step-04 registry-pivot ✅ · step-05 flux-gitrepository-patch ✅
- **step-06 helmrepository-patches ✅** — the cert-tail gate (#4586 source-controller
  certSecretRef trust). 00720a7a wedged here on `x509: certificate signed by unknown
  authority`; 5150f96 cleared it.
- **step-07 ghcr-detether ✅**
- step-08 egress-block-test — running (600s deny-egress hold).

## #4573 / #4557 PROOF — HelmRepositories pivoted to the local Harbor
`kubectl get helmrepositories -A` URL hosts on 5150f96:
```
     62 oci://registry.kv030704.omani.works   <- pivoted (was oci://ghcr.io at step-03)
      1 https://charts.loft.sh
```
61/63 reconciled Ready from the local LE-staging Harbor with NO source-controller
x509 error — the #4586 certSecretRef trust path works end-to-end on a clean prov.

## #4563 — detether
Node `registries.yaml` mirrors ghcr→local Harbor (step-04); the egress hold (step-08)
is the live proof that the cluster reconciles green with github.com/ghcr.io/
harbor.openova.io black-holed. cutoverComplete is set only if it stays green.

## Disposition
UAT rows 165/166 (and #4573/#4563/#4557) flip on `cutoverComplete=true` — NOT filled
here (no false-pass mid-hold). This doc captures the cert-tail clearance proof.

## UPDATE — step-08 egress hold is PASSING (sovereignty proof succeeding)
During the 600s deny-egress NetworkPolicy hold (github.com / ghcr.io /
harbor.openova.io black-holed), the cluster is **fully green**:
`kubectl get hr -A` shows **all 60+ HelmReleases Ready=True** — bp-cilium@1.4.5,
bp-catalyst-platform@1.4.940, bp-keycloak, bp-cnpg, bp-harbor, bp-gitea, … — every
one reconciling from `oci://registry.kv030704.omani.works` (the local Harbor), zero
from ghcr. The Sovereign runs entirely on its own Gitea+Harbor with the mothership
black-holed. `cutoverComplete=true` sets when the hold timer elapses with the cluster
still green — i.e. Pillar-5 is proving live, first clean run. #4563 (no residual ghcr
tether) is demonstrated: fresh reconciles succeed from local Harbor under egress block.
