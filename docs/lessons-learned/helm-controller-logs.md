> NOTE (2026-06-03): pending migration into docs/RUNBOOKS.md §7 (troubleshooting) per lean-doc strategy.

# helm-controller log format

Operational knowledge about the structure of stdout lines emitted by Flux's `helm-controller` Pod, which any external log-tailer or log-parsing tool needs to handle correctly.

## Flux v2.4 emits HelmRelease as a nested JSON object, not a flat string

Older documentation and example regexes assume helm-controller log lines tag the related HelmRelease as a **flat string**:

```text
... helmrelease="flux-system/bp-cilium" ...
... "helmrelease":"flux-system/bp-cilium" ...
```

Against the version Catalyst-Zero pins (Flux v2.4), helm-controller actually emits **nested-object structured JSON** with the release name + namespace as separate keys:

```json
{
  "level": "info",
  "ts": "2026-04-30T18:37:49.961Z",
  "msg": "dependencies do not meet ready condition (dependency 'flux-system/bp-seaweedfs' is not ready): retrying in 30s",
  "controller": "helmrelease",
  "controllerGroup": "helm.toolkit.fluxcd.io",
  "controllerKind": "HelmRelease",
  "HelmRelease": {
    "name": "bp-mimir",
    "namespace": "flux-system"
  },
  "namespace": "flux-system",
  "name": "bp-mimir",
  "reconcileID": "24709ccb-ed85-4f16-9fcd-8bc5b89aabf8"
}
```

A regex written for the flat-string format will match **zero lines** — every helm-controller stdout entry gets silently dropped, and any external observability surface that depends on the parse (per-component log streaming, alerting, etc.) renders empty.

Verified by tailing the live otech cluster's `helm-controller-86c6b84dcd-t58td` Pod with `kubectl logs -n flux-system helm-controller-* --tail=10` — every emitted line over a 30s reconcile cycle uses the nested-object shape.

**Rule**: Any code that parses helm-controller stdout against Flux v2.4 must support the nested-object format. If you're writing a regex, use an alternation that covers BOTH the flat-string format (legacy / older Flux versions / debug-mode loggers) AND the nested-object format (current production). Don't assume one shape is canonical — Flux's logger format is not stable across versions or across operator-configured logger backends.

A working alternation regex (used by `internal/helmwatch/logtailer.go`):

```go
var helmControllerNameRe = regexp.MustCompile(
    `(?:` +
        `(?:helmrelease|HelmRelease)["']?\s*[:=]\s*["']?` +
        regexp.QuoteMeta(FluxNamespace) + `/(bp-[a-z0-9-]+)` +
        `)|(?:` +
        `["']?HelmRelease["']?\s*:\s*\{[^}]*?["']?name["']?\s*:\s*["'](bp-[a-z0-9-]+)["']` +
        `)`,
)
```

The first alternative covers the flat-string format; the second matches the nested-object shape. The consumer picks whichever capture group fired. Pin test fixtures to real production log samples (committed verbatim into `internal/helmwatch/logtailer_test.go`) so a future Flux upgrade that breaks the parse surfaces as a CI failure, not a silent observability outage.

**Ref**: #305
