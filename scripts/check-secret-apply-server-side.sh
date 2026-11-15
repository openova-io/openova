#!/usr/bin/env bash
# check-secret-apply-server-side.sh - secret guard G13 (#7901).
#
# No client-side 'kubectl apply' of a Secret in what runs on a cluster or on
# a node: the YAML, template and shell files under platform/, products/,
# clusters/, infra/ and core/, without their tests/ and testdata/ folders.
#
# Client-side apply stores the manifest it applied in the annotation
# kubectl.kubernetes.io/last-applied-configuration. For a Secret that is
# every value, readable by 'kubectl describe secret' and by anything that
# lists metadata. 'kubectl apply --server-side' writes no such annotation;
# neither do 'kubectl create' and 'kubectl patch'.
#
# The rule reads each 'kubectl apply' without --server-side (and without
# --dry-run) and looks at what it applies: what the same statement pipes
# into it, the statement, loop, block or here-document that writes the -f
# file in the same source file, a cloud-init write_files entry or a
# '<name>: |' block of that name, or the statement that assigns a variable
# piped in. A finding is:
#   - an input that is a Secret: 'kubectl create secret' or 'kubectl get
#     secret' piped in, a manifest with 'kind: Secret' and data or
#     stringData, or a file or variable whose name says "secret";
#   - an input the rule cannot read at all. "Not found" is not "not a
#     Secret": apply it server-side, write the manifest where the rule can
#     read it, or add the file to APPLY_INPUT_ELSEWHERE in
#     scripts/secret-guards.py with what it is.
# A Secret manifest without data and stringData (a ServiceAccount token
# request) holds no value and is not counted. This guard has no allow-list:
# any finding fails.
#
# The form to use, also for a Secret that an older client-side apply already
# wrote (the other field manager then no longer refreshes the annotation):
#   kubectl apply --server-side --force-conflicts --field-manager=<writer> -f ...
#
# Usage, from any folder:
#   bash scripts/check-secret-apply-server-side.sh                 scan the tree
#   bash scripts/check-secret-apply-server-side.sh --self-test     prove that each rule can fail
#   bash scripts/check-secret-apply-server-side.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-secret-apply-server-side.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" secret-apply-server-side "$@"
