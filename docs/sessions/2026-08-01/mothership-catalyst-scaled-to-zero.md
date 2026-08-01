# Mothership `catalyst` namespace scaled to zero (observed 2026-08-01 ~13:1x UTC)

**Not an action of mine.** Recording state only — no scale-up attempted; the mothership is
read-only in this session and the change may be deliberate (hw292 prep, maintenance).

## What is true right now

```
$ kubectl -n catalyst get pods
No resources found in catalyst namespace.

$ kubectl -n catalyst get deploy
NAME                  READY   UP-TO-DATE   AVAILABLE   AGE
catalyst-api          0/0     0            0           93d
catalyst-ui           0/0     0            0           134d
openova-flow-server   0/0     0            0           81d

$ kubectl -n catalyst get events --sort-by=.lastTimestamp | tail -1
41m   Normal   NoPods   poddisruptionbudget/openova-flow-pg-primary   No matching pods found
```

User-visible impact:

| URL | Code |
|---|---|
| `https://console.openova.io/sovereign/` | **503** |
| `https://console.openova.io/` | 302 |

## Blast radius — scoped to `catalyst` only

The cluster itself is healthy. Node `vmi3116389` Ready (153d), **110 pods Running cluster-wide**:

| ns | running |
|---|---|
| catalyst | **0** |
| iogrid | 24 |
| axon | 2 |
| agenity | 1 |
| chepherd-hub | 1 |

So this is not a node or cluster failure — the `catalyst` workloads specifically are at zero desired
replicas.

## Why it matters for this session's evidence

Earlier today I walked the deployment wizard on this exact surface and captured findings W1-W5,
including the `ORG_DEFAULTS` fabrication and the HQ→region propagation. **That evidence remains
valid** — it was captured live, timestamped, with screenshots, while the surface was serving
(wizard steps 1-6 walked between ~09:00 and 12:29 UTC). It simply cannot be re-walked until
`catalyst` is scaled back up.

Likewise the janitor observation (`[JANITOR] pass complete` at `07:29:52Z`, which confirmed #5545
live in production) came from pod `catalyst-api-7c558f4d98-89scx`, which **no longer exists**. The
log line was read directly at the time and is quoted verbatim in
`mothership-janitor-live-observation.md`; the pod's disappearance does not retract it, but nobody
can re-read those logs now.

## Consequence for remaining walk work

Every mothership-scoped walk is blocked until `catalyst` returns:

- the wizard (steps 7-8, and any re-walk of 1-6)
- the janitor loop (R1 was already vacuous; it is now unobservable as well)
- the deployment registry via `catalyst-api` exec
- the `/infrastructure/topology` endpoint (was 401; now 503)

Sovereign-scoped rows were already blocked on hw292. With `catalyst` at zero, the mothership-scoped
remainder is blocked too — the walkable surface for this session is currently empty.

## Deliberately not done

- **No scale-up.** `kubectl scale deploy/catalyst-api --replicas=1` would "fix" the 503, but the
  mothership is read-only for me and I do not know the intent behind the scale-down. Reversing a
  deliberate maintenance action is worse than reporting it.
- **No issue filed.** This is live operational state, not a code defect. It needs an operator
  decision, not a ticket.
