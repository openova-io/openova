#!/usr/bin/env python3
"""Render docs/ledger/UAT.md as an HTML <table> so GitHub WRAPS every cell and
each row shows its screenshot as a clickable thumbnail in the last column.

Markdown tables on GitHub cannot wrap a wide column or hold an in-cell image
that enlarges on click — so the ledger table is authored as raw HTML, which
GitHub renders fit-to-width with wrapping cells. The frozen columns
(Epic/Ticket/Test-case) keep their exact text; scripts/uat_html_compat.py maps
the HTML rows back to the markdown the identity/drift/shape/partition guards
read, so nothing about verification changes.

Idempotent: run on the markdown ledger to convert it; run again (on the HTML
ledger) and it re-emits the same HTML.
"""
import html
import re
import sys
from pathlib import Path

UAT = Path(__file__).resolve().parent.parent / "docs" / "ledger" / "UAT.md"
from uat_html_compat import to_pipe  # noqa: E402

ROW = re.compile(r"^\|\s*(R?\d+|[GWM]\d+)\s*\|")
UNESC = re.compile(r"(?<!\\)\|")
LINK = re.compile(r"\[([^\]]+)\]\(([^)]+)\)")
THUMB_MD = re.compile(r"\[<img[^\]]*\]\((screenshots/[^)]+)\)")   # inline thumb md
CAM_MD = re.compile(r"\[📷\]\((screenshots/[^)]+)\)")             # 📷 link md
# Four VISIBLE columns so the Evidence column (with its screenshot) never gets
# pushed off the right edge; the id/epic/issue metadata folds into column 1 and
# env folds under the verdict in column 3.
COLS = ["#", "Test case", "Result", "Evidence"]


def esc(s: str) -> str:
    return html.escape(s, quote=True)


def cell_html(text: str, is_evidence: bool) -> str:
    """Markdown cell text -> HTML, screenshots become clickable thumbnails."""
    thumbs = THUMB_MD.findall(text) + CAM_MD.findall(text)
    text = THUMB_MD.sub("", text)
    text = CAM_MD.sub("", text)
    # pull markdown links out before escaping, re-insert as <a> after
    links = []

    def _stash(m):
        links.append((m.group(1), m.group(2)))
        return f"\x00{len(links)-1}\x00"

    text = LINK.sub(_stash, text)
    text = esc(text.strip())
    # inline emphasis / code (order matters: code first)
    text = re.sub(r"`([^`]+)`", lambda m: f"<code>{m.group(1)}</code>", text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)

    def _link(m):
        i = int(m.group(1))
        label, url = links[i]
        return f'<a href="{esc(url)}">{esc(label)}</a>'

    text = re.sub(r"\x00(\d+)\x00", _link, text)
    for p in thumbs:
        text += (f' <a href="{esc(p)}" title="click to enlarge">'
                 f'<img src="{esc(p)}" width="150"></a>')
    return text.strip()


def main() -> int:
    md = to_pipe(UAT.read_text())          # accept either form as input
    lines = md.split("\n")
    pre, rows, gallery, in_table, seen_gallery = [], [], [], False, False
    for line in lines:
        if line.startswith("|") and re.match(r"^\|\s*#\s*\|", line):
            in_table = True
            continue
        if re.match(r"^\|[\s:|-]+\|\s*$", line):    # md separator row
            continue
        # Drop any table-wrapper line from a PRIOR HTML render so it is not
        # re-emitted as a stray/empty table above the freshly built one.
        if re.match(r"^\s*</?(table|thead|tbody)\b", line):
            in_table = True
            continue
        if ROW.match(line):
            c = [x for x in UNESC.split(line)][1:8]
            c = (c + [""] * 7)[:7]
            rid, epic, ticket, clause, env, verdict, evidence = (x.strip() for x in c)
            # ticket markdown -> inline <a> links (keep the exact issue URLs)
            issue = LINK.sub(lambda m: f'<a href="{esc(m.group(2))}">{esc(m.group(1))}</a>',
                             esc(ticket)) if "](" not in ticket else \
                    " ".join(f'<a href="{u}">{esc(t)}</a>'
                             for t, u in LINK.findall(ticket))
            col_a = (f'<strong>{esc(rid)}</strong><br>'
                     f'<sub>{esc(epic)}'
                     + (f' · {issue}' if issue else "") + '</sub>')
            col_c = f'{esc(verdict)}<br><sub>{esc(env)}</sub>'
            tds = (f"<td>{col_a}</td>"
                   f"<td>{cell_html(clause, False)}</td>"
                   f"<td>{col_c}</td>"
                   f"<td>{cell_html(evidence, True)}</td>")
            rows.append(f'<tr id="row-{rid}">{tds}</tr>')
            continue
        if line.strip().startswith("## Screenshot evidence"):
            seen_gallery = True
        if seen_gallery:
            gallery.append(line)
        elif not in_table:
            pre.append(line)

    # Explicit per-column widths so the wide Test-case column cannot squeeze the
    # Evidence/screenshot column off the right edge. GitHub honours width on
    # <th>/<td> (verified against its render API); width on the header row sets
    # the whole column.
    widths = ["6%", "44%", "10%", "40%"]
    head = ("<thead><tr>"
            + "".join(f'<th width="{w}">{h}</th>' for h, w in zip(COLS, widths))
            + "</tr></thead>")
    table = ('<table width="100%">\n' + head + "\n<tbody>\n"
             + "\n".join(rows) + "\n</tbody>\n</table>")
    out = "\n".join(pre).rstrip() + "\n\n" + table + "\n"
    if gallery:
        out += "\n" + "\n".join(gallery).rstrip() + "\n"

    if "--apply" in sys.argv:
        UAT.write_text(out)
        print(f"APPLIED: {len(rows)} rows rendered as an HTML table; "
              f"gallery lines kept: {len(gallery)}")
    else:
        print(f"DRY: would render {len(rows)} rows as HTML table.")
        print(rows[0][:300])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
