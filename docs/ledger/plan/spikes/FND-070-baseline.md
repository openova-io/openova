# FND-070: Baseline of the first fresh installation of main

Two runs of the gate workflow against one fresh installation of a release candidate. Nothing was repaired on the environment. This file is written from the two records by the tool of the ticket FND-070 and is not edited by hand.

Candidate: rc/20261115T144352Z-67402a1 at commit 67402a108e92d9e44c154ba4650867dec6c1af16
Environment: hw307, deployment 9836870916c31984, provider huawei
Record 1: docs/ledger/acceptance/hw307-20261115T195357Z-8023.json
Record 2: docs/ledger/acceptance/hw307-20261115T203526Z-ec9f.json
Record before the wipe: docs/ledger/acceptance/hw307-20261115T124804Z-66a6.json
Previous deployment: b82699b905faa56a, resources left after its wipe: 0
Result: run 1 FAIL, run 2 FAIL
Fresh: run 1 false (not fresh: check FND-G02 failed in step FND-G02.start.1: 10 findings: Deployment catalyst-system/catalyst-api: field manager kubectl-set (Update); Deployment catalyst-system/catalyst-organization-controller: field manager kubectl-set (Update); Deployment flux-system/helm-controller: annotation ku...), run 2 false (not fresh: check FND-G02 failed in step FND-G02.start.1: 10 findings: Deployment catalyst-system/catalyst-api: field manager kubectl-set (Update); Deployment catalyst-system/catalyst-organization-controller: field manager kubectl-set (Update); Deployment flux-system/helm-controller: annotation ku...)
Hand-over chain: completed
Environment created at: 2026-11-15T16:03:19Z
End of the hand-over chain: 2026-11-15T18:55:03Z
Pre-flight read at: 2026-11-15T15:41:06Z
Minutes from the pre-flight reading to the end of the hand-over chain: 193
Minutes from the creation of the environment to the end of the hand-over chain: 171
Certificates issued: not counted by the gate records

## The cloud account before the fire

| Kind | Used | Allowed | Left by the wipe |
|---|---|---|---|
| servers | 1 | - | 0 |
| volumes | 2 | 400 | 0 |
| addresses | 1 | - | 0 |
| load_balancers | 0 | - | 0 |
| nat_gateways | 0 | - | 0 |
| networks | 1 | 5 | 0 |
| security_groups | 3 | - | 0 |
| keypairs | 3 | - | 0 |
| peerings | 0 | - | 0 |
| buckets | 48 | 100 | 0 |
| dns_records | - | - | 0 |

## Nodes per region

| Region | Requested | Ready in run 1 | Ready in run 2 |
|---|---|---|---|
| hw-me-east-215-a-rtz-prod | 6 | 6 | 6 |
| hw-me-east-215-b-rtz-prod | 6 | 6 | 6 |

## Journeys

| Journey | Run 1 | Run 2 |
|---|---|---|
| J1 | PASS | PASS |
| J2 | PASS | PASS |
| J3 | PASS | PASS |
| J4 | PASS | PASS |
| J5 | PASS | PASS |

## Checks

One line per check. The first failing step and the reason are those of run 1, or of run 2 when run 1 passed.

| Check | State at run | Run 1 | Run 2 | First failing step | Reason |
|---|---|---|---|---|---|
| AIP-G01 | required | PASS | PASS | - | - |
| BSS-G02 | required | PASS | PASS | - | - |
| CLD-G06 | required | PASS | PASS | - | - |
| CLD-G09 | required | PASS | PASS | - | - |
| CMP-G01 | expected-fail | PASS | PASS | - | - |
| CMP-G02 | expected-fail | FAIL | FAIL | run 1 CMP-G02.3: every listed policy shows the mode of its engine object | 10 of 26 policies differ: cilium-l7-mtls: the API shows "permissive" and the engine object carries enforce; flux-managed: the API shows "permissive" and the engine object carries enforce; forbid-local-path-storage: the API shows "permissive" and the engine object carries enforce; forbid-nodeport-... |
| CTL-G04 | expected-fail | FAIL | FAIL | run 1 CTL-G04.1: the lock report of every region is read | region hw-me-east-215-b-rtz-prod has no binding of the policy catalyst-intent-lock: the lock is not installed |
| CTL-G07 | required | PASS | PASS | - | - |
| CTL-G12 | required | PASS | PASS | - | - |
| FND-G01 | expected-fail | FAIL | FAIL | run 1 FND-G01.6: every container has a digest and one image reference has one digest in region hw-me-east-215-b-rtz-prod | 1 containers have no image digest: guacamole/guacamole-bp-guacamole-admin-enroll-29912888-k6mfp/admin-enroll |
| FND-G02 | expected-fail | FAIL | FAIL | run 1 FND-G02.start.1: first look, before the journeys: no tracked object of region hw-me-east-215-a-rtz-prod was changed by hand | 10 findings: Deployment catalyst-system/catalyst-api: field manager kubectl-set (Update); Deployment catalyst-system/catalyst-organization-controller: field manager kubectl-set (Update); Deployment flux-system/helm-controller: annotation kubectl.kubernetes.io/restartedAt=2026-11-15T18:42:00Z; Dep... |
| FND-G03 | expected-fail | PASS | PASS | - | - |
| FND-G04 | expected-fail | FAIL | FAIL | run 1 FND-G04.3: no container of a platform namespace has more than 3 restarts in region hw-me-east-215-a-rtz-prod | 13 containers have more than 3 restarts: cert-manager/cert-manager-cainjector-64d644f774-44997/cert-manager-cainjector (5); chargeback/chargeback-5444c5b7f6-82wnt/chargeback (4); chargeback/chargeback-5444c5b7f6-whwfs/chargeback (4); cnpg-system/cnpg-cloudnative-pg-cbb9dfbb9-qt9zt/manager (5); cn... |
| FND-G05 | expected-fail | PASS | PASS | - | - |
| FND-G06 | expected-fail | PASS | PASS | - | - |
| FND-G07 | expected-fail | PASS | PASS | - | - |
| FND-G08 | expected-fail | PASS | PASS | - | - |
| FND-G09 | expected-fail | PASS | PASS | - | - |
| FND-G10 | expected-fail | PASS | PASS | - | - |
| FND-G11 | expected-fail | PASS | PASS | - | - |
| FND-G12 | expected-fail | PASS | PASS | - | - |
| FND-G13 | expected-fail | PASS | PASS | - | - |
| INF-G01 | required | FAIL | FAIL | run 1 INF-G01.4: kured cannot run on control-plane nodes, every node is schedulable and Ready, and one worker node that runs workloads is chosen in region hw-me-east-215-a-rtz-prod | unschedulable or not Ready before the check started: catalyst-hw307-omani-works-98368709-me-east-215-a-we92dcb |
| NET-G01 | required | PASS | PASS | - | - |
| NET-G03 | required | PASS | PASS | - | - |
| NET-G04 | required | PASS | PASS | - | - |
| NET-G08 | required | FAIL | FAIL | run 1 NET-G08.1: the network registry of region hw-me-east-215-a-rtz-prod equals its Cilium configuration | region hw-me-east-215-a-rtz-prod has 0 Sovereign objects, want exactly 1 |
| SEC-G01 | required | PASS | PASS | - | - |
| SEC-G02 | expected-fail | FAIL | FAIL | run 1 SEC-G02.2: the k3s token is not of the derived form, the kubeconfig has mode 600 and no bootstrap file remains on node catalyst-hw307-omani-works-98368709-me-east-215-a-cp1-c7d347 of region hw-me-east-215-a-rtz-prod | /etc/rancher/k3s/k3s.yaml has mode 644, want 600; bootstrap files remain: /var/lib/catalyst/cloud-credentials-secret.yaml, /var/lib/catalyst/ghcr-pull-secret.yaml, /var/lib/catalyst/handover-jwt-public-secret.yaml, /var/lib/catalyst/harbor-robot-token-secret.yaml, /var/lib/catalyst/object-storage... |
| SEC-G04 | required | PASS | PASS | - | - |
| SEC-G07 | required | PASS | PASS | - | - |
| SEC-G11 | expected-fail | FAIL | FAIL | run 1 SEC-G11.1: the Secrets of the platform namespaces of region hw-me-east-215-a-rtz-prod that match no register entry are at most allowedUnmatched | 432 Secrets of the platform namespaces match no register entry; the register allows 0 |
| SVC-G06 | expected-fail | FAIL | FAIL | run 1 SVC-G06.2: every foundation HelmRelease that is not exempt has a passing function test in region hw-me-east-215-a-rtz-prod | 35 of 35 foundation HelmReleases have no passing function test: flux-system/bp-catalyst-platform (bp-catalyst-platform) none: spec.test.enable is not true; flux-system/bp-cert-manager (bp-cert-manager) none: spec.test.enable is not true; flux-system/bp-cert-manager-issuers (bp-cert-manager-issuer... |

## Does a failed hand-over chain explain a failed journey check?

Chain cause FND-G10: no, the hand-over chain completed
Chain cause FND-G11: no, the hand-over chain completed
Chain cause FND-G12: no, the hand-over chain completed
