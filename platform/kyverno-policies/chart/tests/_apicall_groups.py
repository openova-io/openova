#!/usr/bin/env python3
"""Print every apiGroup granted by a ClusterRole in the rendered chart, one per line.

Helper for apicall-rbac-correspondence.sh (#6832). Kept as a file rather than an
inline heredoc: the first version nested a heredoc inside a command substitution
that also used a here-string, so python read an empty stdin and the guard
reported every apiGroup as ungranted — a check that fails on everything is as
useless as one that passes on everything.
"""
import sys
import yaml

groups = set()
for doc in yaml.safe_load_all(open(sys.argv[1])):
    if not doc or doc.get("kind") != "ClusterRole":
        continue
    for rule in doc.get("rules") or []:
        for group in rule.get("apiGroups") or []:
            groups.add(group)
print("\n".join(sorted(groups)))
