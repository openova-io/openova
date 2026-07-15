# #5088 — mothership NodePort forensics + §854-clean migration runbook

Live walk 2026-07-15 (read-only: `get`/`describe`/`logs`/external `curl` probes only).
Mothership = single-node k3s `vmi3116389`, public IP `45.151.123.50`, external serving
mechanism = `type=LoadBalancer` via k3s ServiceLB (klipper): `svclb-*` pods hold the
**hostPort** and forward to the Service **ClusterIP** (proven live: `svclb-traefik`
container env `DEST_IPS=10.43.17.31` = traefik ClusterIP — no nodePort in the datapath
when `externalTrafficPolicy=Cluster`). That is how `console.openova.io:443` serves today
and it is the §854-clean mirror mechanism for anything here that still needs external
exposure.

None of the 4 Services below is sourced in this repo or in `openova-private` — all 4
are hand-applied (`kubectl.kubernetes.io/last-applied-configuration` present, no Helm
labels) or controller-generated. Replacement artifacts therefore live in THIS session
directory, per the #5088 remediation plan.

The durable admission guard already shipped: `platform/kyverno-policies`
`24-forbid-nodeport-service.yaml` (K24, merged via #5089) flags every `type=NodePort`
Service and every LB Service without `allocateLoadBalancerNodePorts: false`, cluster-wide,
Audit mode first.

---

## Service-by-service forensics + verdict

### 1. `openova-system/powerdns-api-ext` (8081:30853, 74d) — DELETE, no replacement object needed

| Fact | Evidence |
|---|---|
| Hand-applied, label `catalyst.openova.io/purpose: acme-webhook-external-access` | `last-applied-configuration` annotation; no Helm ownership; not sourced in openova or openova-private |
| The §854-clean path ALREADY EXISTS and predates it | Ingress `openova-system/powerdns-api` → `pdns.openova.io` (traefik, 77d vs the Service's 74d); `dig pdns.openova.io` → 45.151.123.50; `curl -sk https://pdns.openova.io/api/v1/servers/localhost` → **401** (reachable, API-key wall — correct) |
| Every consumer in the codebase uses the clean path | `clusters/_template/bootstrap-kit/49-bp-cert-manager-powerdns-webhook.yaml:138` → `host: "${PDNS_API_HOST:=https://pdns.openova.io}"`; `infra/providers/huawei/variables.tf` → `pdns_api_host` default `https://pdns.openova.io`; hetzner main.tf passes `""` → same default; pool env `CATALYST_POOL_POWERDNS_API_URL=https://pdns.openova.io` |
| Zero references to `30853` anywhere | repo-wide grep of openova + openova-private: only the K24 test fixtures and UAT evidence rows |
| Post-cutover Sovereigns don't dial the mothership pdns at all | `pdns_api_host` description: "franchised Sovereigns override post-cutover" (hw255 is post-cutover → local PowerDNS) |
| The NodePort is live-exposed plaintext HTTP and being probed | `curl http://45.151.123.50:30853/api/v1/servers/localhost` → 401; powerdns logs (48h) show ONLY `Authentication by API Key failed` probe lines — an unencrypted external door for an API-key-authenticated API |
| Mothership's own cert-manager never uses a powerdns webhook | ClusterIssuers: `letsencrypt-prod` (http01 traefik ingress), `letsencrypt-prod-dns01-dynadot`, `letsencrypt-prod-http01` (gateway), `letsencrypt-staging-http01` — no pdns DNS-01 issuer |

**Verdict: legacy, superseded by the `pdns.openova.io` Ingress before it was even created.
Deleting it also closes a plaintext-HTTP external door to the DNS API. Safe to delete now.**

### 2. `iogrid/cm-acme-http-solver-smv78` (8089:30633, 42d) — auto-GCs once the stuck Certificate is removed

Root-cause chain (all verified live):

1. Certificate `iogrid/proxy-iogrid-org` (secret `proxy-iogrid-org-tls`, dns `proxy.iogrid.org`) uses ClusterIssuer `letsencrypt-prod-http01`.
2. That issuer's solver is `http01.gatewayHTTPRoute` with `parentRefs: gateway-system/iogrid` — but cert-manager runs WITHOUT `--enable-gateway-api` (args verified), so Present fails: Challenge reason = `gateway api is not enabled`, pending 42d.
3. Even with the flag, the parentRef is wrong twice over: the live Gateway is `iogrid/iogrid` (namespace mismatch) with `gatewayClassName: cilium` — unprogrammed, since the k3s mothership runs traefik, not cilium.
4. cert-manager already created the solver Pod + **NodePort** Service; the ownerRef chain is Certificate → CertificateRequest `proxy-iogrid-org-1` → Order → Challenge → solver Service, so the whole tree GCs when the Certificate goes.
5. The Certificate is REDUNDANT: `proxy.iogrid.org` TLS is already served by secret `iogrid-proxy-tls` (Certificate `iogrid-proxy-tls`, Ready=True, issuer `letsencrypt-prod`), which is the ONLY tls secret mounted by the `proxy-gateway` pod. Nothing mounts `proxy-iogrid-org-tls`.

**Verdict: delete Certificate `iogrid/proxy-iogrid-org` → solver Service/Pod garbage-collect
themselves. Also repair or remove the broken `letsencrypt-prod-http01` issuer so the next
Certificate that references it doesn't leak another solver. Safe to execute now.**

(Sibling observation: Certificate `iogrid/iogrid-org` is also Ready=False, 53d, but has no
active Order — dormant duplicate of `iogrid-org-tls` (True). Same delete treatment applies.)

### 3. `iogrid/proxy-gateway-socks5` (1080:31080, 44d) — hand-applied bypass for a dangling traefik entrypoint; founder-timing

| Fact | Evidence |
|---|---|
| Hand-applied bare NodePort | `last-applied-configuration`, no labels |
| It exists because the intended clean path was left half-wired | IngressRouteTCP `iogrid/proxy-socks5-iogrid-org` (44d, SAME age) routes `HostSNI(*)` → `proxy-gateway:1080` on entrypoint `tcpingress-1080` — **that entrypoint does not exist in traefik's args** (only bolt/7687, metro/8081, web, websecure, metrics). The route is dangling; the NodePort was the workaround |
| The production consumer path is live and §854-clean | IngressRouteTCP `proxy-iogrid-org-passthrough`: `HostSNI(proxy.iogrid.org)` on `websecure` → `proxy-gateway:443` TLS-passthrough. iogrid source (`infra/k8s/traefik/ingressroutetcp-proxy-passthrough.yaml`) documents customers as `curl --proxy socks5h://user:token@proxy.iogrid.org:443 …` and calls port 1080 "plain SOCKS5, in-cluster only" |
| The service DOES have a live endpoint | endpoint `10.42.0.207:1080` (proxy-gateway pod) — unlike catalog-svc this door is functional, so external clients MAY be dialing `45.151.123.50:31080` |

**Verdict: delete, but on founder timing — only the founder knows whether any iogrid
customer/tooling still dials `node:31080` plain-SOCKS5 instead of `proxy.iogrid.org:443`.**
Consumer-audit command included below. If external plain-1080 must stay, use either
replacement in this directory (LB Service with `allocateLoadBalancerNodePorts: false`, or
finish the traefik `tcpingress-1080` entrypoint) — both §854-clean, external port becomes
**1080** (not 31080), so consumers change `:31080` → `:1080` once.

### 4. `cinova/catalog-svc` (8765:30341, 114d) — DEAD Service; protected SME namespace

| Fact | Evidence |
|---|---|
| Selector pins a stale ReplicaSet hash | `selector: {app: cinova-metro, pod-template-hash: 7b8488b969}` — a `kubectl expose` artifact |
| Zero endpoints, zero pods | `kubectl get endpoints -n cinova catalog-svc` → `<none>`; `kubectl get pods -n cinova` → `No resources found`; all cinova RS scaled to 0 |
| cinova's real external TCP is served clean already | traefik entrypoints `metro` (:8081, TLS) + `bolt` (:7687) via LB hostPorts; IngressRouteTCP `cinova/cinova-neo4j-bolt` |
| Not sourced in the cinova repo | grep of `~/repos/cinova` for `catalog-svc`/`NodePort` → nothing |

**Verdict: it routes to nothing and has routed to nothing for a long time — deleting it can
break no consumer. cinova is never-touch, so the founder runs the one-liner. If catalog is
ever redeployed, `30-cinova-catalog-svc-clusterip.yaml` here is the corrected (ClusterIP,
hash-free selector) Service.**

---

## MIGRATION RUNBOOK (founder-executed, minutes)

Execution order: **1 → 2 → 4 → 3** (3 last, it has the consumer question).
Every step: backup first, delete, verify, rollback line ready.

### Step 0 — one-time backup of all four originals (rollback source)

```bash
mkdir -p /root/5088-backups
kubectl get svc -n openova-system powerdns-api-ext        -o yaml > /root/5088-backups/powerdns-api-ext.yaml
kubectl get svc -n iogrid         proxy-gateway-socks5    -o yaml > /root/5088-backups/proxy-gateway-socks5.yaml
kubectl get svc -n cinova         catalog-svc             -o yaml > /root/5088-backups/catalog-svc.yaml
kubectl get certificate -n iogrid proxy-iogrid-org        -o yaml > /root/5088-backups/certificate-proxy-iogrid-org.yaml
```

(Originals deliberately NOT stored in-repo: they are apply-able `type=NodePort` manifests.)

### Step 1 — `powerdns-api-ext`

```bash
# pre: clean path serves (expect 401 = API-key wall)
curl -sk -o /dev/null -w '%{http_code}\n' https://pdns.openova.io/api/v1/servers/localhost   # 401

kubectl delete svc -n openova-system powerdns-api-ext

# post: nodePort door closed (expect timeout/refused), clean path still 401
curl -s -o /dev/null -w '%{http_code}\n' --max-time 5 http://45.151.123.50:30853/ || echo "closed (expected)"
curl -sk -o /dev/null -w '%{http_code}\n' https://pdns.openova.io/api/v1/servers/localhost   # 401
# downstream proof on the next tenant-DNS operation: Sovereign cert-manager-powerdns-webhook
# still presents TXT records via https://pdns.openova.io (bootstrap-kit slot 49 default).
```
Rollback: `kubectl apply -f /root/5088-backups/powerdns-api-ext.yaml`

### Step 2 — stuck ACME solver (`cm-acme-http-solver-smv78`)

```bash
# nothing consumes the target secret (verified: only iogrid-proxy-tls is mounted)
kubectl delete certificate -n iogrid proxy-iogrid-org
kubectl delete secret -n iogrid proxy-iogrid-org-tls --ignore-not-found

# solver svc + pod GC via ownerRefs (allow ~1 min)
kubectl get svc,pod -n iogrid | grep cm-acme || echo "solver gone (expected)"
kubectl get challenges -n iogrid                # expect: none pending

# serving proof unchanged:
curl -s -o /dev/null -w '%{http_code}\n' --resolve proxy.iogrid.org:443:45.151.123.50 https://proxy.iogrid.org/  # any code except 000/timeout
```
Root-cause hardening (prevents the next leak — pick one):
```bash
# (a) retire the broken issuer if nothing else uses it:
kubectl get certificate -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}: {.spec.issuerRef.name}{"\n"}{end}' | grep letsencrypt-prod-http01
kubectl delete clusterissuer letsencrypt-prod-http01     # only if the grep shows no remaining consumer
# (b) or repoint its solver to the proven traefik ingress solver (same shape as letsencrypt-prod).
```
Rollback: `kubectl apply -f /root/5088-backups/certificate-proxy-iogrid-org.yaml` (recreates the pending state; harmless).

### Step 3 — `proxy-gateway-socks5` (founder-timing)

```bash
# consumer audit — connections arriving via the NodePort (ETP=Cluster) are SNAT'd, so
# in-pod they appear from the CNI gateway (10.42.0.1); in-cluster ClusterIP traffic shows pod IPs:
kubectl logs -n iogrid deploy/proxy-gateway --since=168h | grep -iE 'accept|connect|1080' | head -50
```
- **No external :31080 traffic** → delete:
  ```bash
  kubectl delete svc -n iogrid proxy-gateway-socks5
  # customers keep using the live passthrough: socks5h://<user>:<token>@proxy.iogrid.org:443
  ```
- **External plain-1080 still needed** → apply the §854-clean replacement FIRST, then delete:
  ```bash
  kubectl apply -f 20-proxy-gateway-socks5-lb.yaml       # external endpoint becomes 45.151.123.50:1080
  curl -s --max-time 8 --proxy socks5h://45.151.123.50:1080 https://example.org -o /dev/null -w '%{http_code}\n'
  kubectl delete svc -n iogrid proxy-gateway-socks5
  # notify consumers: :31080 → :1080 (one-time port change)
  ```
  Alternative with zero new Services: add traefik entrypoint `--entryPoints.tcpingress-1080.address=:1080/tcp`
  + port `{name: tcpingress-1080, port: 1080, targetPort: 1080}` on `kube-system/traefik` —
  the existing dangling IngressRouteTCP `proxy-socks5-iogrid-org` starts working as designed.

Rollback: `kubectl apply -f /root/5088-backups/proxy-gateway-socks5.yaml`

### Step 4 — `cinova/catalog-svc` (never-touch namespace — founder one-liner)

```bash
kubectl get endpoints -n cinova catalog-svc    # expect <none> — proves no consumer can break
kubectl delete svc -n cinova catalog-svc
```
Rollback: `kubectl apply -f /root/5088-backups/catalog-svc.yaml`.
Future redeploy: use `30-cinova-catalog-svc-clusterip.yaml` (hash-free selector, ClusterIP).

### Final gate

```bash
kubectl get svc -A --field-selector spec.type=NodePort    # expect: No resources found
# K24 PolicyReport (once kyverno-policies ≥ the #5089 release is rolled to the mothership):
kubectl get clusterpolicyreport -o wide | grep forbid-nodeport-service
```

---

## Appendix — LB Services that still allocate nodePorts (K24 rule-2 audit surface)

Live state (`allocNP` = `allocateLoadBalancerNodePorts`):

| Service | ETP | allocNP | klipper datapath | Action |
|---|---|---|---|---|
| `kube-system/traefik` | Cluster | true | hostPort → **ClusterIP** (env `DEST_IPS=10.43.17.31`) | Safe to set `allocNP: false` — nodePorts 30574/31495/32558/30569 are unused by the datapath |
| `openova-system/powerdns-anycast` | Cluster | **false** | hostPort → ClusterIP | Already clean (healed on chart roll) |
| `stalwart/stalwart-mail` | **Local** | true | hostPort → **hostIP:nodePort** | Do NOT flip allocNP alone — with ETP=Local, klipper targets the nodePort; flipping breaks mail. ETP=Local is there to preserve client source IPs (SPF/rate-limits). Leave; migrate off klipper before hardening |
| `iogrid/vpn-gateway-wireguard` | **Local** | true | hostPort → hostIP:nodePort | Same constraint as stalwart |
| `iogrid/vpn-svc-stun` | **Local** | true | hostPort → hostIP:nodePort (env `DEST_PORT=31162`, `DEST_IPS=hostIPs`) | Same; STUN needs real client source IP |

These nodePorts are internal klipper plumbing (nothing external dials them — external
traffic enters on the hostPorts 25/443/3478/… held by `svclb-*` pods), but K24 rule 2 will
report the three ETP=Local rows in Audit mode. That is the correct signal to leave visible
until they migrate off klipper's ETP=Local mode; do not add namespace excludes for them.
