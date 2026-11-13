#!/usr/bin/env bash
# check-k3s-secrets-encryption.sh - etcd encryption on every k3s server (SEC-030).
#
# A k3s server writes every Kubernetes Secret to etcd. Started with the flags
# --secrets-encryption and --secrets-encryption-provider=secretbox it writes
# each Secret as secretbox ciphertext; without them it writes it in the clear.
# Gate check SEC-G01 proves the result on a running Sovereign. This guard keeps
# the flags and the k3s version in the source. It scans infra/ and fails when:
#
#   rule 1  no line under infra/ starts a k3s server (a line that holds
#           INSTALL_K3S_EXEC="server). Then nothing was scanned.
#   rule 2  a k3s server line lacks the word --secrets-encryption.
#   rule 3  a k3s server line lacks the word
#           --secrets-encryption-provider=secretbox, or names another provider.
#   rule 4  no file infra/**/*.tf declares variable "k3s_version". Then no
#           version was scanned.
#   rule 5  a variable "k3s_version" has no default, or its default is lower
#           than v1.31.8 (the lowest k3s release this platform runs with the
#           secretbox provider).
#
# The flags must be words of the server line itself. A flag that reaches k3s
# through a provider's extra arguments is not seen and does not count.
#
# Usage:
#   scripts/check-k3s-secrets-encryption.sh [--root <repository root>]
#   scripts/check-k3s-secrets-encryption.sh --self-test
# The self-test builds its own trees in a temporary directory and proves that
# each rule can fail. It reads no file of the checkout.
#
# Exit codes: 0 every rule holds, or the self-test passed; 1 a rule failed, or
# a self-test case failed; 2 wrong usage.
set -euo pipefail

MIN_VERSION="v1.31.8"
SERVER_MARK='INSTALL_K3S_EXEC="server'

usage() {
  echo "usage: $0 [--root <repository root>] | --self-test" >&2
  exit 2
}

# server_lines <infra dir>: prints "<file>:<line>:<text>" for every line that
# starts a k3s server, sorted.
server_lines() {
  grep -r -n -I -F -e "$SERVER_MARK" "$1" 2>/dev/null | LC_ALL=C sort || true
}

# judge_server_line <text>: prints the rules that the line breaks, one
# "<rule> <reason>" per line. The words are split at blanks and quotes.
judge_server_line() {
  printf '%s\n' "$1" | awk '
    {
      n = split($0, w, /[ \t"]+/)
      enc = 0; box = 0; other = ""
      for (i = 1; i <= n; i++) {
        if (w[i] == "--secrets-encryption") enc = 1
        else if (w[i] == "--secrets-encryption-provider=secretbox") box = 1
        else if (index(w[i], "--secrets-encryption-provider=") == 1) other = other " " w[i]
      }
      if (!enc) print "2 the server line lacks --secrets-encryption"
      if (!box) print "3 the server line lacks --secrets-encryption-provider=secretbox"
      if (other != "") print "3 the server line names another provider:" other
    }'
}

# version_defaults <infra dir>: prints "<file>:<line>:<default>" for every
# variable "k3s_version" in a .tf file, sorted. The default is empty when the
# block has none. A block ends at the first line that is only "}".
version_defaults() {
  local f
  find "$1" -type f -name '*.tf' | LC_ALL=C sort | while IFS= read -r f; do
    awk -v file="$f" '
      /^variable "k3s_version"/ { inside = 1; start = FNR; def = ""; next }
      inside && /^}[ \t]*$/ { print file ":" start ":" def; inside = 0; next }
      inside && /^[ \t]*default[ \t]*=/ {
        line = $0
        sub(/^[ \t]*default[ \t]*=[ \t]*/, "", line)
        if (line ~ /^"[^"]*"/) { sub(/^"/, "", line); sub(/".*$/, "", line); def = line }
        else def = "?" line
      }
      END { if (inside) print file ":" start ":" def }' "$f"
  done
}

# version_ok <version>: true when <version> is vMAJOR.MINOR.PATCH(+k3sN) and
# not lower than MIN_VERSION. The numbers are compared as numbers.
version_ok() {
  printf '%s %s\n' "$1" "$MIN_VERSION" | awk '
    {
      if ($1 !~ /^v[0-9]+\.[0-9]+\.[0-9]+(\+k3s[0-9]+)?$/) exit 1
      a = $1; b = $2
      sub(/^v/, "", a); sub(/\+.*$/, "", a); sub(/^v/, "", b)
      split(a, x, "."); split(b, y, ".")
      for (i = 1; i <= 3; i++) {
        if (x[i] + 0 > y[i] + 0) exit 0
        if (x[i] + 0 < y[i] + 0) exit 1
      }
      exit 0
    }'
}

# scan <repository root>: applies the five rules to <root>/infra. Prints one
# line per finding and a summary line. Returns 0 when every rule holds.
scan() {
  local root="$1" infra="$1/infra" findings=0 servers=0 versions=0
  local entry file rest num text rule reason def
  if [ ! -d "$infra" ]; then
    echo "check-k3s-secrets-encryption: $infra is not a directory" >&2
    return 2
  fi
  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    servers=$((servers + 1))
    file="${entry%%:*}"; rest="${entry#*:}"; num="${rest%%:*}"; text="${rest#*:}"
    while IFS= read -r rule; do
      [ -n "$rule" ] || continue
      reason="${rule#* }"; rule="${rule%% *}"
      echo "FAIL rule $rule: ${file#"$root"/}:$num: $reason"
      findings=$((findings + 1))
    done < <(judge_server_line "$text")
  done < <(server_lines "$infra")
  if [ "$servers" -eq 0 ]; then
    echo "FAIL rule 1: no line under infra/ holds $SERVER_MARK, so no k3s server line was scanned"
    findings=$((findings + 1))
  fi
  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    versions=$((versions + 1))
    file="${entry%%:*}"; rest="${entry#*:}"; num="${rest%%:*}"; def="${rest#*:}"
    if [ -z "$def" ]; then
      echo "FAIL rule 5: ${file#"$root"/}:$num: variable \"k3s_version\" has no default"
      findings=$((findings + 1))
    elif ! version_ok "$def"; then
      echo "FAIL rule 5: ${file#"$root"/}:$num: the default $def of variable \"k3s_version\" is lower than $MIN_VERSION or not a k3s version"
      findings=$((findings + 1))
    fi
  done < <(version_defaults "$infra")
  if [ "$versions" -eq 0 ]; then
    echo "FAIL rule 4: no .tf file under infra/ declares variable \"k3s_version\", so no version was scanned"
    findings=$((findings + 1))
  fi
  echo "check-k3s-secrets-encryption: $servers k3s server line(s), $versions k3s_version default(s), $findings finding(s)"
  [ "$findings" -eq 0 ]
}

self_test() {
  local failed=0 cases=0
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  local good="  INSTALL_K3S_EXEC=\"server --cluster-init --secrets-encryption --secrets-encryption-provider=secretbox --agent-token=\${t} \${k3s_extra_args} --write-kubeconfig-mode=0644\""
  # tree <server line or "-"> <k3s_version default or "-" or "none"> [second server line]
  tree() {
    rm -rf "$work/t"
    mkdir -p "$work/t/infra/providers/_shared" "$work/t/infra/providers/a"
    if [ "$1" != "-" ]; then
      printf 'runcmd:\n  - |\n%s\n    sh install.sh\n' "$1" > "$work/t/infra/providers/_shared/boot.tftpl"
    else
      printf 'runcmd:\n  - echo no server here\n' > "$work/t/infra/providers/_shared/boot.tftpl"
    fi
    if [ -n "${3:-}" ]; then printf '%s\n' "$3" >> "$work/t/infra/providers/_shared/boot.tftpl"; fi
    case "$2" in
      -) printf 'variable "other" {\n  type = string\n}\n' > "$work/t/infra/providers/a/variables.tf" ;;
      none) printf 'variable "k3s_version" {\n  type = string\n}\n' > "$work/t/infra/providers/a/variables.tf" ;;
      *) printf 'variable "k3s_version" {\n  type        = string\n  description = <<-EOT\n    k3s release (e.g. v1.31.4+k3s1).\n  EOT\n  default     = "%s"\n  validation {\n    condition     = true\n    error_message = "x"\n  }\n}\n' "$2" > "$work/t/infra/providers/a/variables.tf" ;;
    esac
  }
  # expect <0|1> <text the output must hold> <name of the case>
  expect() {
    local want="$1" needle="$2" name="$3" got=0 out
    cases=$((cases + 1))
    out="$(scan "$work/t" 2>&1)" || got=$?
    if [ "$got" = "$want" ] && printf '%s\n' "$out" | grep -q -F -e "$needle"; then
      echo "ok    $name"
    else
      echo "FAIL  $name: exit $got (wanted $want), or the output does not hold: $needle"
      printf '%s\n' "$out" | sed 's/^/      /'
      failed=1
    fi
  }
  tree "$good" "v1.31.8+k3s1"
  expect 0 "1 k3s server line(s), 1 k3s_version default(s), 0 finding(s)" "a server line with both flags and the default v1.31.8+k3s1 passes"
  tree "$good" "v1.31.10+k3s1"
  expect 0 "0 finding(s)" "v1.31.10 passes: the numbers are compared as numbers, not as text"
  tree "$good" "v1.32.0+k3s1"
  expect 0 "0 finding(s)" "a newer minor version passes"
  tree "-" "v1.31.8+k3s1"
  expect 1 "FAIL rule 1:" "rule 1: a tree with no k3s server line fails"
  tree "${good/ --secrets-encryption --secrets-encryption-provider=secretbox/ --secrets-encryption-provider=secretbox}" "v1.31.8+k3s1"
  expect 1 "FAIL rule 2: infra/providers/_shared/boot.tftpl:3: the server line lacks --secrets-encryption" "rule 2: a server line without --secrets-encryption fails"
  tree "${good/ --secrets-encryption-provider=secretbox/}" "v1.31.8+k3s1"
  expect 1 "FAIL rule 3: infra/providers/_shared/boot.tftpl:3: the server line lacks --secrets-encryption-provider=secretbox" "rule 3: a server line without the provider fails"
  tree "${good/=secretbox/=aescbc}" "v1.31.8+k3s1"
  expect 1 "FAIL rule 3: infra/providers/_shared/boot.tftpl:3: the server line names another provider: --secrets-encryption-provider=aescbc" "rule 3: the provider aescbc fails"
  tree "${good} --secrets-encryption-provider=aescbc" "v1.31.8+k3s1"
  expect 1 "names another provider: --secrets-encryption-provider=aescbc" "rule 3: a second provider beside secretbox fails"
  tree "${good/ --secrets-encryption --secrets-encryption-provider=secretbox/}" "v1.31.8+k3s1"
  expect 1 "FAIL rule 2:" "rule 2: flags left to the provider's extra arguments do not count"
  tree "$good" "v1.31.8+k3s1" '  INSTALL_K3S_EXEC="server --cluster-init"'
  expect 1 "FAIL rule 2: infra/providers/_shared/boot.tftpl:5:" "rule 2: every server line is judged, not only the first"
  tree "$good" "-"
  expect 1 "FAIL rule 4:" "rule 4: a tree without variable k3s_version fails"
  tree "$good" "v1.31.4+k3s1"
  expect 1 "FAIL rule 5: infra/providers/a/variables.tf:1: the default v1.31.4+k3s1" "rule 5: the default v1.31.4+k3s1 fails"
  tree "$good" "v1.31.7+k3s2"
  expect 1 "FAIL rule 5:" "rule 5: the default v1.31.7+k3s2 fails"
  tree "$good" "v1.30.12+k3s1"
  expect 1 "FAIL rule 5:" "rule 5: an older minor version fails"
  tree "$good" "none"
  expect 1 "has no default" "rule 5: a k3s_version without a default fails"
  tree "$good" "latest"
  expect 1 "FAIL rule 5:" "rule 5: a default that is not a k3s version fails"
  if [ "$failed" -ne 0 ]; then
    echo "self-test FAILED"
    return 1
  fi
  echo "self-test passed: $cases cases"
}

ROOT="."
case "${1:-}" in
  --self-test)
    [ $# -eq 1 ] || usage
    self_test
    exit $?
    ;;
  --root)
    [ $# -eq 2 ] || usage
    ROOT="$2"
    ;;
  "") [ $# -eq 0 ] || usage ;;
  *) usage ;;
esac
scan "$ROOT"
