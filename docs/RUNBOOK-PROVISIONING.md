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
| **Sovereign domain** | Three modes (post-#169): (a) **Pool** — pick a subdomain under `omani.works` / `openova.io` (the wizard reserves it via PDM `/v1/reserve` and creates the per-Sovereign PowerDNS zone on commit); (b) **BYO with manual NS-flip** (`byo-manual`) — bring your own registered domain; the wizard shows the OpenOva NS records you paste into your registrar UI; (c) **BYO with API NS-flip** (`byo-api`) — bring your own domain plus a registrar API token (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot) and OpenOva flips NS for you. Captured at Step 6 (after sizing + creds + components) so the wizard can pair the domain with the deployed footprint | Wizard validates registrar tokens read-only (`POST /api/v1/registrars/validate`) before accepting |
| **Organisation profile** | Org name, industry, size, HQ, compliance frame; the sovereign-admin email is captured at Step 6 (Domain) so it pairs with the Sovereign's external surface | Email must be deliverable — Keycloak sends the password reset there |
| **Topology choice** | Single-region (SME default) or 1-CP-1-worker minimal vs `ha_enabled=true` (3-CP HA) + `worker_count` ≥ 1; control-plane + worker SKU pickers driven by `PROVIDER_NODE_SIZES[provider]` (#176) | Wizard surfaces these as form fields |

**Cost estimate for a default single-region run:** 1× control-plane CPX21 (~€8/mo) + 1× worker CPX31 (~€16/mo) + 1× lb11 (~€6/mo) + ~€1 storage = **~€31/mo** before workload growth. HA topology (3 CPs + 2 workers) is closer to ~€80/mo.

---

## Step-by-step

### 1. Open the provisioning wizard

```
https://console.openova.io/sovereign
```

Log in as a Catalyst-Zero user (your existing OpenOva-issued credentials) and click **New Sovereign**.

### 2. Walk the 7-step wizard

The wizard's Vite scaffold lives at [`products/catalyst/bootstrap/ui/`](../products/catalyst/bootstrap/ui/). Each step writes its inputs into the wizard's local store; nothing is sent to the catalyst-api until **Review** + **Provision**. The 7-step indicator lives in the page header (per #174); per-step ordering is canonical from `STEPS` in [`src/pages/wizard/WizardPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/wizard/WizardPage.tsx). The canonical order — operator picks workload sizing, then provider, then credentials, then components, then names the Sovereign in DNS — is:

| Step | What it captures | Notes |
|---|---|---|
| 1. Organisation | Org profile: name, industry, size, HQ, compliance frame | No email or domain capture here — the sovereign-admin email pairs with the Sovereign's external surface and is captured at Step 6 (Domain) |
| 2. Topology | Regions, building blocks (mgt + rtz/dmz), HA toggle, control-plane + worker SKU + worker count | Single-region is the supported path at first launch — multi-region remains design-only. Per #176 the SKU pickers are driven by `PROVIDER_NODE_SIZES[provider]` so the catalog stays per-provider correct (no Hetzner-only literals leaking into the AWS/Azure/OCI paths) |
| 3. Provider | Cloud per region (Hetzner today; AWS / GCP / Azure / OCI / Huawei per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §9.1 are design-only) | |
| 4. Credentials | Provider API token + project ID (when applicable), SSH public key | Validated read-only via `POST /api/v1/credentials/validate` before advancing; the token is sent once over TLS, never logged, redacted from SSE event stream |
| 5. Components | Single flat marketplace card grid (#162, #b0ec0c43) with family chips on each card and search + product-family chip filter at the top. Two tabs: **Choose Your Stack** (recommended + optional, default-on for recommended) and **Always Included** (the post-promotion mandatory closure, read-only) | Apps can be added post-provisioning too — only pre-select the must-haves. Per #175 dependency-aware cascades pull transitive deps automatically (e.g. picking Harbor pulls in cnpg + seaweedfs + valkey); per #d3346441 each card's family chip is clickable and routes to the family portfolio page, the card body routes to the product detail page, and only the explicit Select / Selected button toggles the wizard store |
| 6. Domain | Pool subdomain OR BYO (manual NS / registrar API), per #169's three-mode flow, plus the sovereign-admin email | Pool = PDM `/v1/reserve`. BYO byo-api = registrar token (Cloudflare/Namecheap/GoDaddy/OVH/Dynadot, #170). BYO byo-manual = wizard surfaces NS list to paste at customer registrar |
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

Sign in with the sovereign-admin email you provided at Step 6 (Domain). Keycloak's `catalyst-admin` realm sends a password-reset email; click the link, set a strong password (24+ chars per [`feedback_passwords.md`](https://github.com/openova-io/openova-private/blob/main/CLAUDE.md)), then complete the realm flow.

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

The catalyst-api retains the OpenTofu state per-Sovereign in `/tmp/catalyst/tofu/<sovereign-fqdn>/` — the `CATALYST_TOFU_WORKDIR` env var on the catalyst-api Deployment (commit `27527e4c`, see [`products/catalyst/chart/templates/api-deployment.yaml`](../products/catalyst/chart/templates/api-deployment.yaml) and the comment block explaining why `/var/lib/catalyst/...` is unwritable for UID 65534) points the provisioner at the Pod's writable `/tmp` emptyDir (2 Gi sizeLimit) so each Sovereign run gets its own subdirectory. Re-running with the same Sovereign FQDN is idempotent (`tofu apply` on existing state). This means most failures are recoverable without manual cleanup of Hetzner resources.

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

### Phase 1 watch shows 0 HelmReleases

**Symptom.** The wizard's progress page reaches `flux-bootstrap` successfully, then the Sovereign Admin banner shows the warning:

> `Phase 1 watch saw 0 HelmReleases in 15m0s; the bootstrap-kit Kustomization may not be reconciling. Operator: run flux get kustomization -n flux-system on the new cluster.`

The deployment status flips to `failed` with `Phase1Outcome=flux-not-reconciling` and the error message names this runbook section.

**What this means.** Phase 0 (`tofu apply` + cloud-init) succeeded — the new k3s cluster is up and Flux is installed. But the Phase-1 catalyst-api watcher, which observes `bp-*` HelmReleases in `flux-system` via a read-only client-go informer, never saw a single HelmRelease appear within the first-seen window (`CATALYST_PHASE1_FIRST_SEEN_TIMEOUT`, default **15 minutes**). That means **Flux on the new Sovereign isn't materialising the bootstrap-kit Kustomization** — typically because the Kustomization itself can't reach its Git source, can't decrypt a SOPS secret, or its dependencies haven't reconciled yet.

This is **not** a "wait it out" condition: the watcher continues running so a late HelmRelease still flows, but the cluster needs operator inspection before the install can complete.

**Operator playbook.** SSH into the control-plane node (the IP is in the Hetzner Cloud Console; the SSH key is the one you supplied at Step 4 of the wizard) and walk these in order:

1. **Confirm the catalyst-api Pod actually has the kubeconfig.** This eliminates the "watcher misconfigured" branch before you go hunting on the new cluster.

   ```bash
   # On the catalyst-zero cluster (where catalyst-api runs):
   kubectl -n openova-system get deployment catalyst-api -o jsonpath='{.spec.template.spec.containers[0].env}' \
     | jq '.[] | select(.name=="CATALYST_PHASE1_FIRST_SEEN_TIMEOUT" or .name=="CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS" or .name=="CATALYST_PHASE1_WATCH_TIMEOUT")'
   ```

   The defaults (15m / 11 / 60m) are fine for a normal run — only override for diagnostic re-runs.

2. **Check the GitRepository on the new Sovereign.** Flux's source-controller fetches the OpenOva monorepo; if it can't, every downstream Kustomization is starved.

   ```bash
   # On the new Sovereign (KUBECONFIG=<the kubeconfig captured at Phase 0>):
   kubectl get gitrepository -n flux-system -o wide
   kubectl describe gitrepository -n flux-system openova-public
   ```

   Look for `Conditions[type=Ready].status=True` and a recent `lastAppliedRevision`. Common failures: 401/403 (deploy-key missing or wrong scope), 404 (branch / path mismatch), connection refused (DNS / firewall egress).

3. **Check the bootstrap-kit Kustomization.** This is what materialises the 11 `bp-*` HelmRelease objects.

   ```bash
   kubectl get kustomization -n flux-system
   kubectl describe kustomization -n flux-system <sovereign-fqdn>-bootstrap-kit
   ```

   If `Ready=False`, the `Message` field names the cause: missing CRD (`HelmRelease`), unrecognised `apiVersion` (Flux upgrade lockstep), `path` not found in the Git source, or `dependsOn` unresolved.

4. **Inspect source-controller and kustomize-controller logs.** When the GitRepository looks healthy but no Kustomization fires, these are the next layers down.

   ```bash
   kubectl -n flux-system logs deploy/source-controller --tail=200
   kubectl -n flux-system logs deploy/kustomize-controller --tail=200
   ```

   A clean log shows a periodic reconcile loop with revision SHAs. A stuck log shows the same error repeating every reconcile interval — that error is the root cause.

5. **Re-run reconciliation manually** once the cause is fixed:

   ```bash
   flux reconcile source git openova-public -n flux-system
   flux reconcile kustomization <sovereign-fqdn>-bootstrap-kit -n flux-system
   ```

   The catalyst-api watcher is still running on the wizard side (the `flux-not-reconciling` warn event does NOT terminate the watch loop — it just surfaces the banner). Once HelmReleases start appearing, normal per-component pills resume in the Sovereign Admin UI.

**If the watcher has already terminated** (overall `CATALYST_PHASE1_WATCH_TIMEOUT` of 60m elapsed): the watch goroutine has exited. Start a new wizard run — the Hetzner side is idempotent (`tofu apply` on existing state) so you keep the cluster, but the per-deployment HelmRelease watch is owned by the old deployment id. A fresh run is the cleanest path until the wizard surfaces a "rejoin watch" button.

**Why this is a dedicated symptom.** Earlier builds misread an empty informer cache as "all components done" and reported `finalStatus: ready` one second after `flux-bootstrap`. The current build refuses to consider termination until at least one `bp-*` HelmRelease has been observed AND the count meets `CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS`, so the only way to land here is a real Flux-side problem on the new cluster — not a timing race in the watcher. Trust the diagnostic and walk the playbook above.

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
