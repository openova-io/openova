# docrender — the BSS document renderer

**What it is.** A stateless HTTP service that turns a structured document —
an **invoice**, a **credit note**, a **statement** or a **quote** — into a
print-ready A4 PDF. It is what makes invoice download, quote documents and,
later, the human-readable rendition an e-invoice attachment carries, possible
without any of that living inside the billing application.

**Role in Catalyst.** A component of the BSS, shipped as the optional
`docrender` sub-chart of `bp-chargeback` (`docrender.enabled`). It is not a
Blueprint of its own and never appears in the catalog: it has no UI, no
external address and exactly one caller, the chargeback application.

**Canonical docs.** [`../DESIGN.md`](../DESIGN.md) §Documents,
[`../../../docs/ARCHITECTURE.md`](../../../docs/ARCHITECTURE.md),
[`../../../docs/SECURITY.md`](../../../docs/SECURITY.md).

---

## API

### `POST /v1/render` → `application/pdf`

```jsonc
{
  "template": "invoice",        // invoice | credit-note | statement | quote
  "locale":   "en",             // label catalog; empty = en
  "currency": "OMR",            // ISO 4217 — fixes the minor unit money is rendered at
  "document": {
    "number":       "INV-2026-00042",
    "status":       "overdue",  // draft|issued|sent|paid|overdue|cancelled|pro_forma
    "issued_at":    "2026-09-01",
    "due_at":       "2026-10-01",
    "period_start": "2026-08-01",
    "period_end":   "2026-08-31",
    "seller":     { "name": "…", "address": "…\n…", "tax_registration": "…",
                    "logo_data_uri": "data:image/png;base64,…" },
    "buyer":      { "name": "…", "tax_registration": "…", "email": "…" },
    "references": { "po": "…", "invoice_number": "…", "statement_id": "…", "external": "…" },
    "lines": [
      { "sku": "k8s.vcpu", "description": "…", "unit": "vcpu-hour",
        "quantity": "2976.000000", "unit_price": "0.012000", "amount": "35.712000" }
    ],
    "waterfall": {
      "list_subtotal": "97.080000",   // optional — derived from net + discounts when absent
      "discount_total": "9.708000",
      "net_subtotal":  "87.372000",
      "tax_rate":      "0.05",
      "tax":           "4.368600",
      "total":         "91.740600",
      "paid": "50.000000", "credited": "…", "balance": "41.740600",
      "applied": "…", "unapplied": "…"   // credit notes
    },
    "discounts": [{ "label": "Launch campaign", "amount": "9.708000" }],
    "tax":       { "exempt": false, "exempt_reason": "…", "discount_rule": "best-single" },
    "payments":  [{ "paid_at": "2026-09-05", "method": "transfer",
                    "reference": "TRF-88213", "amount": "50.000000" }],
    "terms":     { "payment_terms_days": 30, "text": "…" },
    "notes": "…"
  }
}
```

Ask for `Accept: text/html` (or `?format=html`) and the same document comes
back as the HTML rendition instead — the template the console can show
inline.

**Every amount is a decimal STRING.** `"91.740600"`, never `91.7406` and
never a float. The renderer parses with `math/big` and rounds only once, at
the last kept digit, half away from zero. A value that has been through a
float, a locale formatter or a spreadsheet (`"1,234.56"`, `"1.2e3"`) is
**refused with 400**, because a wrong total on an invoice is worse than an
error.

**The minor unit follows the currency**, not a constant: three decimals for
**OMR, BHD, KWD, JOD, IQD, LYD, TND**, two for everything else. The same
document in OMR reads `91.741` and in USD `91.74`.

Responses:

| Status | When |
|---|---|
| `200` | the document, `application/pdf` (or `text/html`) |
| `400` | the document is not renderable — the body lists **every** problem by JSON path |
| `401` | `X-Render-Token` missing or wrong (only when a token is configured) |
| `405` | a method other than POST on `/v1/render` |
| `413` | the request body is past the cap (2 MiB by default) |
| `415` | the body is not `application/json` |
| `504` | the render outran its deadline (10 s by default) |

### `GET /healthz` · `GET /readyz` · `GET /metrics`

`/healthz` is liveness and reports the version, the templates and the locales
it can serve. **`/readyz` is a proven capability, not a restatement of
liveness**: at start-up the binary renders its sample invoice through the
real path, and answers `/readyz` only if that worked. An image whose fonts,
templates or locales are broken fails to start rather than serving 500s.

`/metrics` is Prometheus text exposition:

| Metric | Type | Labels |
|---|---|---|
| `docrender_renders_total` | counter | `template`, `format` |
| `docrender_render_duration_seconds` | histogram | `template`, `format` |
| `docrender_render_bytes` | histogram | `template`, `format` |
| `docrender_render_errors_total` | counter | `reason` (`invalid`/`locale`/`render`/`timeout`) |
| `docrender_http_requests_total` | counter | `path`, `status` |

---

## Templates and locales

`templates/_layout.html` is the shared layout — masthead and status stamp,
the two party blocks, the metadata band, the charges table, the waterfall,
the payments, the terms — and `templates/<kind>.html` is one content file per
document kind. The seller block (legal name, address, tax registration) comes
from the document, and the logo is an **optional data URI in the request**:
the renderer never fetches an image, which is why it can deny all egress.

**No template carries a literal label.** Every one goes through `t` / `tf`,
which read `locales/<lang>.json`. English ships; a second language is a
second file plus, for a non-Latin script, a TrueType font (`config.fontFile`)
— the core PDF fonts are cp1252-only, and a rune they cannot carry is
transliterated or replaced, never written raw.

The PDF is **not** rendered from that HTML. It is drawn directly from the
document JSON with [`github.com/go-pdf/fpdf`](https://github.com/go-pdf/fpdf)
— a pure-Go library with real font metrics — so columns, wrapped cells and
page breaks are **measured**, the output carries a genuine text layer, and
there is no headless browser anywhere in the image.

Look at what it produces:

```bash
make preview      # writes every fixture's PDF to bin/preview
```

---

## Security posture

The renderer is **in-cluster only**, by construction rather than by
convention:

- **No external door.** ClusterIP Service, no HTTPRoute, no Ingress, and the
  chart *refuses to render* any `service.type` but ClusterIP. **Never a
  NodePort** (§854).
- **A default-deny NetworkPolicy.** Ingress is the chargeback pods on the
  service port and nothing else; egress is kube-dns on 53 and nothing else.
- **No outbound call of any kind.** No database, no PVC, no remote font, no
  remote logo. A document arrives in the request body and leaves in the
  response body.
- **No Kubernetes access.** A zero-RBAC ServiceAccount with
  `automountServiceAccountToken: false`.
- **Non-root, read-only rootfs**, numeric uid/gid 65532, all capabilities
  dropped, `RuntimeDefault` seccomp, on distroless static with no shell.
- **`X-Render-Token`** — an optional shared secret from a Secret
  (`auth.existingSecret`), compared in constant time. It is defence in depth
  *on top of* the NetworkPolicy, never the perimeter by itself.
- **Bounded work**: a 2 MiB request cap and a 10 s render deadline, both
  enforced by the server.

`chart/tests/kyverno-policies.sh` applies the platform's own Kyverno baseline
(at its canonical Enforce actions) to the rendered manifests and requires
zero failures — and proves it can go red by seeding five defects.

---

## Configuration

| Env | Default | What |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | listen address |
| `RENDER_TIMEOUT` | `10s` | one render's deadline |
| `MAX_BODY_BYTES` | `2097152` | request-body cap |
| `RENDER_TOKEN` | *(empty)* | required `X-Render-Token`; empty = not required |
| `FONT_FILE` | *(empty)* | TrueType font to embed instead of the core font |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

Chart values are in [`chart/values.yaml`](chart/values.yaml). The BSS side is
`docrender.enabled` on `bp-chargeback`, which renders this chart **and** sets
the application's `DOCRENDER_URL` to its Service.

---

## Development

```bash
make test       # go vet + go test -count=1 ./...
make preview    # render every fixture to bin/preview and look at them
make run        # serve on :8080
make image      # build the container image

bash chart/tests/render-contract.sh    # chart contract, incl. URL/Service agreement
bash chart/tests/kyverno-policies.sh   # the platform's admission baseline
```

`testdata/bss-invoice.json` is the **contract between this module and the
BSS**: `products/chargeback/internal/docs` writes it from its own mapper and
asserts it byte for byte, and this module's tests render it. The two are
separate Go modules and cannot import each other, so a field renamed on
either side fails one of the two suites instead of surfacing as a 400 on a
customer's invoice download months later.
