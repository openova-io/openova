#!/usr/bin/env bash
# check-no-secret-values-in-git.sh - secret guard G1 (SEC-090).
#
# No Secret manifest under clusters/: a line 'kind: Secret' in a tracked file
# under clusters/ is a Secret value committed to Git. The allow-list holds
# today's files and may only shrink.
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
#   bash scripts/check-no-secret-values-in-git.sh --self-test     prove that each rule can fail
#   bash scripts/check-no-secret-values-in-git.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-secret-values-in-git.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" no-secret-values-in-git "$@"
