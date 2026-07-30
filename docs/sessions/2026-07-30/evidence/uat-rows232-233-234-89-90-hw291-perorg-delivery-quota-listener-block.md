# hw291 UAT walk — rows 232/233/234/89/90 (per-Org application delivery)
env: hw291.omantel.biz  dep 2c2d746b578c636b  Org=uatcorp (plan s, single-region me-east-215-a)
walked: 2026-07-30T01:15:02Z   READ-ONLY

## 1. PURCHASE LANDED (refutes the earlier 'nothing was purchased' stamp)
The per-Org app HelmReleases now EXIST, delivered by Kustomization 'org-tenants'
(GitRepository/openova-org-tenants, path ./clusters/hw291.omantel.biz/org-tenants,
rev org-tenants@sha1:4ce5e89621ca6f1a8dd1c1524c64ecff0e53e3a2, Ready=True).
The earlier walk checked only uatcorp/catalyst-tenant@6e01e449 and missed this source.
NAME                         KUST          READY   STATUS
bp-agenity                   org-tenants   True    Helm upgrade succeeded for release uatcorp/bp-agenity.v2 with chart bp-agenity@0.5.21
bp-keycloak                  org-tenants   False   Helm upgrade failed for release uatcorp/bp-keycloak with chart bp-keycloak@1.5.7: context deadline exceeded
bp-newapi                    org-tenants   False   dependency 'uatcorp/bp-keycloak' is not ready
bp-openclaw                  org-tenants   False   dependency 'uatcorp/bp-keycloak' is not ready
bp-stalwart-tenant           org-tenants   False   dependency 'uatcorp/bp-keycloak' is not ready
bp-wordpress-tenant          org-tenants   False   dependency 'uatcorp/bp-keycloak' is not ready
uatwalk-ahs-07300830-rtz-a   <none>        True    Helm install succeeded for release uatcorp/uatwalk-ahs-07300830-rtz-a.v1 with chart bp-postgres@0.2.14

## 2. BLOCKER A — plan-s ResourceQuota cannot fit the per-Org baseline
plan 's' = CPU 2 / Mem 4Gi, hard-capped: core/controllers/organization/internal/gitops/manifests.go:121
  "s": {Slug: "s", CPU: "2", Mem: "4Gi", Burstable: false}

Occupancy BEFORE keycloak (measured):
  bp-agenity-0                 Guaranteed  1     CPU / 2Gi     <- 50% of the entire plan
  bp-keycloak-postgresql-0     Guaranteed  500m      / 512Mi
  oidc-gate-agenity-uatcorp    Guaranteed  50m       / 64Mi
  ------------------------------------------------  used 1550m / 2624Mi  of 2 / 4Gi
bp-keycloak-0 requests 1 CPU / 2Gi  ->  2550m / 4674Mi  ->  EXCEEDS BOTH caps.

Live admission rejection (repeated, verbatim from Events):
  FailedCreate  statefulset/bp-keycloak  create Pod bp-keycloak-0 in StatefulSet bp-keycloak failed
  error: pods "bp-keycloak-0" is forbidden: exceeded quota: plan-quota,
  requested: limits.cpu=1,limits.memory=2Gi,requests.cpu=1,requests.memory=2Gi,
  used: limits.cpu=1550m,limits.memory=2624Mi, limited: limits.cpu=2,limits.memory=4Gi

Terminal HR state after the 15m Helm timeout elapsed (polled 01:04Z -> 01:12Z):
  Stalled=True (RetriesExceeded) Failed to upgrade after 1 attempt(s)
  Ready=False (UpgradeFailed) Helm upgrade failed for release uatcorp/bp-keycloak with chart bp-keycloak@1.5.7: context deadline exceeded
  Released=False (UpgradeFailed) Helm upgrade failed for release uatcorp/bp-keycloak with chart bp-keycloak@1.5.7: context deadline exceeded
  upgradeFailures=1
=> bp-keycloak Stalled/RetriesExceeded permanently. All 4 purchased app HRs
   (bp-openclaw, bp-wordpress-tenant, bp-stalwart-tenant, bp-newapi) declare
   dependsOn: bp-keycloak, so they are permanently DependencyNotReady and never
   render a pod, Service or HTTPRoute. Related: #5393 (2-CPU plan cannot fit its
   own baseline), #5391 (Stalled per-Org HR has no operator remedy).

## 3. BLOCKER B — *.uatcorp.omani.homes listeners declared but NOT admitted (#5341 class)
cilium-gateway-console (ns kube-system): 8 listeners declared, 6 admitted.
declared:
  console-https host=console.hw291.omantel.biz
  console-http host=console.hw291.omantel.biz
  api-https host=api.hw291.omantel.biz
  api-http host=api.hw291.omantel.biz
  marketplace-https host=marketplace.hw291.omantel.biz
  marketplace-http host=marketplace.hw291.omantel.biz
  console-https-uatcorp host=*.uatcorp.omani.homes
  console-http-uatcorp host=*.uatcorp.omani.homes
admitted:
  console-https
  console-http
  api-https
  api-http
  marketplace-https
  marketplace-http
Both console-https-uatcorp / console-http-uatcorp (*.uatcorp.omani.homes) are DROPPED.
NOT a cert problem: Certificate kube-system/org-wildcard-tls-uatcorp-omani-homes
Ready=True, dnsNames ["*.uatcorp.omani.homes","uatcorp.omani.homes"].
Gateway Programmed=False(AddressNotAssigned) is NORMAL under the 854 hostPort model.

## 4. BLOCKER C — per-Org A-records are SPLIT across two EIPs (pool misalignment)
  console-ELB EIP      = 212.72.24.1  (console.hw291.omantel.biz, the working control)
  primary gateway EIP  = 212.72.24.6  (apex hw291.omantel.biz)
  agenity.uatcorp.omani.homes    -> 212.72.24.1  console-ELB   (pool-aligned)
  console.uatcorp.omani.homes    -> 212.72.24.1  console-ELB   (pool-aligned)
  wordpress.uatcorp.omani.homes  -> 212.72.24.6  PRIMARY       (MISALIGNED)
  openclaw.uatcorp.omani.homes   -> 212.72.24.6  PRIMARY       (MISALIGNED)
  mail.uatcorp.omani.homes       -> 212.72.24.6  PRIMARY       (MISALIGNED)
  keycloak.uatcorp.omani.homes   -> 212.72.24.6  PRIMARY       (MISALIGNED)
cilium-gateway (the .6 primary) declares ONLY *.hw291.omantel.biz + apex - it has NO
*.uatcorp.omani.homes listener at all. So row 233's explicit clause 'host resolves to
the console-ELB EIP (not the primary ELB)' FAILS independently of the workload.

## 5. HTTP EVIDENCE (6 independent curl invocations each; each 000 = one TLS reset)
### FINAL probe round 2026-07-30T01:13:43Z — 6 independent curl invocations each
CONTROL console.hw291 (.1)                      000 200 200 200 200 200   (ok=5 fail=1 of 6)
CONTROL2 marketplace.hw291 (.1)                 200 200 000 200 200 200   (ok=5 fail=1 of 6)
ROW232 openclaw/readyz (.6)                     000 000 000 000 000 000   (ok=0 fail=6 of 6)
ROW233+90 wordpress (.6)                        000 000 000 000 000 000   (ok=0 fail=6 of 6)
ROW234 mail (.6)                                000 000 000 000 000 000   (ok=0 fail=6 of 6)
ROW89 per-Org console (.1)                      000 000 000 000 000 000   (ok=0 fail=6 of 6)
XTRA agenity (.1, HR Ready=True)                000 000 000 000 000 000   (ok=0 fail=6 of 6)
XTRA keycloak (.6)                              000 000 000 000 000 000   (ok=0 fail=6 of 6)

TLS-level cause, identical on both EIPs:
  curl: (35) OpenSSL SSL_connect: Connection reset by peer

## 6. THE DECISIVE CONTROL
agenity.uatcorp.omani.homes is a per-Org app that is FULLY HEALTHY:
  bp-agenity HR Ready=True (Helm upgrade succeeded, chart bp-agenity@0.5.21)
  pod bp-agenity-0 1/1 Running; HTTPRoute oidc-gate-agenity-uatcorp present
  DNS pool-aligned to the console-ELB EIP 212.72.24.1
...and it STILL returns 0/6 (TLS reset), while console.hw291.omantel.biz on the SAME
VIP returns 5/6 200. This isolates Blocker B from the workload layer: even a perfectly
delivered per-Org app cannot serve while its gateway listener is unadmitted.
Controls also quantify the known VIP drop: 1/6 on each control (~17%), vs 6/6 on
every uatcorp host - categorical, not noise.

## 7. region-b (single-region Org: absence is CORRECT, not a defect)
  ns uatcorp: NotFound. No openclaw/wordpress/stalwart/per-Org-mysql pod.
  (dragonfly/dragonfly-mysql-0 is the PLATFORM registry mysql, not the row-233 DB.)

## 8. NodePort audit (854)
  Services of type=NodePort across all namespaces, both regions: NONE
