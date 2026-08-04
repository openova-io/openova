# #5482 — attempted live navigation, 2026-08-01

Attempted the walk the coach asked for: open the App detail Overview on the
only reachable console and observe whether it renders a host-cluster label as
PRIMARY REGION.

**Result: the route does not exist on this console. No app-detail surface to
assess.**

    Playwright: page.goto('https://console.openova.io/sovereign/applications/')
      Page URL   : https://console.openova.io/sovereign/applications
      Page Title : OpenOva Corporate
      Console    : 1 error
      Accessibility snapshot, in full:
        - paragraph [ref=f4e3]: "Not Found"

Screenshot: `uat-5482-console-openova-io-applications-notfound-2026-08-01.png`

## Why the HTTP 200 was misleading

`curl -o /dev/null -w '%{http_code}'` against that path returns **200**, which
reads as "the page is up". It is the SPA shell answering — the same 1063-byte
document is served for *every* path, including `/sovereign/apps`, which is not
a real route. The router then renders `Not Found`.

So the 200 measures "a web server responded", not "the applications view
exists". Taking a screenshot of that page and filing it against a row
asserting a mislabelled PRIMARY REGION would be evidence of nothing — the
page contains one paragraph and no application.

Consistent with the rest of the surface map: this cluster holds **zero**
catalyst CRDs (`kubectl get crd | grep -icE 'catalyst|openova' → 0`) and
`GET /api/v1/applications` → **404**. The mothership is the deployment
control plane; it does not host the Catalyst object model.

## Status

#5482's read-side fix is delivered (`b41c93b3c`). Its emit half is localized —
flat `status.primaryRegion` ← `plan.PrimaryRegion` (the DECLARED value,
`application_controller.go:2593`) vs nested ← the normalized leaseHolder
(`placement_projection.go:279`) — and deliberately deferred: writing
`status.placement` on the DR path cannot be validated without an environment,
and risking the Pillar-3 proof immediately before a keystone fire is a bad
trade.

The observable assertion re-walks on hw292.
