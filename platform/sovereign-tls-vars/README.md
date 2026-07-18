# bp-sovereign-tls-vars

Renders the `sovereign-tls-vars` ConfigMap in `flux-system` — nothing else.

## What it is

A single-ConfigMap Catalyst Blueprint. It is the Flux
`postBuild.substituteFrom` source the `sovereign-tls` Kustomization
(`clusters/_template/sovereign-tls/`) reads to materialise the shared
Cilium Gateway's and console Gateway's listener arrays
(`PARENT_DOMAINS_LISTENERS_YAML` / `CONSOLE_LISTENERS_YAML`).

## Its role in Catalyst

Control plane — foundation infrastructure, installed by the bootstrap
kit (slot 12a, `clusters/_template/bootstrap-kit/
12a-bp-sovereign-tls-vars.yaml`), not an operator-visible Application
Blueprint. See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §2/§3
for the bootstrap-kit slot model.

## Why this chart exists (Refs #5187)

The listener-array data used to be rendered by `bp-catalyst-platform`
(`products/catalyst/chart`, bootstrap-kit slot 13). That HelmRelease is
**primary-region-only** — `suspend: ${SECONDARY_HR_SUSPEND:=false}`
(G2 #2574): the Sovereign control plane (catalyst-api/catalyst-ui/...)
installs on the primary region's cluster only.

The `sovereign-tls` Kustomization that *consumes* the ConfigMap,
however, reconciles on **every** region's own control plane — it has
no region-role gate (see the top-of-block comment in
`infra/providers/_shared/cloudinit-control-plane.tftpl`) and its
`postBuild.substituteFrom` is `optional: false`. On a 2-region
Sovereign the secondary (DR-standby) region therefore never got the
ConfigMap — its own `sovereign-tls` Kustomization sat `Ready=False`
forever (`configmaps "sovereign-tls-vars" not found`), and the
standby's own wildcard `Certificate` + `Gateway` never issued /
programmed.

This is not purely cosmetic: a region-kill promotes the standby to
primary, and the platform's shared-EIP / LB-IPAM Gateway design (see
repo `CLAUDE.md` §NodePorts, `lbipam.cilium.io/sharing-key`) expects
the standby's own Gateway + wildcard cert to already be warm — DNS-01
issuance + propagation takes minutes, which a live region-A failure
can't wait for. Pre-warming both regions is the correct DR posture.

## Fix shape

Extracted the ConfigMap template verbatim (same rendering logic,
same values contract: `global.sovereignFQDN` / `parentZones` /
`consoleGateway.{httpsPort,httpPort}`) out of `bp-catalyst-platform`
into this dedicated chart, installed via a NEW bootstrap-kit slot
(12a) with **no** `region-role` / `suspend` gate — the same
"no gate ⇒ every region" shape `bp-cilium` / `bp-gateway-api` /
`bp-cert-manager` already use. Region-a is unaffected: it now gets the
identical ConfigMap from this chart instead of from
`bp-catalyst-platform` (the old template was deleted there to avoid a
two-Helm-release-owner conflict on the same-named ConfigMap).

## Configuration knobs / configSchema highlights

| Value | Default | Source |
|---|---|---|
| `global.sovereignFQDN` | `""` | `${SOVEREIGN_FQDN}` cloud-init substitute |
| `parentZones` | `[]` | `${PARENT_DOMAINS_YAML}` cloud-init substitute |
| `consoleGateway.httpsPort` | `443` | `${CONSOLE_GATEWAY_HTTPS_PORT:=443}` (Huawei hostNetwork sets `8443`) |
| `consoleGateway.httpPort` | `80` | `${CONSOLE_GATEWAY_HTTP_PORT:=80}` (Huawei hostNetwork sets `8080`) |

Empty `global.sovereignFQDN` (Catalyst-Zero / not-yet-provisioned)
renders **zero** resources — see `chart/templates/configmap.yaml`'s
top-level guard.

## Operational notes

- No backups (stateless render, no PVC).
- No CRDs, no dependencies — installs immediately (`depends: []`).
- Multi-region: installs identically on every region (`active-active`
  placement) — this IS the fix; do not re-add a `region-role`/`suspend`
  gate without re-introducing the #5187 gap on a future secondary
  region.
