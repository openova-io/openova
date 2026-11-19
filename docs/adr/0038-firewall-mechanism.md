# ADR-0038 — Firewall mechanism: Coraza in the Envoy of Cilium through a listener the platform writes, the native firewall as the second implementation, and the packages of epic NET-250

Status: Accepted
Date: 2026-11-19

Written by work package NET-251, from the result of lab test NET-270 (`docs/ledger/plan/spikes/NET-270.md`, outcome ATTACHED). It records how a web application firewall is put in the public path of a Sovereign, the contract that its two implementations share, and the packages of epic NET-250 that build them, in order. The decisions are WF1 to WF8.

Terms used here:

- **Gateway listener**: the Envoy listener that the Cilium operator writes for a Gateway, in the CiliumEnvoyConfig `cilium-gateway-<name>` that it owns.
- **Front listener**: an Envoy listener that the platform writes in a CiliumEnvoyConfig of its own, in front of a gateway listener.
- **The filter**: Coraza built as WebAssembly (`coraza-proxy-wasm`), run by the HTTP filter `envoy.filters.http.wasm`.
- **Native firewall**: the web application firewall service of a cloud, in front of that cloud's load balancer.
- **Reference cloud**: the primary substrate (ADR-0024).

## Context

- No firewall is in the path of any Sovereign. NET-040 removed the Coraza chart, which nothing called. ADR-0019 point 7 allows the claim "firewall in path" only where a `WebApplicationFirewall` is Ready, and no such kind exists.
- Lab test NET-270 ran the reference class (k3s, Cilium 1.19.3, the gateway in the host network) on a CI runner, three runs. It measured:
  - The shipped `cilium-envoy` image carries `envoy.filters.http.wasm`. It has no `lua` and no `golang` filter.
  - The operator writes three HTTP filters into the gateway listener from a literal in its code. An edit is restored within 0.2 seconds.
  - A front listener in a CiliumEnvoyConfig in the Gateway's namespace, whose HTTP connection manager names the gateway's route configuration, met the pass rule on port 80 with the filter (Core Rule Set at paranoia level 1, block mode): a clean request 200, a cross-site-scripting request and an SQL injection request 403, every route answering. A new HTTPRoute was served within 2 seconds. The result held after restarts of the operator, of Envoy and of the agent. The Gateway's own listener had been moved to port 18080.
  - Cilium forces a route-configuration name into the namespace of the object that names it, so a cluster-wide object cannot reuse the gateway's routes (404).
  - Without the annotation `cec.cilium.io/use-original-source-address: "false"` a front listener answered clean requests with 503.
  - A second tier, Envoy Gateway v1.5.9 on an edge-labelled node with host ports and the filter attached through its extension point, also met the pass rule.
  - `cilium-envoy` used 315 to 359 MiB with the filter. No Service had a node port.
- Not measured: HTTPS, request-body rules, latency, memory over time, more than one node, two regions, the reference cloud. A filter that cannot start fails closed (HTTP 503), and Envoy rejects an update whose filter does not start. The release notes of coraza-proxy-wasm 0.6.0 warn of memory growth.
- Contract C6 (`docs/contracts/c6-crossplane-conventions.md`) names a Composition `<contract>-<implementation>` with no provider name, ships Compositions only in provider overlays, and merges a definition only in the pull request that carries two Compositions for it.

## Decision

**WF1 Mechanism.** Coraza runs as the filter inside the shipped `cilium-envoy`, in a front listener that the platform writes. One tier, no second proxy. The gateway listener is never edited.

**WF2 The front listener.** A CiliumEnvoyConfig in the namespace of the Gateway it protects, with the annotation `cec.cilium.io/use-original-source-address: "false"`. Its HTTP filters are the filter, then the router. It names the gateway's own route configuration, so HTTPRoutes stay the only source of routes. It binds the Gateway's public host ports on the nodes where the gateway listeners bind (edge nodes once NET-182 has merged). The Gateway's listeners then move to internal ports that no load-balancer listener and no security-group rule opens. For HTTPS the front listener terminates TLS with the Gateway's certificates. NET-252 proves HTTPS before anything is built on it.

**WF3 Delivery of the filter.** One pinned release of `coraza-proxy-wasm`, its SHA-256 in the repository, served from the Sovereign's own registry, never fetched from a public registry at run time. The file is placed on every node that binds a front listener before any front listener names it, and `cilium-envoy` reads it from a read-only host path. The first pin is 0.6.0 (Coraza 3.3.3, Core Rule Set 4.14.0).

**WF4 Contract.** One composite resource definition: kind `WebApplicationFirewall`, group `compose.openova.io`, version `v1alpha1`, namespaced, shipped by `bp-crossplane-claims`. An object lives in the namespace of the Gateway it protects, because the front listener must live there (Context).

- `spec.gatewayRef.name` (required); `spec.mode`, `detect` or `block` (required); `spec.level`, the paranoia level 1 to 4 (required); `spec.ruleSet.version`, exact (required); `spec.failureMode`, `closed` or `open`, default `closed`; `spec.requestBodyLimitKb`, default 128.
- `spec.hostOverrides[]`: `host`, `mode`, `level`.
- `spec.exclusions[]`, the waivers: `id`, `ruleIds` (at least one), `host`, `pathPrefix`, `reason`, `approvedBy`, `expires` (a date at most 180 days ahead). All are required.
- `status.ready`, `status.implementation` (`coraza` or `native`), `status.effectiveMode`, `status.lastProofAt`.
- Admission rejects an unknown `ruleSet.version`, a `pathPrefix` of `/` and an `expires` in the past. `status.ready` is false with reason `ProofFailed` while WAF-2 of WF7 fails.
- A field enters the definition in the package that first reads or writes it: `status.lastProofAt` and the reason `ProofFailed` in NET-258, every other field in NET-257.

**WF5 Two implementations.** Compositions `waf-coraza` (WF1 to WF3) and `waf-native` (the native firewall in front of the region's load balancer, through a unit module of the provider pack, with the edge nodes reachable only through that load balancer). Labels `compose.openova.io/contract: waf` and `compose.openova.io/implementation`. `waf-coraza` ships in every provider overlay, `waf-native` only in the overlay of a pack whose cloud passed NET-253. A pack sets `defaults.waf` to `waf-native` where it ships one, else to `waf-coraza`. The definition merges in the pull request that carries both Compositions (C6).

**WF6 Choice per Sovereign.** A Gateway has a firewall in its path only while a `WebApplicationFirewall` for it exists and is Ready. The object is committed through the write path of ADR-0021, never written to the cluster directly. Choosing `coraza` for a Gateway is one commit that adds the object and moves the Gateway's listeners to their internal ports. Removing it is one commit that reverts both. The `waf-coraza` Composition is the only writer of the front listener. Without an object, the gate record, the console and the documents say "no web application firewall in the path".

**WF7 Proof.** Journey WAF. WAF-1: a benign request returns 200. WAF-2: every request of the corpus `tests/waf/corpus/` returns 403 in `block` mode. WAF-3: the same requests pass and are logged in `detect` mode. WAF-4: no public path reaches a backend without passing the firewall; a direct dial to a node address fails. WAF-5: an exclusion whose `expires` is in the past is rejected at admission. The corpus is written fresh from the upstream regression tests of the pinned Core Rule Set version. Gate check NET-G15, "Firewall proof", enters `docs/ledger/gates.yaml` in state not-built together with the packages of WF8: with an object, WAF-1 to WAF-5 pass; with none, the record prints "no web application firewall in the path".

**WF8 The packages of epic NET-250, in order.**

| Package | Type | Size | Lane | Depends on | Delivers |
|---|---|---|---|---|---|
| NET-252 | spike | S | SPIKE | NET-251 | HTTPS through a front listener with the Gateway's certificates for two host names, the pass rule over HTTPS, and `cilium-envoy` memory with and without the filter under load for four hours. Outcome HTTPS or NONE. |
| NET-253 | spike | S | SPIKE | NET-251 | The native firewall of the reference cloud created, read and deleted through its API in front of a load balancer, and the pass rule through it. Outcome NATIVE or NONE. |
| NET-254 | new | S | CHARTS | NET-182, NET-252 | The filter file of WF3 on every edge node, and its read-only host path in `platform/cilium`. |
| NET-255 | test | S | GATE | NET-251 | The corpus, journey WAF and the implementation of gate check NET-G15, which stays not required. |
| NET-256 | new | M | SUBSTRATE | NET-253, NET-184, CLD-174 | The unit module of the native firewall for the reference pack, proven as a plain unit Workspace. |
| NET-257 | new | M | CHARTS | NET-254, NET-255, NET-256 | The definition of WF4, `waf-coraza` with its front listener, `waf-native`, and the move of the Gateway's ports of WF6. WAF-2 passes; gate check NET-G15 becomes required. |
| NET-258 | new | S | CHARTS | NET-257 | An hourly run of WAF-1 and WAF-2 for each object, which writes `status.lastProofAt` and `status.ready`. |
| NET-259 | new | S | CONSOLE | NET-257 | The console shows the firewall state of each Gateway and commits the sovereign-admin's choice of implementation, mode and level. |

The second implementation, `waf-native`, is NET-256 with its Composition in NET-257. If NET-252 ends NONE, a new decision record replaces WF1 to WF3 with the second tier of NET-270 before NET-254 starts. If NET-253 ends NONE, a new decision record names the second implementation before NET-257 starts.

## Alternatives rejected

- **Editing the gateway listener.** The operator restores its filter list within 0.2 seconds (Context).
- **A cluster-wide object with its own route table.** It works, but the routes are then a second copy beside the HTTPRoutes.
- **The second tier as the mechanism.** A second proxy, a second controller to upgrade, a second TLS termination and about 384 MiB more on each edge node, for a result that one tier meets. It stays the measured fallback of WF8.
- **Fetching the filter from a public registry, or by a remote source in Envoy.** A Sovereign depends on nothing outside itself after hand-over (ADR-0002), and a remote source was not measured for `cilium-envoy`.
- **An external Coraza service called through `ext_proc` or `ext_authz`.** One more service and one more hop in every request, and not measured.
- **The contract with one implementation.** C6 merges a definition only with two Compositions, and a capability with one implementation gives the sovereign-admin no choice.

## Consequences

- The claim "firewall in path" holds for a Gateway only where its `WebApplicationFirewall` is Ready (ADR-0019 point 7).
- A Cilium upgrade can rename the gateway's route configuration. Once NET-257 has merged, it merges only with journey WAF green.
- Choosing or removing `coraza` moves the Gateway's listener ports. NET-257 measures how long the entry does not answer during that commit.
- The Gateway status `Programmed=False`, measured on both mechanisms, gates nothing.
- No node port is used, on either implementation (ADR-0026).

## Shipped via

NET-251 writes this decision record. In epic NET-250: NET-252, NET-253, NET-254, NET-255, NET-256, NET-257, NET-258 and NET-259, as WF8 lists them.
