#!/bin/sh
# check-kube-access-rendered.sh <rendered manifest>
# A chart-test helper (platform/kube-access-lib/README.md). In a `helm template`
# render, every document that is a Helm test hook and runs kubectl must export the
# kubeconfig of kube-access-lib.in-cluster-kubeconfig before its first other kubectl
# call (kubectl with a client flag and no kubeconfig talks to localhost:8080).
# Exit 1 names each offending document; exit 2 when the file cannot be read.
set -eu
[ -r "${1:-}" ] || { echo "check-kube-access-rendered: cannot read '${1:-}'" >&2; exit 2; }
awk '
  function judge() { if (hook && kube && !ok) { print "kube-access: " name ": a test hook runs kubectl before the kube-access-lib set-up"; bad = 1 } }
  /^---/ { judge(); hook = 0; kube = 0; ok = 0; seen = 0; name = ""; next }
  /^  name: / && name == "" { name = $2 }
  /helm\.sh\/hook"?: *"?test/ { hook = 1 }
  /^ *# kube-access-lib\.in-cluster-kubeconfig:/ { seen = 1 }
  seen && !kube && /export KUBECONFIG=\/tmp\/kube-access-kubeconfig$/ { ok = 1 }
  /^ *#/ { next }
  /(^|[^A-Za-z0-9_.\/-])kubectl( |$)/ && !/kubectl config / { kube = 1 }
  END { judge(); exit bad }
' "$1"
