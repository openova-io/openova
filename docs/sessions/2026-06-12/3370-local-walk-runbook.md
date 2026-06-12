# #3370 local evidence walk — reproduction runbook

Every #3370 DoD proof ran the REAL code of the PR branch on an isolated local
stack (nothing touched hw130; zero-touch held):

```
chromium (playwright) → vite dev :5173 (proxy /api + /catalyst + /auth/handover)
  → whoami shim :8080 (answers ONLY GET /api/v1/whoami; forwards everything else)
    → REAL catalyst-api binary :18080 (cmd/api of the branch)
      → REAL kube-apiserver v1.36 + etcd (envtest assets, token-auth, RBAC)
```

## Boot the control plane

1. `setup-envtest use -p path` → etcd/kube-apiserver/kubectl binaries.
2. etcd on `127.0.0.1:12379`; kube-apiserver on `:16443` with a self-signed
   serving cert carrying a `127.0.0.1` SAN, `--token-auth-file` granting
   `system:masters` to a static token, `--disable-admission-plugins=ServiceAccount`.
3. kubeconfig: token user against `https://127.0.0.1:16443`.

## Seed the hw130-mimic state

1. Apply `products/catalyst/chart/crds/application.yaml` + `blueprint.yaml`
   (the CEL rules compile = first proof) + stub CRDs for
   `helmreleases.helm.toolkit.fluxcd.io` and CNPG `clusters`/`databases`
   (preserve-unknown-fields + status subresource).
2. Namespaces: flux-system, shared-data, the 7 consumer namespaces, acme, valkey, catalyst.
3. The three slot HRs (16a/16c/16d) with `SOVEREIGN_ENABLE_SHARED_PG=true`
   substituted, statuses patched Ready=True (`--subresource=status`).
4. `helm template` bp-postgres per slot values with
   `--api-versions postgresql.cnpg.io/v1 --api-versions apps.openova.io/v1`,
   applied — this is where the chart SELF-REGISTERS the three Application CRs.
5. Reflector pushes simulated: each hub Secret copied into its consumer
   namespace (exactly what bp-reflector does live).
6. Catalog-seed Blueprint CRs applied (the chained catalog client's
   in-cluster fallback source).

## Run the real binaries

- `application-controller`: `KUBECONFIG=<envtest> go run ./cmd` — its existing
  local-kubeconfig fallback (cmd/main.go). Watch the
  `bootstrap-owned adoption: status-only reconcile` lines; capture
  `kubectl get hr -A` before/after (sha256-identical = DoD-2).
- `catalyst-api`: the branch binary with `rest.InClusterConfig` satisfied via
  `KUBERNETES_SERVICE_HOST/PORT` env + token/ca files under
  `/var/run/secrets/kubernetes.io/serviceaccount` (sudo; REMOVE
  `/var/lib/catalyst` afterwards — its existence flips the handler store onto
  disk and pollutes the `TestLoad_*` suite, which assumes the CI shape).
  Env: `SOVEREIGN_FQDN`, `CATALYST_SELF_DEPLOYMENT_ID`,
  `CATALYST_HANDOVER_JWT_PUBLIC_PATH` (public key derived from the hw130
  signing key), `CATALYST_SESSION_COOKIE_SECRET`,
  `CATALYST_DEPLOYMENTS_DIR`/`CATALYST_TOFU_WORKDIR` under /tmp.
- whoami shim: auth is Keycloak-backed and not a #3370 surface; the local API
  runs with RequireSession in its documented no-KC passthrough. The shim
  answers ONLY `/api/v1/whoami` with the sovereign-admin identity and
  forwards every other byte to the real API.
- UI: `VITE_CATALYST_MODE=sovereign` in `.env.local`, `npx vite`.

## The walk

Playwright (isolated chromium, NOT the shared MCP browser) drives:
/apps → /catalog/postgres → /app/shared-pg/contexts → /app/wiki/dependencies
→ New-instance dialog (topology + the generic backing selector; Create new
default / Reuse existing dropdown) → submit reuse → the new Context row on
the target instance → /catalog/valkey + /app/demo-cache/contexts (generality)
→ Catalog-tab badges. Screenshots land in `evidence/3370-*.png`.

## Defects this walk caught (all fixed in the PR)

1. `createApplicationInstance` POSTed to `/api/catalyst/v1/...` → chi 404
   (the #2878/#2884 prefix-bug class on the CREATE call).
2. No console `/app/$componentId/$tab` route → the ⛓ badge had no deep-link.
3. `contextSchemaFor` dialed the chained catalog per blueprint inside the
   10s-budgeted /sovereign/apps path → instance cards silently vanished;
   flipped to embedded-catalog-first.
4. No console-created Application CR had EVER passed real CRD admission:
   blueprintRef.version never stamped, placement carried the raw topology
   string, and the regions[] pattern rejected every real region key
   (kom4dc digits — the #3195 Continuum precedent).
5. The adoption status writer re-triggered itself via its own watch events
   (~600ms/CR hot loop) → no-change guard.

Refs #3370.
