# hw128 — jobs-both-regions PASS (#3278 / defect-A) — live walk 2026-06-11

Logged into the sovereign operator console (console.hw128.omani.works) via SSO, clicked Jobs.

**PASS** — the Jobs table renders a **Region** column + "Filter by region" control, and jobs from BOTH regions appear:
- me-east-215-a (primary) — 16 rows
- me-east-215-b-1 (secondary) — 690 rows (alloy, catalyst-platform, cert-manager, cilium, cluster-autoscaler, …)

Columns: Name · App · **Region** · Deps · Parent · Status · Started · Duration.

This is exactly the defect-A fix (#3278): the post-handover sovereign jobs page aggregates the secondary region via the second helmwatch informer (D16 handover-export), with a first-class Job.Region field. Pre-fix it showed one region only. Screenshot: hw128-jobs-both-regions-PASS.png.
