# bp-cert-manager-dynadot-webhook

Catalyst Blueprint for the cert-manager DNS-01 external webhook for
Dynadot. Closes [openova#159](https://github.com/openova-io/openova/issues/159).

## What it is

A Go binary that satisfies cert-manager's external webhook contract
(`webhook.acme.cert-manager.io/v1alpha1` — `Present` / `CleanUp` on a
`ChallengeRequest`) and writes ACME challenge TXT records to a
Dynadot-managed pool domain via the api3.json endpoint.

The binary lives at `core/cmd/cert-manager-dynadot-webhook/`. The
HTTP transport, command builders, and zone-safety contract live in
`core/pkg/dynadot-client/` and are shared with the other Catalyst
services that talk to Dynadot (pool-domain-manager, catalyst-dns).

## Why this exists separately from external-dns-dynadot-webhook

cert-manager's webhook contract and external-dns's webhook contract are
DIFFERENT protocols. external-dns expects a sidecar that implements
`records.list / records.add / records.delete` over an HTTP RPC schema;
cert-manager expects an aggregated Kubernetes apiserver that responds to
ChallengeRequest CRs. The two binaries cannot share code at the
transport layer. They DO share the underlying Dynadot HTTP client at
`core/pkg/dynadot-client/`.

## What this chart deploys

| Resource | Purpose |
|---|---|
| Deployment | Runs the webhook binary as a non-root pod in the chart's release namespace. |
| Service | ClusterIP fronting the Deployment on port 443. |
| APIService | Registers `v1alpha1.acme.dynadot.openova.io` so the kube-apiserver routes ChallengeRequest calls to the Service. |
| Issuer (selfsigned) | Bootstraps the CA chain that issues the webhook's serving cert. |
| Issuer (CA) | Signs the leaf serving cert from the CA Secret. |
| Certificate (CA) | Root CA cert used by the APIService's `cert-manager.io/inject-ca-from` annotation. |
| Certificate (serving) | Leaf cert mounted into the Deployment at `/tls`. |
| ServiceAccount | Identity for the Deployment. |
| ClusterRoleBinding (auth-delegator) | Lets the aggregated apiserver delegate auth back to kube-apiserver. |
| RoleBinding (auth-reader) | Reads `extension-apiserver-authentication` ConfigMap from `kube-system`. |
| Role + RoleBinding (dynadot secret) | Grants the SA read access to the Dynadot credentials Secret in the configured namespace. |

## Pairing with bp-cert-manager

`bp-cert-manager`'s `letsencrypt-dns01-prod` ClusterIssuer points at this
webhook via `solvers[].dns01.webhook.groupName + solverName`. The two
charts MUST be deployed on the same Sovereign and bp-cert-manager-dynadot-
webhook MUST be Ready before any wildcard `Certificate` is requested.

The `bp-cert-manager` chart now ships with `dns01.enabled: true` by
default (changed in this PR — was `false` while the webhook was being
built). The interim `letsencrypt-http01-prod` issuer remains templated
as the rollback path; flip `certManager.issuers.dns01.enabled=false` in
the umbrella values to disable wildcard issuance and continue with
per-host certs.

## Credentials

The webhook reads three values from a Kubernetes Secret in its release
namespace:

| Env var | Default secret key |
|---|---|
| `DYNADOT_API_KEY` | `api-key` |
| `DYNADOT_API_SECRET` | `api-secret` |
| `DYNADOT_MANAGED_DOMAINS` | `domains` (legacy fallback: `domain`) |

The canonical secret (`dynadot-api-credentials` in `openova-system`) is
shared with `pool-domain-manager` and `catalyst-dns`. Because Pod
`secretKeyRef` cannot cross namespaces, the cluster overlay MUST
replicate the secret into the webhook's release namespace via
ExternalSecret (preferred) or reflector annotations. See
`clusters/_template/dynadot-credentials-replication.yaml`.

## Domain allowlist

`DYNADOT_MANAGED_DOMAINS` is a comma- or whitespace-separated allowlist
of pool domains the webhook is permitted to mutate. ChallengeRequests
for domains NOT under any allowlisted apex are rejected before any
Dynadot API call is made. This is the same defence pattern
pool-domain-manager and catalyst-dns use; it prevents a misconfigured
ClusterIssuer from causing the webhook to write to a third-party domain.

## Zone safety

The shared `core/pkg/dynadot-client/` enforces the safety contract
documented in `memory/feedback_dynadot_dns.md`: every mutation either
uses the append path (`add_dns_to_current_setting=yes`) or performs a
read-modify-write via `domain_info → set_dns2`. The destructive
zone-wipe variant of `set_dns2` is unexported. The webhook's `Present`
path uses `AddRecord` (append); `CleanUp` uses `RemoveSubRecord`
(read-modify-write that match-deletes a single record).

## Smoke test

Once both charts are reconciled on a Sovereign:

```bash
# Verify the webhook is running and the APIService is healthy
kubectl get -n cert-manager deploy/release-name-bp-cert-manager-dynadot-webhook
kubectl get apiservices.apiregistration.k8s.io v1alpha1.acme.dynadot.openova.io

# Issue a wildcard cert against the Sovereign apex
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wildcard-omantel-omani-works
  namespace: cilium-gateway
spec:
  secretName: wildcard-omantel-omani-works-tls
  issuerRef:
    name: letsencrypt-dns01-prod
    kind: ClusterIssuer
  dnsNames:
    - "*.omantel.omani.works"
EOF

# Watch the Order + Challenge progress
kubectl get certificate,order,challenge -A -w
```

## See also

- `core/cmd/cert-manager-dynadot-webhook/` — binary source
- `core/pkg/dynadot-client/` — shared Dynadot HTTP client
- `platform/cert-manager/chart/templates/clusterissuer-letsencrypt-dns01.yaml` — paired ClusterIssuer
- [openova#159](https://github.com/openova-io/openova/issues/159) — closing issue
- [cert-manager DNS-01 webhook docs](https://cert-manager.io/docs/configuration/acme/dns01/webhook/)
