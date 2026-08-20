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


def to_pipe(text: str) -> str:
    """Return `text` with every HTML <tr> row rewritten as a markdown pipe row.

    Non-<tr> lines (headings, the <table> wrapper, gallery <img> lines, and any
    line that is already a markdown pipe row) pass through untouched, so the
    function is a no-op on the legacy markdown ledger and safe for the guards'
    string-built self-tests.
    """
    out = []
    for line in text.split("\n"):
        if _TR.match(line):
            cells = _CELL.findall(line)
            if cells:
                line = "| " + " | ".join(_cell_to_md(c) for c in cells) + " |"
        out.append(line)
    return "\n".join(out)


if __name__ == "__main__":
    import sys
    sys.stdout.write(to_pipe(sys.stdin.read()))
