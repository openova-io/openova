#!/usr/bin/env python3
"""Resolve products/catalyst/chart/Chart.yaml + slot-pin conflicts by keeping
BOTH changelog sides (main's newer entries then the PR's entry) and taking
main's version line as the baseline for the canonical writer to re-bump."""
import sys

def split_conflicts(path):
    lines = open(path).read().split('\n')
    out, i, blocks = [], 0, 0
    res = []
    while i < len(lines):
        if lines[i].startswith('<<<<<<<'):
            j = i+1; head = []
            while not lines[j].startswith('======='):
                head.append(lines[j]); j += 1
            j += 1; theirs = []
            while not lines[j].startswith('>>>>>>>'):
                theirs.append(lines[j]); j += 1
            res.append((len(out), head, theirs))
            out.append(None)   # placeholder
            blocks += 1
            i = j+1
        else:
            out.append(lines[i]); i += 1
    return out, res

def write(path, out, filled):
    final = []
    k = 0
    for x in out:
        if x is None:
            final.extend(filled[k]); k += 1
        else:
            final.append(x)
    open(path, 'w').write('\n'.join(final))

def is_ver(l):
    return l.strip().startswith('version:') or l.strip().startswith('appVersion:')

for path in sys.argv[1:]:
    out, res = split_conflicts(path)
    filled = []
    for _, head, theirs in res:
        head_ver   = [l for l in head   if is_ver(l)]
        their_ver  = [l for l in theirs if is_ver(l)]
        head_cmt   = [l for l in head   if not is_ver(l)]
        their_cmt  = [l for l in theirs if not is_ver(l)]
        if head_ver and their_ver:
            # changelog + version-line conflict: keep BOTH changelogs, main's version
            filled.append(head_cmt + their_cmt + head_ver)
        elif head_ver or their_ver:
            filled.append(head_ver or their_ver)
        else:
            filled.append(head_cmt + their_cmt)
    write(path, out, filled)
    print(f"{path}: resolved {len(res)} conflict block(s)")
