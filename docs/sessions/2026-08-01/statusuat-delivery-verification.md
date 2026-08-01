# `status/uat` delivery verification — 2026-08-01

Five issues carry `status/uat`, which means **code-complete, awaiting a live walk**. With
no Sovereign reachable (see [`completion-matrix.md`](completion-matrix.md) §Surface map —
contexts `Terminated`, every FQDN 000, zero catalyst CRDs on the mothership) the walk
cannot run. What CAN be established without a cluster is whether the code is actually
delivered, so the walk is fast and its verdict is trustworthy.

**This does NOT flip any UAT row.** A row asserts runtime behaviour; this records code
delivery. They are different claims and are kept apart deliberately — the same discipline
`f47870f6d` used for rows 9/53/60 ("verdicts deliberately UNCHANGED").

| issue | code state | evidence |
|---|---|---|
| **#5515** derivePattern fails open | **delivered — and correctly placed** | see analysis below |
| **#5477** lag published as measurement when never probed | **delivered** | `continuum_controller.go:1298` — `if su.LagObserved { status["replicationLagSeconds"] = … }`; the key is OMITTED when unprobed. Reader agrees: `placement_projection.go:121-127` carries `HasLag bool` + a `-1` sentinel |
| **#5489** four surfaces claim a nonexistent vCluster | **5 of 6 tasks delivered** | `organizations.api.ts:152` `isolation:'cluster'` · `org_list_from_cr.go:197` `vclusterNameFor(isolation, slug)` · `vclusterStatusFor()` returns zero `VClusterStatus{}` when `!BoundaryIsVcluster` · zero `vcluster` matches in `CreateOrganizationPage.tsx` · `DEFAULT_APP_ENVIRONMENT` removed. **Open: task 6**, no Environments console surface |
| **#5482** host-cluster label as PRIMARY REGION | **read side delivered; emit side open** | `b41c93b3c` fixed the handler. Emit seam localized: flat `status.primaryRegion` ← `plan.PrimaryRegion` (`application_controller.go:2593`, the DECLARED value) vs nested `status.placement.primaryRegion` ← normalized leaseHolder (`placement_projection.go:279`). Deferred deliberately — writing `status.placement` on the DR path cannot be validated without an env, and risking the Pillar-3 proof before a keystone fire is a bad trade |
| **#5485** observability defects 4/5/6 | **delivered, PR held** | `53bdf8052` ancestry into image `fb41faf`; PR #5534 open |

## #5515 — why the remaining fail-open is correct, not a gap

`DerivePattern` still returns `PatternSingleton` for an **empty** target list:
`primaries=0, standbys=0` falls through to `case standbys == 0`. There is no
`PatternUnknown` constant. On its face that is the reported fail-open, in the function
whose own doc says *"This is the ONLY place patterns come from."*

Both callers were checked before concluding anything:

| caller | guarded? | effect of an empty list |
|---|---|---|
| `applications_update.go:135` | **yes** — `len(b.Placement.Targets) > 0` | empty never reaches `DerivePattern` |
| `placement_fanout.go:146` | no | `empty → singleton → patternToBcpTopology → no Switchover` |

The unguarded one is **desired-state fanout**, and there `no Switchover` is the correct
outcome — the adjacent comment says so: *"A singleton / active-active placement gets no
Switchover (the producer skips it — nothing to fail over)."* An app with no declared
targets has nothing to fail over, so the current behaviour is right.

The fail-open therefore only ever harmed **display** — a console asserting "singleton" as
a fact about an app whose targets were simply absent. That is precisely where it was
fixed: `796e587b2` (console, *"never claim DR health without live backing"*) and
`applications_placement_runtime.go`, which derives real placement from live Pods.

**Conclusion: changing `DerivePattern`'s empty return would risk the DR fanout for no
display benefit.** The fix is complete and in the right layer. Recorded so the next reader
does not "fix" the derivation and regress the fanout.

## What still needs the walk

All five. Code delivery is not row-green: #5477 needs a real unprobed pair to show no lag
key; #5489 needs the four surfaces rendered; #5515 needs an app with absent targets shown
honestly; #5482 needs the Overview read; #5485 needs the observability surfaces. Those
stamp on hw292.
