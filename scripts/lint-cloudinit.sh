#!/usr/bin/env bash
# lint-cloudinit.sh — fast, local POSIX-syntax validator for the OpenTofu
# cloud-init control-plane templates.
#
# WHY THIS EXISTS (#3129): a cloud-init `runcmd:` shell-syntax bug that only
# the target VM's ancient `/bin/sh` (the kom4dc image's dash) rejects currently
# costs a ~1-hour live provision to discover (the CP boots, runcmd aborts mid-
# stream, the kubeconfig PUT-back never fires, Phase-1 times out). This harness
# renders the template with a complete fixture, extracts every `runcmd` item,
# and runs `dash -n` (POSIX `sh` parse-only) over each item AND the whole
# concatenated script — catching the class of bug in seconds, locally.
#
# It does NOT provision anything, does NOT touch the network, and does NOT
# modify the .tftpl templates. It only renders + parses.
#
# USAGE:
#   scripts/lint-cloudinit.sh hetzner
#   scripts/lint-cloudinit.sh huawei
#   scripts/lint-cloudinit.sh both      # lint both, aggregate exit status
#
# EXIT: 0 = all runcmd items parse under dash -n AND no sh-run item uses a
#       bash-only construct; non-zero = a `dash -n` syntax error OR a bash-only
#       construct in an sh-run item (or the template failed to render).
#       Bash-ONLY constructs that the kom4dc /bin/sh REJECTS at runtime — and
#       which `dash -n` parse-only does NOT catch (the #3129 trap) — are a HARD
#       FAIL when the item is run by /bin/sh: `(( … ))` arithmetic-command,
#       `[[ … ]]` test, `&>` redirect, `function` keyword. (`$(( … ))`
#       arithmetic EXPANSION is POSIX and is NOT flagged.) The same construct
#       inside an explicit `bash -c "…"` body is a WARNING only (bash's domain).
#
# REQUIREMENTS: tofu (templatefile rendering — no providers needed), dash,
#               python3 (+ pyyaml or a vendored fallback parser).
set -u

# ─────────────────────────────────────────────────────────────────────────────
# Paths + tool discovery
# ─────────────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PROVIDERS_DIR="${REPO_ROOT}/infra/providers"

TOFU_BIN="${TOFU_BIN:-tofu}"
DASH_BIN="${DASH_BIN:-dash}"
PY_BIN="${PY_BIN:-python3}"

C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'; C_BLU=$'\033[34m'; C_RST=$'\033[0m'
if [ ! -t 1 ]; then C_RED=; C_GRN=; C_YEL=; C_BLU=; C_RST=; fi

say()  { printf '%s\n' "$*"; }
info() { printf '%s[lint-cloudinit]%s %s\n' "${C_BLU}" "${C_RST}" "$*"; }
ok()   { printf '%s  PASS%s %s\n' "${C_GRN}" "${C_RST}" "$*"; }
warn() { printf '%s  WARN%s %s\n' "${C_YEL}" "${C_RST}" "$*"; }
err()  { printf '%s  FAIL%s %s\n' "${C_RED}" "${C_RST}" "$*"; }
die()  { printf '%s[lint-cloudinit] FATAL%s %s\n' "${C_RED}" "${C_RST}" "$*" >&2; exit 2; }

for t in "${TOFU_BIN}" "${DASH_BIN}" "${PY_BIN}"; do
  command -v "${t}" >/dev/null 2>&1 || die "required tool not found on PATH: ${t}"
done

# ─────────────────────────────────────────────────────────────────────────────
# Per-provider fixture maps.
#
# Each function emits the body of an HCL object literal: `key = value` lines
# that become the second arg of templatefile(). Every variable the provider's
# real templatefile() call (in infra/providers/<p>/main.tf) supplies is present
# here with a realistic placeholder, so the template renders without a
# "missing map value" error. Values mimic the SHAPE tofu produces at apply time
# (strings, the "true"/"false" string-bools the chart envsubst expects, JSON
# literals for the *_json / *_yaml keys, base64-ish blobs for secrets).
#
# Keep these in lockstep with the main.tf templatefile() maps: if a provider's
# map gains/loses a key, update the matching fixture below.
# ─────────────────────────────────────────────────────────────────────────────

fixture_common() {
  # Shared keys EVERY provider passes with the same meaning, to the ONE
  # cloud-agnostic template infra/providers/_shared/cloudinit-control-plane.tftpl.
  cat <<'HCL'
    sovereign_fqdn                    = "t99.omani.works"
    sovereign_fqdn_slug               = "t99-omani-works"
    deployment_id                     = "test-dep-0001"
    org_name                          = "Test Org \"Quote\" & Co"
    org_email                         = "ops@t99.omani.works"
    region                            = "test-region-a"
    sovereign_region_key              = "test-region-a"
    region_canonical_label            = "xx-test-region-a-rtz-prod"
    primary_region_canonical_label    = "xx-test-region-a-rtz-prod"
    replica_region_canonical_label    = "xx-test-region-b-rtz-prod"
    cluster_mesh_name                 = "t99-omani-works-a"
    cluster_mesh_id                   = "1"
    k3s_version                       = "v1.31.5+k3s1"
    k3s_token                         = "0123456789abcdef0123456789abcdef"
    cp_private_ip                     = "10.0.1.2"
    cluster_cidr                      = "10.42.0.0/16"
    service_cidr                      = "10.96.0.0/16"
    gitops_repo_url                   = "https://github.com/openova-io/openova"
    gitops_branch                     = "main"
    ghcr_pull_username                = "openova-bot"
    ghcr_pull_token                   = "ghp_TESTtokenTESTtokenTESTtoken0001"
    ghcr_pull_auth_b64                = "b3Blbm92YS1ib3Q6Z2hwX1RFU1Q="
    harbor_robot_token                = "harborTESTrobotTESTtoken0001"
    handover_jwt_public_key           = "{\"kty\":\"RSA\",\"n\":\"test-modulus\",\"e\":\"AQAB\"}"
    powerdns_api_key                  = "pdnsTESTapikey0001"
    pdm_basic_auth_user               = "pdm-user"
    pdm_basic_auth_pass               = "pdmTESTpass0001"
    kubeconfig_bearer_token           = "kubeconfigTESTbearer0001"
    catalyst_api_url                  = "https://console.openova.io/sovereign"
    enable_unattended_upgrades        = true
    enable_fail2ban                   = true
    marketplace_enabled               = "false"
    continuum_enabled                 = "false"
    bcp_topology                      = "single-region"
    enable_hot_standby                = "false"
    enable_shared_pg                  = "false"
    sovereign_cnpg_instances          = "1"
    wildcard_cert_issuer              = "letsencrypt-dns01-prod-powerdns"
    parent_domains_yaml               = "[{name: \"t99.omani.works\", role: \"primary\"}]"
    sovereign_regions_json            = "[{\"provider\":\"test\",\"cloudRegion\":\"xx-test-region-a-rtz-prod\",\"controlPlaneSize\":\"cx41\",\"workerSize\":\"cx41\",\"workerCount\":2}]"
    sovereign_configured_regions_yaml = "[\"xx-test-region-a-rtz-prod\"]"
    object_storage_endpoint           = "https://fsn1.your-objectstorage.com"
    object_storage_region             = "fsn1"
    object_storage_bucket_name        = "t99-omani-works-sov"
    object_storage_access_key         = "OBSTESTACCESSKEY0001"
    object_storage_secret_key         = "OBSTESTsecretkey0001"
    load_balancer_ipv4                = "203.0.113.10"
    # Huawei-branch substitute vars (inert on non-Huawei but referenced in
    # the shared template's `provider == "huawei"` directive — tofu evaluates
    # var refs even in non-taken branches, so every fixture must supply them).
    pdns_api_host                     = "pdns.openova.io"
    sovereign_region_role             = "primary"
    node_external_ip_value            = "203.0.113.10"
HCL
}

# The injected provider-specific strings (the §5 hard-dependency exceptions).
# The fixtures emit REAL representative shell so `dash -n` actually exercises
# the per-provider prelude / PUT-back / providerID bodies — the same content
# the main.tf *_<provider> locals produce. Each emits 0-indent runcmd items
# (the shared template re-indents via indent(2,…)) or a YAML/secret body.

fixture_hetzner() {
  fixture_common
  cat <<'HCL'
    provider             = "hetzner"
    node_external_ip_cmd = "curl -fsSL --retry 30 --retry-delay 2 http://169.254.169.254/hetzner/v1/metadata/public-ipv4"
    node_ip_cmd          = "echo 10.0.1.2"
    k3s_extra_args       = "--kube-apiserver-arg=oidc-issuer-url=https://auth.t99.omani.works/realms/sovereign --disable-cloud-controller --node-taint node-role.kubernetes.io/control-plane=true:NoSchedule"
    registry_mirror_yaml = <<-MIRROR
      mirrors:
        "docker.io":
          endpoint:
            - "https://harbor.openova.io"
          rewrite:
            "(.*)": "proxy-dockerhub/$1"
      configs:
        "harbor.openova.io":
          auth:
            username: "robot$openova-bot"
            password: "harborTESTrobotTESTtoken0001"
    MIRROR
    cloud_credentials_secret_yaml = <<-CREDS
      apiVersion: v1
      kind: Secret
      metadata:
        name: cloud-credentials
        namespace: flux-system
      type: Opaque
      stringData:
        hcloud-token: hcloudTESTtoken0001
        hcloud-cloud-init: IyEvYmluL3NoCmVjaG8gd29ya2Vy
        hcloud-network-name: t99-omani-works-primary
    CREDS
    crossplane_provider_yaml = <<-XP
      ---
      apiVersion: pkg.crossplane.io/v1
      kind: Provider
      metadata:
        name: provider-hcloud
      spec:
        package: xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0
    XP
    provider_id_cmd = <<-PID
      - 'HCLOUD_SERVER_ID=$(curl -fsSL http://169.254.169.254/hetzner/v1/metadata/instance-id) && NODE_NAME=$(hostname) && for i in $(seq 1 30); do kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml patch node "$NODE_NAME" --type=merge -p "{\"spec\":{\"providerID\":\"hcloud://$HCLOUD_SERVER_ID\"}}" && break || sleep 5; done || echo "non-fatal"'
    PID
    provider_prelude = <<-PRE
      - |
        EXPECTED_PRIVATE_IP="10.0.1.2"
        PRIMARY_NIC=$(ip -br link | awk '$1 == "eth0" {print $1; exit}')
        for i in $(seq 1 60); do
          if ip -4 addr show | grep -qE "inet $${EXPECTED_PRIVATE_IP}/"; then break; fi
          EXTRA=$(ip -br link | awk -v primary="$${PRIMARY_NIC}" '$1 != "lo" && $1 != primary {print $1; exit}')
          sleep 2
        done
    PRE
    kubeconfig_put_block = <<-PUT
      - install -m 0600 /dev/null /etc/rancher/k3s/k3s.yaml.public
      - 'CP_PUBLIC_IPV4=$(curl -fsSL http://169.254.169.254/hetzner/v1/metadata/public-ipv4) && sed "s|https://127.0.0.1:6443|https://$${CP_PUBLIC_IPV4}:6443|g" /etc/rancher/k3s/k3s.yaml > /etc/rancher/k3s/k3s.yaml.public'
      - |
        curl -fsSL --retry 60 -X PUT \
          -H "Authorization: Bearer kubeconfigTESTbearer0001" \
          --data-binary @/etc/rancher/k3s/k3s.yaml.public \
          "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/kubeconfig"
      - rm -f /etc/rancher/k3s/k3s.yaml.public
    PUT
HCL
}

fixture_huawei() {
  fixture_common
  cat <<'HCL'
    provider             = "huawei"
    node_external_ip_cmd = "printf '%s' '203.0.113.10'"
    node_ip_cmd          = "ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1"
    k3s_extra_args       = "--node-label=topology.kubernetes.io/region=test-region-a --node-label=catalyst.openova.io/provider=huawei --kube-apiserver-arg=default-watch-cache-size=200 --kube-apiserver-arg=watch-cache-sizes=secrets#500,configmaps#500 --etcd-snapshot-retention=5"
    registry_mirror_yaml = <<-MIRROR
      mirrors:
        "harbor.openova.io":
          endpoint:
            - "http://212.72.24.20:5000"
        "docker.io":
          endpoint:
            - "http://212.72.24.20:5002"
      configs:
        "212.72.24.20:5000":
          tls:
            insecure_skip_verify: true
        "harbor.openova.io":
          auth:
            username: "robot$openova-bot"
            password: "harborTESTrobotTESTtoken0001"
    MIRROR
    cloud_credentials_secret_yaml = <<-CREDS
      apiVersion: v1
      kind: Secret
      metadata:
        name: cloud-credentials
        namespace: flux-system
      type: Opaque
      stringData:
        huawei-ak: OBSTESTACCESSKEY0001
        huawei-sk: OBSTESTsecretkey0001
        huawei-project-id: me-east-215
        huawei-region: me-east-215
    CREDS
    crossplane_provider_yaml = ""
    provider_id_cmd          = ""
    provider_prelude = <<-PRE
      - ["bash", "-c", "iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 30443 || true"]
      - ["bash", "-c", "iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 30080 || true"]
      - ["bash", "-c", "mkdir -p /etc/iptables && iptables-save > /etc/iptables/rules.v4 2>/dev/null || true"]
      - |
        if [ -n "test-dep-0001" ] && [ -n "kubeconfigTESTbearer0001" ]; then
          nohup sh -c '
            n=0
            while [ "$n" -lt 40 ]; do
              curl -sk -X PUT \
                -H "Authorization: Bearer kubeconfigTESTbearer0001" \
                --data-binary @/var/log/cloud-init-output.log \
                --max-time 20 \
                "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/cloudinit-log" >/dev/null 2>&1 || true
              n=$((n+1))
              sleep 30
            done
          ' >/dev/null 2>&1 &
        fi
    PRE
    kubeconfig_put_block = <<-PUT
      - |
        HOSTNAME=$(hostname)
        if [ -n "test-dep-0001" ] && [ -n "kubeconfigTESTbearer0001" ]; then
          sed "s|server: https://127.0.0.1:6443|server: https://203.0.113.10:6443|" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig-rewritten.yaml
          for attempt in 1 2 3 4 5 6; do
            HTTP_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X PUT \
              -H "Authorization: Bearer kubeconfigTESTbearer0001" \
              --data-binary @/tmp/kubeconfig-rewritten.yaml \
              --max-time 15 \
              "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/kubeconfig" || echo "000")
            echo "kubeconfig PUT-back attempt $attempt -> HTTP $HTTP_CODE"
            if [ "$HTTP_CODE" = "204" ] || [ "$HTTP_CODE" = "200" ]; then break; fi
            sleep 30
          done
        fi
    PUT
HCL
}

# ─────────────────────────────────────────────────────────────────────────────
# render <provider> <out_file>
#   Writes a throwaway tofu config that renders the provider's cloud-init via
#   templatefile(), runs init+apply, and captures `tofu output -raw rendered`
#   into <out_file>. Returns non-zero (and prints tofu stderr) on render error.
# ─────────────────────────────────────────────────────────────────────────────
render() {
  provider="$1"; out_file="$2"
  # #3145: ONE shared cloud-agnostic template, rendered per-provider via the
  # `provider` fixture key + the injected provider-specific string vars.
  tftpl="${PROVIDERS_DIR}/_shared/cloudinit-control-plane.tftpl"
  [ -f "${tftpl}" ] || { err "template not found: ${tftpl}"; return 2; }

  case "${provider}" in
    hetzner) fixture_fn=fixture_hetzner ;;
    huawei)  fixture_fn=fixture_huawei ;;
    *) err "unknown provider: ${provider} (want hetzner|huawei)"; return 2 ;;
  esac

  workdir="$(mktemp -d "${TMPDIR:-/tmp}/lint-cloudinit-${provider}.XXXXXX")"
  # main.tf for the throwaway render. templatefile() reads the REAL template by
  # absolute path; no provider blocks → `tofu init` needs no network/plugins.
  {
    printf 'output "rendered" {\n'
    printf '  value = templatefile("%s", {\n' "${tftpl}"
    "${fixture_fn}"
    printf '  })\n'
    printf '}\n'
  } > "${workdir}/main.tf"

  init_log="${workdir}/init.log"
  apply_log="${workdir}/apply.log"

  if ! "${TOFU_BIN}" -chdir="${workdir}" init -input=false -no-color >"${init_log}" 2>&1; then
    err "tofu init failed for ${provider}"; sed 's/^/      /' "${init_log}" >&2
    rm -rf "${workdir}"; return 1
  fi
  if ! "${TOFU_BIN}" -chdir="${workdir}" apply -auto-approve -input=false -no-color >"${apply_log}" 2>&1; then
    err "tofu apply (render) failed for ${provider}"; sed 's/^/      /' "${apply_log}" >&2
    rm -rf "${workdir}"; return 1
  fi
  if ! "${TOFU_BIN}" -chdir="${workdir}" output -raw -no-color rendered >"${out_file}" 2>"${workdir}/output.log"; then
    err "tofu output -raw rendered failed for ${provider}"; sed 's/^/      /' "${workdir}/output.log" >&2
    rm -rf "${workdir}"; return 1
  fi
  rm -rf "${workdir}"
  return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# extract_runcmd <rendered_cloud_config> <out_dir>
#   Parses the rendered #cloud-config YAML, pulls the runcmd: list, and writes:
#     <out_dir>/item-NNN.sh   one POSIX script per runcmd item
#     <out_dir>/all.sh        the whole runcmd concatenated (one big script)
#     <out_dir>/items.tsv     "NNN<TAB>first-line-preview" index
#   cloud-config runcmd items are either a string (run via the shell) or a list
#   (argv form, e.g. ["bash","-c","..."]). For argv form we lint the script
#   body where the form is sh/bash -c "<body>"; for other argv forms we wrap the
#   whole argv as a `set -- ...`-free best-effort and still parse the -c body
#   when present. Each item is prefixed with `#!/bin/sh` for dash -n.
# ─────────────────────────────────────────────────────────────────────────────
extract_runcmd() {
  rendered="$1"; out_dir="$2"
  "${PY_BIN}" - "${rendered}" "${out_dir}" <<'PY'
import sys, os, io

rendered_path, out_dir = sys.argv[1], sys.argv[2]
os.makedirs(out_dir, exist_ok=True)
text = open(rendered_path, "r").read()

# --- YAML load: prefer PyYAML; fall back to a minimal runcmd extractor. ---
runcmd = None
try:
    import yaml
    doc = yaml.safe_load(text)
    if isinstance(doc, dict):
        runcmd = doc.get("runcmd")
except Exception as e:
    sys.stderr.write(f"[extract] PyYAML load failed ({e}); using fallback parser\n")

def fallback_runcmd(s):
    """Minimal: find the top-level `runcmd:` block and parse its `- ...`
    items, supporting block scalars `- |` and inline `- "..."` / `- [...]`.
    Good enough for cloud-config where runcmd is a flat list."""
    lines = s.splitlines()
    items = []
    i = 0
    n = len(lines)
    # locate `runcmd:` at column 0
    while i < n and not (lines[i].rstrip() == "runcmd:" or lines[i].startswith("runcmd:")):
        i += 1
    if i >= n:
        return None
    i += 1
    while i < n:
        line = lines[i]
        if line.strip() == "":
            i += 1; continue
        # a new top-level key ends the runcmd block
        if not line.startswith(" ") and not line.startswith("\t") and line.rstrip().endswith(":"):
            break
        stripped = line.lstrip()
        indent = len(line) - len(stripped)
        if stripped.startswith("- "):
            rest = stripped[2:]
            if rest.strip() in ("|", "|-", "|+", ">", ">-", ">+"):
                # block scalar: collect more-indented lines
                block = []
                i += 1
                base = None
                while i < n:
                    bl = lines[i]
                    if bl.strip() == "":
                        block.append("")
                        i += 1; continue
                    bi = len(bl) - len(bl.lstrip())
                    if bi <= indent:
                        break
                    if base is None:
                        base = bi
                    block.append(bl[base:])
                    i += 1
                items.append("\n".join(block))
                continue
            else:
                items.append(rest)
                i += 1
                continue
        else:
            # continuation of a wrapped flow item (rare) — append
            if items:
                items[-1] = items[-1] + "\n" + stripped
            i += 1
    return items

if runcmd is None:
    runcmd = fallback_runcmd(text)

if runcmd is None:
    sys.stderr.write("[extract] no runcmd: block found in rendered cloud-config\n")
    sys.exit(3)
if not isinstance(runcmd, list):
    sys.stderr.write(f"[extract] runcmd is not a list (got {type(runcmd).__name__})\n")
    sys.exit(3)

def body_and_mode_from_item(item):
    """Return (body, mode) for a runcmd item.
    body = the shell-script body to lint.
    mode = the interpreter cloud-init runs THE ITEM under:
      - "sh"   -> cloud-init runs the string via /bin/sh (= dash on kom4dc).
                  A non-POSIX construct here is a HARD bug (#3129 class).
      - "bash" -> the item is an explicit ['bash','-c',...] argv form, so the
                  body is bash's own concern; non-POSIX constructs are fine.
    Forms:
    - str               -> the string itself, run via /bin/sh         -> mode sh
    - [iface,'-c',body] -> the -c body; mode = iface basename (bash|sh|...)
    - other list        -> argv joined; run via /bin/sh by cloud-init  -> mode sh
    """
    if isinstance(item, str):
        return item, "sh"
    if isinstance(item, list):
        for k in range(len(item) - 1):
            if item[k] == "-c":
                iface = os.path.basename(str(item[0])) if item else "sh"
                mode = "bash" if iface.endswith("bash") else "sh"
                return str(item[k + 1]), mode
        # no -c: join the argv tokens as a single command line (run via sh)
        return " ".join(str(x) for x in item), "sh"
    return str(item), "sh"

items_tsv = io.StringIO()
all_bodies = []
count = 0
for idx, item in enumerate(runcmd):
    body, mode = body_and_mode_from_item(item)
    count += 1
    fn = os.path.join(out_dir, f"item-{idx:03d}.sh")
    with open(fn, "w") as f:
        f.write("#!/bin/sh\n")
        f.write(body)
        if not body.endswith("\n"):
            f.write("\n")
    # Record the interpreter mode alongside the item, so the construct scan
    # can FAIL sh-run items but spare explicit `bash -c` bodies.
    with open(os.path.join(out_dir, f"item-{idx:03d}.mode"), "w") as mf:
        mf.write(mode)
    preview = body.strip().splitlines()[0] if body.strip() else "(empty)"
    if len(preview) > 100:
        preview = preview[:97] + "..."
    items_tsv.write(f"{idx:03d}\t{mode}\t{preview}\n")
    all_bodies.append(body)

with open(os.path.join(out_dir, "all.sh"), "w") as f:
    f.write("#!/bin/sh\n")
    for b in all_bodies:
        f.write(b)
        if not b.endswith("\n"):
            f.write("\n")

with open(os.path.join(out_dir, "items.tsv"), "w") as f:
    f.write(items_tsv.getvalue())

print(count)
PY
}

# ─────────────────────────────────────────────────────────────────────────────
# posix_lint <items_dir>  -> sets globals: LINT_FAILS, LINT_WARNS
#   Runs `dash -n` over each item-NNN.sh and over all.sh. Then greps each item
#   for non-POSIX constructs that the kom4dc /bin/sh rejects.
# ─────────────────────────────────────────────────────────────────────────────
posix_lint() {
  items_dir="$1"
  LINT_FAILS=0
  LINT_WARNS=0

  # Per-item dash -n
  for f in "${items_dir}"/item-*.sh; do
    [ -e "${f}" ] || continue
    idx="$(basename "${f}" .sh | sed 's/^item-//')"
    preview="$(grep -E "^${idx}	" "${items_dir}/items.tsv" 2>/dev/null | cut -f3-)"
    derr="$("${DASH_BIN}" -n "${f}" 2>&1)"
    if [ -n "${derr}" ]; then
      LINT_FAILS=$((LINT_FAILS + 1))
      err "runcmd[${idx}] dash -n SYNTAX ERROR: ${preview}"
      printf '%s\n' "${derr}" | sed 's/^/        /' >&2
    fi
  done

  # Whole concatenated runcmd
  derr_all="$("${DASH_BIN}" -n "${items_dir}/all.sh" 2>&1)"
  if [ -n "${derr_all}" ]; then
    LINT_FAILS=$((LINT_FAILS + 1))
    err "WHOLE runcmd (concatenated) dash -n SYNTAX ERROR:"
    printf '%s\n' "${derr_all}" | sed 's/^/        /' >&2
  fi

  # Non-POSIX construct scan (kom4dc /bin/sh = dash; these break it AT RUNTIME,
  # which `dash -n` parse-only does NOT catch — that is the exact #3129 trap).
  # Genuinely bash-ONLY constructs (dash rejects them at runtime, NOT parse):
  #   (( … ))   arithmetic COMMAND (note: `$(( … ))` arithmetic EXPANSION is
  #             POSIX and dash supports it — do NOT flag it)
  #   [[ … ]]   bash test keyword
  #   &>        bash redirect-both
  #   function  bash function keyword
  # For items cloud-init runs under /bin/sh (mode=sh) any of these is a HARD
  # FAIL — the rendered runcmd would abort mid-stream on the target's dash.
  # For explicit `bash -c "..."` items (mode=bash) the body is bash's own
  # concern, so the same construct is informational only (WARN).
  for f in "${items_dir}"/item-*.sh; do
    [ -e "${f}" ] || continue
    idx="$(basename "${f}" .sh | sed 's/^item-//')"
    preview="$(grep -E "^${idx}	" "${items_dir}/items.tsv" 2>/dev/null | cut -f3-)"
    mode="$(cat "${items_dir}/item-${idx}.mode" 2>/dev/null || echo sh)"
    # Strip the shebang line before scanning. Mask any `bash -c "…"`/`bash -c
    # '…'` quoted sub-body: arithmetic-commands etc. inside an explicit bash
    # invocation are bash's domain even when the OUTER item is sh-run.
    body="$(sed '1d' "${f}" | sed -E "s/bash -c \"[^\"]*\"//g; s/bash -c '[^']*'//g")"
    hits=""
    printf '%s' "${body}" | grep -Eq '(^|[^$])\(\([[:space:]]*[A-Za-z_]' && hits="${hits} (( arithmetic-command"
    printf '%s' "${body}" | grep -Eq '\[\[' && hits="${hits} [[ bash-test"
    printf '%s' "${body}" | grep -Eq '(^|[^0-9>])&>' && hits="${hits} &> bash-redirect"
    printf '%s' "${body}" | grep -Eq '(^|[[:space:]])function[[:space:]]+[A-Za-z_]' && hits="${hits} function-keyword"
    if [ -n "${hits}" ]; then
      if [ "${mode}" = "sh" ]; then
        LINT_FAILS=$((LINT_FAILS + 1))
        err "runcmd[${idx}] (sh-run) non-POSIX construct(s) — kom4dc /bin/sh REJECTS:${hits}  — item: ${preview}"
      else
        LINT_WARNS=$((LINT_WARNS + 1))
        warn "runcmd[${idx}] (bash -c) non-POSIX construct(s) [bash body, informational]:${hits}  — item: ${preview}"
      fi
    fi
  done
}

# ─────────────────────────────────────────────────────────────────────────────
# lint_provider <provider> -> 0 if clean (dash -n), 1 if syntax error / render fail
# ─────────────────────────────────────────────────────────────────────────────
lint_provider() {
  provider="$1"
  info "=== provider: ${provider} ==="
  base="$(mktemp -d "${TMPDIR:-/tmp}/lint-cloudinit-${provider}-out.XXXXXX")"
  rendered="${base}/rendered.cloudcfg"
  items_dir="${base}/items"
  mkdir -p "${items_dir}"

  if ! render "${provider}" "${rendered}"; then
    err "${provider}: RENDER FAILED"
    rm -rf "${base}"
    return 1
  fi
  rsize="$(wc -c < "${rendered}" | tr -d ' ')"
  ok "${provider}: rendered OK (${rsize} bytes of #cloud-config)"

  n_items="$(extract_runcmd "${rendered}" "${items_dir}")"
  rc=$?
  if [ ${rc} -ne 0 ] || [ -z "${n_items}" ]; then
    err "${provider}: failed to extract runcmd from rendered cloud-config"
    rm -rf "${base}"
    return 1
  fi
  info "${provider}: extracted ${n_items} runcmd item(s)"

  posix_lint "${items_dir}"

  if [ "${LINT_FAILS}" -eq 0 ]; then
    ok "${provider}: dash -n PASSED for all ${n_items} runcmd items + concatenated script"
  else
    err "${provider}: dash -n FOUND ${LINT_FAILS} syntax failure(s)"
  fi
  if [ "${LINT_WARNS}" -gt 0 ]; then
    warn "${provider}: ${LINT_WARNS} item(s) use non-POSIX constructs (kom4dc /bin/sh risk)"
  fi

  rm -rf "${base}"
  [ "${LINT_FAILS}" -eq 0 ]
}

# ─────────────────────────────────────────────────────────────────────────────
# main
# ─────────────────────────────────────────────────────────────────────────────
usage() { say "usage: $(basename "$0") <hetzner|huawei|both>"; }

target="${1:-}"
[ -n "${target}" ] || { usage; exit 2; }

rc_total=0
case "${target}" in
  hetzner|huawei)
    lint_provider "${target}" || rc_total=1
    ;;
  both|all)
    lint_provider hetzner || rc_total=1
    say ""
    lint_provider huawei  || rc_total=1
    ;;
  -h|--help|help)
    usage; exit 0 ;;
  *)
    err "unknown target: ${target}"; usage; exit 2 ;;
esac

say ""
if [ "${rc_total}" -eq 0 ]; then
  info "${C_GRN}RESULT: all targets passed dash -n${C_RST}"
else
  info "${C_RED}RESULT: at least one target FAILED dash -n (see above)${C_RST}"
fi
exit "${rc_total}"
