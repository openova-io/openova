#!/usr/bin/env bash
# bp-newapi — #5612: the region-consistent session-secret carrier must be
# ACTIVE in the configuration bootstrap-kit slot 80 ACTUALLY SHIPS.
#
# THE DEFECT THIS GUARDS
# ──────────────────────────────────────────────────────────────────────────
# #5612 was filed as "newapi's session expires in under an hour". It is not a
# TTL. Upstream NewAPI v0.13.2 `main.go` builds ONE session store:
#
#     store := cookie.NewStore([]byte(common.SessionSecret))
#     store.Options(sessions.Options{ Path: "/", MaxAge: 2592000, ... })
#
# — a gorilla cookie store sealed with SESSION_SECRET and a THIRTY-DAY MaxAge.
# There is no sub-hour expiry anywhere in the code to configure.
#
# What actually happened (measured live on hw292, dep 1c56518035a83e03, BOTH
# regions sampled, 2026-08-06): the Sovereign ran TWO independent newapi
# Deployments behind ONE `newapi.<fqdn>` hostname, and their SESSION_SECRETs
# DIFFERED — sha256 fed37217c5689beb (region-a) vs 4263893b8a06f816 (region-b)
# (CRYPTO_SECRET likewise). Both regions render an HTTPRoute claiming that same
# hostname, and the shared VIP fans a browser's connections across both
# gateways (#5459 — a browser can never be assumed to pin one backend). So a
# cookie sealed by region-a is undecryptable by region-b, which answers
# `[sessions] ERROR! securecookie: the value is not valid` → 401 → the SPA
# redirects to /login?expired=true. The user reads that as "my session expired
# after 57 minutes"; the real trigger is WHICH BACKEND the next request landed
# on. Elapsed time is a coincidence, not a cause.
#
# That is the A16 split-state class, closed at the SECRET seam by #5466/#5480
# (chart 1.4.147): SESSION_SECRET/CRYPTO_SECRET stop being minted per-region by
# credentials-secret.yaml's `lookup`-or-`randAlphaNum 64` and instead ride the
# bp-sso-bridge → OpenBao → ExternalSecret carrier that the sibling
# OIDC_CLIENT_SECRET already used, so every region resolves ONE bundle.
#
# WHY THIS GUARD EXISTS ON TOP OF 5466-credentials-region-consistency-render.sh
# ──────────────────────────────────────────────────────────────────────────
# That guard proves the CHART responds correctly when the carrier gate is on.
# It renders from a hand-authored heredoc of what the author believed slot 80
# ships — it never reads the slot. `bp-newapi.credsBridgeSynced` (_helpers.tpl)
# is "true" only when EVERY leg holds: placement.role=all + adminUI keycloak
# mode + ssoBridgeSync.enabled + sovereignFQDN + a sovereign-realm issuer + no
# credentials.existingSecret override + the ESO CRD capability. When ANY leg is
# false the chart falls back to the per-region generator — silently, by design,
# "everything renders exactly as pre-#5466".
#
# So the production slot can drift off any one of those legs, #5612 returns in
# the field, and every existing chart test stays green because they all hand-set
# the legs themselves. This guard closes that by rendering from the REAL
# `clusters/_template/bootstrap-kit/80-newapi.yaml`, substituting its `${VAR}`
# placeholders exactly as Flux postBuild does (textually, then parse).
#
# VACUITY — checked in BOTH directions
# ──────────────────────────────────────────────────────────────────────────
# Case 1 is the CONTROL and passes on BOTH trees: the slot render must produce a
# real, working newapi — a Deployment that HAS SESSION_SECRET and CRYPTO_SECRET
# env vars at all, plus the public HTTPRoute for the sovereign FQDN. A fix that
# stopped the split by rendering no app, or no session env, is not a fix; this
# case is what stops every later assertion from passing vacuously on an empty or
# errored render.
#
# Case 4 is the in-test vacuity proof: the SAME slot values with exactly ONE
# gate leg flipped must pivot SESSION_SECRET BACK to the per-region generator.
# If that case ever stops firing, Case 2's assertion has become untestable and
# this whole file is theatre. Verified by hand against this tree
# (2026-08-06) — each of ssoBridgeSync.enabled=false, placement.role=
# vcluster-app and credentials.existingSecret=byo-creds moves the env off
# `-app-creds-oidc`.
#
# Case 3 covers the PRE-FLIP WINDOW, which no other guard reaches. #5599's
# cross-region singleton is gated on `crossRegion.enabled:
# ${SOVEREIGN_ENABLE_CNPG_PAIR:=false}`, and catalyst-api flips that true only
# AFTER it confirms the full ClusterMesh. Until then — and on every single-region
# prov — BOTH regions render a complete Deployment, so the ONLY thing holding a
# session together across the VIP fan is this secret carrier. The carrier must
# therefore be active INDEPENDENTLY of crossRegion. If anyone ever makes it
# conditional on the pair flag, #5612 silently returns for the whole bootstrap
# window and the singleton guard would not notice.
set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
repo_root="$(cd "$chart_dir/../../.." && pwd)"
slot="$repo_root/clusters/_template/bootstrap-kit/80-newapi.yaml"
helm="${HELM_BIN:-helm}"

[ -f "$slot" ] || { echo "FAIL: bootstrap-kit slot not found at $slot — this guard is slot-bound by design; if the slot moved, repoint it rather than deleting the coverage" >&2; exit 1; }

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Release name `newapi` == slot 80's releaseName, so rendered object names
# match what a Sovereign actually gets.
release="newapi"
fullname="newapi-bp-newapi"
bridge_secret="${fullname}-app-creds-oidc"   # ESO target, ONE bundle, all regions
perregion_secret="${fullname}-app-creds"     # chart-generated, randAlphaNum 64 PER CLUSTER

values_file=$(mktemp)
preflip_file=$(mktemp)
trap 'rm -f "$values_file" "$preflip_file"' EXIT

# Extract spec.values from the REAL slot, substituting ${VAR} / ${VAR:=default}
# textually first — that is exactly Flux's postBuild substitution order, so a
# placeholder that only parses after substitution behaves here as it does live.
extract_slot_values() { # extract_slot_values <enable_cnpg_pair> <region_role>
  SLOT_FILE="$slot" PAIR="$1" ROLE="$2" python3 - <<'PY'
import os, re, sys, yaml
raw = open(os.environ["SLOT_FILE"]).read()
env = {
    "SOVEREIGN_FQDN": "t00.omani.works",
    "SOVEREIGN_ENABLE_CNPG_PAIR": os.environ["PAIR"],
    "SOVEREIGN_REGION_ROLE": os.environ["ROLE"],
    "SOVEREIGN_CNPG_INSTANCES": "2",
    "SOVEREIGN_PEER_CLUSTERMESH_NAMES": "[]",
    "MARKETPLACE_ENABLED": "false",
    "SOVEREIGN_VLLM_ENABLED": "false",
    "LLM_PARTNER_BASE_URL": "",
    "LLM_PARTNER_ACCOUNT_ID": "",
    "LLM_PARTNER_CONTRACT_REF": "",
}
txt = re.sub(r'\$\{([A-Za-z0-9_]+)(?::=|:-)?([^}]*)\}',
             lambda m: env.get(m.group(1), m.group(2) or ""), raw)
for d in yaml.safe_load_all(txt):
    if d and d.get("kind") == "HelmRelease" and d.get("metadata", {}).get("name") == "bp-newapi":
        print(yaml.safe_dump(d["spec"]["values"], default_flow_style=False))
        sys.exit(0)
sys.exit("FAIL: no bp-newapi HelmRelease with spec.values in the slot")
PY
}

extract_slot_values true  primary > "$values_file"
extract_slot_values false primary > "$preflip_file"
[ -s "$values_file" ] || { echo "FAIL: extracted NO values from $slot" >&2; exit 1; }

# The carrier gate tests .Capabilities.APIVersions for the ESO CRD, so the
# render must advertise it — a Sovereign always has external-secrets installed
# (slot 07) before slot 80.
render() { # render <values-file> [extra helm args...]
  local vf="$1"; shift
  local out rc
  # Assert on helm's EXIT CODE, not on error-looking text in the output: chart
  # comments legitimately contain strings like `CreateContainerConfigError:`,
  # and a text grep matched one of those and "failed" a perfectly good render
  # during authoring. A failed render exits non-zero with no manifests, which
  # would make every "renders nothing" assertion pass vacuously — hard-fail.
  set +e
  out=$("$helm" template "$release" "$chart_dir" -f "$vf" \
        --api-versions external-secrets.io/v1beta1 "$@" 2>&1)
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: helm render exited ${rc} — assertions against this output would be vacuous:" >&2
    echo "$out" | grep -E "^Error:|execution error|parse error" | head -5 >&2
    exit 1
  fi
  printf '%s' "$out"
}

# Secret name backing a given env var in the rendered Deployment (VALUE assert,
# not a key-presence assert — #5639: a grep for the KEY passes on an EMPTY value).
env_secret() { # env_secret <rendered> <ENV_NAME>
  awk -v want="$2" '
    $0 ~ ("- name: " want "$") {hit=1; next}
    hit && /name:/ { v=$0; sub(/.*name:[ \t]*/,"",v); gsub(/["[:space:]]/,"",v); print v; exit }
  ' <<<"$1"
}

slot_render=$(render "$values_file")

# ── Case 1: CONTROL — the slot render is a real, working newapi ──────────────
# Passes on BOTH the pre-#5466 and post-#5466 trees. Without this, "the env does
# not point at the per-region secret" would also be satisfied by rendering no
# Deployment at all.
echo "[5612] Case 1 (control): slot 80 renders a working newapi with session env + public route"
grep -qE "^kind: Deployment$" <<<"$slot_render" \
  || { echo "FAIL: slot 80 render contains no Deployment" >&2; exit 1; }
for var in SESSION_SECRET CRYPTO_SECRET; do
  grep -qE "^[ \t]*- name: ${var}$" <<<"$slot_render" \
    || { echo "FAIL: Deployment has no ${var} env var — newapi cannot seal a session at all" >&2; exit 1; }
done
grep -qE "^kind: HTTPRoute$" <<<"$slot_render" \
  || { echo "FAIL: slot 80 render contains no HTTPRoute" >&2; exit 1; }
grep -q "newapi.t00.omani.works" <<<"$slot_render" \
  || { echo "FAIL: no HTTPRoute hostname newapi.<sovereignFQDN> — the fanned front door #5612 is about" >&2; exit 1; }
echo "  ok: Deployment + SESSION_SECRET + CRYPTO_SECRET + HTTPRoute newapi.t00.omani.works"

# ── Case 2: LOAD-BEARING — session bytes come from the ONE bridge bundle ─────
echo "[5612] Case 2: SESSION_SECRET/CRYPTO_SECRET resolve from the region-consistent carrier"
for var in SESSION_SECRET CRYPTO_SECRET; do
  src=$(env_secret "$slot_render" "$var")
  [ -n "$src" ] || { echo "FAIL: ${var} has no secretKeyRef name in the slot render" >&2; exit 1; }
  if [ "$src" = "$perregion_secret" ]; then
    echo "FAIL: ${var} resolves from ${perregion_secret} — the PER-REGION generator." >&2
    echo "      That is the #5612 defect: each region mints its own 64-char value, so a" >&2
    echo "      session cookie sealed by one region is rejected by the other behind the" >&2
    echo "      shared newapi.<fqdn> VIP and the user is bounced to /login?expired=true." >&2
    echo "      A leg of bp-newapi.credsBridgeSynced has drifted in $slot." >&2
    exit 1
  fi
  [ "$src" = "$bridge_secret" ] \
    || { echo "FAIL: ${var} resolves from '${src}', expected the bridge-published ${bridge_secret}" >&2; exit 1; }
done
grep -q "name: ${fullname}-app-creds-sync" <<<"$slot_render" \
  || { echo "FAIL: the -app-creds-sync ExternalSecret does not render under slot 80's values" >&2; exit 1; }
for prop in session_secret crypto_secret; do
  grep -q "{{ .${prop} }}" <<<"$slot_render" \
    || { echo "FAIL: ExternalSecret does not map ${prop} from the OpenBao bundle" >&2; exit 1; }
done
echo "  ok: both env vars resolve from ${bridge_secret}; -app-creds-sync maps session_secret + crypto_secret"

# ── Case 3: the PRE-FLIP window — carrier must NOT depend on crossRegion ─────
echo "[5612] Case 3: carrier stays active with SOVEREIGN_ENABLE_CNPG_PAIR=false (pre-mesh-confirm + single-region)"
preflip_render=$(render "$preflip_file")
grep -qE "^kind: Deployment$" <<<"$preflip_render" \
  || { echo "FAIL: pre-flip render has no Deployment — cannot assert its session env" >&2; exit 1; }
for var in SESSION_SECRET CRYPTO_SECRET; do
  src=$(env_secret "$preflip_render" "$var")
  [ "$src" = "$bridge_secret" ] \
    || { echo "FAIL: with crossRegion off, ${var} resolves from '${src}' not ${bridge_secret} — the carrier has been made conditional on the CNPG-pair flag, so #5612 returns for the entire pre-mesh-confirm bootstrap window and on every single-region prov" >&2; exit 1; }
done
echo "  ok: carrier is independent of crossRegion — session survives the pre-flip window"

# ── Case 4: VACUITY — one flipped gate leg MUST reintroduce the split ────────
# If this stops firing, Case 2 has become untestable.
echo "[5612] Case 4 (vacuity): flipping one gate leg pivots SESSION_SECRET back to the per-region generator"
drift_hits=0
for drift in "auth.adminUI.keycloak.ssoBridgeSync.enabled=false" "placement.role=vcluster-app"; do
  drift_render=$(render "$values_file" --set "$drift")
  src=$(env_secret "$drift_render" SESSION_SECRET)
  if [ "$src" = "$perregion_secret" ]; then
    drift_hits=$((drift_hits + 1))
    echo "  ok: --set ${drift} → SESSION_SECRET falls back to ${perregion_secret}"
  else
    echo "FAIL: --set ${drift} left SESSION_SECRET on '${src}'." >&2
    echo "      The carrier gate no longer responds to that leg, so Case 2 can no longer" >&2
    echo "      go red and this guard would pass on a broken tree." >&2
    exit 1
  fi
done
[ "$drift_hits" -eq 2 ] || { echo "FAIL: expected 2 drift legs to fire, got ${drift_hits}" >&2; exit 1; }

echo "[5612] PASS — slot 80 ships the region-consistent session-secret carrier"
