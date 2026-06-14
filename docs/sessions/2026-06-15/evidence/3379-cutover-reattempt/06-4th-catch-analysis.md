# 4th catch on hw139 cutover step-03 harbor-prewarm — TRANSIENT kom4dc egress, NOT a #3558 logic defect

## What #3558 FIXED and proved live (both halves of Defect-1)
- New harbor-prewarm Job (`cutover-harbor-prewarm-1781473205`) minted at resume with
  `activeDeadlineSeconds: 5400` (the OLD failed Job `…1781464129` had 900) → proves
  `cutoverStepDeadline()` now honors the chart per-step timeout instead of clamping to
  the global 15m. Evidence: 00b-old-prewarm-job-DeadlineExceeded.json (900) vs
  03-new-prewarm-job-deadline5400.json (5400).
- The prewarm log shows `pushing 29 image(s) with parallelism=6` → the PREWARM_PARALLELISM=6
  fan-out is live. The old serial path stalled at 6/29; this run reached 27/29 in ONE pass
  (~11 min) — a clean ~4.5x improvement past the old wall. Evidence:
  04-prewarm-parallelism6-29images.log.
- Defect-2 (resume re-runs a transiently-failed step) proved live: catalyst-api startup
  emitted `cutover-resume: in-flight cutover detected on startup` (failedStep=harbor-prewarm)
  then `cutover-resume: spawning runCutover`, the engine DELETED the old DeadlineExceeded
  Job and minted the fresh one. Evidence: 02-catalyst-api-cutover-resume.log.

## The NEW (4th) catch — a transient throttled-egress connection drop on 2 of 29 images
Attempt-1 pod (`…-cf8gw`) tally: `push_ok=27 push_fail=2`. The 2 failures were
`catalyst-api:9c6f864` and `guacd:1.5.5`, BOTH identical:
```
level=info  msg="Reading blob body from https://ghcr.io/v2/.../blobs/sha256:… failed (unexpected EOF), reconnecting after 52428801 bytes…"
level=fatal msg="writing blob: Patch \"https://registry.hw139.omani.works/v2/.../blobs/uploads/…\": use of closed network connection"
```
i.e. a 50MB-blob read from ghcr.io got an unexpected EOF, skopeo reconnected, and the
WRITE to the LOCAL Harbor then hit `use of closed network connection`. This is the
documented kom4dc throttled-egress flake (the IPv6/throttle family), NOT a logic bug in
#3558 — the per-copy `--retry-times 3` simply wasn't enough for two simultaneous
large-blob drops. Evidence: 05-prewarm-attempt1-2transient-egress-drops.log.

## Auto-recovery is in-flight via the Job backoffLimit (zero-touch, no hand-patch)
The Job has `backoffLimit: 3`; after attempt-1 `failed=1`, a retry pod (`…-bpv87`) is
Running. It re-enumerates all 29 (the 27 already-pushed are idempotent skopeo no-ops) and
re-copies the 2 that failed. The retry's own skopeo static-binary fetch from
github.com/lework/skopeo-binary is itself slow on the throttled egress (the chart bounds it
with `curl --max-time 600 --retry 3 -C -`, per the in-template comment "~120-360KB/s, 38MB").
