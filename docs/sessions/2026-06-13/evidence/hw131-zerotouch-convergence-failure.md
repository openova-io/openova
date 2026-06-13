# hw131 (f8d298b0f1615f7b) — zero-touch convergence FAILURE forensic (2026-06-13)

Fresh 2-region prov (SHARED_PG=true, me-east-215-a/-b, omani.works) fired to validate the
5 tickets reproduce zero-touch. **It did not converge** (~3h, status flipped `failed` at the
120-min watcher budget; cluster never reached steady-state; console TLS never came up).

## Root-cause chain (all observed in the mothership deployment log stream)
1. **bp-cnpg slow-ghcr artifact fetch (the root delay).** Flux source-controller logged
   `HelmChart 'flux-system/flux-system-bp-cnpg' is not ready: does not have an artifact` /
   `Source not ready: artifact not found. Retrying in 30s` for ~2h. The pin (1.0.9) IS
   published in ghcr — so not a bad pin, a slow/flaky source fetch. EVERY stateful component
   (3× shared-PG, cnpg-pair, harbor, grafana, openbao, **powerdns**) gates on bp-cnpg, so this
   one fetch starved the whole boot.
2. **powerdns zone-bootstrap DeadlineExceeded (the terminal blocker).** Once cnpg finally
   installed (`Helm install succeeded ... bp-cnpg@1.0.9`), powerdns still never went Ready:
   `job powerdns-zone-bootstrap failed: DeadlineExceeded`, repeating. The hook's 840s deadline
   (slot-11 raised it for the cold-pdns-pg-initdb case) was exhausted because cnpg+pdns-pg came
   up ~2h late. powerdns-not-ready → cert-manager DNS01 can't issue the wildcard → **no TLS** →
   console + every app endpoint = 000.
3. **Operator blinded.** kubeconfig never captured (`409 ... has not been captured`; cloud-init
   PUT gap), SSH firewall-blocked on kom4dc (:22 Connection timed out), cloud-init self-upload
   log 404 (#3132 gap on this prov). No in-place diagnosis/fix possible.

## #3380 robustness findings (fold into the open issue — do NOT file new TBDs)
- bp-cnpg source fetch must be resilient to slow-ghcr (harbor-proxy mirror + retry, or pre-pull),
  since the entire boot single-points on it.
- powerdns zone-bootstrap should gate on the pdns-pg Cluster being Ready (not a fixed deadline),
  so a late cnpg doesn't permanently wedge it.
- cloud-init kubeconfig PUT + #3132 log self-upload both failed on this huawei prov — the
  PUT-early discipline (#3135) regressed or the upload step didn't execute.

## Recovery
Wipe hw131 + re-fire (fresh source-controller almost always draws a fast bp-cnpg fetch; the
intermittency is per-prov). If the re-fire ALSO wedges on powerdns, the findings above are
confirmed reproducible and get source fixes before any further walk.
