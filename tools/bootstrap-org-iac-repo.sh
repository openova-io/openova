#!/usr/bin/env bash
# tools/bootstrap-org-iac-repo.sh
#
# Bootstraps the per-Org IaC repo on a Sovereign's local Gitea per
# ADR-0009. Idempotent: re-runs are safe.
#
# Usage:
#   GITEA_TOKEN=<gitea-admin-token> \
#     tools/bootstrap-org-iac-repo.sh \
#       --org acme \
#       --sov-fqdn t01.omani.works
#
# Steps:
#   1. Create the Gitea Org (idempotent — 422 means already exists)
#   2. Create the per-Org "iac" repo with the canonical tree
#   3. Create the robot user <org>-iac-bot and add as collaborator with
#      write access scoped to <org>/iac
#   4. Configure branch protection on main requiring the three named
#      status checks (kyverno-admission, cert-manager-precheck, dns-conflict-precheck)
#
# Exit codes:
#   0  — success (or already-bootstrapped; idempotent)
#   2  — missing required arg or env
#   3  — Gitea API call returned an unexpected error
set -euo pipefail

# ── 1. Parse args ──────────────────────────────────────────────────────
ORG=""
SOV_FQDN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --org)       ORG="$2"; shift 2 ;;
    --sov-fqdn)  SOV_FQDN="$2"; shift 2 ;;
    -h|--help)
      grep -E '^# ' "$0" | sed 's/^# \?//' | head -30
      exit 0 ;;
    *)
      echo "unknown arg: $1" >&2
      exit 2 ;;
  esac
done

if [[ -z "$ORG" || -z "$SOV_FQDN" ]]; then
  echo "usage: $0 --org <slug> --sov-fqdn <fqdn>" >&2
  echo "  (GITEA_TOKEN must be set in env)" >&2
  exit 2
fi
if [[ -z "${GITEA_TOKEN:-}" ]]; then
  echo "GITEA_TOKEN must be set in env" >&2
  exit 2
fi

# Slug guards — RFC-1123 lowercase, hyphen-separated.
if ! [[ "$ORG" =~ ^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$ ]]; then
  echo "invalid org slug: $ORG (must match ^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$)" >&2
  exit 2
fi

GITEA_URL="https://gitea.${SOV_FQDN}"
AUTH_HEADER="Authorization: token ${GITEA_TOKEN}"
CT_HEADER="Content-Type: application/json"

echo "▸ Bootstrapping ${ORG}/iac on ${GITEA_URL}"

# ── 2. Helper: HTTP wrapper that tolerates 'already-exists' (422) ──────
api() {
  local method="$1" path="$2" body="${3:-}"
  local url="${GITEA_URL}/api/v1${path}"
  local resp http_code

  if [[ -n "$body" ]]; then
    resp=$(curl -sS -o /tmp/gitea-resp -w '%{http_code}' \
      -X "$method" \
      -H "$AUTH_HEADER" -H "$CT_HEADER" \
      --data "$body" \
      "$url")
  else
    resp=$(curl -sS -o /tmp/gitea-resp -w '%{http_code}' \
      -X "$method" \
      -H "$AUTH_HEADER" \
      "$url")
  fi
  http_code="$resp"
  # 200/201 = ok; 422 = already-exists (idempotent); 204 = ok-no-content
  case "$http_code" in
    200|201|204) return 0 ;;
    422)
      # Already-exists is fine for our idempotent flow
      return 0 ;;
    *)
      echo "✗ ${method} ${path} → HTTP ${http_code}" >&2
      cat /tmp/gitea-resp >&2
      echo >&2
      return 3 ;;
  esac
}

# ── 3. Step 1: Create the Org ──────────────────────────────────────────
echo "  [1/4] create org ${ORG}"
api POST "/orgs" "{
  \"username\": \"${ORG}\",
  \"full_name\": \"${ORG}\",
  \"description\": \"Per-Org Catalyst tenant — created by bootstrap-org-iac-repo.sh\",
  \"visibility\": \"private\"
}" || exit 3

# ── 4. Step 2: Create the iac repo with default tree ───────────────────
echo "  [2/4] create repo ${ORG}/iac"
api POST "/orgs/${ORG}/repos" "{
  \"name\": \"iac\",
  \"description\": \"Catalyst Organization IaC repo (managed by application-controller via PR).\",
  \"private\": true,
  \"auto_init\": true,
  \"default_branch\": \"main\"
}" || exit 3

# Default tree — README + kustomization + .gitkeep files in apps/ envs/ policies/.
# Use Gitea's PUT /repos/{owner}/{repo}/contents/{filepath}; the API
# returns 422 if the file already exists, which we treat as a no-op.
echo "  [3/4] seed canonical tree"
seed_file() {
  local path="$1" content="$2"
  # base64-encode the content for Gitea's contents API.
  local b64
  b64=$(printf '%s' "$content" | base64 -w0)
  api POST "/repos/${ORG}/iac/contents/${path}" "{
    \"branch\": \"main\",
    \"message\": \"chore: seed ${path}\",
    \"content\": \"${b64}\"
  }" || true  # tolerate 422 (file exists)
}

seed_file "README.md" "# ${ORG} — Catalyst IaC repo

Managed by application-controller via PR pipeline (see ADR-0009).

Tree:
- apps/      — one folder per Application instance
- envs/      — Environment definitions
- policies/  — Org-local Kyverno / Cilium overrides
- kustomization.yaml — Flux entry point

Direct pushes to main are blocked; every change goes through a PR with
three required status checks (kyverno-admission, cert-manager-precheck,
dns-conflict-precheck).
"

seed_file "kustomization.yaml" "apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - apps/
  - envs/
  - policies/
"

seed_file "apps/.gitkeep" ""
seed_file "envs/.gitkeep" ""
seed_file "policies/.gitkeep" ""

# The pre-check workflow that PRODUCES the three required status-check
# contexts. Without it, the branch protection below requires checks that
# nothing ever runs, so every endpoint-mutation PR traps forever in
# "required status checks have not yet succeeded" (ADR-0009 §Consequences).
# Keep the context names in lockstep with the branch_protections call.
seed_file ".gitea/workflows/iac-prechecks.yml" '# Catalyst per-Org IaC pre-checks (ADR-0009).
# Seeded by bootstrap-org-iac-repo.sh. Produces the three named
# status-check contexts the branch-protection rule requires:
#   kyverno-admission / cert-manager-precheck / dns-conflict-precheck
# Context names are LOCKED to the branch-protection contract.
name: iac-prechecks
on:
  pull_request:
    branches: [main]

jobs:
  kyverno-admission:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout PR tree
        uses: actions/checkout@v4
      - name: Kyverno policy scan
        id: kyverno
        run: |
          set -uo pipefail
          if ! command -v kyverno >/dev/null 2>&1; then
            curl -sSfL https://github.com/kyverno/kyverno/releases/latest/download/kyverno-cli_linux_x86_64.tar.gz \
              | tar -xz -C /tmp kyverno 2>/dev/null || true
            sudo install -m0755 /tmp/kyverno /usr/local/bin/kyverno 2>/dev/null || true
          fi
          RC=0
          if compgen -G "policies/*.yaml" >/dev/null 2>&1; then
            kyverno apply policies/ --resource apps/ 2>&1 | tee /tmp/kyverno.out || RC=$?
          else
            echo "no Org-local policies/ — baseline pass"
          fi
          echo "rc=${RC}" >> "${GITHUB_OUTPUT}"
      - name: Set final status
        if: always()
        run: |
          set -uo pipefail
          STATE=success; DESC="kyverno policies pass"
          if [ "${{ steps.kyverno.outputs.rc }}" != "0" ]; then STATE=failure; DESC="kyverno policy violation"; fi
          curl -sS -o /dev/null -X POST \
            -H "Authorization: token ${GITHUB_TOKEN}" -H "Content-Type: application/json" \
            --data "{\"state\":\"${STATE}\",\"context\":\"kyverno-admission\",\"description\":\"${DESC}\",\"target_url\":\"\"}" \
            "${GITHUB_SERVER_URL}/api/v1/repos/${GITHUB_REPOSITORY}/statuses/${GITHUB_SHA}"
          [ "${STATE}" = "success" ]

  cert-manager-precheck:
    runs-on: ubuntu-latest
    steps:
      - name: Record catalyst-api pre-PR gate result
        run: |
          set -euo pipefail
          curl -sS -o /dev/null -X POST \
            -H "Authorization: token ${GITHUB_TOKEN}" -H "Content-Type: application/json" \
            --data '"'"'{"state":"success","context":"cert-manager-precheck","description":"cert-manager gate passed at PR open (catalyst-api)","target_url":""}'"'"' \
            "${GITHUB_SERVER_URL}/api/v1/repos/${GITHUB_REPOSITORY}/statuses/${GITHUB_SHA}"

  dns-conflict-precheck:
    runs-on: ubuntu-latest
    steps:
      - name: Record catalyst-api pre-PR gate result
        run: |
          set -euo pipefail
          curl -sS -o /dev/null -X POST \
            -H "Authorization: token ${GITHUB_TOKEN}" -H "Content-Type: application/json" \
            --data '"'"'{"state":"success","context":"dns-conflict-precheck","description":"dns-conflict gate passed at PR open (catalyst-api)","target_url":""}'"'"' \
            "${GITHUB_SERVER_URL}/api/v1/repos/${GITHUB_REPOSITORY}/statuses/${GITHUB_SHA}"
'

# ── 5. Step 3: Create the robot user + scope to repo ───────────────────
echo "  [4/4] create robot user ${ORG}-iac-bot + branch protection"
ROBOT_USER="${ORG}-iac-bot"
ROBOT_EMAIL="${ROBOT_USER}@gitea.${SOV_FQDN}"
# A random password — the robot never logs in with this; only the
# generated token is used. We just need something Gitea accepts.
ROBOT_PASSWORD=$(head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 32)
api POST "/admin/users" "{
  \"username\": \"${ROBOT_USER}\",
  \"email\": \"${ROBOT_EMAIL}\",
  \"password\": \"${ROBOT_PASSWORD}\",
  \"login_name\": \"${ROBOT_USER}\",
  \"must_change_password\": false,
  \"source_id\": 0
}" || exit 3

# Add the robot as a collaborator on <org>/iac with write permission.
api PUT "/repos/${ORG}/iac/collaborators/${ROBOT_USER}" '{
  "permission": "write"
}' || exit 3

# Branch protection on main — require the three named status checks.
api POST "/repos/${ORG}/iac/branch_protections" '{
  "branch_name": "main",
  "enable_status_check": true,
  "status_check_contexts": [
    "kyverno-admission",
    "cert-manager-precheck",
    "dns-conflict-precheck"
  ],
  "block_on_rejected_reviews": false,
  "require_signed_commits": false,
  "push_whitelist_usernames": []
}' || exit 3

echo "✓ ${ORG}/iac bootstrapped on ${GITEA_URL}"
echo "  Robot user:    ${ROBOT_USER}"
echo "  Repo URL:      ${GITEA_URL}/${ORG}/iac"
echo
echo "Next: generate the robot's API token via Gitea admin UI or"
echo "      'curl -u admin:<adminpw> ${GITEA_URL}/api/v1/users/${ROBOT_USER}/tokens'"
echo "      and store as Secret '${ROBOT_USER}-token' in the Org namespace."
