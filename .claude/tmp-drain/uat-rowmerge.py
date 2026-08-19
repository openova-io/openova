#!/usr/bin/env python3
"""Row-wise resolver for docs/ledger/UAT.md rebase conflicts.

Policy (per drain brief): take the PR's line ONLY where main's cell is ☐
or main's evidence lacks `hw293`; otherwise keep main.  Never clobber a peer
stamp.  Rows are parsed with re.split(r'(?<!\\)\|', line) -> 9 fields, so a
row carrying an escaped pipe is not corrupted.
"""
import re, subprocess, sys

FIELD_RE = re.compile(r'(?<!\\)\|')

def parse(line):
    f = FIELD_RE.split(line)
    return f if len(f) == 9 else None

def index(text):
    d = {}
    for ln in text.split('\n'):
        if not ln.startswith('|'):
            continue
        f = parse(ln)
        if f:
            d[f[1].strip()] = (ln, f)
    return d

def evidence_blob(f):
    return ' '.join(f[2:8])

def pick(rid, main_row, pr_row, log):
    """Return the winning line for rid."""
    if main_row is None:
        log.append(f"  {rid}: PR-only row -> take PR")
        return pr_row[0]
    if pr_row is None:
        return main_row[0]
    if main_row[0] == pr_row[0]:
        return main_row[0]
    mblob = evidence_blob(main_row[1])
    # main's cell is unwalked (☐) anywhere in the row, or main carries no hw293 evidence
    main_unwalked = '☐' in mblob
    main_lacks_hw293 = 'hw293' not in mblob
    if main_unwalked or main_lacks_hw293:
        why = '☐' if main_unwalked else 'no hw293'
        log.append(f"  {rid}: main={why} -> take PR")
        return pr_row[0]
    log.append(f"  {rid}: main already stamped on hw293 -> KEEP MAIN (peer stamp preserved)")
    return main_row[0]

def merge(base_text, main_text, pr_text):
    mi, pi = index(main_text), index(pr_text)
    bi = index(base_text)
    log = []
    out = []
    for ln in main_text.split('\n'):
        f = parse(ln) if ln.startswith('|') else None
        if not f:
            out.append(ln)
            continue
        rid = f[1].strip()
        pr = pi.get(rid)
        base = bi.get(rid)
        # only consider the PR's version if the PR actually CHANGED it
        if pr and base and pr[0] == base[0]:
            out.append(ln)      # PR did not touch this row
            continue
        out.append(pick(rid, (ln, f), pr, log))
    # rows the PR added that main does not have
    for rid, pr in pi.items():
        if rid not in mi:
            log.append(f"  {rid}: present only in PR -> appended? (manual review)")
    return '\n'.join(out), log

if __name__ == '__main__':
    base_ref, main_ref, path = sys.argv[1], sys.argv[2], sys.argv[3]
    g = lambda r: subprocess.run(['git','show',f'{r}:{path}'],capture_output=True,text=True).stdout
    merged, log = merge(g(base_ref), g(main_ref), open(path+'.prside').read())
    open(path,'w').write(merged)
    print('\n'.join(log) if log else '  (no row-level divergence)')
