# Convergence-hardening train — 2026-06-27

Founder-facing. This session drove a fresh single-region keystone prov on **kom4dc** (Huawei me-east-215) plus the `bp-self-sovereign-cutover` walk, and surfaced a **chain of 7 distinct silent-failure faults** along the fresh-prov → cutover path. Each fault is a quiet non-fatal failure: the prov keeps advancing past it, then wedges downstream where the symptom looks unrelated to the cause. All 7 are now fixed (5 merged, 2 in flight). Once the whole train merges and the catalyst-api image carrying the baked templates rolls, a fresh kom4dc prov + cutover converges **zero-touch**.

## The 7-fault train (in prov-advance order)

| # | Fault | Where it wedges | Fix | PR |
|---|---|---|---|---|
| 1 | **Subnet-name overflow** — long FQDN label (`kstone06270453-omani-works`) + the `-me-east-215-a-subnet` suffix overflowed Huawei's VPC subnet name cap (`VPC.0201`). | `tofu apply` fails at subnet create — whole prov dies before any cluster exists. | Use a short `SOV_LABEL` (`kv<HHMMSS>`) so `<label>-<region>-a-subnet` stays under the cap. | (prov-input) |
| 2 | **Console-isolation 3-EIP fit** — kom4dc EIP quota=10, free=3; a single-region prov with console-isolation ON needs 4 EIPs (cp + nat + elb_primary + elb_console). | EIP allocation fails. | `console_isolation_enabled=false` drops `elb_console` → 3 EIPs → fits. (2-region needs 6 → stays EIP-quota-bump-gated.) | #4502 |
| 3 | **Silent Cilium bootstrap** — cloud-init `helm install cilium` runs in a non-fatal runcmd; a network flake on the chart/image pull fails it silently → CNI-less cluster, nodes NotReady, Flux can't schedule. | 0 HRs forever; cluster looks "up" at the tofu layer but is dead. | `rt()` retry + `helm upgrade --install` idempotency + a fail-fast sentinel that HALTS loudly instead of yielding a CNI-less cluster. | #4503 / #4504 |
| 4 | **Flux-bootstrap 2-NAME namespace bug** — `kubectl create namespace catalyst-system openbao` passed TWO names to a one-name command → non-zero exit halted the runcmd → the flux-bootstrap apply (GitRepository + Kustomizations) never ran. A latent `%{ endif ~}` tilde also stripped a newline → YAML error. | Flux installed but **0 sources / 0 HRs**. | Split namespace creation one-per-name; drop the tilde; add a fail-fast sentinel. | #4513 / #4520 |
| 5 | **provider-opentofu install deadlock** — the `infrastructure-config` Kustomization atomically bundles the crossplane Provider together with a ProviderConfig (`tf.upbound.io/v1beta1`), a CRD the Provider itself installs → the ProviderConfig dry-run fails (CRD absent) → the whole Kustomization is rejected → the Provider never installs. | crossplane-adoption seam inert. | Split Provider / ProviderConfig into `dependsOn`-ordered layers so the Provider (and its CRD) lands first. | #4488 / #4521 (split PR #4528, in flight) |
| 6 | **Cutover harbor-prewarm TLS** — cutover step-03 ran `skopeo copy --dest-tls-verify=true` to the in-cluster Harbor whose wildcard cert is LE-**staging** (untrusted) → x509 fail. | Cutover wedges **before** the 600s deny-egress hold ever starts. | `--dest-tls-verify=false` for the in-cluster registry destination only (keep `--src-tls-verify=true` for external ghcr). Deny-egress proof step untouched. | #4529 / #4531 (Refs #3379) |
| 7 | **Registry-pivot anonymous ghcr 401** — the cold-start mirror pulls private `ghcr.io/openova-io/*` images anonymously → 401. | Transient catalyst-api `ImagePullBackOff` post-cutover. | Present `ghcr-pull` credentials on the registry-pivot mirror pull. | #4527 (in flight) |

## Meta-lesson — merged ≠ delivered

catalyst-api **bakes** the cloud-init / infra templates into its container image at `/infra/providers/` — they are not git-cloned at prov time. So a merged `infra/` fix (faults 1–5 all live in those baked templates) is **inert until the catalyst-api image rebuilds and the deployment rolls the new image**. Several times this session a fault was "fixed and merged" yet the next re-fire reproduced it, because the running catalyst-api still carried the old baked template.

**Guard before re-firing a prov:** grep the running catalyst-api pod's baked template under `/infra/providers/` to confirm the new line is present. If the baked template is still old, the image has not rolled — re-firing is futile.

## Net

The 7-fault train, once fully merged and rolled into the catalyst-api image, makes a fresh **kom4dc single-region prov + `bp-self-sovereign-cutover`** converge **zero-touch**. Five fixes are merged (#4502, #4503/#4504, #4513/#4520, #4529/#4531); two are in flight (#4528 provider split, #4527 registry-pivot creds). The 2-region path remains separately EIP-quota-bump-gated (needs 6 EIPs; free=3 on kom4dc today).

Refs #909.
