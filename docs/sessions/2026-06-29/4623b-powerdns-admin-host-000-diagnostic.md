# #4623b — `pdns-admin.<fqdn>` serves HTTP 000 — diagnostic (code is correct; needs live confirmation)

**Status:** No code defect found. The repo wiring for `pdns-admin.<fqdn>` is
correct end-to-end and structurally identical to the working grafana / gitea /
guacamole / openova-flow paths. The HTTP 000 observed on a converged Sovereign
is a **runtime condition**, not a chart bug. This note records the full trace so
the next fresh-prov walk can confirm the root cause in seconds instead of
re-deriving it.

> Note on the host name: the issue text says `powerdns-admin.<fqdn>`. The
> canonical host is the single-label **`pdns-admin.<fqdn>`** (chosen in #3150 so
> it is covered by the `*.<fqdn>` wildcard cert and matches the KC client
> redirectUris). There is no `powerdns-admin.<fqdn>` host anywhere in the repo —
> `powerdns-admin` is only the chart / namespace / Service name. The failing
> host is `pdns-admin.<fqdn>`.

## Serving topology (what SHOULD happen)

`pdns-admin.<fqdn>` is a **gated** app: the generic `bp-oidc-gate` (oauth2-proxy,
bootstrap-kit slot 13c) owns the hostname and fronts the PowerDNS-Admin pod.

```
browser ──TLS(*.<fqdn>)──▶ cilium-gateway (shared, kube-system)
                            │  HTTPRoute: oidc-gate-powerdns-admin (ns oidc-gate)
                            ▼
                          oidc-gate-powerdns-admin Svc :80 ▶ oauth2-proxy :4180
                            │  (SSO via Keycloak; X-Auth headers)
                            ▼
                          powerdns-admin-bp-powerdns-admin Svc :80 ▶ PDA :9191
```

The PDA chart's OWN HTTPRoute is intentionally disabled
(`gateway.enabled: false`, slot 11a) — two HTTPRoutes on one hostname is
undefined routing, so the gate is the single owner.

## Everything verified CORRECT in the repo

| Layer | Evidence | Result |
|---|---|---|
| Gate HTTPRoute | `platform/oidc-gate/chart/templates/instances.yaml` renders hostname `pdns-admin.<fqdn>`, parentRef `cilium-gateway/kube-system`, redirect on Exact `/` + catch-all backend `oidc-gate-powerdns-admin:80` | well-formed, same shape as grafana |
| Wildcard TLS listener | `clusters/_template/bootstrap-kit/01-cilium.yaml` — `https` listener `*.<fqdn>` `Terminate` `sovereign-wildcard-tls-<dashed>`, `allowedRoutes.namespaces.from: All` | `pdns-admin.<fqdn>` (single-label) is covered |
| DNS via external-dns | `platform/external-dns/chart/values.yaml` — `sources: [service, ingress, gateway-httproute]`, `namespace: ""` (all ns), no label/annotation/gateway-namespace filter | the gate's HTTPRoute hostname yields an A-record cluster-wide |
| DNS via catalyst-api (backstop) | `products/.../handler/sovereign_dns_records.go:86` — `pdns-admin` is in `CanonicalSovereignSubdomains` and NOT in `ConsoleGatewaySubdomains` → `recordTargetIP` assigns it the **shared** gateway LB IP (correct; the gate parents the shared gateway). `sovereign_dns_republish.go` republishes it even if external-dns lags | A-record doubly-covered, correct target |
| plane-isolation (gate ns) | `platform/plane-isolation/chart/values.yaml` — `oidc-gate` component has `gatewayIngress: true` → `gateway-ingress-cnp.yaml` admits the reserved `ingress` entity to the gate ns | gateway→gate traffic admitted |
| gate→upstream egress | `default-deny-networkpolicy.yaml` oidc-gate egress allows `namespaceSelector:{}` + `ipBlock 0.0.0.0/0`; `powerdns-admin` ns has NO default-deny (absent from components list) | gate can reach the PDA upstream |
| Service wiring | gate upstream `http://powerdns-admin-bp-powerdns-admin.powerdns-admin.svc:80` matches `service.yaml` (`<release>-bp-powerdns-admin`, port 80→9191) | matches |

The ONLY differences between powerdns-admin and the (presumably working)
openova-flow gate instance are `rootRedirectPath`/`appNativeCallbackPath` and an
explicit `hostname:` — none of which affect TCP/TLS reachability (which is what
HTTP 000 measures, *before* any redirect fires). Both gate instances are
`enabled: true` by default, so if the cause were gate-wide, **openova-flow would
000 too** — the issue likely only named the one host the operator checked.

## Most probable runtime causes (in priority order)

HTTP 000 = the TCP/TLS connection itself fails (no listener / no programmed
route / no healthy backend / unresolved DNS). Diagnose live, in this order:

1. **Gate HTTPRoute not `Accepted`/`Programmed`** on the shared cilium-gateway
   at observation time (Cilium gateway route-programming lag, or a transient
   `Programmed=False` on the shared gateway when many routes churn).
   ```
   kubectl get httproute oidc-gate-powerdns-admin -n oidc-gate \
     -o jsonpath='{.status.parents[*].conditions[*].type}={.status.parents[*].conditions[*].status}{"\n"}'
   ```
2. **Gate Service has 0 ready endpoints.** Pod can be `Running` but `0/1 Ready`,
   or the oauth2-proxy `/ping` passes while the powerdns-admin **upstream**
   Service has 0 endpoints (PDA `0/1 Ready` because its readinessProbe `httpGet /`
   500s on a not-yet-migrated DB). A backend with 0 endpoints makes envoy reset
   the connection → curl 000.
   ```
   kubectl get endpoints -n oidc-gate oidc-gate-powerdns-admin
   kubectl get endpoints -n powerdns-admin powerdns-admin-bp-powerdns-admin
   kubectl get pods -n oidc-gate -n powerdns-admin   # READY column, not just STATUS
   ```
3. **DNS not yet written/propagated** for `pdns-admin.<fqdn>` at curl time
   (external-dns initial-sync lag; the catalyst-api republish backstop covers
   this within a cycle).
   ```
   dig +short pdns-admin.<fqdn>           # empty ⇒ NXDOMAIN ⇒ 000
   kubectl logs -n external-dns deploy/external-dns | grep pdns-admin
   ```
4. **gateway-ingress CNP for `oidc-gate` not yet applied** — `gateway-ingress-cnp.yaml`
   defers the CNP until the `oidc-gate` namespace exists (it is created by slot
   13c with `createNamespace: true`); it self-heals on the next 15-min
   plane-isolation reconcile but a curl in the gap would see drops.
   ```
   kubectl get ciliumnetworkpolicy -n oidc-gate oidc-gate-allow-gateway-ingress
   ```

## Why no code change ships for #4623b

Every serving layer renders correctly and matches the working apps. A
speculative chart edit to a correct path would be anti-theater (per
`docs/PRINCIPLES.md`). The fix, if any, will be a one-line ordering/readiness
tweak that can only be identified once the live `status.parents[].conditions`
(item 1) or the `endpoints` (item 2) are observed on a fresh prov. This note is
the diagnostic so that observation takes seconds.

Refs #4623
