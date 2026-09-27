# Catalyst BSS — training course

`Catalyst-BSS-Training.pdf` is the twelve-module course (142 slides): the concept,
a worked example with numbers, the console screen with click-by-click callouts
and a self-check, per module, in journey order — leads, customers and sources,
metering, price books and rating, contracts, cost analysis, billing, finance,
partners, capacity, access and notifications.

## Rebuilding it

    pip install python-pptx pillow            # once
    node shoot.js                             # screenshots of a signed-in console → shots/
    python3 build.py Catalyst-BSS-Training.pptx
    soffice --headless --convert-to pdf Catalyst-BSS-Training.pptx

`shoot.js` expects `cookies.json` beside it: the `catalyst_session` and
`_oidc_gate_chargeback` cookies of a signed-in operator session (export them
from the browser; never commit them) and the entity ids at the top of the file
for the Sovereign you are shooting. `decklib.py` holds the slide kinds, `content_a.py`
modules 0–5, `content_b.py` modules 6–12. Every slide names its page path, so a
reader can open the same screen while reading. Figures are illustrative unless a
slide says a test pins them; those come from `DESIGN.md`.
