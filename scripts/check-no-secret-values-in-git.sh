#!/usr/bin/env bash
# check-no-secret-values-in-git.sh - secret guard G1 (SEC-090, SEC-153).
#
# No Secret value under clusters/: a line 'kind: Secret' in a tracked file
# under clusters/ is a Secret value committed to Git. The allow-list holds
# today's files and may only shrink.
#
# A template of references is not a value (SEC-153): a file whose every Secret
# is a manifest that Flux renders from a Secret of the cluster
# (postBuild.substituteFrom) is not counted. Each entry under data and
# stringData of such a manifest is exactly one reference ${NAME} (capitals,
# digits and underscores; no literal, no text around it, no default, no empty
# value), it has at least one entry, and no field besides apiVersion, kind,
# metadata, type, immutable, data and stringData. The Go command
# tools/guards/cmd/secret-templates names those files; this script hands its
# list to the rules in scripts/secret-guards.py (variable G1_TEMPLATES), which
# leave them out. Every other Secret manifest under clusters/ stays a finding.
#
# Secret VALUES anywhere in the tree (keys, tokens) are found by the workflow
# security-scan, which runs Trivy's secret scanner over the whole tree and
# fails on a new finding. This guard covers the manifests a value scanner
# cannot judge.
#
# Allow-list: scripts/secret-guard-baselines/check-no-secret-values-in-git.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-no-secret-values-in-git.sh                 scan the tree
#   bash scripts/check-no-secret-values-in-git.sh --self-test     prove that each rule can fail: the tests
#                                                                 of secret-templates, then those of the rules
#   bash scripts/check-no-secret-values-in-git.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-secret-values-in-git.sh --list <path>   print 'path:count' per file under <path>
#
# Needs the Go of tools/guards. Exit codes: 0 pass, 1 finding (or failed
# self-test), 2 usage or setup error (also when secret-templates cannot read).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
if [ "${1:-}" = --self-test ]; then
  (cd "$here/../tools/guards" && go test -count=1 ./cmd/secret-templates) || exit 1
  G1_TEMPLATES='' exec python3 "$here/secret-guards.py" no-secret-values-in-git "$@"
fi
G1_TEMPLATES="$(cd "$here/../tools/guards" && go run ./cmd/secret-templates)" || exit 2
export G1_TEMPLATES
exec python3 "$here/secret-guards.py" no-secret-values-in-git "$@"
