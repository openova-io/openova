#!/bin/sh
# test-cloudinit-first-boot-fails-loudly.sh - two first-boot fragments see the
# failure of the first command of their pipe, and the Cilium item's set -e
# still stops the boot at a failed SEC-040 apply.
#
# A. The helm download of the Cilium leg. It takes the retry helper rt() and
#    the helm download line from
#    infra/providers/_shared/cloudinit-control-plane.tftpl as they stand and
#    runs them under sh with a curl stub on PATH:
#    1. curl fails every time and writes nothing (a DNS miss, a reset or a
#       timeout): the download is tried 5 times, the step exits 91 and the
#       sentinel cilium-bootstrap-FAILED is written;
#    2. curl fails twice and then serves the installer: the step succeeds on
#       the third try, the installer runs and no sentinel is written.
#    The line was `rt sh -c 'curl -sSL ... | bash'`: under sh the pipe
#    reported bash's 0 when curl failed, so case 1 ended after one try with
#    status 0, no sentinel and no helm on the node.
#
# B. The name pinning helper r4() of the control-plane template and of
#    infra/providers/huawei/cloudinit-worker.tftpl. When getent finds no
#    address, r4 must ask the next tier (nslookup against a public resolver).
#    It was `getent ... | awk ... && return`: awk's 0 returned an empty answer.
#
# C. The set -e of the Cilium item. runcmd is one script, and the SEC-040
#    item relies on it: its `(exit $?)` stops the boot when the apply or the
#    create fails, so the secret files are not shredded. The test runs the
#    Cilium item and the SEC-040 item as they stand, in that order, with
#    stubs whose server-side apply fails: the script must stop there with
#    status 1. The same run without the item's set -e must go on, which
#    shows the case measures what set -e does.
#
# Usage: sh scripts/tests/test-cloudinit-first-boot-fails-loudly.sh
#        (TPL=<file> WORKER_TPL=<file> test other copies)
# Exit: 0 = every case holds; 1 = a case failed.
set -eu

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
TPL=${TPL:-$ROOT/infra/providers/_shared/cloudinit-control-plane.tftpl}
WORKER_TPL=${WORKER_TPL:-$ROOT/infra/providers/huawei/cloudinit-worker.tftpl}
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir "$W/bin"
cat >"$W/bin/curl" <<STUB
#!/bin/sh
n=\$((\$(cat "$W/tries" 2>/dev/null || echo 0) + 1))
echo "\$n" >"$W/tries"
[ "\$n" -le "\$CURL_FAILS" ] && exit 6
while [ \$# -gt 0 ]; do
  [ "\$1" = -o ] && printf 'touch %s/installer-ran\n' "$W" >"\$2"
  shift
done
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' >"$W/bin/sleep"
printf '#!/bin/sh\nexit 2\n' >"$W/bin/getent"
# shellcheck disable=SC2016 # the stub's own $2 and $3
printf '#!/bin/sh\nprintf "Server:\\t\\t%%s\\nAddress:\\t%%s#53\\n\\nName:\\t%%s\\nAddress: 192.0.2.7\\n" "$3" "$3" "$2"\n' >"$W/bin/nslookup"
printf '#!/bin/sh\nexit 0\n' >"$W/bin/helm"
# shellcheck disable=SC2016 # the stub's own $a and $@
printf '#!/bin/sh\nfor a in "$@"; do [ "$a" = --server-side ] && exit 1; done\nexit 0\n' >"$W/bin/kubectl"
printf '#!/bin/sh\ntouch %s/shredded\n' "$W" >"$W/bin/shred"
chmod +x "$W/bin/curl" "$W/bin/sleep" "$W/bin/getent" "$W/bin/nslookup" "$W/bin/helm" "$W/bin/kubectl" "$W/bin/shred"

# ── A. the helm download ─────────────────────────────────────────────────
# The fragment, with the template's paths and its mark prefix moved into $W.
grep -E '^    rt\(\)\{|^    rt sh -c .*get-helm-3' "$TPL" \
  | sed -e "s#\${fail_mark_cmd}#touch $W/#" -e "s#/var/lib/catalyst/#$W/#g" >"$W/fragment.sh"
[ "$(wc -l <"$W/fragment.sh")" -eq 2 ] || fail "expected the rt() helper and one get-helm-3 line in $TPL, found: $(cat "$W/fragment.sh")"

run() { # $1 = how many times curl fails, $2 = the fragment (fragment.sh)
  rm -f "$W/tries" "$W/installer-ran" "$W/cilium-bootstrap-FAILED" "$W/shredded" "$W/after-sec040"
  rc=0
  CURL_FAILS=$1 PATH="$W/bin:$PATH" sh "$W/${2:-fragment.sh}" >"$W/out" 2>&1 || rc=$?
  tries=$(cat "$W/tries" 2>/dev/null || echo 0)
}

run 99
[ "$rc" -eq 91 ] || fail "network down: exit $rc after $tries tries, want 91 ($(cat "$W/out"))"
[ "$tries" -eq 5 ] || fail "network down: the download was tried $tries times, want 5"
[ -f "$W/cilium-bootstrap-FAILED" ] || fail "network down: no cilium-bootstrap-FAILED sentinel"
echo "ok: a download that never succeeds is tried 5 times, exits 91 and writes the sentinel"

run 2
[ "$rc" -eq 0 ] || fail "two transient failures: exit $rc, want 0 ($(cat "$W/out"))"
[ "$tries" -eq 3 ] || fail "two transient failures: $tries tries, want 3"
[ -f "$W/installer-ran" ] || fail "two transient failures: the downloaded installer never ran"
[ ! -f "$W/cilium-bootstrap-FAILED" ] || fail "two transient failures: the sentinel was written"
echo "ok: a download that fails twice succeeds on the third try and runs the installer"

# ── B. r4() falls through a getent miss ──────────────────────────────────
for t in "$TPL" "$WORKER_TPL"; do
  awk '/^    r4\(\) \{$/{p=1} p{print} p&&/^    \}$/{exit}' "$t" >"$W/r4.sh"
  [ "$(head -n 1 "$W/r4.sh")" = "    r4() {" ] || fail "no r4() helper in $t"
  # shellcheck disable=SC2016 # $1 of the extracted script
  echo 'r4 "$1"' >>"$W/r4.sh"
  got=$(PATH="$W/bin:$PATH" sh "$W/r4.sh" github.com 2>/dev/null || true)
  [ "$got" = 192.0.2.7 ] || fail "r4() of $t answered '$got' when getent found nothing, want the nslookup address 192.0.2.7"
  echo "ok: r4() of $(basename "$t") asks nslookup when getent finds nothing"
done

# ── C. the Cilium item's set -e stops the boot at a failed SEC-040 apply ──
item() { # the runcmd item (its indented lines) that carries the string $1
  awk -v want="$1" '
    /^  - \|$/ { if (hit) exit; buf = ""; on = 1; next }
    on && /^    / { buf = buf $0 "\n"; if (index($0, want)) hit = 1; next }
    { if (hit) exit; on = 0 }
    END { if (hit) printf "%s", buf }' "$TPL"
}
item 'rt(){' >"$W/cilium-item"
item '(exit $?)' >"$W/sec040-item"
[ "$(head -n 1 "$W/cilium-item")" = "    set -e" ] || fail "the Cilium item of $TPL does not start with set -e: $(cat "$W/cilium-item")"
grep -q '^    (exit \$?)$' "$W/sec040-item" || fail "no SEC-040 item with (exit \$?) in $TPL"
cat "$W/cilium-item" "$W/sec040-item" \
  | sed -e "s#\${fail_mark_cmd}#touch $W/#" -e "s#/var/lib/catalyst#$W#g" >"$W/boot.sh"
echo "touch $W/after-sec040" >>"$W/boot.sh"
sed '1d' "$W/boot.sh" >"$W/boot-without-set-e.sh"

run 0 boot.sh
[ "$rc" -eq 1 ] || fail "failed apply: the boot script exited $rc, want 1 ($(cat "$W/out"))"
[ -f "$W/installer-ran" ] || fail "failed apply: the Cilium item did not run to its end ($(cat "$W/out"))"
[ ! -f "$W/shredded" ] || fail "failed apply: the secret files were shredded"
[ ! -f "$W/after-sec040" ] || fail "failed apply: the boot script went on past the SEC-040 item"
run 0 boot-without-set-e.sh
[ -f "$W/after-sec040" ] || fail "without the Cilium item's set -e the boot script still stopped: the case does not measure set -e ($(cat "$W/out"))"
echo "ok: the Cilium item's set -e stops the boot at a failed SEC-040 apply, and the files stay"
