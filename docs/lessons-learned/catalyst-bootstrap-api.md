> NOTE (2026-06-03): pending migration into docs/RUNBOOKS.md §7 (troubleshooting) per lean-doc strategy.

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

## Renaming a persisted JSON field silently drops legacy data

`encoding/json.Unmarshal` ignores any JSON object key that doesn't match a struct
field tag. When you rename a tag — e.g. `Job.BatchID \`json:"batchId"\`` →
`Job.ParentID \`json:"parentId"\`` — every existing on-disk record under the old
tag becomes invisible on the next `Unmarshal`. No error, no warning. Tests miss
this because every fixture is written with the new shape; only a deployment
provisioned before the rename surfaces the problem, and only on a UI surface
that depends on the renamed field. The pre-#351 deployment `ce476aaf80731a46`
hit exactly this: every leaf came back with empty `ParentID`, the canvas
rendered zero parent relationships, and there was no log line to chase.

**Rule**: when renaming a persisted struct's JSON tag, add a read-tolerant
sibling field with the **legacy** tag, hoist it to the new field in `loadIndex`
(or equivalent), and strip it before the next persist so the on-disk record
becomes canonical. Add a test that hand-writes the legacy JSON shape and
asserts the migration. If the new wire shape promises derived data the old
records can't supply (e.g. synthesized parent-group rows in a recursive
model), synthesize that data at read time too — the migration must be
invisible to consumers, not just to the unmarshaller.

```go
// types.go — read-only migration field
type Job struct {
    ParentID      string `json:"parentId"`
    LegacyBatchID string `json:"batchId,omitempty"` // hoisted on read; stripped on write
}

// store.go — loadIndex hoist
for i := range idx.Jobs {
    if idx.Jobs[i].ParentID == "" && idx.Jobs[i].LegacyBatchID != "" {
        idx.Jobs[i].ParentID = JobID(deploymentID, idx.Jobs[i].LegacyBatchID)
    }
    idx.Jobs[i].LegacyBatchID = ""
}

// store.go — persistIndex strip
for i := range persisted.Jobs {
    persisted.Jobs[i].LegacyBatchID = ""
}
```

**Ref**: #351
