#!/usr/bin/env bash
# check-agenity-retired-name.sh — CI guard: the retired pre-revamp product
# name must not reappear under products/agenity/ (Refs #5435).
#
# THE DEFECT THIS EXISTS TO PREVENT
#
# The product is Agenity. `chepherd` is its retired pre-revamp name. It
# survived in products/agenity/ long after the rebrand, and — critically —
# the chart's StatefulSet named its main container `chepherd`. A Kubernetes
# object NAME is operator-facing output: it is what `kubectl get pod -o
# wide`, `kubectl logs -c`, `kubectl exec -c`, and every console surface
# that renders container names actually print. Verified live on hw292:
#
#   $ kubectl -n uatco get sts uatco-agenity-rtz-a-bp-agenity \
#       -o jsonpath='{.spec.template.spec.containers[*].name}'
#   chepherd
#
# That is the #5435 class: a banned term reaches operator-facing output from
# a RUNTIME object name, so grepping the console/UI source finds nothing and
# the term looks eradicated when it is not.
#
# WHY THIS GUARD RENDERS THE CHART
#
# A prose-only grep of the source tree is exactly the guard that cannot
# catch the defect it is named for. Two failure modes it would miss:
#
#   1. The name arrives via a Helm VALUE or a template expression, so no
#      source line literally spells it.
#   2. The name sits in a source line that IS a YAML comment — which never
#      reaches the cluster — and the guard counts it, inflating the signal
#      while the real object field goes unchecked.
#
# So Phase 2 runs `helm template` and walks the PARSED manifests. YAML
# comments vanish on parse; container names, env-var names, mount paths,
# labels and block-scalar scripts survive as real values. That is the
# runtime surface, and it is what the assertion is made against.
#
# THE ALLOWLIST — literals that legitimately keep the retired spelling
#
# bp-agenity overlays OpenOva's openova-mcp binary onto the UPSTREAM daemon
# built from github.com/agenity-org/agenity. A fixed set of literals is that
# upstream's own contract, not a name OpenOva chose. Renaming them renames
# nothing upstream — it only breaks the image:
#
#   CHEPHERD_*                   read by name inside the upstream Go binary
#   /usr/local/bin/chepherd      upstream chepherd-entrypoint.sh execs it
#   chepherd-entrypoint[.sh]     upstream script filename = image ENTRYPOINT
#   /home/chepherd               image HOME; upstream entrypoint hardcodes
#                                --state-dir /home/chepherd/.local/state/chepherd
#   chepherd run                 upstream cobra root (cmd/root.go Use:)
#   chepherd-agent               upstream per-agent image name
#   ghcr.io/agenity-org/chepherd retired upstream package, referenced as history
#   bp-chepherd-operator         upstream unshipped operator (upstream #130)
#
# Anything NOT on that list is ours, and must read `agenity`.
#
# Usage:
#   scripts/check-agenity-retired-name.sh
#
# Exit codes:
#   0  — clean: no non-allowlisted occurrence in source or rendered manifests
#   1  — at least one occurrence found
#   2  — self-test / tooling failure (the guard itself is broken)

set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TARGET_DIR="${ROOT}/products/agenity"
CHART_DIR="${TARGET_DIR}/chart"

RETIRED='chepherd'
EXIT=0

# ─── Allowlist ────────────────────────────────────────────────────────────
# Longest / most specific first — these are stripped from the text BEFORE
# the search, so a partial pattern must not shadow a longer one.
ALLOW=(
  'CHEPHERD_[A-Z0-9_]+'
  'CHEPHERD_\*'
  'ghcr\.io/agenity-org/chepherd'
  '/usr/local/bin/chepherd-entrypoint'
  '/usr/local/bin/chepherd'
  'chepherd-entrypoint(\.sh)?'
  'chepherd-agent'
  'bp-chepherd-operator'
  # The upstream entrypoint hardcodes this exact --state-dir; it must be
  # stripped BEFORE the bare /home/chepherd prefix or the trailing
  # component survives and reports a false positive.
  '/home/chepherd/\.local/state/chepherd'
  '/home/chepherd'
  'chepherd:chepherd'
  'chepherd:100000:65536'
  'useradd -m -u 1000 -s /bin/bash chepherd'
  'Use: "chepherd"'
  '`chepherd run`'
  # \b so this upstream-CLI pattern cannot also swallow prose like
  # "chepherd runtime" — an over-broad allowlist entry silently whitelists
  # the very occurrences this guard exists to catch.
  'chepherd run\b'
  '`chepherd`'
)

# Strip every allowlisted literal from stdin, leaving only occurrences that
# are OURS to rename.
strip_allowed() {
  local sed_args=()
  local pat
  for pat in "${ALLOW[@]}"; do
    sed_args+=(-e "s#${pat}##g")
  done
  sed -E "${sed_args[@]}"
}

# ─── Phase 0: vacuity self-test ───────────────────────────────────────────
#
# An absence-assertion reports "clean" both when the thing is absent AND
# when the check stopped working (#5512). Prove BOTH directions before
# trusting a green run: a planted violation must be seen, and an
# allowlisted literal must not be.
selftest() {
  local planted='        - name: chepherd'
  local allowed='            - name: CHEPHERD_EXTRA_MCP_JSON'
  local allowed2='              mountPath: /home/chepherd/.claude'

  if ! printf '%s\n' "$planted" | strip_allowed | grep -qi "${RETIRED}"; then
    echo "SELF-TEST FAIL: a planted container name was NOT detected." >&2
    echo "  The guard would pass every PR. Fix strip_allowed()/ALLOW." >&2
    exit 2
  fi
  if printf '%s\n' "$allowed" | strip_allowed | grep -qi "${RETIRED}"; then
    echo "SELF-TEST FAIL: allowlisted env var '${allowed}' flagged." >&2
    echo "  The guard is too strict and would fail every PR." >&2
    exit 2
  fi
  if printf '%s\n' "$allowed2" | strip_allowed | grep -qi "${RETIRED}"; then
    echo "SELF-TEST FAIL: allowlisted path '${allowed2}' flagged." >&2
    echo "  The guard is too strict and would fail every PR." >&2
    exit 2
  fi
}
selftest
echo "Phase 0: vacuity self-test OK (detects a planted name, honours the allowlist)."

# ─── Phase 1: source tree ─────────────────────────────────────────────────
echo
echo "Phase 1: source scan of products/agenity/ …"
PHASE1_HITS=0
while IFS= read -r -d '' f; do
  rel="${f#"${ROOT}"/}"
  # Number the lines first so the report keeps real line numbers after
  # the allowlisted literals are stripped out.
  while IFS= read -r hit; do
    echo "  ✘ ${rel}:${hit}"
    PHASE1_HITS=$((PHASE1_HITS + 1))
  done < <(grep -n '' "$f" | strip_allowed | grep -i "${RETIRED}" || true)
done < <(find "${TARGET_DIR}" -type f -print0)

if [ "${PHASE1_HITS}" -gt 0 ]; then
  echo "  ${PHASE1_HITS} non-allowlisted occurrence(s) of the retired name in source." >&2
  EXIT=1
else
  echo "  clean — every remaining occurrence is an allowlisted upstream literal."
fi

# ─── Phase 2: rendered runtime surface ────────────────────────────────────
#
# This is the phase that would have caught the container name. It renders
# the chart and walks the PARSED objects, so YAML comments are gone and
# only fields that actually reach the Kubernetes API are asserted on.
echo
echo "Phase 2: rendered-manifest scan (helm template → parsed objects) …"

if ! command -v helm >/dev/null 2>&1; then
  echo "  helm not found — Phase 2 cannot run." >&2
  echo "  Refusing to report a PASS from a check that did not execute." >&2
  exit 2
fi

RENDER="$(mktemp)"
trap 'rm -f "${RENDER}"' EXIT

# Render with the flags AND the --api-versions that switch on every
# optional template, so no runtime field escapes the scan behind an `if`
# or a Capabilities gate. Without the --api-versions the Cilium policies,
# the ExternalSecrets and the oidc-gate companion never render, and the
# scan would silently cover only three of the chart's objects.
if ! helm template agenity-guard "${CHART_DIR}" \
      --api-versions cilium.io/v2 \
      --api-versions external-secrets.io/v1beta1 \
      --api-versions gateway.networking.k8s.io/v1 \
      --set anthropic.credentialsKey=credentials.json \
      --set openovaMCP.enabled=true \
      --set openovaMCP.mcpBearer.externalSecret.enabled=true \
      --set persistence.enabled=true \
      --set podman.inPod=true \
      --set agent.forceLocalSpawner=true \
      > "${RENDER}" 2>/dev/null; then
  echo "  helm template FAILED — cannot verify the runtime surface." >&2
  exit 2
fi

if [ ! -s "${RENDER}" ]; then
  echo "  helm template produced NO output — refusing to pass on nothing." >&2
  exit 2
fi

# Coverage floor. The StatefulSet is the object that carries the container
# name — the exact field that produced #5435. If the render ever stops
# emitting it, this guard would "pass" while asserting on nothing.
if ! grep -q '^kind: StatefulSet' "${RENDER}"; then
  echo "  render contains NO StatefulSet — the container-name field is" >&2
  echo "  unverified, so a PASS here would be meaningless." >&2
  exit 2
fi

RENDER_FILE="${RENDER}" RETIRED_NAME="${RETIRED}" python3 - <<'PY'
import os, re, sys, yaml

path    = os.environ["RENDER_FILE"]
retired = os.environ["RETIRED_NAME"]

ALLOW = [
    r"CHEPHERD_[A-Z0-9_]+",
    r"ghcr\.io/agenity-org/chepherd",
    r"/usr/local/bin/chepherd-entrypoint",
    r"/usr/local/bin/chepherd",
    r"chepherd-entrypoint(\.sh)?",
    r"chepherd-agent",
    r"bp-chepherd-operator",
    # Must precede the bare /home/chepherd prefix (see the bash ALLOW note).
    r"/home/chepherd/\.local/state/chepherd",
    r"/home/chepherd",
    r"chepherd:chepherd",
    r"chepherd:100000:65536",
    r"chepherd run\b",
    r"`chepherd`",
]

def strip_allowed(s):
    for pat in ALLOW:
        s = re.sub(pat, "", s)
    return s

docs = [d for d in yaml.safe_load_all(open(path)) if d]
if not docs:
    print("  parsed ZERO objects from the render — refusing to pass.", file=sys.stderr)
    sys.exit(2)

hits = []

def walk(node, trail):
    if isinstance(node, dict):
        for k, v in node.items():
            # Keys are runtime surface too (an env-var name is a key's value,
            # but a label key or annotation key is a key).
            if isinstance(k, str) and retired in strip_allowed(k).lower():
                hits.append((trail + "/" + str(k), "<key> " + k))
            walk(v, trail + "/" + str(k))
    elif isinstance(node, list):
        for i, v in enumerate(node):
            walk(v, trail + "[%d]" % i)
    elif isinstance(node, str):
        if retired in strip_allowed(node).lower():
            for line in node.splitlines():
                if retired in strip_allowed(line).lower():
                    hits.append((trail, line.strip()))

for d in docs:
    kind = d.get("kind", "?")
    name = (d.get("metadata") or {}).get("name", "?")
    walk(d, "%s/%s" % (kind, name))

print("  parsed %d rendered object(s)." % len(docs))
if hits:
    for trail, sample in hits:
        print("  ✘ %s\n      %s" % (trail, sample[:160]), file=sys.stderr)
    print("  %d non-allowlisted occurrence(s) REACH THE CLUSTER." % len(hits), file=sys.stderr)
    sys.exit(1)

print("  clean — no non-allowlisted occurrence reaches a Kubernetes object.")
PY
P2=$?
if [ "${P2}" -eq 2 ]; then exit 2; fi
if [ "${P2}" -ne 0 ]; then EXIT=1; fi

echo
if [ "${EXIT}" -eq 0 ]; then
  echo "PASS — the retired name appears only as allowlisted upstream literals."
else
  echo "FAIL — the retired product name is back. Rename it to 'agenity'," >&2
  echo "       or, if it is genuinely an upstream contract literal, add it" >&2
  echo "       to ALLOW in this script WITH the reason it cannot change." >&2
fi
exit "${EXIT}"
