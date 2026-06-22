# Demo Org convergence — verified fixes (omantel.biz, dep 4635277cae4ffed9)
## Updated: 2026-06-22T15:39:29Z

## BLOCKER 1 — bp-cnpg HR stalled on #3859 flux-managed webhook-gate denial
- Root: HR was Stalled(RetriesExceeded); the #3859 openova.io/organization Exists exclusion IS working (server-dry-run of the exact hook Job shape → created).
- Fix: reconciled (suspend/resume). RESULT:
  bp-cnpg READY=True
  bp-cnpg-cloudnative-pg-69d4bd4cfc-smv2b           1/1     Running                      0              48m

## BLOCKER 2 — demo-Org ImagePullBackOffs
### vc-demo coredns (bare coredns/coredns:1.11.0 → harbor proxy 1.11.3)
  coredns-9cd65dfc5-vdndb-x-kube-system-x-vc-demo   1/1     Running                      0               38m
### bp-stalwart-tenant (bare docker.io → harbor proxy; chart 0.1.4 LIVE)
  image=harbor.openova.io/proxy-dockerhub/stalwartlabs/stalwart@sha256:5d75cff4e9c6d75e64636e9ef9674b1d877f8f6fb2e11ee8176fbad3faaa5289
  (image pull RESOLVED; remaining CreateContainerConfigError = missing stalwart-admin secret, separate SSO gap)
### bp-openclaw-controller — 0.1.0-placeholder scaffold image (no real image; not a routing fix)

## BLOCKER 3 (discovered) — bp-wordpress-tenant local-path PVC + CNPG sync param
### local-path: chart 0.4.3 (PR #4140/#4141) — wp-content PVC now Bound on durable CSI
  PVC status=Bound storageClass=evs-ssd
### CNPG sync: chart 0.4.4 (PR #4142) — native spec.postgresql.synchronous (pending publish+reconcile)
