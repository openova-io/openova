# bp-cert-manager-powerdns-webhook

Catalyst Blueprint for the cert-manager DNS-01 external webhook for
PowerDNS. Closes [openova#373](https://github.com/openova-io/openova/issues/373).

## What it is

A wrapper around the upstream
[zachomedia/cert-manager-webhook-pdns](https://github.com/zachomedia/cert-manager-webhook-pdns)
binary that satisfies cert-manager's external webhook contract
(`webhook.acme.cert-manager.io/v1alpha1` — `Present` / `CleanUp` on a
`ChallengeRequest`) and writes ACME challenge TXT records to the
**central openova PowerDNS** (authoritative for `omani.works`) via
PowerDNS's REST API at `https://pdns.openova.io`.

This blueprint **supersedes** `bp-cert-manager-dynadot-webhook` for
omani.works Sovereigns: `omani.works` is registered at Dynadot but is
delegated to `ns1/2/3.openova.io` which run on contabo's PowerDNS.
Dynadot is NOT the API-level authority for omani.works subdomains;
contabo PowerDNS is. Caught live on otech43–46 where the dynadot
webhook silently failed to write challenge TXT records visible on the
public DNS chain.

## How DNS-01 validation works for `*.${SOVEREIGN_FQDN}`

When Let's Encrypt validates a DNS-01 challenge for
`*.otechN.omani.works`, its resolvers walk the public DNS chain:
Dynadot → ns1/2/3.openova.io (contabo PowerDNS). Until pool-domain-
manager has committed the per-Sovereign NS delegation into contabo
PowerDNS — and that delegation has propagated — the Sovereign's own
PowerDNS is INVISIBLE on the public chain.

This webhook writes the ACME challenge TXT record DIRECTLY to contabo's
central PowerDNS, so Let's Encrypt validation succeeds on the first
attempt regardless of whether the Sovereign-side delegation has sealed.

## What this chart deploys

| Resource | Purpose |
|---|---|
| Deployment | Runs the upstream `zachomedia/cert-manager-webhook-pdns` image as a non-root pod. |
| Service | ClusterIP fronting the Deployment on port 443. |
| APIService | Registers `v1alpha1.acme.powerdns.openova.io` so the kube-apiserver routes ChallengeRequest calls to the Service. |
| Issuer (selfsigned) | Bootstraps the CA chain that issues the webhook's serving cert. |
| Issuer (CA) | Signs the leaf serving cert from the CA Secret. |
| Certificate (CA) | Root CA cert used by the APIService's `cert-manager.io/inject-ca-from` annotation. |
| Certificate (serving) | Leaf cert mounted into the Deployment at `/tls`. |
| ServiceAccount | Identity for the Deployment. |
| ClusterRoleBinding (auth-delegator) | Lets the aggregated apiserver delegate auth back to kube-apiserver. |
| RoleBinding (auth-reader) | Reads `extension-apiserver-authentication` ConfigMap from `kube-system`. |
| ClusterRole + ClusterRoleBinding (secret-reader) | Grants the SA `get` on Secrets cluster-wide so it can read the PowerDNS API-key Secret on demand. |
| ClusterRole + ClusterRoleBinding (domain-solver) | Lets cert-manager `create` ChallengeRequest CRs in the webhook's API group. |
| ClusterIssuer (`letsencrypt-dns01-prod-powerdns`) | Paired DNS-01 issuer. Renders when `clusterIssuer.enabled=true` (chart's default `powerdns.host=https://pdns.openova.io` is sufficient for the omani.works pool; cluster overlays may override the host for non-omani.works pools). |

## Pairing with bp-cert-manager

The blueprint declares `bp-cert-manager` as `depends:` in `blueprint.yaml`
(provides the cert-manager controllers + CRDs). It does NOT depend on
`bp-powerdns` — the webhook calls contabo's central PowerDNS, an
out-of-cluster endpoint, not the Sovereign's local PowerDNS.

Flux `dependsOn` enforces ordering at the HelmRelease level (see
`clusters/_template/bootstrap-kit/49-bp-cert-manager-powerdns-webhook.yaml`).

## Configuration (per-Sovereign overlay)

The chart's defaults render a runnable webhook + skip the ClusterIssuer
(default `clusterIssuer.enabled=false` for safe CI smoke renders).
Sovereign overlays flip `clusterIssuer.enabled=true` and set the email:

```yaml
clusterIssuer:
  enabled: true
  email: ops@<sovereign-fqdn>
  acmeServer: https://acme-v02.api.letsencrypt.org/directory   # or staging during bring-up

# `powerdns.host` defaults to https://pdns.openova.io (contabo central
# PowerDNS, authoritative for omani.works). Override only when
# provisioning a Sovereign in a non-omani.works pool.
# powerdns:
#   host: "https://pdns.<other-pool>"
```

The credential Secret `powerdns-api-credentials` MUST live in the
`cert-manager` namespace on every Sovereign (the upstream webhook
ignores any `namespace:` field on the apiKeySecretRef and reads the
Secret from cert-manager's cluster-resource-namespace). The Secret's
`api-key` value MUST match the API key configured on contabo's central
PowerDNS — provisioned by cloud-init at control-plane boot time
(infra/providers/_shared/cloudinit-control-plane.tftpl).

Per `docs/PRINCIPLES.md` #4 every URL/zone is operator-
overridable. No hardcoded `omantel.omani.works` lives in this chart.

## Smoke test

Once both charts (bp-cert-manager + bp-cert-manager-powerdns-webhook)
are reconciled on a Sovereign:

```bash
# Verify the webhook is running and the APIService is healthy
kubectl get -n cert-manager deploy/release-name-bp-cert-manager-powerdns-webhook
kubectl get apiservices.apiregistration.k8s.io v1alpha1.acme.powerdns.openova.io

# Issue a wildcard cert against the Sovereign apex
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wildcard-omantel-omani-works
  namespace: kube-system
spec:
  secretName: wildcard-omantel-omani-works-tls
  issuerRef:
    name: letsencrypt-dns01-prod-powerdns
    kind: ClusterIssuer
  dnsNames:
    - "*.omantel.omani.works"
EOF

# Watch the Order + Challenge progress
kubectl get certificate,order,challenge -A -w
```

## See also

- `docs/ARCHITECTURE.md` — Catalyst architecture; this webhook sits in the cert-issuance / DNS-01 path
- Upstream: https://github.com/zachomedia/cert-manager-webhook-pdns
- `platform/cert-manager/chart/templates/clusterissuer-letsencrypt-dns01.yaml` — legacy `letsencrypt-dns01-prod` (now default-disabled; was dynadot-backed)
- `platform/powerdns/` — the per-Sovereign DNS authority for app-level records (NOT in the cert-issuance path)
- [openova#373](https://github.com/openova-io/openova/issues/373) — closing issue
- [cert-manager DNS-01 webhook docs](https://cert-manager.io/docs/configuration/acme/dns01/webhook/)
