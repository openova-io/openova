# 3647 — Cutover bulletproof: zero external deps, proven — UAT walk (web UI + operator)

> **Ticket:** [#3647](https://github.com/openova-io/openova/issues/3647) · **Car:** T1 · **PR:** #3650 · **Train:** `train/hw150`
>
> **What this proves:** after handover + cutover the Sovereign has **no external dependency** — and the
> cutover gate PROVES it by force-rolling a pod under the deny-egress hold and requiring a fresh pull
> from the **local Harbor** (`registry.<fqdn>`). Holds for host AND vcluster apps. `cutoverComplete=true`
> is set ONLY when that fresh pull succeeds.
>
> **Format law (founder, 2026-06-03):** UI rows where the surface exists; the genuinely-infra checks
> (a pod pulling an image) are operator actions, clearly marked — NOT demoted, because the pull IS the
> acceptance. Replace `<fqdn>` = hw150 FQDN; `<JWT>` = handover token. Tick **☑** pass, **☒** fail.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in as emrah.baysal, no login form.

## Section 1 — The gate sets `cutoverComplete` only after a fresh pull under deny-egress (founder #0/#3)

| Step | Go to (URL) / action | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/jobs` (after handover fires the cutover) | watch the `Cutover` activity (visible via T3) | step-08 **egress-block** runs the fresh-pull proof, then reaches **Succeeded** | ☐ |
| 1.2 | `/jobs` → the Cutover group | read the group state at completion | the group is `Succeeded` ONLY after step-08; `cutoverComplete=true` | ☐ |

## Section 2 — A pod rolled AFTER cutover pulls from local Harbor, not ghcr (the #3647 fix, live)

| Step | Action (operator) | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | host app (e.g. grafana) | `kubectl rollout restart deploy/grafana-bp-grafana -n <ns>` | new pod schedules | ☐ |
| 2.2 | `kubectl describe pod <new grafana pod>` | read the Events / image source | image pulled from `registry.<fqdn>` (local Harbor); **no** `ghcr.io ... 401`, no `ImagePullBackOff` | ☐ |
| 2.3 | a **vcluster** app (generality) | roll one of its pods | pulls from local Harbor too — proves it is not a host-only fix | ☐ |

## Section 3 — Egress to the outside is blocked while the cluster stays alive

| Step | Action | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | during the 600s deny-egress hold | `kubectl exec <a pod> -- curl -m5 https://ghcr.io` | times out / connection refused (external egress denied) | ☐ |
| 3.2 | same window | `kubectl get nodes` + console still loads | apiserver / DNS / intra-cluster reachable (`enableDefaultDeny.egress:false` preserved, #3640) | ☐ |

## Appendix — automated (NOT acceptance)
- `helm template platform/self-sovereign-cutover/chart` renders clean; both embedded scripts `dash -n` clean.
- `04-registry-pivot-daemonset.yaml` writes containerd `certs.d/<host>/hosts.toml`; `08-egress-block-test-job.yaml` `run_fresh_pull_proof()` force-deletes a pod + fails the gate on `ImagePullBackOff`.
