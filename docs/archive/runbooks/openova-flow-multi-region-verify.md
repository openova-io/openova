# OpenovaFlow multi-region rendering — prov #34 verification runbook

End-to-end verification that OpenovaFlow renders **2 bubbles per HelmRelease**
(one per region) on a multi-region Sovereign. Runs after PR #1389 (TS core
+ canvas), PR #1390 (Go server + flux adapter + bootstrap-kit slots
56/57), PR #1394 (catalyst-ui temporary revert until npm workspaces
land), PR #1395 (chart-render no-op), and the integrator PR (catalyst-api
proxy + cloud-init thread).

**Scope note for the canvas (UI) layer:**
PR #1394 reverted Agent #1's catalyst-ui wiring of `@openova/flow-*` because
the catalyst-ui Docker build had no `node_modules` for the cross-workspace
canvas source. That revert kept the legacy `flowLayoutOrganic` +
`FlowCanvasOrganic` stack in place. **Steps 1-6 below validate the
infrastructure path end-to-end**; **Step 7 (visual canvas confirmation)
requires a follow-up PR** that re-wires `@openova/flow-canvas` via npm
workspaces at the repo root.

Until that follow-up lands, the legacy canvas still renders a SINGLE bubble
per HR (no region multiplexing on the UI side). The infrastructure pieces in
this runbook are nevertheless verifiable from the API surface — `Step 6`
(snapshot count) is the canonical pass gate.

## 1. Provision body (multi-region cpx42 fsn1 + hel1)

```bash
COOKIE=$(cat /tmp/cz-cookie-prov13.txt)
curl -sS -X POST https://console.openova.io/sovereign/api/v1/deployments \
  -b "$COOKIE" \
  -H 'Content-Type: application/json' \
  -d @- <<'EOF'
{
  "sovereignFQDN": "omantel.biz",
  "sovereignDomainMode": "byo",
  "region": "fsn1",
  "haEnabled": false,
  "controlPlaneSize": "cpx42",
  "workerSize": "cpx42",
  "workerCount": 0,
  "qaTestEnabled": true,
  "regions": [
    { "id": "fsn1",   "controlPlaneSize": "cpx42", "workerSize": "cpx42", "workerCount": 0, "cloudRegion": "fsn1" },
    { "id": "hel1-1", "controlPlaneSize": "cpx42", "workerSize": "cpx42", "workerCount": 0, "cloudRegion": "hel1" }
  ],
  "orgName": "Omantel SME",
  "orgEmail": "sre@omantel.biz"
}
EOF
```

Capture the returned `id`; downstream commands refer to it as
`$DEP_ID`.

## 2. Wait for `bp-catalyst-platform` Ready=True

```bash
kubectl --context=omantel get hr -n flux-system bp-catalyst-platform \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# expect: True
```

(Use `iter_orchestrator.py --provision-id 34 --iter 1` or a Monitor +
poll loop — not an Agent — to wait. See feedback_agent_misuse.)

## 3. Both openova-flow HRs Ready on primary

```bash
kubectl --context=omantel get hr -n flux-system | grep openova-flow
# expect 2 rows, both Ready=True:
#   bp-openova-flow-server    True
#   bp-openova-flow-emitter   True
```

## 4. Emitter Ready on secondary (hel1) — server runs primary-only

```bash
kubectl --context=omantel-hel get hr -n flux-system | grep openova-flow-emitter
# expect 1 row Ready=True
kubectl --context=omantel-hel get hr -n flux-system | grep openova-flow-server
# expect: nothing (server is primary-only by design)
```

## 5. Pods up

```bash
kubectl --context=omantel    get pods -n catalyst-system -l app.kubernetes.io/instance=openova-flow-server
kubectl --context=omantel    get pods -n catalyst-system -l app.kubernetes.io/instance=openova-flow-emitter
kubectl --context=omantel-hel get pods -n catalyst-system -l app.kubernetes.io/instance=openova-flow-emitter
```

Each should show `Running` / `Ready=1/1`.

## 6. catalyst-api proxy snapshot end-to-end

```bash
# From the operator's browser session cookie:
curl -sS -b "$COOKIE" \
  "https://console.openova.io/sovereign/api/v1/flows/${DEP_ID}/snapshot" \
  | jq '.type, (.nodes | length)'
# expect: "snapshot", 86  (43 HRs × 2 regions)
```

## 7. UI — open the canvas, expect **2 bubbles per HR per region**

```
https://console.openova.io/sovereign/provision/${DEP_ID}/jobs/install-trivy
```

Visual checks (in this order):

1. The `install-trivy` node appears **twice** — one with `fsn1` region
   tag, one with `hel1-1`. Both bubbles share the same label.
2. Folding to depth 1 (FoldControls "1") collapses both regions into
   two super-bubbles (one per region) connected by `contains`
   relationships.
3. Folding to depth 2 expands each region's super-bubble to show its
   per-batch group nodes (Phase-0, bootstrap-kit, catalyst-platform
   sub-batches).
4. Total node count when fully expanded: **86** (43 HRs × 2). The
   StatusStrip reports `finished / total` with the same divisor.

## 8. Failure-class quick triage

| Symptom | Likely root cause | Check |
|---|---|---|
| `snapshot` returns 502 | catalyst-api can't reach openova-flow-server | `kubectl logs -n catalyst-system deploy/catalyst-api \| grep openova-flow` |
| `snapshot` returns `nodes: []` | emitter isn't POSTing | `kubectl logs -n catalyst-system deploy/openova-flow-emitter` |
| Only 1 bubble per HR | `SOVEREIGN_REGION_KEY` not threaded into secondary CP cloud-init | `kubectl --context=omantel-hel get kustomization -n flux-system bootstrap-kit -o jsonpath='{.spec.postBuild.substitute}'` should show `SOVEREIGN_REGION_KEY: hel1-1` |
| All bubbles share the same FlowID | `SOVEREIGN_DEPLOYMENT_ID` not threaded | same kustomization, check `SOVEREIGN_DEPLOYMENT_ID` is the 16-char hex, not the FQDN |
| FlowPage falls back to bridge | `openovaFlowEnabled` flag missing on deployment record | `curl /api/v1/deployments/$DEP_ID \| jq .openovaFlowEnabled` — must be `true` |

## 9. Convergence gate

The runbook is GREEN when:

- Steps 3, 4, 5 each return the expected counts on the FIRST poll
  (within 90 min of the POST).
- Step 6 returns `nodes: 86` and at least 80% of `nodes[].status` is
  `succeeded`.
- Step 7 visual checks all pass on an incognito browser session
  (founder rule from session_2026_05_09 — one human walkthrough
  required, not just API 200).

If any step fails, do not patch the symptom layer. Trace the chain
backwards per `~/.claude/CLAUDE.md` Rule 1 ("Trace requirements
end-to-end before fixing symptoms"). Most likely break-point is
SOVEREIGN_REGION_KEY substitution failing on the secondary's
bootstrap-kit Kustomization — that's where the per-region tag
diverges from primary.
