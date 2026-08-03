#!/usr/bin/env bash
# bp-self-sovereign-cutover — MAX_ARG_STRLEN guard (#5593).
#
# The Linux kernel caps a SINGLE exec argument (and each env string) at
# MAX_ARG_STRLEN = 32 * PAGE_SIZE = 131072 bytes. A step whose podSpec
# packs its shell script as one inline `args` element dies at exec with
# "argument list too long" BEFORE a single line runs — live hw292
# (dep 1c56518035a83e03): step-03's rendered script hit 135775 bytes and
# the cutover chain retry-looped at step 03 forever, cc=true unreachable.
#
# Rule: every rendered step podSpec's per-container inline args AND env
# values stay under the 110000-byte early-warning budget (margin under
# 131072 so growth is caught in CI, not on a live Sovereign). Large
# scripts belong in a ConfigMap key mounted and exec'd FROM FILE (the
# step-03 run.sh pattern, chart 0.1.159).
set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUDGET=110000

render="$(mktemp)"
trap 'rm -f "${render}"' EXIT
helm template arg-strlen-guard "${CHART_DIR}" >"${render}"

python3 - "${render}" "${BUDGET}" <<'PY'
import sys, yaml

render_path, budget = sys.argv[1], int(sys.argv[2])
failures = []
checked = 0

with open(render_path) as f:
    docs = list(yaml.safe_load_all(f))

for d in docs:
    if not d or d.get("kind") != "ConfigMap":
        continue
    labels = d.get("metadata", {}).get("labels", {})
    if labels.get("app.kubernetes.io/component") != "cutover-step":
        continue
    name = d["metadata"]["name"]
    pod_spec_raw = d.get("data", {}).get("podSpec")
    if not pod_spec_raw:
        continue
    spec = yaml.safe_load(pod_spec_raw)
    containers = spec.get("containers", []) + spec.get("initContainers", [])
    for c in containers:
        checked += 1
        for arg in c.get("args", []) or []:
            if len(arg) > budget:
                failures.append(
                    f"{name}/{c['name']}: inline arg {len(arg)} B > {budget} B budget"
                    " — move the script into a ConfigMap key exec'd from file (#5593)")
        for e in c.get("env", []) or []:
            v = e.get("value") or ""
            if len(v) > budget:
                failures.append(
                    f"{name}/{c['name']}: env {e['name']} {len(v)} B > {budget} B budget (#5593)")

# Vacuity check (render_guard_needs_a_vacuity_check): the guard must have
# actually inspected step containers, or a rename silently hollows it out.
if checked < 10:
    failures.append(f"vacuity: only {checked} step containers inspected (expect >=10) — guard is not seeing the steps")

if failures:
    print("arg-strlen-guard: FAIL")
    for f in failures:
        print("  " + f)
    sys.exit(1)
print(f"arg-strlen-guard: PASS — {checked} step containers, every inline arg/env under {budget} B (MAX_ARG_STRLEN 131072)")
PY
