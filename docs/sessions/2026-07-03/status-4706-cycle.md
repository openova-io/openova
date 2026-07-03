# Session status — #4706 fix cycle + fresh-prov validation (2026-07-03)

Concrete status of the #4706 (Console 000 / no-nodePort gateway) fix cycle:
what is committed, what state each environment is in, and what remains open.

## Committed (all merged to main; baked into catalyst-api image `0071ed2` + bootstrap-kit pin 1.4.993)

| Founder item (#4706) | Fix | Commits |
|---|---|---|
| (1) Console 000 / no-nodePort gateway | cilium 1.19.3 gateway-api hostNetwork (§854-clean, envoy binds node:443/:80 direct); console gateway on host ports 8443/8080 (no collision, no nodePort); slot-13 consumes the port substitute; console-ELB member set self-heals on node churn | `5c97fbf2d` (#4708), `d44d4bfb7` (#4718), `0071ed2e6` (#4721) |
| (2) False-"ready" readiness gate | `ready` gated on console front door answering HTTP <400 (`phase1_watch.go` `defaultConsoleReachable`) | `ba887fdf5` (#4707), `c8d59e03b` (#4716) |
| (3) Hetzner LB annotations leaked onto Huawei gateways | annotations provider-scoped | `d57c98ec9` (#4711) |
| (4) NodePorts forbidden | no-nodePort gateway path + CI guard against the hw218 console-port collision | `5c97fbf2d` (#4708), `e8ee08b27` (#4717) |
| (adjacent) Stale per-Org DNS → Console 000 on re-prov | per-Org DNS deprovision on Org delete + tenant-DNS teardown pool-key self-heal | `87ed0959d` (#4720), `6b7eda077` (#4719) |
| (adjacent) 2-region admission floor | POST with <2 regions and no explicit `bcpTopology` → HTTP 400 | in `5c97fbf2d` |

## Environment states

- **hw218** (`t?…` 2-region, was converged): WIPED. Judgment error — it was
  wiped to make room for a validation prov instead of being walked first;
  acknowledged to the founder, who then directed the current course (wipe the
  failed successor, re-fire on the settled image). Its 4 UAT evidence rows
  were reset via `scripts/reset-uat.py hw218` per the wiped-env policy.
- **hw219** (`2dc0950d93b121d0`): WIPED clean. It failed "abandoned mid-apply"
  because the prov was fired into an in-flight catalyst-api roll (the
  #4720/#4721 merges rolled the image at 05:59:42Z; the prov fired 06:04) —
  a violation of the documented pre-flight. Canonical wipe completed
  06:08:31Z (HTTP 200, 50s tofu destroy); record + workdir gone; janitor
  inventory afterwards: 0 VPCs, 0 orphan EIPs in the project.
- **hw220** (`907629bc3577355b`, `hw220.omani.works`): LIVE, converging.
  Fired 06:19:45Z on the fully settled `0071ed2` image — the first prov
  carrying the complete #4706 fix set. Tofu applied both regions
  (me-east-215-a/b, 5+5 workers, active-hotstandby) in ~7 min;
  `phase1-watching` since 06:27Z; 57/63 HRs (region-a) / 61 (region-b)
  at 07:00Z. LE-PROD certs (qaTestEnabled=false).
  Note: the reused prov body carried literal `"<redacted>"` OBS credentials
  (rebuilt from a redacting GET); the create handler only auto-derives OBS
  creds when the fields are EMPTY, so they were stripped before firing.

## Remaining open

1. **hw220 → `ready`** — the flip itself live-validates item (2); console
   answering <400 on the fresh prov live-validates (1)/(3)/(4) + #4718/#4721.
   Then: console 200 wire-capture + re-walk the 4 reset UAT rows on hw220.
2. **North Star walk (Pillar 4 / Phase 2e)**: fresh Org → agenity →
   `create_application` → HTTP 201, zero-touch, on hw220.
3. **Live-repro-gated candidates recorded on #4706** (deliberately not
   blind-fixed): per-Org cnpg RBAC crash (prior fix history #4143/#4322 —
   a blind edit would reopen the webhook hijack) and the `version:*` env
   default (`organization_gitops.go` `star()` empty→`"*"`; blind change
   would downgrade prod against stale catalog pins). hw220 is the repro
   environment for both.
