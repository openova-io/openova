# Runbook — Provisioning a New Sovereign

**Status:** Operator-level procedure. **Updated:** 2026-04-29.
**Audience:** Sovereign cloud team (e.g. `omantel-cloud`) onboarding their first Sovereign via Catalyst-Zero. Read this with [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) (the architectural contract) and [`PROVISIONING-PLAN.md`](PROVISIONING-PLAN.md) (the Catalyst-Zero waterfall).

---

## What this runbook gets you

A new **Sovereign** — a self-sufficient deployed Catalyst — provisioned end-to-end on Hetzner from Catalyst-Zero (`console.openova.io/sovereign`). At the end:

- A k3s cluster running on Hetzner Cloud servers in your chosen region
- Cilium CNI + Gateway API as ingress, Flux as GitOps reconciler, Crossplane as day-2 IaC
- The 12-component bootstrap kit installed and reconciling cleanly: cilium → cert-manager → flux → crossplane → sealed-secrets → spire → nats-jetstream → openbao → keycloak → gitea → powerdns → bp-catalyst-platform
- Reachable URLs: `console.<your-fqdn>`, `gitea.<your-fqdn>`, `admin.<your-fqdn>` (TLS via cert-manager + Let's Encrypt)
- Initial sovereign-admin user in Keycloak's `catalyst-admin` realm
- The Sovereign is now self-sufficient — the catalyst-provisioner has zero ongoing connection to it (Phase 1 hand-off complete)

This runbook does NOT cover Day-1 setup (cert-manager issuers, backup destination, Org onboarding) — see [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §5 for that.

---

## Before you start — what you need

Gather all of the following BEFORE opening the wizard. The wizard does not save partial input across sessions.

| Item | Where to get it | Validation |
|---|---|---|
| **Hetzner Cloud account + project** | https://console.hetzner.cloud → Projects → New Project | Project ID visible in Cloud Console URL after selection |
| **Hetzner Cloud API token** | Inside the project: Security → API Tokens → New Token, scope **Read & Write** | Save it once — it is shown only at creation |
| **Hetzner region** | One of: `fsn1` (Falkenstein), `nbg1` (Nuremberg), `hel1` (Helsinki), `ash` (Ashburn US East), `hil` (Hillsboro US West) | Wizard validates against this list |
| **SSH public key** | Your sovereign-admin break-glass keypair — generate with `ssh-keygen -t ed25519 -C "sovereign-admin@<your-org>" -f ~/.ssh/sovereign_admin` | The PUBLIC half (`*.pub`) is what the wizard takes |
| **Sovereign domain** | Three modes (post-#169): (a) **Pool** — pick a subdomain under `omani.works` / `openova.io` (the wizard reserves it via PDM `/v1/reserve` and creates the per-Sovereign PowerDNS zone on commit); (b) **BYO with manual NS-flip** (`byo-manual`) — bring your own registered domain; the wizard shows the OpenOva NS records you paste into your registrar UI; (c) **BYO with API NS-flip** (`byo-api`) — bring your own domain plus a registrar API token (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot) and OpenOva flips NS for you | Wizard validates registrar tokens read-only (`POST /api/v1/registrars/validate`) before accepting |
| **Organisation name + email** | Your organisation's display name + the email that becomes the initial sovereign-admin in Keycloak | Email must be deliverable — Keycloak sends the password reset there |
| **Topology choice** | Single-region (SME default) or 1-CP-1-worker minimal vs `ha_enabled=true` (3-CP HA) + `worker_count` ≥ 1 | Wizard surfaces these as form fields |

**Cost estimate for a default single-region run:** 1× control-plane CPX21 (~€8/mo) + 1× worker CPX31 (~€16/mo) + 1× lb11 (~€6/mo) + ~€1 storage = **~€31/mo** before workload growth. HA topology (3 CPs + 2 workers) is closer to ~€80/mo.

---

## Step-by-step

### 1. Open the provisioning wizard

```
https://console.openova.io/sovereign
```

Log in as a Catalyst-Zero user (your existing OpenOva-issued credentials) and click **New Sovereign**.

### 2. Walk the 7-step wizard

The wizard's Vite scaffold lives at [`products/catalyst/bootstrap/ui/`](../products/catalyst/bootstrap/ui/). Each step writes its inputs into the wizard's local store; nothing is sent to the catalyst-api until **Review** + **Provision**. The 7-step indicator lives in the page header (per #174); per-step ordering is canonical from `WIZARD_STEPS` in `src/app/layouts/WizardLayout.tsx`.

| Step | What it captures | Notes |
|---|---|---|
| 1. Organisation | Org name, contact email, sovereign-admin email | Becomes the initial sovereign-admin |
| 2. Domain | Pool subdomain OR BYO (manual NS / registrar API), per #169's three-mode flow | Pool = PDM `/v1/reserve`. BYO byo-api = registrar token (Cloudflare/Namecheap/GoDaddy/OVH/Dynadot, #170). BYO byo-manual = wizard surfaces NS list to paste at customer registrar |
| 3. Topology | Regions, building blocks (mgt + rtz/dmz), HA toggles | Single-region is the supported path at first launch — multi-region remains design-only |
| 4. Provider | Cloud per region (Hetzner today; AWS / GCP / Azure / OCI / Huawei per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §9.1 are design-only) | |
| 5. Credentials | Hetzner Cloud API token + project ID, SSH public key | Validated read-only via `POST /api/v1/credentials/validate` before advancing; the token is sent once over TLS, never logged, redacted from SSE event stream |
| 6. Components | Two-tab StepComponents (#161, #162): a "Mandatory infra" tab listing the always-installed blueprint set and an "Apps" tab with the optional-Application card grid | Apps are added post-provisioning too — only pre-select the must-haves. Per #175 dependency-aware cascades pull transitive deps automatically (e.g. picking Harbor pulls in cnpg + seaweedfs + valkey) |
| 7. Review | Show every captured value, **Provision** button | Click → catalyst-api accepts the request and starts streaming |

### 3. Watch the SSE event stream

Once you click **Provision**, the wizard's progress page shows a live event log streamed from the catalyst-api `/v1/sovereigns/{id}/events` endpoint. Phases you will see:

```
tofu-init       Initialising OpenTofu working directory
tofu-plan       Planning Hetzner resources (network, firewall, server, LB, DNS)
tofu-apply      Applying — this provisions real Hetzner resources, please wait
tofu-output     Reading OpenTofu outputs (control_plane_ip, load_balancer_ip)
flux-bootstrap  Cloud-init has bootstrapped Flux + Crossplane in the new
                cluster — Flux will now reconcile clusters/<sovereign-fqdn>/
                from the public OpenOva monorepo, installing the 12-component
                bootstrap kit and bp-catalyst-platform umbrella in dependency
                order.
```

After `flux-bootstrap`, the wizard polls Flux Kustomizations on the new cluster (via the catalyst-api which has temporary kubeconfig from the OpenTofu output) and shows a per-Kustomization readiness grid. Steady-state takes 25–55 minutes from `tofu-apply` to `bp-catalyst-platform: Ready=True`.

**If the SSE stream goes silent for >60s:** the catalyst-api connection may have dropped (browser refresh recovers; events queue server-side). If it is silent for >5 minutes during `tofu-apply`, check the Hetzner Cloud Console for stuck server creation — most often this is API rate-limiting under your project; it resolves itself.

### 4. First login

When the wizard shows **Done — your Sovereign is ready**, navigate to:

```
https://console.<sovereign-fqdn>
```

(For pool domains, this is e.g. `console.omantel.omani.works`. For BYO, you must first add a CNAME from `*.<your-fqdn>` to the load-balancer DNS name shown on the success screen.)

Sign in with the email you provided in Step 2. Keycloak's `catalyst-admin` realm sends a password-reset email; click the link, set a strong password (24+ chars per [`feedback_passwords.md`](https://github.com/openova-io/openova-private/blob/main/CLAUDE.md)), then complete the realm flow.

### 5. Day-1 setup checklist

Per [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §5:

- [ ] Configure cert-manager Issuer (Let's Encrypt prod or your corporate CA)
- [ ] Configure Velero backup destination (cloud object storage)
- [ ] Configure Harbor image-scanning policies + retention
- [ ] (Optional) Federate Keycloak to your corporate IdP (Azure AD / Okta / Google)
- [ ] (Optional) Configure observability exports (datadog, SIEM)
- [ ] Onboard your first Catalyst Organization
- [ ] Create your first Environment in that Organization
- [ ] Install your first Application from the marketplace

---

## What can go wrong, and what to do

The catalyst-api retains the OpenTofu state per-Sovereign in `/var/lib/catalyst/tofu/<sovereign-fqdn>/`. Re-running with the same Sovereign FQDN is idempotent (`tofu apply` on existing state). This means most failures are recoverable without manual cleanup of Hetzner resources.

| Symptom | Most likely cause | What to do |
|---|---|---|
| `tofu plan` fails with `403 Forbidden` from hcloud | Hetzner token has only Read scope, or expired | Generate a new Read+Write token; re-run wizard with same FQDN |
| `tofu plan` fails with `quota exceeded` | Hetzner project default limits (typically 10 servers, 1 LB) | Open a Hetzner support ticket to raise limits; re-run when granted |
| `tofu apply` hangs at `hcloud_server.control_plane[0]: Still creating...` for >10 min | Hetzner regional capacity transient | Wait 15 min total; if still stuck, cancel + re-run with a different region |
| `flux-bootstrap` shows `connection refused` from kubectl | Cilium CNI not yet up (chicken-and-egg with API server readiness) | Wait — k3s + Cilium + Flux take ~5 min to converge before `kubectl` works through Flux |
| `bp-cilium` Kustomization stuck at `Ready=Unknown` for >10 min | Network configuration mismatch (most likely cloud-init didn't pass `--flannel-backend=none` correctly) | SSH into the control-plane node (the IP is visible in the Hetzner Cloud Console; SSH key is the one you provided) and run `journalctl -u k3s -n 100`; share the output with OpenOva support |
| `bp-cert-manager` reconciles but cert issuance fails | Let's Encrypt rate-limit (50 certs / week / domain) or DNS records not propagated | Check `cert-manager` events: `kubectl -n cert-manager describe challenge`; for rate-limit, wait. For DNS, dig the records: `dig console.<your-fqdn> +short` should return the LB IP |
| `console.<sovereign-fqdn>` returns 404 / connection-refused | Per-Sovereign PowerDNS zone records not yet visible to public resolvers (parent-zone NS-delegation TTL ~15 min for pool, customer-registrar TTL for BYO byo-manual / byo-api) | `dig <sovereign-fqdn> NS` should return OpenOva NS; `dig console.<sovereign-fqdn>` should return the LB IP. Allow up to 30 min for DNS propagation |
| Keycloak reset-password email never arrives | SMTP not configured in Keycloak realm yet | Reset via the catalyst-admin realm-admin flow inside the cluster: `kubectl -n catalyst-system exec -it keycloak-0 -- /opt/keycloak/bin/kcadm.sh ...` (the catalyst-admin path is documented in `clusters/<sovereign-fqdn>/keycloak/README.md`) |

**Escalation:** if the runbook doesn't unblock you, file an issue against `github.com/openova-io/openova` with the `area/platform` and `kind/provisioning` labels, including: Sovereign FQDN, region, last 50 SSE events, last 100 lines of `kubectl -n flux-system get events`, and the OpenTofu workdir contents (excluding `tofu.auto.tfvars.json` which contains the Hetzner token).

---

## Re-runs and idempotency

`tofu apply` on an existing state is idempotent: rerunning the wizard with the **same Sovereign FQDN** updates only what changed (worker count up/down, k3s version upgrade, new firewall rules from a new cloud-init template). The cluster's running pods are untouched.

To intentionally re-run cloud-init on the control-plane (e.g. to apply a new Flux GitRepository config), the cleanest path is via Crossplane Compositions in `clusters/<sovereign-fqdn>/`, NOT by re-running cloud-init directly. Cloud-init runs once per server lifetime by default; replacing it requires either:

1. A Crossplane-driven server replacement (preferred — drains the old node, brings up a new one, lets Flux reconcile fresh)
2. SSH + manual `cloud-init clean && cloud-init init` (allowed only as break-glass)

---

## Decommissioning

If you need to tear down a Sovereign you just provisioned (e.g. test run):

```
1. From Catalyst console: Admin → Sovereign → Decommission
   → Crossplane begins teardown of host clusters
   → OpenBao final state exported and stored encrypted (download link in admin UI)
   → DNS records removed
   → Cloud resources reclaimed
2. (For pool domains only) PDM releases the subdomain reservation and prunes the per-Sovereign PowerDNS zone; the parent-zone NS-delegation update at the registrar (Dynadot for pool) propagates within ~15 min TTL
3. (Manual cleanup) tofu destroy -auto-approve in the catalyst-api workdir for that Sovereign
```

This is the same flow as [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §10.2.

---

## What to read next

- [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §4–§10 — Phase 1 hand-off, Day-1 setup, multi-region, decommission
- [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md) — sovereign-admin journey for Day-1 onwards
- [`SRE.md`](SRE.md) — running the Sovereign in steady-state (alerting, backups, upgrades)
- [`SECURITY.md`](SECURITY.md) §5 — OpenBao replication semantics across regions

---

*Part of [OpenOva](https://openova.io). Operator-facing companion to [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) (the architectural contract) and [`PROVISIONING-PLAN.md`](PROVISIONING-PLAN.md) (the Catalyst-Zero waterfall).*
