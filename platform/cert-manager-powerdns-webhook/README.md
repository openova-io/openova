# bp-cert-manager-powerdns-webhook

Catalyst Blueprint for the cert-manager DNS-01 external webhook for
PowerDNS. Closes [openova#373](https://github.com/openova-io/openova/issues/373).

## What it is

A wrapper around the upstream
[zachomedia/cert-manager-webhook-pdns](https://github.com/zachomedia/cert-manager-webhook-pdns)
binary that satisfies cert-manager's external webhook contract
(`webhook.acme.cert-manager.io/v1alpha1` — `Present` / `CleanUp` on a
`ChallengeRequest`) and writes ACME challenge TXT records to the
per-Sovereign PowerDNS instance via PowerDNS's REST API.

The chart structure mirrors `bp-cert-manager-dynadot-webhook` (its
sibling for the parent-zone DNS-01 path); the only differences are the
upstream image (`zachomedia/cert-manager-webhook-pdns` vs the Catalyst-
authored Dynadot binary) and the per-issuer config block (PowerDNS host
+ API key vs Dynadot api-key + api-secret + managed-domains).

## Why two webhooks (dynadot + powerdns) on the same fleet

The two webhooks solve DNS-01 challenges against **different DNS
authorities** at **different lifecycle stages** of a Sovereign:

| Stage | Authority | Solver | Blueprint |
|---|---|---|---|
| Onboarding (parent-zone cert) | Dynadot (`omani.works`) | `dynadot` | `bp-cert-manager-dynadot-webhook` |
| Post-handover (Sovereign-zone cert) | Sovereign's own PowerDNS (`omantel.omani.works`) | `powerdns` | `bp-cert-manager-powerdns-webhook` |

Per ADR-0001 §9.4 the goal is **zero contabo dependency** post-handover:
once a Sovereign's NS delegation flips, its TLS renewals must run
entirely against its own PowerDNS — no reachback to openova-controlled
Dynadot credentials. This blueprint is the post-handover companion.

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
| ClusterRole + ClusterRoleBinding (secret-reader) | Grants the SA `get` on Secrets cluster-wide so it can read the operator-supplied PowerDNS API-key Secret on demand. |
| ClusterRole + ClusterRoleBinding (domain-solver) | Lets cert-manager `create` ChallengeRequest CRs in the webhook's API group. |
| ClusterIssuer (`letsencrypt-dns01-prod-powerdns`) | Paired DNS-01 issuer. Only renders when `clusterIssuer.enabled=true` AND `powerdns.host` is set (skip-render pattern). |

## Pairing with bp-cert-manager and bp-powerdns

The blueprint declares both as `depends:` in `blueprint.yaml`:
- `bp-cert-manager` provides the cert-manager controllers + CRDs.
- `bp-powerdns` provides the per-Sovereign DNS authority + REST API.

Flux `dependsOn` enforces ordering at the HelmRelease level (see
`clusters/_template/bootstrap-kit/<NN>-bp-cert-manager-powerdns-webhook.yaml`).

## Configuration (per-Sovereign overlay)

The chart's defaults render a runnable webhook + skip the ClusterIssuer.
Cluster overlays MUST set the following for the issuer to materialise:

```yaml
clusterIssuer:
  enabled: true
  email: ops@<sovereign-fqdn>
  acmeServer: https://acme-v02.api.letsencrypt.org/directory   # or staging during bring-up

powerdns:
  # In-cluster routing (preferred — no external network hop, no public TLS):
  host: "http://powerdns.powerdns:8081"
  # ...or external routing when the API is fronted by an ingress:
  # host: "https://pdns.<sovereign-fqdn>"
  apiKeySecretRef:
    name: powerdns-api-credentials   # canonical name from bp-powerdns
    key: api-key
    namespace: openova-system        # canonical namespace; webhook reads cross-NS via clientset
```

Per `docs/INVIOLABLE-PRINCIPLES.md` #4 every URL/zone is operator-
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

- Upstream: https://github.com/zachomedia/cert-manager-webhook-pdns
- Sibling: `platform/cert-manager-dynadot-webhook/` — parent-zone DNS-01 solver
- `platform/cert-manager/chart/templates/clusterissuer-letsencrypt-dns01.yaml` — the dynadot-side ClusterIssuer
- `platform/powerdns/` — the per-Sovereign DNS authority this webhook talks to
- [openova#373](https://github.com/openova-io/openova/issues/373) — closing issue
- [cert-manager DNS-01 webhook docs](https://cert-manager.io/docs/configuration/acme/dns01/webhook/)
