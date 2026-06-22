# Demo Org convergence — live evidence (omantel.biz, dep 4635277cae4ffed9)
## Captured: 2026-06-22T15:17:25Z

### bp-cnpg HR (was Stalled → reconciled to Ready)
NAME      AGE   READY   STATUS
bp-cnpg   13h   True    Helm upgrade succeeded for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-cnpg.v3 with chart bp-cnpg@1.0.10

### All demo Org HRs
NAME                  AGE     READY   STATUS
agenity-demo          5h44m   True    Helm upgrade succeeded for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/agenity-demo.v3 with chart bp-agenity@0.3.0
bp-cnpg               14h     True    Helm upgrade succeeded for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-cnpg.v3 with chart bp-cnpg@1.0.10
bp-keycloak           14h     True    Helm upgrade succeeded for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-keycloak.v3 with chart bp-keycloak@1.5.0
bp-newapi             14h     False   Helm install failed for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-newapi with chart bp-newapi@1.4.121: context deadline exceeded
bp-openclaw           14h     False   Helm install failed for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-openclaw with chart bp-openclaw@0.2.1: context deadline exceeded
bp-stalwart-tenant    14h     False   Helm install failed for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-stalwart-tenant with chart bp-stalwart-tenant@0.1.3: context deadline exceeded
bp-wordpress-tenant   14h     False   Helm install failed for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/bp-wordpress-tenant with chart bp-wordpress-tenant@0.4.1: failed to create resource: admission webhook "validate.kyverno.svc-fail" denied the request: ...
vc-demo               14h     False   Helm upgrade failed for release org-7283eb4a-19e5-4e86-9066-d4aa26762064/vc-demo with chart vcluster@0.19.10: context deadline exceeded

### vc-demo coredns (live-patched to harbor proxy)
NAME                                              READY   STATUS                       RESTARTS   AGE
bp-cnpg-cloudnative-pg-69d4bd4cfc-smv2b           1/1     Running                      0          26m
bp-stalwart-tenant-0                              0/1     CreateContainerConfigError   0          14m
coredns-9cd65dfc5-vdndb-x-kube-system-x-vc-demo   1/1     Running                      0          15m

### WordPress Applications (demo Org)
NAME           BLUEPRINT      VERSION   ENVIRONMENT                                     PLACEMENT   PHASE     AGE
demo-wp-blog   bp-wordpress   0.4.1     org-7283eb4a-19e5-4e86-9066-d4aa26762064-prod   singleton   Pending   4h51m
demo-wp-shop   bp-wordpress   0.4.1     org-7283eb4a-19e5-4e86-9066-d4aa26762064-prod   singleton   Pending   5h36m
test-shop      bp-wordpress   0.4.1     demo-prod                                       singleton   Pending   29m
