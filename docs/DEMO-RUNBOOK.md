# Demo Runbook — First Franchised Sovereign End-to-End (DoD)

**Status:** Operator-level. **Updated:** 2026-04-29. **Scope:** `docs/ORCHESTRATOR-STATE.md` §"What still needs to happen for DoD" — every step turned into a copy-paste procedure for the omantel.omani.works DoD demo.

This runbook is the **single document** an operator follows to take the omantel demo from "console.openova.io is the only running cluster" through to "fictional Omantel SME tenant has redeemed a voucher and created their first Org+Env+App on a freshly-provisioned Hetzner Sovereign at `omantel.omani.works`."

It is the operator-facing companion to [`tests/dod/dod_test.go`](../tests/dod/dod_test.go) (the Go test that drives the same flow non-interactively when `HETZNER_TEST_TOKEN` is populated).

---

## Pre-flight

Before opening anything, gather:

| Item | Notes |
|---|---|
| **Hetzner Cloud project** | Real Hetzner account, real project. Create one at https://console.hetzner.cloud → Projects → New if you don't have one. **Cost note:** ~€31/mo equivalent at hourly billing, ~€0.05/h while the demo is up. |
| **Hetzner API token (Read+Write)** | Inside the project: Security → API Tokens → New Token. Save the token once — it is shown only at creation. **NEVER paste it into Slack, GitHub, or commit messages.** It goes only into the wizard's password-style input field. |
| **Hetzner project ID** | Visible in the Cloud Console URL after selecting the project, e.g. `https://console.hetzner.cloud/projects/<numeric-id>/...`. |
| **SSH public key** | Generate fresh if you don't already have a sovereign-admin keypair: `ssh-keygen -t ed25519 -C "sovereign-admin@omantel" -f ~/.ssh/omantel_sovereign_admin`. The PUBLIC half (`*.pub`) is what the wizard takes. |
| **Pool subdomain reserved** | We will pick `omantel` under the `omani.works` pool domain. PDM `/v1/reserve` checks availability against `pdm-pg`; on commit it (a) creates the per-Sovereign PowerDNS zone for `omantel.omani.works`, (b) writes the canonical 6-record set, and (c) updates the parent-zone NS delegation via the OpenOva Dynadot registrar adapter using the K8s secret `dynadot-api-credentials/openova-system`. |
| **Catalyst-Zero login** | Your existing OpenOva-issued credentials for `console.openova.io`. Confirm you can log in BEFORE running the demo. |
| **kubectl context to Contabo** | For Step 1 only. SSH+kubectl on the Contabo VPS as user `openova`. |

If any of the above is missing, **stop here and gather it first** — partial input across wizard sessions is not preserved.

---

## Step 1 — Operator confirms Group C cutover and triggers Flux reconciliation

Group C (consolidation cutover from openova-private→openova-public GitRepository) was prepared by the Catalyst-Zero waterfall but is gated on operator confirmation because it touches the running cluster.

**On the Contabo VPS** (user `openova`):

```bash
# 1.1 Confirm the Group C branch is ready in openova-private
cd /home/openova/repos/openova-private
git fetch origin
git checkout group-c-cutover-catalyst-zero
git log --oneline -5
# Expect: a clean tip with the parallel openova-public GitRepository manifest

# 1.2 Merge to main (or have the operator do it via PR review)
git checkout main
git merge --ff-only group-c-cutover-catalyst-zero
git push origin main

# 1.3 Force Flux reconciliation immediately so the cutover lands now
kubectl annotate --overwrite gitrepository/flux-system -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)"
kubectl annotate --overwrite gitrepository/openova-public -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)"

# 1.4 Watch the cutover land (~5 min outage on a few non-critical paths,
# fully reversible by reverting the merge + re-annotating)
flux get kustomizations --watch
```

**Expected output:** every Kustomization moves through `Reconciling` → `Ready=True`. The website, contact-api, stalwart, axon, umami, langfuse, temporal, talentmesh, sme, console, marketplace, admin, billing, catalyst-api Kustomizations all settle as `Ready=True` within ~5 min. **The catalyst-api pod is now reading from `openova-public` (the catalyst-build CI pushes images here at SHA-pinned tags) and bootstrap CRDs/blueprints are sourced from this same GitRepository.**

**If it fails:**

- Kustomization stuck at `Ready=False` with `path /clusters/.../<x> not found` → the merge missed a file. Revert the merge, push, re-annotate.
- A workload pod CrashLooping after the cutover → image pull fails (likely GHCR auth on the new path). `kubectl describe pod` to confirm; check `ghcr-pull-secret` is present in the affected namespace.

When all Kustomizations are green and `kubectl -n catalyst get pods` shows `catalyst-api-*` Running with the SHA from `git log -1 --format=%H` matching the catalyst-build CI tag, **proceed to Step 2.**

---

## Step 2 — Provide real Hetzner credentials via the wizard at `console.openova.io/sovereign`

This is the **kickoff for the omantel.omani.works Sovereign**. Closes ticket [#149](https://github.com/openova-io/openova/issues/149).

```
https://console.openova.io/sovereign
```

Click **New Sovereign**. Walk the 7-step wizard (canonical order from `WIZARD_STEPS`, Org → Domain → Topology → Provider → Credentials → Components → Review):

| Step | Field | Value for omantel demo |
|---|---|---|
| 1. Organisation | Organisation name | `Omantel Cloud` |
| 1. Organisation | Contact / sovereign-admin email | The omantel-admin email — becomes initial sovereign-admin in Keycloak |
| 2. Domain | Domain mode | **Pool** (per #169 the other modes are `byo-manual` and `byo-api`) |
| 2. Domain | Pool domain | `omani.works` |
| 2. Domain | Subdomain | `omantel` (validated via `POST /api/v1/subdomains/check` → PDM `/v1/reserve`) |
| 3. Topology | Single-region vs multi-region | Single-region |
| 4. Provider | Cloud | Hetzner Cloud |
| 4. Provider | Region | `fsn1` (Falkenstein) — closest EU region with capacity for the demo |
| 4. Provider | Control plane size | `cpx21` (default) |
| 4. Provider | Worker size | `cpx31` (default) |
| 4. Provider | Worker count | `1` |
| 4. Provider | HA enabled | `false` (single-CP demo; HA is supported but adds €€ for the demo) |
| 5. Credentials | Hetzner API token | Paste the Read+Write token from Pre-flight (validated read-only via `POST /api/v1/credentials/validate`) |
| 5. Credentials | Hetzner project ID | The numeric project ID from Pre-flight |
| 5. Credentials | SSH public key | Paste the `*.pub` content from Pre-flight |
| 6. Components | Mandatory infra tab | Read-only — bp-cilium, bp-flux, bp-crossplane, bp-cert-manager, bp-spire, bp-nats-jetstream, bp-openbao, bp-keycloak, bp-gitea, bp-sealed-secrets, bp-powerdns. Always installed. |
| 6. Components | Apps tab | Leave defaults (apps come post-provisioning anyway). Per #175 dependency-aware cascades pull transitive deps automatically. |
| 7. Review | Show every captured value, **Provision** button | Click → catalyst-api accepts the request and starts streaming |

The wizard validates the token against `POST /api/v1/credentials/validate` and the subdomain against `POST /api/v1/subdomains/check` before letting you advance. If either rejects:

- **`token invalid` (401 from Hetzner)** → token has only Read scope, expired, or you copied a partial value. Generate a new one in the Hetzner Cloud Console with Read+Write scope.
- **`subdomain taken`** → another tenant already reserved `omantel.omani.works`. Pick a different subdomain (e.g. `omantel-demo`) or contact OpenOva to release the old reservation.
- **`pool domain not in catalog`** → `omani.works` is missing from the Catalyst-Zero pool catalog. This is a Group G regression; check `core/services/catalyst-api/internal/handler/subdomains.go`.

**Closes:** ticket [#149](https://github.com/openova-io/openova/issues/149) ("[M] dod: provision omantel.omani.works from console.openova.io/sovereign live").

---

## Step 3 — Click Provision and watch the SSE event stream

Click **Review** → **Provision**. The wizard POSTs to `POST /api/v1/deployments` on `console.openova.io` (which proxies to catalyst-api). The response carries a `deployment-id` and `streamURL`.

The wizard's progress page connects to `GET /api/v1/deployments/{id}/logs` (Server-Sent Events) and renders a per-phase progress widget. **You will see 11 phases** in dependency order (Phase 0 owned by catalyst-api's OpenTofu wrapper; Phase 1 by Flux on the new Sovereign):

| Phase | Owner | Typical duration |
|---|---|---|
| `tofu-init`        | catalyst-api OpenTofu workdir | <30s |
| `tofu-plan`        | catalyst-api OpenTofu workdir | ~30s |
| `tofu-apply`       | catalyst-api OpenTofu workdir | 4–6 min (hcloud server creation) |
| `tofu-output`      | catalyst-api OpenTofu workdir | <5s |
| `flux-bootstrap`   | catalyst-api OpenTofu workdir | ~1 min (cloud-init handshake) |
| `cilium`           | Flux on new Sovereign | 1–2 min |
| `cert-manager`     | Flux on new Sovereign | ~1 min |
| `flux`             | Flux on new Sovereign (self) | <30s |
| `crossplane`       | Flux on new Sovereign | 1–2 min |
| `sealed-secrets`   | Flux on new Sovereign | ~30s |
| `spire`            | Flux on new Sovereign | ~1 min |
| `jetstream`        | Flux on new Sovereign | ~1 min |
| `openbao`          | Flux on new Sovereign | 1–2 min |
| `keycloak`         | Flux on new Sovereign | 2–3 min |
| `gitea`            | Flux on new Sovereign | 1–2 min |
| `bp-catalyst-platform` | Flux on new Sovereign | 2–3 min |

Total wall-clock: **~10–12 minutes** for a clean run. The progress widget uses cf60bd7's failed-phase UX — if any phase goes red, you get a **Retry phase** button.

**If a phase fails:**

The retry button POSTs to `POST /api/v1/deployments/{id}/phases/{phase}/retry` (per [`products/catalyst/bootstrap/api/internal/handler/retry.go`](../products/catalyst/bootstrap/api/internal/handler/retry.go), shipped at commit `cf60bd7`). Behaviour depends on which phase failed:

- **Phase 0 retries** (`tofu-init`, `tofu-plan`, `tofu-apply`, `tofu-output`, `flux-bootstrap`) re-run `tofu apply` against the existing per-deployment workdir (idempotent — `tofu apply` on existing state). Most transient Hetzner errors clear with one retry. Reopen the SSE stream after clicking Retry; the progress widget reconnects.
- **Phase 1 retries** (the 11 bootstrap-kit HelmReleases) emit a structured event explaining that **Flux owns the retry loop** (`HelmRelease.spec.install.remediation.retries: 3`). The `cf60bd7` retry endpoint surfaces the exact `kubectl annotate` command the operator runs against the new Sovereign's kube-context if Flux's automatic retries have exhausted.

Manual retry (curl, e.g. for the `tofu-apply` phase that you want to re-drive without using the wizard UI):

```bash
DEPLOYMENT_ID=<copy from the wizard URL>
curl -sX POST "https://console.openova.io/api/v1/deployments/${DEPLOYMENT_ID}/phases/tofu-apply/retry" | jq .
```

The response is a JSON object with the new `streamURL`; reconnect the SSE stream there.

**If `tofu-apply` hangs >10 min** at `hcloud_server.control_plane[0]: Still creating...` — Hetzner regional capacity transient. Wait 15 min total; if still stuck, cancel via wizard, change region in Step 2, re-run.

---

## Step 4 — DNS auto-write to the per-Sovereign PowerDNS zone

After `tofu-output` resolves the LB IP, catalyst-api calls the pool-domain-manager (PDM) `/v1/commit` endpoint. PDM's commit transaction (#163, #167, #168, #170):

1. **Creates the per-Sovereign PowerDNS zone** `omantel.omani.works.` on the bp-powerdns deployment in `openova-system` (CNPG-backed `pdns-pg`, DNSSEC-signed with ECDSAP256SHA256, lua-records enabled).
2. **Writes the canonical 6-record set** into that zone via the PowerDNS REST API (`PATCH /api/v1/servers/localhost/zones/omantel.omani.works.`):

```
@                A → <LB-IP>
*                A → <LB-IP>
console          A → <LB-IP>
api              A → <LB-IP>
gitea            A → <LB-IP>
harbor           A → <LB-IP>
```

3. **Updates the parent-zone NS delegation.** For pool sovereigns this means PDM's Dynadot registrar adapter writes `omantel NS ns1.openova.io / ns2.openova.io / ns3.openova.io` into the `omani.works` zone at Dynadot. For BYO `byo-api` sovereigns the matching registrar adapter (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot, #170) does the same NS-flip at the customer's registrar; for `byo-manual` PDM skips the NS-flip and the wizard polls until the customer paste the NS list themselves.

**Verify after PDM /v1/commit completes:**

```bash
LB_IP=$(curl -sH "Authorization: Bearer ${YOUR_CONSOLE_TOKEN}" \
  "https://console.openova.io/api/v1/deployments/${DEPLOYMENT_ID}" \
  | jq -r '.result.loadBalancerIP')

# Authoritative answer from the per-Sovereign PowerDNS zone:
dig +short console.omantel.omani.works @ns1.openova.io
# Expected: <LB-IP>

# Recursive resolver answer (gated by parent-zone NS-delegation TTL):
dig +short console.omantel.omani.works
# Expected: <LB-IP> (within ~15 min — parent-zone NS TTL at Dynadot)

dig +short '*.omantel.omani.works'
# Expected: <LB-IP>
```

**If DNS doesn't propagate within 30 min:**

- Confirm the per-Sovereign zone exists: `kubectl -n openova-system exec deploy/powerdns -- pdnsutil list-zone omantel.omani.works`. Missing means PDM `/v1/commit` failed — check `kubectl -n openova-system logs deploy/pool-domain-manager` for the registrar-adapter error.
- Confirm the parent-zone NS delegation: `dig omantel.omani.works NS @ns1.dynadot.com` should return `ns1.openova.io.` etc. Missing means the registrar-adapter NS-flip failed — re-run `POST /api/v1/deployments/${DEPLOYMENT_ID}/phases/dns/retry`.
- **Never run `set_dns2` by hand for exploration** — each call wipes all records. See `~/.claude/projects/.../memory/feedback_dynadot_dns.md`. The right path is re-running PDM `/v1/commit` via the wizard retry endpoint, which uses `add_dns_to_current_setting=yes` inside the Dynadot adapter.

Closes the DNS portion of the DoD; the per-Sovereign PowerDNS zone model (#167/#168) + PDM commit (#163) + registrar adapters (#170) is what makes `omani.works` a usable pool domain.

---

## Step 5 — TLS auto-issue via cert-manager + Let's Encrypt

Once `bp-cert-manager` is `Ready=True` (Phase 1 phase #2) and the DNS records resolve, cert-manager's ClusterIssuer triggers Let's Encrypt:

- **Preferred:** DNS-01 challenge using cert-manager-webhook-pdns against the per-Sovereign PowerDNS zone (#167); enables wildcard certs.
- **Fallback:** HTTP-01 challenge (works as long as `*.omantel.omani.works` resolves to the LB and the Gateway routes `/.well-known/acme-challenge` to cert-manager's solver pod)

**Verify:**

```bash
# From your workstation, hit any *.omantel.omani.works URL after ~5 min
curl -vI https://console.omantel.omani.works 2>&1 | grep -E '(HTTP/|subject|issuer)'
# Expected: HTTP/2 200 (or 302 redirect to login), TLS subject CN matching the FQDN,
# issuer = "Let's Encrypt"
```

**If TLS issuance fails (cert-manager Challenge stuck):**

```bash
# On the new Sovereign (kubeconfig from the OpenTofu output, see Step 4 verify)
kubectl --kubeconfig=/path/to/omantel/kubeconfig -n cert-manager get challenges,orders,certificates
kubectl --kubeconfig=/path/to/omantel/kubeconfig -n cert-manager describe challenge
```

Most likely cause: Let's Encrypt rate-limit (50 certs/week/domain) — if you've been re-running the demo. Mitigation: switch the ClusterIssuer to `letsencrypt-staging` for demo-only testing, OR wait out the rate-limit window.

The `bp-cert-manager` HelmRelease has `install.remediation.retries: 3`; if all three exhausted, manually annotate to force a fresh attempt:

```bash
kubectl --kubeconfig=/path/to/omantel/kubeconfig annotate --overwrite \
  helmrelease/bp-cert-manager -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)"
```

---

## Step 6 — omantel-admin logs into `console.omantel.omani.works`

Closes ticket [#150](https://github.com/openova-io/openova/issues/150).

Once `bp-catalyst-platform` is `Ready=True` AND TLS is issued, the success screen in the Catalyst-Zero wizard becomes:

> **Done — your Sovereign is ready.**
> Console: https://console.omantel.omani.works
> Gitea: https://gitea.omantel.omani.works
> Admin: https://admin.omantel.omani.works
> Sovereign-admin email: \<the email from Step 2\>

Open `https://console.omantel.omani.works`. Sign in with the omantel-admin email. Keycloak's `catalyst-admin` realm sends a password-reset email; click the link, set a strong password (24+ chars per CLAUDE.md Rule 10), complete the realm flow, **arrive at the Catalyst console for the new Sovereign.**

**Verify the Sovereign survived k3s + Flux warmup:**

```bash
curl -sI https://console.omantel.omani.works/healthz
# Expected: HTTP/2 200, body {"status":"ok"}
```

**If the Keycloak reset email never arrives:**

SMTP not configured on the new Sovereign yet (Day-1 setup item per [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §5). Reset via the realm-admin path:

```bash
kubectl --kubeconfig=/path/to/omantel/kubeconfig -n catalyst-system exec -it keycloak-0 -- \
  /opt/keycloak/bin/kcadm.sh config credentials \
    --server http://localhost:8080 \
    --realm master \
    --user admin \
    --password "$(kubectl --kubeconfig=/path/to/omantel/kubeconfig -n catalyst-system \
      get secret keycloak-admin -o jsonpath='{.data.password}' | base64 -d)"
kubectl --kubeconfig=/path/to/omantel/kubeconfig -n catalyst-system exec -it keycloak-0 -- \
  /opt/keycloak/bin/kcadm.sh set-password \
    -r catalyst-admin -u "<omantel-admin-email>" \
    --new-password "$(python3 -c 'import secrets,string; print("".join(secrets.choice(string.ascii_letters+string.digits) for _ in range(32)))')"
```

(Print the new password to a file with `chmod 600`, NOT to stdout — see CLAUDE.md Rule 10.)

Once logged in, you should see the empty Catalyst console for omantel.omani.works — no Organizations yet, no Apps installed yet.

---

## Step 7 — omantel-admin issues a voucher via `/admin/billing`

Closes ticket [#151](https://github.com/openova-io/openova/issues/151).

Navigate to `https://admin.omantel.omani.works` (the admin app, not the console). Sign in with the same omantel-admin credentials. The admin app's left rail shows **Billing**. Click into **Billing → Vouchers**.

| Field | Value for demo |
|---|---|
| Code | `OMANTEL-DEMO-100` |
| Credit (OMR) | `100` |
| Description | `DoD demo voucher — first franchised Sovereign launch` |
| Active | `true` |
| Max redemptions | `1` |

Click **Save**. The admin UI POSTs to `POST /billing/vouchers/issue` (per [`docs/FRANCHISE-MODEL.md`](FRANCHISE-MODEL.md) and [`core/services/billing/handlers/vouchers.go`](../core/services/billing/handlers/vouchers.go)). Response:

```json
{
  "code": "OMANTEL-DEMO-100",
  "credit_omr": 100,
  "description": "DoD demo voucher — first franchised Sovereign launch",
  "active": true,
  "max_redemptions": 1,
  "times_redeemed": 0,
  "created_at": "2026-04-28T..."
}
```

**Verify via curl from the operator workstation** (as a final shape-check that the voucher API is propagated correctly per the franchise invariant):

```bash
SOV=https://api.omantel.omani.works
TOKEN=<the omantel-admin JWT, copy from the admin app's localStorage or browser devtools>
curl -s -H "Authorization: Bearer $TOKEN" "$SOV/billing/vouchers/list" | jq .
# Expected: [ { "code":"OMANTEL-DEMO-100", "credit_omr":100, ... } ]
```

**If issuance fails:**

- 403 → JWT lacks `sovereign-admin` claim. Confirm the omantel-admin user has the correct realm role in Keycloak (`catalyst-admin` realm → Users → omantel-admin → Role mappings → realm-roles → `sovereign-admin`).
- 500 → billing service DB not migrated. `kubectl --kubeconfig=/path/to/omantel/kubeconfig -n catalyst-system logs deploy/billing | tail -50` and look for migration errors. The `core/services/billing/store.Migrate()` runs on first start.

---

## Step 8 — Tenant redeems voucher at `omantel.omani.works/redeem?code=OMANTEL-DEMO-100`

Closes ticket [#152](https://github.com/openova-io/openova/issues/152).

The voucher distribution URL (per [`docs/FRANCHISE-MODEL.md`](FRANCHISE-MODEL.md) §"Redemption flow"):

```
https://omantel.omani.works/redeem?code=OMANTEL-DEMO-100
```

This is the **public, unauthenticated landing page** ([`core/marketplace/src/pages/redeem.astro`](../core/marketplace/src/pages/redeem.astro)). Open in a fresh browser session (incognito) — the fictional Omantel SME tenant is NOT logged in yet.

The page:

1. Reads `?code=OMANTEL-DEMO-100` from the URL
2. POSTs to `/api/billing/vouchers/redeem-preview` (rate-limited at ingress; no auth)
3. Renders `{credit_omr: 100, description: "DoD demo voucher...", accepting_redemptions: true}`
4. Shows **Sign up to redeem** button → routes to `/plans` with the code stashed in localStorage as `sme-pending-voucher`

**Verify the preview without going through the UI:**

```bash
curl -s -X POST "https://api.omantel.omani.works/billing/vouchers/redeem-preview" \
  -H "Content-Type: application/json" \
  -d '{"code":"OMANTEL-DEMO-100"}' | jq .
# Expected: { "code":"OMANTEL-DEMO-100", "credit_omr":100, "active":true, "accepting_redemptions":true }
```

**If the page renders "voucher not valid":**

- Spelling mismatch in the URL (case-sensitive on display, case-insensitive on the API — but typos in the UUID portion fail).
- Voucher was issued on a different Sovereign (Catalyst-Zero, not omantel) — vouchers are scoped to the issuing Sovereign per franchise model.
- Keycloak realm sync issue — the API response actually returns 500 not 404. Check `kubectl logs deploy/billing` for the SQL error.

---

## Step 9 — Tenant signs up, creates Org+Env+App, voucher is consumed at checkout

Closes tickets [#153](https://github.com/openova-io/openova/issues/153), [#154](https://github.com/openova-io/openova/issues/154), [#155](https://github.com/openova-io/openova/issues/155), [#156](https://github.com/openova-io/openova/issues/156).

Click **Sign up to redeem**. The marketplace's signup wizard (already implemented, lives at `/plans` → `/checkout`) walks the tenant through:

1. **Email + magic-link** (or Google OAuth) → fictional tenant authenticates as e.g. `kestrel-pharmacy@example.com`
2. **Catalyst auto-creates an Organization** for the tenant (default name `kestrel-pharmacy`)
3. **The voucher is applied at first checkout** via `POST /billing/checkout` with `promo_code: "OMANTEL-DEMO-100"`. The redemption is transactional with the Order — atomic insert into `promo_redemptions`, increment of `times_redeemed`, positive entry in `credit_ledger`.
4. **Tenant lands in the marketplace** — credit balance shown as **100 OMR** in the top-right wallet
5. **Tenant creates an Environment** in their Organization (e.g. `production`)
6. **Tenant installs first Application** — picks any zero-tier App (e.g. `bp-wordpress`). The App install consumes a small amount from the credit_ledger; remaining balance shown.
7. **Tenant reaches their App URL** — Catalyst provisions the App's vcluster scope, Crossplane composes the App's resources, the App's URL becomes reachable (e.g. `https://kestrel-pharmacy-production-wordpress.omantel.omani.works`)

**Verify the redemption was consumed (back in admin app from Step 7):**

```bash
SOV=https://api.omantel.omani.works
TOKEN=<the omantel-admin JWT>
curl -s -H "Authorization: Bearer $TOKEN" "$SOV/billing/vouchers/list" | jq '.[] | select(.code=="OMANTEL-DEMO-100")'
# Expected: { "times_redeemed":1, "max_redemptions":1, ... }
# (i.e. the voucher is now exhausted — single-use demo)
```

**If the App install hangs at "Provisioning":**

- Crossplane Composition for the App is missing or unhealthy. Check `kubectl --kubeconfig=/path/to/omantel/kubeconfig get compositions,xrds`.
- Catalyst-platform umbrella didn't fully reconcile — `kubectl --kubeconfig=/path/to/omantel/kubeconfig -n flux-system get helmreleases` and verify `bp-catalyst-platform` is `Ready=True`.

**If the App URL returns 404:**

- DNS for the App-specific subdomain hasn't propagated. The wildcard `*.omantel.omani.works` already resolves to the LB IP; the LB's Gateway routes by hostname. If `kubectl --kubeconfig=... -n <tenant-ns> get gateways,httproutes` shows the route, `dig` will confirm and `curl -k` will reach the App pod.

---

## Final step — append VALIDATION-LOG entry and close out

Closes ticket [#157](https://github.com/openova-io/openova/issues/157).

```bash
cd /home/openova/repos/openova
git checkout main
git pull origin main

# Append the Pass entry
cat >> docs/VALIDATION-LOG.md <<'EOF'

## Pass NNN (2026-MM-DD) — DoD MET — first franchised Sovereign live

**Operator:** <name>
**Sovereign FQDN:** omantel.omani.works
**Hetzner region:** fsn1
**Total wall-clock from Provision-click to App-URL-reachable:** ~MM minutes
**Voucher exercised:** OMANTEL-DEMO-100 (100 OMR, 1/1 redeemed)
**App installed:** bp-wordpress at kestrel-pharmacy-production-wordpress.omantel.omani.works

DoD Met:
- [x] Group C cutover applied, Flux reconciled clean
- [x] Wizard provisioned omantel.omani.works in ~10 min
- [x] DNS authoritative on the per-Sovereign PowerDNS zone; parent-zone NS-delegation written by PDM via the Dynadot registrar adapter
- [x] TLS auto-issued via cert-manager + Let's Encrypt
- [x] omantel-admin logged into console.omantel.omani.works
- [x] Voucher issued via /admin/billing
- [x] Tenant redeemed at omantel.omani.works/redeem
- [x] Tenant created Org + Env, installed first App, App URL reached HTTP/2 200

EOF

git add docs/VALIDATION-LOG.md
git -c user.name="hatiyildiz" -c user.email="hatiyildiz@openova.io" \
  commit -m "docs(validation-log): DoD MET — first franchised Sovereign live"
git push origin main
```

Move all Group M tickets (#149–#157) to `status/completed`:

```bash
for n in 149 150 151 152 153 154 155 156 157; do
  gh issue edit $n \
    --remove-label "status/in-progress" \
    --remove-label "status/uat" \
    --add-label "status/completed" \
    --repo openova-io/openova
done
```

(Per CLAUDE.md Rule 9.7: **NEVER close issues** — only the user closes after verification.)

---

## Decommission (post-demo cleanup)

```bash
DEPLOYMENT_ID=<the deployment ID from Step 3>
curl -s -X POST "https://console.openova.io/api/v1/deployments/${DEPLOYMENT_ID}/destroy"
# (Implements `tofu destroy -auto-approve` against the per-deployment workdir.)
```

If the destroy endpoint is not yet implemented on catalyst-api (it will be — see PROVISIONING-PLAN.md "Decommission" section), the manual fallback is:

```bash
# On the Contabo VPS:
kubectl -n catalyst-system exec -it deploy/catalyst-api -- \
  sh -c "cd /var/lib/catalyst/tofu/omantel.omani.works && \
         HCLOUD_TOKEN=<the same token from Step 2> \
         tofu destroy -auto-approve -no-color"
```

After destroy, **verify**:

```bash
# Hetzner Cloud Console → Servers → empty for the project
# Hetzner Cloud Console → Load balancers → empty for the project
dig +short console.omantel.omani.works  # may still resolve until parent-zone NS-delegation TTL expires (~15 min, set at Dynadot for pool sovereigns)
```

The voucher row stays in the billing DB (soft-delete preserves audit trail per #91). To purge for a true cold start, drop the per-Sovereign Postgres PVC — but for the DoD demo, leaving the row is correct.

---

## Reference

- [`docs/INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) — non-negotiable rules
- [`docs/PROVISIONING-PLAN.md`](PROVISIONING-PLAN.md) — canonical 8-phase plan
- [`docs/SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) — architectural contract
- [`docs/RUNBOOK-PROVISIONING.md`](RUNBOOK-PROVISIONING.md) — operator-level wizard guide (this file's parent)
- [`docs/FRANCHISE-MODEL.md`](FRANCHISE-MODEL.md) — voucher mechanism
- [`docs/ORCHESTRATOR-STATE.md`](ORCHESTRATOR-STATE.md) — live waterfall state
- [`tests/dod/dod_test.go`](../tests/dod/dod_test.go) — Go test that drives this same flow non-interactively when `HETZNER_TEST_TOKEN` is set

---

*Part of [OpenOva](https://openova.io). The DoD demo is the proof — per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #7, "DoD E2E 2-pass GREEN on the current deployed SHA is the ONLY valid proof of done."*
