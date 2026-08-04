# catalyst-api memory leak — measured on hw292, 2026-08-03

Live evidence for **#5642**. Everything here is a direct read from the running Sovereign
(dep `1c56518035a83e03`, post-cutover); nothing is inferred.

## The measurement

`api-mem-trend.log` samples the pod every 75s for ~9 minutes. The pod had OOMKilled between an
earlier reading and this window (`restartCount` 15 -> 16), so the curve starts from a **fresh
process**:

    559Mi -> 973Mi over 8.8 min = 47.2 Mi/min   (monotonic, no plateau)
    at that rate, 0 -> 4096Mi = 87 min
    observed cadence: 16 restarts / ~15.5h = 58 min per OOM

The rate predicts the cadence to the same order, and the residual is what a non-zero start
(559Mi within a minute of launch) and a non-uniform rate would produce.

## Why this is a leak and not a large working set

`products/catalyst/chart/templates/api-deployment.yaml:1804-1812` documents the designed
behaviour in place, written when the request was last trimmed:

> Actual steady-state usage observed at ~80-120Mi via kubectl top; the 96Mi request fits
> comfortably with ~16Mi headroom. limit stays at 4Gi (multi-region helmwatch + tofu plan
> still need burst budget).

The process passes that band inside its first minute and never flattens. A steady state, however
large, would flatten.

## Controls

| check | result |
|---|---|
| `kubectl top` on catalyst-ui, same namespace, same query | 9Mi — the measurement discriminates |
| `lastState.terminated.reason` on catalyst-ui | `Unknown` (vs `OOMKilled` on catalyst-api) — the field discriminates |
| window vs the region-kill drill (ended 20:22Z) | curve is 23:10-23:19Z on a process started ~23:09Z — not drill fallout |

## The trap this evidence exists to prevent

Raising `limits.memory` is the obvious fix and it is strictly wrong: at 47 Mi/min an 8Gi ceiling
buys ~3 hours instead of ~1 — the same failure, less often, and far harder to catch in the act.
The 4Gi limit is currently the only bound on the growth and is doing useful work by forcing the
symptom into the open hourly. Raising `requests` is equally beside the point: 96Mi was correct
for the behaviour observed when it was written.

## Blast radius

catalyst-api is this Sovereign's `catalyst-pin` OIDC provider, so every service needing a fresh
OIDC handshake depends on it. UAT rows 26, 29, 32, 34, 36, 37 and 38 all fail downstream of it
(see the same-day SSO walk). Not claimed: that the OOM caused the lost console session — the
session cookie is signed and stateless from a stable Secret and survives restarts.
