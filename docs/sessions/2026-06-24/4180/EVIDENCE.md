# #4180 — agenity durable chart-pin delivery — live evidence (omantel.biz demo Org, 2026-06-23T16:37:06Z)

## BEFORE (orphaned hand-applied HR)
```
HR_CHART=0.3.0  IMAGE_TAG_OVERRIDE=0.9.6  httpRoute.enabled=false  reconcileStrategy=ChartVersion  spec.suspend=true
Last deployed helm release: chart 0.3.0 / appVersion 0.9.4  (release agenity-demo.v3)
Pod image: harbor.openova.io/openova-io/bp-agenity:0.9.5 (hand-patched StatefulSet)
HTTPRoute 'agenity': hand-applied, NO ownerReferences, NO managed-by, NO blueprint label (reverts on reconcile)
managedFields: helm-controller + kubectl-client-side-apply + kubectl-patch + kubectl-annotate (NO Application/org-controller manager)
```

## CONTROLLED ACTION (chart-pin bump, NOT a hand-patch)
- Re-applied agenity-demo HR pinned to chart 0.5.3, REMOVED image.tag override, httpRoute.enabled=true (parentRef cilium-gateway-console/kube-system, hostname agenity.demo.omani.homes)
- Deleted the hand-applied 'agenity' HTTPRoute (chart now renders 'agenity-demo-bp-agenity')
- Unsuspended the HR (suspend-reason 'unsuspend after pin bumps to 0.5.0' satisfied) + cleared the stale annotation

## AFTER (1st reconcile)
```
HR: READY=True  deployed chart 0.5.3 / appVersion 0.9.6  (release agenity-demo.v4)  HR_PIN=0.5.3  IMAGE_TAG=[] (no override)
Pod: READY=true  image=ghcr.io/openova-io/bp-agenity:0.9.6  restarts=0
HTTPRoute: ONLY agenity-demo-bp-agenity (managed-by=Helm, blueprint=bp-agenity)
GET /app/ -> 200, Dashboard.BmDlYS5Q.js (NEW), cache-control: no-cache, must-revalidate (from chart)
GET / -> 302 -> https://agenity.demo.omani.homes:443/app/
Runtime: chepherd daemon up, MCP listening; spawned worker 'verify-4180' (claude --model claude-opus-4-7), MCP initialize OK + tools/list OK (27 tools), session live, bytes flowing
```

## REVERT-IMMUNITY (2nd forced reconcile)
```
HR: READY=True  deployed chart 0.5.3 / appVersion 0.9.6  HR_PIN=0.5.3  IMAGE_TAG=[]
Pod: READY=true  image=bp-agenity:0.9.6  restarts=0 (did NOT re-roll — stable)
HTTPRoute: ONLY agenity-demo-bp-agenity (hand 'agenity' route did NOT reappear)
GET /app/ -> 200, Dashboard.BmDlYS5Q.js, cache-control: no-cache, must-revalidate
GET / -> 302 -> :443/app/   |   /_astro/Dashboard.BmDlYS5Q.js -> 200 (immutable asset, correctly NOT no-cache'd)
Runtime: online, workers=1
```

## Repo side (already on origin/main via #4173 — DoD 6 green)
- products/agenity/chart/Chart.yaml: version 0.5.3, appVersion 0.9.6
- products/agenity/blueprint.yaml: version 0.5.3
- blueprints.json + catalog.generated.ts: bp-agenity 0.5.3 → a FRESH catalog install pins 0.5.3
- GHCR: chart 0.5.3 + image 0.9.6 published + gate-verified
