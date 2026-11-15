#!/usr/bin/env python3
"""test-sso-probe-hubble-row.py - the hubble row of the sign-in probe holds on the page hubble-ui shows.

Gate check FND-G07 (scripts/sso-zero-click-probe.mjs) opens https://hubble.<sovereign>/ as a
signed-in user and requires every positive marker of the row. Its marker used to be the body
text regex `Hubble|Service Map|Namespace`, which matches nothing the bare URL of hubble-ui
0.13.3 shows, so the row failed for a user who was signed in (run of 15 November 2026: the
oauth2-proxy log shows that user's requests answered 200, the probe reported "missing positive
marker").

The fixture below is the page of hubble-ui 0.13.3 (quay.io/cilium/hubble-ui:v0.13.3) at its bare
URL, read from its own files: the title of /app/index.html and the text of its welcome screen in
/app/bundle.main.*.js. The test reads the row out of the probe and evaluates its positive markers
the way evalMarker does (text: substring of the body; text-regex and title-regex: a search; url-*:
on the final URL), with Python's re for the JavaScript patterns these rows use.

Exit 0: every positive marker of the row holds on the fixture, and none of its login markers
does. Exit 1: one fails. Exit 2: the row cannot be read.
"""
import os
import re
import sys

PROBE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "sso-zero-click-probe.mjs")

FIXTURE = {
    "url": "https://hubble.sov.test/",
    "title": "Hubble UI",
    "body": "Welcome!To begin select one of the namespaces:Choose namespace",
}


def row(text, name):
    m = re.search(r"\{\s*(?://[^\n]*\n\s*)*name: '" + re.escape(name) + r"',.*?\n  \},", text, re.S)
    if not m:
        raise LookupError("row %s not found in %s" % (name, PROBE))
    return m.group(0)


def markers(block, field):
    m = re.search(field + r":\s*\[(.*?)\]\s*,\s*\n", block, re.S)
    if not m:
        return []
    return re.findall(r"\{\s*kind:\s*'([^']+)',\s*v:\s*'((?:[^'\\]|\\.)*)'\s*\}", m.group(1))


def holds(kind, v, page):
    v = v.encode().decode("unicode_escape")
    if kind == "text":
        return v in page["body"]
    if kind == "text-regex":
        return re.search(v, page["body"]) is not None
    if kind == "title-regex":
        return re.search(v, page["title"]) is not None
    if kind == "url-includes":
        return v in page["url"]
    if kind == "url-regex":
        return re.search(v, page["url"]) is not None
    raise ValueError("marker kind %s is not evaluated by this test" % kind)


def main():
    try:
        with open(PROBE, encoding="utf-8") as f:
            block = row(f.read(), "hubble")
        positive, login = markers(block, "positive"), markers(block, "loginMarkers")
        if not positive:
            raise LookupError("the hubble row has no positive marker")
        bad = ["missing positive marker %s:%s" % (k, v) for k, v in positive if not holds(k, v, FIXTURE)]
        bad += ["login marker %s:%s holds on the signed-in page" % (k, v) for k, v in login if holds(k, v, FIXTURE)]
    except (OSError, LookupError, ValueError) as e:
        print("test-sso-probe-hubble-row: cannot judge: %s" % e)
        return 2
    for line in bad:
        print("test-sso-probe-hubble-row: FAIL %s" % line)
    if bad:
        return 1
    print("test-sso-probe-hubble-row: ok, %d positive marker(s) hold on the hubble-ui 0.13.3 page" % len(positive))
    return 0


if __name__ == "__main__":
    sys.exit(main())
