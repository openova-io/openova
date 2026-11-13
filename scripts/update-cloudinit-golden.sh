#!/usr/bin/env bash
# update-cloudinit-golden.sh - writes the golden hashes of the shared cloud-init render (CLD-040).
#
# The render test in infra/providers/_shared/tests renders the one shared
# cloud-init template with two fixtures (hetzner and huawei), strips the comment
# lines as the provider modules do, and gives the SHA-256 of each render as an
# output (sha256_hetzner, sha256_huawei). Its run "golden" compares the two
# outputs with the values stored in golden.auto.tfvars.json. So a change to the
# rendered cloud-init fails that run until the stored values are updated.
#
# This script computes the two values and writes that file. Run it when a
# change to the rendered cloud-init is intended, commit the file with the
# change, and say in the commit message why the render changed.
#
# Usage:
#   scripts/update-cloudinit-golden.sh              write golden.auto.tfvars.json
#   scripts/update-cloudinit-golden.sh --self-test  prove that the run "golden" can fail
#
# The self-test works on a copy of the render test in a temporary directory.
# It never changes a file of the checkout.
#
# Exit: 0 = the file is written, or the self-test passed.
#       1 = the render failed, or a self-test case failed.
#       2 = wrong usage, or a missing tool.
# Needs: tofu (set TOFU_BIN to use another binary). No provider, no network, no cloud account.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TESTS_REL="infra/providers/_shared/tests"
GOLDEN_NAME="golden.auto.tfvars.json"
TOFU_BIN="${TOFU_BIN:-tofu}"

command -v "$TOFU_BIN" > /dev/null 2>&1 || { echo "update-cloudinit-golden: '$TOFU_BIN' is not on the PATH" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# The working files of tofu and its state stay in the temporary directory.
export TF_DATA_DIR="$WORK/data"
export TF_IN_AUTOMATION=1

# hashes <directory of the render test>: prints "<hetzner hash> <huawei hash>".
# It applies the render module (it has no resource, so nothing is created) with
# a local state file in the temporary directory, and reads the two outputs.
hashes() {
  local dir="$1" state="$WORK/golden.tfstate" a b
  rm -f "$state"
  "$TOFU_BIN" -chdir="$dir" init -input=false -no-color > "$WORK/init.log" 2>&1 || { cat "$WORK/init.log" >&2; return 1; }
  "$TOFU_BIN" -chdir="$dir" apply -auto-approve -input=false -no-color -state="$state" > "$WORK/apply.log" 2>&1 || { cat "$WORK/apply.log" >&2; return 1; }
  a="$("$TOFU_BIN" -chdir="$dir" output -no-color -state="$state" -raw sha256_hetzner)" || return 1
  b="$("$TOFU_BIN" -chdir="$dir" output -no-color -state="$state" -raw sha256_huawei)" || return 1
  if ! printf '%s\n%s\n' "$a" "$b" | grep -c -E '^[0-9a-f]{64}$' | grep -q -x 2; then
    echo "update-cloudinit-golden: the outputs are not two SHA-256 values: '$a' '$b'" >&2
    return 1
  fi
  printf '%s %s\n' "$a" "$b"
}

# write_golden <directory of the render test>: writes the golden file there.
# The old file is moved away first, so that a file that is not valid JSON (for
# example after a merge conflict) cannot stop the render. It is put back when
# the render fails.
write_golden() {
  local dir="$1" out a b
  if [ -e "$dir/$GOLDEN_NAME" ]; then mv "$dir/$GOLDEN_NAME" "$WORK/golden.old"; fi
  if ! out="$(hashes "$dir")"; then
    if [ -e "$WORK/golden.old" ]; then mv "$WORK/golden.old" "$dir/$GOLDEN_NAME"; fi
    echo "update-cloudinit-golden: the render failed; $GOLDEN_NAME is unchanged" >&2
    return 1
  fi
  rm -f "$WORK/golden.old"
  a="${out% *}"
  b="${out#* }"
  printf '{\n  "golden_hetzner": "%s",\n  "golden_huawei": "%s"\n}\n' "$a" "$b" > "$dir/$GOLDEN_NAME"
  echo "golden_hetzner=$a"
  echo "golden_huawei=$b"
}

# run_test <directory>: runs the render test there; the output goes to $WORK/test.log.
run_test() {
  : > "$WORK/test.log"
  "$TOFU_BIN" -chdir="$1" init -input=false -no-color > "$WORK/init.log" 2>&1 || { cat "$WORK/init.log" >&2; return 1; }
  "$TOFU_BIN" -chdir="$1" test -no-color > "$WORK/test.log" 2>&1
}

self_test() {
  local tree="$WORK/tree" dir tpl failed=0
  dir="$tree/$TESTS_REL"
  tpl="$tree/infra/providers/_shared/cloudinit-control-plane.tftpl"

  # A fresh copy of what the render test reads: the shared template with the
  # test folder, and the folder of the two committed Kustomization files.
  fresh() {
    rm -rf "$tree"
    mkdir -p "$tree/infra/providers" "$tree/clusters/_template/infrastructure"
    cp -R "$ROOT/infra/providers/_shared" "$tree/infra/providers/_shared"
    cp -R "$ROOT/clusters/_template/infrastructure/providers" "$tree/clusters/_template/infrastructure/providers"
  }
  # expect <pass|fail> <text the output must hold> <name of the case>
  expect() {
    local want="$1" needle="$2" name="$3" got=pass
    run_test "$dir" || got=fail
    if [ "$got" = "$want" ] && grep -q -F -- "$needle" "$WORK/test.log"; then
      echo "ok    $name"
    else
      echo "FAIL  $name: the render test ended with '$got' (wanted '$want'), or its output does not hold: $needle"
      sed 's/^/      /' "$WORK/test.log" | tail -n 30
      failed=1
    fi
  }

  fresh
  expect pass 'run "golden"... pass' "the committed golden file matches the render"

  fresh
  sed -i '1a # golden self-test: a comment line' "$tpl"
  expect pass 'run "golden"... pass' "a new comment line in the template changes no hash"

  fresh
  printf 'golden_self_test: "an operative line"\n' >> "$tpl"
  expect fail 'the hetzner fixture renders to SHA-256' "a new operative line in the template fails the hetzner hash"
  expect fail 'the huawei fixture renders to SHA-256' "a new operative line in the template fails the huawei hash"

  fresh
  sed -i 's/"golden_hetzner": "[0-9a-f]*"/"golden_hetzner": "0000000000000000000000000000000000000000000000000000000000000000"/' "$dir/$GOLDEN_NAME"
  expect fail 'the hetzner fixture renders to SHA-256' "a wrong stored hetzner hash fails the run"

  fresh
  sed -i 's/"golden_huawei": "[0-9a-f]*"/"golden_huawei": "0000000000000000000000000000000000000000000000000000000000000000"/' "$dir/$GOLDEN_NAME"
  expect fail 'the huawei fixture renders to SHA-256' "a wrong stored huawei hash fails the run"

  fresh
  rm -f "$dir/$GOLDEN_NAME"
  expect fail 'run "golden"... fail' "a missing golden file fails the run"

  # The writer: after the template changed, writing the golden file makes the run pass again.
  fresh
  printf 'golden_self_test: "an operative line"\n' >> "$tpl"
  if write_golden "$dir" > /dev/null; then
    expect pass 'run "golden"... pass' "the written golden file matches the changed render"
  else
    echo "FAIL  the writer could not write the golden file of the changed render"
    failed=1
  fi

  # The writer replaces a golden file that is not valid JSON.
  fresh
  printf 'not JSON\n' > "$dir/$GOLDEN_NAME"
  if write_golden "$dir" > /dev/null && cmp -s "$dir/$GOLDEN_NAME" "$ROOT/$TESTS_REL/$GOLDEN_NAME"; then
    echo "ok    the writer replaces a golden file that is not valid JSON with the committed content"
  else
    echo "FAIL  the writer did not replace a golden file that is not valid JSON with the committed content"
    failed=1
  fi

  if [ "$failed" -ne 0 ]; then
    echo "self-test FAILED"
    return 1
  fi
  echo "self-test passed: 9 cases"
}

case "${1:-}" in
  "")
    write_golden "$ROOT/$TESTS_REL"
    echo "wrote $TESTS_REL/$GOLDEN_NAME"
    ;;
  --self-test)
    self_test
    ;;
  *)
    echo "usage: scripts/update-cloudinit-golden.sh [--self-test]" >&2
    exit 2
    ;;
esac
