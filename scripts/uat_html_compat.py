#!/usr/bin/env python3
"""Compatibility shim: let the UAT guards read an HTML-<table> ledger.

UAT.md renders as an HTML <table> so cells WRAP and each row carries its
screenshot thumbnail in the last column (GitHub markdown tables cannot do
either). The integrity guards were written for the `| a | b |` markdown form.
Rather than rewrite six guards, they route the file text through `to_pipe()`
here, which turns each `<tr>…</tr>` back into the exact markdown pipe row the
guards already understand — so their semantics and self-tests are unchanged.

The transform is the precise inverse of the generator (scripts/uat_to_html.py):
after it runs, an identity clause equals the canon byte-for-byte again.
"""
import html
import re

_TR = re.compile(r"^\s*<tr\b", re.I)
_CELL = re.compile(r"<t[dh]\b[^>]*>(.*?)</t[dh]>", re.I | re.S)
_THUMB = re.compile(r'<a\s+href="([^"]+\.png)"\s*>\s*<img[^>]*>\s*</a>', re.I)
_IMG = re.compile(r'<img\s+src="([^"]+\.png)"[^>]*>', re.I)
_LINK = re.compile(r'<a\s+href="([^"]+)"\s*>(.*?)</a>', re.I | re.S)
_CODE = re.compile(r"<code>(.*?)</code>", re.I | re.S)
_STRONG = re.compile(r"<(?:strong|b)>(.*?)</(?:strong|b)>", re.I | re.S)
_EM = re.compile(r"<(?:em|i)>(.*?)</(?:em|i)>", re.I | re.S)
_TAG = re.compile(r"<[^>]+>")


def _cell_to_md(cell: str) -> str:
    """One <td> inner HTML back to its markdown cell text."""
    cell = _THUMB.sub(r"[📷](\1)", cell)          # thumbnail anchor -> 📷 link
    cell = _IMG.sub(r"[📷](\1)", cell)            # bare img -> 📷 link
    cell = _LINK.sub(r"[\2](\1)", cell)           # <a href=u>t</a> -> [t](u)
    cell = _CODE.sub(r"`\1`", cell)               # <code>x</code> -> `x`
    cell = _STRONG.sub(r"**\1**", cell)           # <strong> -> **
    cell = _EM.sub(r"*\1*", cell)                 # <em> -> *
    cell = _TAG.sub("", cell)                     # drop any stray tags (e.g. <br>)
    cell = html.unescape(cell)                    # &lt; &amp; … -> < & …
    cell = re.sub(r"\s+", " ", cell).strip()      # collapse the pretty-print whitespace
    return cell.replace("|", "\\|")               # a literal pipe must stay escaped


# ── 4-column compact layout ─────────────────────────────────────────────────
# To keep the Evidence column (with its screenshot) on-screen without a
# horizontal scroll, the ledger folds the seven logical fields into FOUR visible
# columns:
#   A  <strong>ID</strong><br><sub>EPIC · <a>#N</a> …</sub>
#   B  the test-case clause
#   C  VERDICT<br><sub>ENV</sub>
#   D  the evidence + screenshot thumbnail
# _fold_to_pipe reconstructs the exact seven-field markdown row the guards read,
# so clause/ticket/epic identity is preserved byte-for-byte.
import html as _htmlmod  # noqa: E402

_A_ID = re.compile(r"<strong>(.*?)</strong>", re.I | re.S)
_A_SUB = re.compile(r"<sub>(.*?)</sub>", re.I | re.S)
_A_LINK = re.compile(r'<a\s+href="([^"]+)"\s*>(.*?)</a>', re.I | re.S)
_A_TAGS = re.compile(r"<[^>]+>")


def _plain(s: str) -> str:
    """Inner HTML -> markdown TEXT (code/emphasis kept, other tags dropped)."""
    s = _CODE.sub(r"`\1`", s)
    s = _STRONG.sub(r"**\1**", s)
    s = _EM.sub(r"*\1*", s)
    s = _A_TAGS.sub("", s)
    s = _htmlmod.unescape(s)
    return re.sub(r"\s+", " ", s).strip()


def _fold_to_pipe(cells):
    A, B, C, D = cells
    m = _A_ID.search(A)
    row_id = _plain(m.group(1)) if m else _plain(A)
    tickets = " ".join(
        "[%s](%s)" % (_plain(lbl), url) for url, lbl in _A_LINK.findall(A)
    )
    sm = _A_SUB.search(A)
    epic = _plain(_A_LINK.sub("", sm.group(1))).strip(" ·").strip() if sm else ""
    clause = _cell_to_md(B)                              # exact canon reconstruction
    cm = _A_SUB.search(C)
    env = _plain(cm.group(1)) if cm else ""
    verdict = _plain(_A_SUB.sub("", C))
    evidence = _cell_to_md(D)                            # keeps the 📷 png ref
    return "| %s | %s | %s | %s | %s | %s | %s |" % (
        row_id, epic, tickets, clause, env, verdict, evidence
    )


def to_pipe(text: str) -> str:
    """Return `text` with every HTML <tr> row rewritten as a markdown pipe row.

    A 7-cell row maps positionally (legacy layout); a 4-cell row is unfolded to
    the same seven fields. Non-<tr> lines (headings, the <table> wrapper, gallery
    <img> lines, and any line that is already a markdown pipe row) pass through
    untouched, so the function is a no-op on the legacy markdown ledger and safe
    for the guards' string-built self-tests.
    """
    out = []
    for line in text.split("\n"):
        if _TR.match(line):
            cells = _CELL.findall(line)
            if len(cells) == 4:
                line = _fold_to_pipe(cells)
            elif cells:
                line = "| " + " | ".join(_cell_to_md(c) for c in cells) + " |"
        out.append(line)
    return "\n".join(out)


if __name__ == "__main__":
    import sys
    sys.stdout.write(to_pipe(sys.stdin.read()))
