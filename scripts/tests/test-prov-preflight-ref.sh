#!/usr/bin/env bash
# test-prov-preflight-ref.sh - item 4b of scripts/prov-preflight.sh judges the parent's
# catalyst-api against the ref the fire installs (GITOPS_BRANCH: empty or main, or a release
# candidate rc/<id>). Stand-ins for kubectl, git, curl and gh come first on PATH and
# KUBECONFIG is /dev/null: nothing leaves this host and no cluster is read. Each case runs the
# whole script and reads only its 4b line.
#   kubectl: the deployed tag from $T/deployed, the Deployment's labels from $T/labels, the
#            suspend field of Kustomization flux-system/catalyst-platform from $T/suspend
#   git:     fetch fails for the ref in $T/nofetch; show <rev>:<path> prints $T/pins/<rev>
# Usage: bash scripts/tests/test-prov-preflight-ref.sh   Exit 0: every case passed.
set -uo pipefail
export KUBECONFIG=/dev/null

here="$(cd "$(dirname "$0")/../.." && pwd)"
t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin" "$t/pins"
cat > "$t/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *"rollout status"*) exit 0 ;;
  *"get deploy catalyst-api -o jsonpath="*) echo "ghcr.io/openova-io/openova/catalyst-api:$(cat "$T/deployed")" ;;
  *"get deploy catalyst-api -o json"*) printf '{"metadata":{"labels":%s}}\n' "$(cat "$T/labels")" ;;
  "-n flux-system get kustomizations.kustomize.toolkit.fluxcd.io catalyst-platform "*) cat "$T/suspend" ;;
  *) exit 1 ;;
esac
EOF
cat > "$t/bin/git" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  fetch) if [ -s "$T/nofetch" ]; then case "$*" in *"$(cat "$T/nofetch")"*) exit 128 ;; esac; fi; exit 0 ;;
  show) f="$T/pins/$(printf '%s' "${2%%:*}" | tr / _)"; [ -f "$f" ] || exit 128; cat "$f" ;;
  *) exit 1 ;;
esac
EOF
printf '#!/usr/bin/env bash\necho %s\n' "'{\"deployments\":[]}'" > "$t/bin/curl"
printf '#!/usr/bin/env bash\nexit 1\n' > "$t/bin/gh"
chmod +x "$t/bin/"*
export T="$t" PATH="$t/bin:$PATH"
unset BEARER COOK HW_TFVARS GITOPS_BRANCH

rc=rc/20261118T120000Z-aff5232
flux='{"kustomize.toolkit.fluxcd.io/name":"catalyst-platform","kustomize.toolkit.fluxcd.io/namespace":"flux-system"}'
failed=0

pin() { # pin <ref> <tag>: origin/<ref> pins catalyst-api at <tag>
  printf 'images:\n  catalystApi:\n    tag: "%s"\n' "$2" > "$t/pins/origin_$(printf '%s' "$1" | tr / _)"
}
run() { # run <ref> <deployed> <main pin> <candidate pin, - for none> <suspend> [<labels>] [<ref fetch refuses>]
  rm -f "$t/pins/"*
  echo "$2" > "$t/deployed"; pin main "$3"; [ "$4" = - ] || pin "$rc" "$4"
  printf '%s' "$5" > "$t/suspend"; printf '%s' "${6-$flux}" > "$t/labels"; printf '%s' "${7:-}" > "$t/nofetch"
  GITOPS_BRANCH="$1" bash "$here/scripts/prov-preflight.sh" hw999.omani.works auto-hw999 > "$t/out" 2>&1
  grep -F ' 4b. ' "$t/out" > "$t/line"
}
check() { # check <case> <✅|❌> <text the line carries>
  if [ "$(wc -l < "$t/line")" = 1 ] && grep -q -F "  $2 4b. " "$t/line" && grep -q -F -- "$3" "$t/line"; then
    echo "ok   $1"
  else echo "FAIL $1"; sed 's/^/     | /' "$t/line"; failed=1; fi
}

run "$rc" aff5232 2019d4f aff5232 true
check "a candidate the parent runs, its delivery suspended, passes although main moved" ✅ \
  "and its delivery Kustomization flux-system/catalyst-platform is suspended"
run "$rc" 2019d4f 2019d4f aff5232 true
check "a candidate whose pin differs from the deployed tag fails, even when main pins the deployed tag" ❌ \
  "DEPLOYED=2019d4f != PINNED on ${rc}=aff5232"
run "$rc" aff5232 2019d4f aff5232 false
check "a candidate the parent runs, its delivery not suspended and main moved, fails" ❌ \
  "Kustomization flux-system/catalyst-platform is NOT suspended (suspend='false')"
run "$rc" aff5232 aff5232 aff5232 ""
check "a candidate the parent runs, its delivery not suspended, passes when main pins the same tag" ✅ \
  "which main pins too"
run "$rc" aff5232 2019d4f aff5232 true '{}'
check "a candidate the parent runs fails when no Flux object is named on the Deployment and main moved" ❌ \
  "no Flux object is named on deploy/catalyst-api"
run "$rc" aff5232 2019d4f - true
check "a candidate whose pin cannot be read fails" ❌ "could not read catalyst-api deployed/pinned tag of ${rc}"
run "$rc" aff5232 2019d4f aff5232 true "$flux" "$rc"
check "a candidate that cannot be fetched fails" ❌ "the ref ${rc} the fire installs could not be fetched"
run "" 2019d4f 2019d4f - ""
check "no ref, the pinned tag deployed, passes as before" ✅ "catalyst-api on the pinned image (2019d4f)"
run main 2019d4f 2019d4f - ""
check "main, the pinned tag deployed, passes as before" ✅ "catalyst-api on the pinned image (2019d4f)"
run main aff5232 2019d4f - true
check "main, another tag deployed, fails as before whatever the delivery" ❌ \
  "DEPLOYED=aff5232 != PINNED=2019d4f — a deploybot roll is PENDING"

if [ "$failed" = 0 ]; then echo "every case passed"; else echo "a case failed"; fi
exit "$failed"
