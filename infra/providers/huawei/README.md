# Catalyst Sovereign — Huawei Cloud (Stack) Phase-0 module

Canonical OpenTofu module for Catalyst Sovereign provisioning on
Huawei Cloud (Stack — on-prem HCS). Honours the cross-provider
contract at `../PROVIDER-INTERFACE.md` so the catalyst-api
`internal/providers/huawei` Go adapter (Wave 3) can swap providers by
changing only `deployment.Provider`.

## Topology (Tier-B canonical)

Per the operator-confirmed Tier-B sizing (2026-05-23 live API
discovery):

| Resource | Count | Sizing |
|---|---|---|
| VPC | 2 | `10.20.0.0/16`, `10.30.0.0/16` (no peering) |
| Subnet | 2 | 1 per VPC, anchored in `me-east-215a` |
| Security Group | 2 | 1 per VPC; ingress 443/80/6443/2379+12379 (clustermesh-apiserver VIP + host-socket proxy — host ports, §854: NO NodePort)/UDP 51820/ICMP |
| ECS — control-plane | 6 | 3 × `s7n.large.4` (2 vCPU / 8 GB) per region |
| ECS — worker | 4 | 2 × `m7n.xlarge.8` (4 vCPU / 32 GB) per region |
| EIP | 6 | 1 per CP node (workers have NO EIP) |
| OBS bucket | 1 | per-Sovereign, S3-protocol via `hashicorp/aws` |
| **Total** | **10 ECS, 6 EIP, 2 VPC, 2 SG, 1 OBS bucket** | |

Per-CP EIP is non-negotiable per DOD A2 — inter-region traffic flows
EXCLUSIVELY over Cilium WireGuard on public EIPs (DMZ-WG), never over
HCS-internal network peering.

## Endpoints (HCS on-prem)

The module defaults `huawei_region = "me-east-215"`. Per-service
endpoints derive deterministically as
`https://<service>.me-east-215.kom4dc.nationalcloud.om` and are pinned
via `endpoints {}` in `versions.tf`. The on-prem HCS CA is NOT in the
standard trust store; the module defaults `huawei_insecure = true` for
on-prem. Public Huawei Cloud sets it `false` via wizard override.

## Image + flavors (live-API-confirmed, 2026-05-23)

- Image ID — Ubuntu 22.04 server 64-bit (40 GB): `ec509d3b-...`
  (default `var.image_id`; catalyst-api writes the resolved value into
  `tofu.auto.tfvars.json` at provision time so a stale baked-in default
  never blocks a fresh image rotation).
- CP flavor — `s7n.large.4` (2 vCPU / 8 GB).
- Worker flavor — `m7n.xlarge.8` (4 vCPU / 32 GB).

## Credentials

Operator-supplied via the wizard's StepCredentials Huawei block. The
catalyst-api stores per-deployment AK/SK + project_id in
`/var/lib/catalyst/tofu/<dep-id>/tofu.auto.tfvars.json` on the
`catalyst-api-deployments` PVC (mode 0600; wiped on `tofu destroy`).
NOTHING is hardcoded into module defaults.

| Variable | Source | Notes |
|---|---|---|
| `huawei_access_key` | wizard | IAM AK |
| `huawei_secret_key` | wizard | IAM SK |
| `huawei_project_id` | wizard | region-scoped project ID |
| `huawei_region` | wizard (default `me-east-215`) | HCS region code |

## Object storage

Reuses `hashicorp/aws` against the HCS OBS S3-protocol endpoint
(`https://obs.<region>.kom4dc.nationalcloud.om`). Same provider
pattern the Hetzner module uses against Hetzner Object Storage — keeps
bucket purge code uniform across providers.

## Tests

`tests/multi_region.tftest.hcl` exercises the Tier-B canonical two-region
shape + a single-region back-compat shape against mocked providers (no
real Huawei API calls). Run via:

```bash
cd infra/providers/huawei
tofu init -backend=false
tofu test
```

## Outputs

Per `../PROVIDER-INTERFACE.md` §2 — every output the catalyst-api
provisioner depends on is emitted in the same shape Hetzner emits, so
the Go-side `provisioner.readOutputs()` is provider-agnostic.

## Wave 3 → Wave 4 hand-off

Wave 3 (this PR) lands the module + the Go adapter. Wave 4 is the
end-to-end fresh-prov walk per DoD A6 on a real HCS endpoint with the
operator's AK/SK pair. Expect Wave 4 to surface 1-2 cloud-init shape
adjustments specific to HCS (e.g. EIP-from-metadata path; the current
template falls back to `ip route get` if the OpenStack metadata service
doesn't surface `public_ipv4_address` on HCS).
