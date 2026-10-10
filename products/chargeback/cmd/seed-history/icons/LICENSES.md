# Showcase icons — sources and licences

The SVGs in this directory are seed data for the showcase package ladder
(DESIGN.md §22.10). `cmd/seed-history` uploads them to BSS through
`POST /api/v1/icons`; `scripts/apply-package-icons.py` does the same against
an existing Sovereign. `manifest.json` maps each feature, floor item, group
and package to its icon and colours.

Every file was fetched unmodified from the URL named below, then changed in
one way only:

- **Lucide** icons draw with `stroke="currentColor"` (one, `key-round`, also
  has `fill="currentColor"`), which renders black inside an `<img>` because
  an image has no text colour to inherit. The vendored copies write
  `#475569` (slate-600) in its place — a mid-tone that reads on the
  storefront's light and dark themes alike, where a near-black stroke
  vanishes on a dark page. Every feature and floor item is also seeded with
  a light tile (`bg`) behind its icon.
- **Simple Icons** paths have no fill, which also renders black. The
  vendored copies add the brand's colour from the Simple Icons data file as
  `fill` on the `<svg>` root.

Each file passes `store.ValidateIcon` (no script, no foreignObject, no event
handler, no external link, no DTD), which
`TestShowcaseIconsPassTheValidator` holds.

## Simple Icons — CC0 1.0 Universal

<https://github.com/simple-icons/simple-icons> — "Simple Icons is licensed
under CC0 1.0 Universal". The logos remain trademarks of their owners and are
shown here only to name the product a feature is built on.

| File | Source | Brand colour |
|---|---|---|
| `gitea.svg` | https://raw.githubusercontent.com/simple-icons/simple-icons/develop/icons/gitea.svg | `#609926` |
| `kubernetes.svg` | https://raw.githubusercontent.com/simple-icons/simple-icons/develop/icons/kubernetes.svg | `#326CE5` |

Not vendored, on purpose: `letsencrypt` (Simple Icons lists it under
CC-BY-NC-4.0, not CC0 — non-commercial only) and `keycloak` (listed under the
Linux Foundation's custom trademark-usage licence, not CC0). The SSL floor
item uses Lucide `lock` and SSO uses Lucide `key-round` instead.

## Lucide — ISC (some icons MIT, from Feather)

<https://github.com/lucide-icons/lucide>, each from
`https://raw.githubusercontent.com/lucide-icons/lucide/main/icons/<name>.svg`:

`activity`, `archive`, `bug`, `cpu`, `database`, `database-backup`, `gauge`,
`globe`, `hard-drive`, `key-round`, `layout-dashboard`, `layout-grid`,
`life-buoy`, `lock`, `mail`, `network`, `puzzle`, `refresh-cw`, `scale`,
`search`, `server`, `settings`, `shield`, `shield-alert`, `shield-check`,
`sparkles`, `terminal`, `trending-up`, `users`, `wrench`.

```
ISC License

Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```

Of the icons above, `database`, `life-buoy`, `lock`, `search`, `server` and
`terminal` are derived from the Feather project and are also under its
licence:

```
The MIT License (MIT)

Copyright (c) 2013-present Cole Bemis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
