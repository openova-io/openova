# §854 / NodePort + gateway-collision verification — hw288, 2026-07-25

Live evidence gathered this session on **hw288** (dep `027f07559af1f9f7`, 2-region Huawei me-east-215-a/b). Owner-session + kubectl, read-only.

## §854 — NodePorts: 0 on the Sovereign, 3 non-OpenOva on the mothership

Live `kubectl get svc -A -o json | jq '[.items[]|select(.spec.type=="NodePort")]|length'`:

| Cluster | NodePort services |
|---|---|
| hw288 region-a | **0** |
| hw288 region-b | **0** |
| mothership (`~/.kube/config`) | 3 — all non-OpenOva |

The OpenOva Sovereign is **100% NodePort-free in both regions** — §854 compliant, gateway served DIRECT. The 3 mothership NodePorts are **not editable in this repo**:

- `cinova/catalog-svc` :30341 — declared in the **cinova** app's own chart (`foundrylab-app/cinova`); cinova is ⛔ never-touch.
- `iogrid/proxy-gateway-socks5` :31080 — **iogrid's** chart, a separate repo.
- `iogrid/cm-acme-http-solver-sh4np` :31866 — a cert-manager ACME HTTP-01 solver, **created dynamically** by a stuck `proxy.iogrid.org` Challenge; exists in no chart, auto-deleted on Challenge resolution.

Whole-repo `grep -rniE 'type:\s*NodePort'` across `platform/ products/ core/ clusters/` returns **zero** real Service declarations — every hit is a comment, the `check-no-nodeports` CI guard, or cilium's *anti*-nodeport `allocateLoadBalancerNodePorts: false` (`clusters/_template/bootstrap-kit/01-cilium.yaml`). There is no OpenOva chart to convert. Tracked as #5348 (the mothership entries are cinova/iogrid/infra decisions).

## #5341 — console-gateway wildcard collision has a BROAD blast radius

Confirmed this session that the console gateway's `*.<fqdn>` listener collision (fixed by **PR #5354**) degrades **every non-console subdomain on the shared `cilium-gateway`**, not just `/mcp`:

| Subdomain | Gateway | Live 404 rate |
|---|---|---|
| `mcp.hw288.omani.works` | shared | 12/24 (50%) |
| `harbor.hw288.omani.works` | shared | 12/24 (50%) |
| `marketplace.hw288.omani.works` | **console** | 0/24 (clean — console chain has the marketplace route) |

Mechanism (verified in the two gateways' CECs): both `cilium-gateway` and `cilium-gateway-console` compile a filter chain with `serverNames=['*.<fqdn>']` onto the shared cilium-envoy; a TLS handshake for any subdomain matches both, envoy resolves the duplicate wildcard non-deterministically, and ~50% land on the console chain whose router lacks that subdomain's vhost → bare `404 (server: envoy)`. This blocks the ◑ SSO-landing UAT rows (32 Harbor, 235 Grafana, 35 Guacamole, 37/38 newapi) until PR #5354 (`bp-sovereign-tls-vars@0.1.1`) rolls. Post-roll check: loop `harbor.<fqdn>/` + `mcp.<fqdn>/mcp` → expect 0 envoy-404.

See memory `reference_cilium_multi_gateway_wildcard_listener_sni_collision`.

---

## §854 re-audit — 2026-07-25 (LIVE, both contexts) — DEFINITIVE: nothing to convert

Re-ran the full live audit (`kubectl get svc -A -o json` on the mothership + **both** hw288 region kubeconfigs pulled off the `catalyst-api-deployments` PVC) + a whole-repo chart grep:

| Scope | `type: NodePort` Services | Disposition |
|---|---|---|
| **hw288 region-A** | **0** | §854-clean |
| **hw288 region-B** | **0** | §854-clean |
| **mothership** | 3 | all **non-OpenOva**, none editable in this repo |
| **whole repo (`platform/ products/ core/ clusters/`)** | 3 YAML hits, **all one file** | the guard's own **negative-test fixtures** — must stay NodePort |

The 3 mothership NodePorts: `cinova/catalog-svc:30341` (chart `cinova-metro` — ⛔ **cinova never-touch**), `iogrid/proxy-gateway-socks5:31080` (**iogrid** — separate repo), `iogrid/cm-acme-http-solver-*:31866` (an **ephemeral** cert-manager ACME HTTP-01 solver, auto-created by a stuck `proxy.iogrid.org` Challenge, in **no chart**). None is an OpenOva chart; none is convertible from this repo.

**The only repo `type: NodePort` YAML** is `platform/kyverno-policies/chart/tests/forbid-nodeport-service/resource-services.yaml` — its own header: *"#5088 — INPUT fixtures for the §854 forbid-nodeport-service (**Enforce**) policy … VIOLATION: a plain type=NodePort Service → DENIED by rule 1 (§854)."* i.e. OpenOva ships an **Enforce Kyverno policy that forbids NodePort Services**, and these three are the **adversarial inputs the guard must reject** (asserted by the sibling `kyverno-test.yaml`). Converting them to ClusterIP would make the guard's negative test vacuous — they are **required** to stay NodePort.

**Conclusion:** there is **no OpenOva-chart NodePort to convert** — the platform is not merely NodePort-free, it **actively enforces** the ban via an admission policy, and is proven clean live on the current Sovereign (0/0 both regions). The recurring "convert NodePorts to ClusterIP in Helm charts" task has **no target**. Tracked: #5348 (mothership cinova/iogrid = their owners' decisions), #5088 (the enforce policy).
