# Catalyst bootstrap API

Operational findings about `products/catalyst/bootstrap/api/` — the Go service that drives Phase-0 provisioning + Phase-1 watch + the wizard SSE stream.

## `tofu destroy` works against the on-disk workdir without re-prompting credentials, even after catalyst-api GCs the Hetzner token from in-memory state

catalyst-api intentionally clears `Request.HetznerToken` from the in-memory `Deployment` struct after `writeTfvars` finishes, per credential hygiene (avoid keeping a long-lived Hetzner Cloud API token in a process restart-survivor).

The token IS however persisted into `<workdir>/tofu.auto.tfvars.json` on the catalyst-api PVC so OpenTofu can authenticate to the hcloud provider during `apply` (and any subsequent re-`apply` or `destroy`). This file has mode 0600 + lives only on the catalyst-api Pod's writable volume.

**Consequence:** `tofu destroy` against an existing per-deployment workdir succeeds with no fresh-prompted token. Verified 2026-05-01 during #318 wipe-endpoint testing — a `POST /api/v1/deployments/:id/wipe` request body with a placeholder Hetzner token still produced `tofuDestroyed: true` because tofu read the real token from on-disk tfvars and called the hcloud provider directly.

**Implications for destructive endpoints (wipe, decommission, retry-destroy, force-purge):**
- `tofu destroy` only requires the workdir to still exist. The body-supplied Hetzner token is *not* needed for the tofu pass.
- The body-supplied token IS needed for the **Hetzner-direct API orphan-purge fallback** — resources that tofu didn't track (half-failed cloud-init, manually-created experiments, label-tagged orphans from a prior partial apply). That fallback pass enumerates by label-selector `catalyst-deployment-id=<id>` and force-deletes anything matching, and that REST call can't read the on-disk tfvars.

**Rule:** when a reviewer questions "why doesn't this endpoint prompt for the token before destroy?", the answer is: tofu doesn't need it. The prompt is for the orphan-purge safety net. Document the two paths separately so the credential-hygiene rationale stays visible.

**Air-gap caveat:** for installs that configure a remote OpenTofu backend (e.g. encrypted S3-style state), the on-disk tfvars approach above breaks — tofu reads state remotely. Such installs MUST configure the remote backend's auth in env vars at the catalyst-api Pod level, not in tfvars.

**Ref:** #318
