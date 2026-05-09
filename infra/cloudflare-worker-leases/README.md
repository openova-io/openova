# `infra/cloudflare-worker-leases/` — OpenTofu module for the Continuum lease-witness Worker

Slice K-Cont-4 of EPIC-6 (#1101). Deploys the Worker source from `products/continuum/cloudflare-worker/` to Cloudflare alongside its KV namespace and env-bound bearer-token allow-list.

## Inputs

| Variable | Required | Description |
|---|---|---|
| `cloudflare_account_id` | yes | 32-char hex. Read from CF dashboard. |
| `cloudflare_api_token` | yes (sensitive) | API token with `Workers Scripts: Edit` + `Workers KV Storage: Edit`. |
| `bearer_tokens_csv` | yes (sensitive) | Comma-separated allow-list of bearer tokens. Source: per-Sovereign SealedSecret. |
| `worker_name` | no (default `openova-continuum-lease-witness`) | Worker script name (becomes part of the URL). |
| `kv_namespace_title` | no (default `openova-continuum-leases`) | KV namespace display title. |
| `log_level` | no (default `info`) | One of `error` / `info` / `debug`. |
| `worker_script_path` | no (default `../../products/continuum/cloudflare-worker/dist/index.js`) | Path to the bundled Worker JS. Run `npm run build:dryrun` in the worker dir first. |

## Outputs

| Output | Use for |
|---|---|
| `worker_url` | Paste into Continuum CR `spec.leaseClient.config.baseURL`. |
| `kv_namespace_id` | Ad-hoc `wrangler kv:key list` introspection. |
| `worker_name` | Deployed Worker script name. |

## Operator runbook

1. **Build the Worker bundle**:
   ```sh
   cd ../../products/continuum/cloudflare-worker
   npm install && npm run build:dryrun
   ```
   This produces `dist/index.js` (the bundled Worker script).

2. **Extract bearer tokens** from the per-Sovereign SealedSecret:
   ```sh
   kubectl --kubeconfig <sov-kubeconfig> get secret -n catalyst-system \
     continuum-cf-witness-tokens -o jsonpath='{.data.token}' | base64 -d
   ```
   For rotation, prepare a `<new>,<old>` CSV.

3. **Apply**:
   ```sh
   cd ../infra/cloudflare-worker-leases
   tofu init
   TF_VAR_cloudflare_account_id=<acct> \
   TF_VAR_cloudflare_api_token=<token> \
   TF_VAR_bearer_tokens_csv='<csv>' \
     tofu apply
   ```

4. **Wire the Continuum CR** with the `worker_url` output. The catalyst-controllers reconciler picks up the change on next reconcile (~30s); verify via `kubectl logs -n catalyst-controllers continuum-controller-*`.

## Why no `cloudflare_workers_route`?

The Worker's default `*.workers.dev` URL is sufficient for the controller's HTTPS calls. A custom hostname (e.g. `lease.openova.io`) would require zone setup outside this module. Operators wanting a custom domain set `spec.leaseClient.config.baseURL` directly to the custom hostname AFTER setting up the route in the CF dashboard or a sibling tofu module.

## What this module does NOT do

- **Doesn't deploy automatically.** Per the K-Cont-4 brief, `tofu apply` is operator-run. CI only verifies `tofu validate` + `tofu fmt -check`.
- **Doesn't manage the SealedSecret.** That's a `clusters/<sov>/...` overlay artifact; the operator extracts plaintext to feed this module's `bearer_tokens_csv` var.
- **Doesn't tear down on Continuum CR deletion.** Worker + KV outlive the CR (they may serve other CRs). Manual `tofu destroy` when retiring a Sovereign.
