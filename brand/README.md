# OpenOva Brand Kit

**Status**: 1.0 (Wave 5.70 #2371) · **License**: CC-BY-SA-4.0 · **Owner**: openova-io

Public-facing brand assets for third-party Blueprints, partner surfaces, and "powered by OpenOva" lockups. Filed in response to chepherd v0.5 lift (#2316) — chepherd is the first third-party consumer.

## What's in this directory

| File | Use case |
|---|---|
| `openova-mark.svg` | Icon-only mark (the canonical "OO" interlocking arcs). Favicons, app icons, sidebar marks, lockup icon-position. 700×400 viewBox, gradient blue→indigo. |
| `powered-by-openova.svg` | "Powered by OpenOva" horizontal lockup. Drop-in SVG combining the mark + wordmark. 480×80 viewBox. Use on third-party UIs that consume OpenOva infrastructure (chepherd pairing screens, partner cards, OEM surfaces). |
| `powered-by-openova-stacked.svg` | Stacked vertical variant of the lockup. 240×200 viewBox. Use in square-ish placements: footer badges, splash cards, sticker exports, app-card chrome. |

## License

Both files are **CC-BY-SA-4.0**. You may:

- Copy + redistribute in any medium / format
- Remix, transform, build upon
- Use commercially

Under these terms:

- **Attribution** — credit "OpenOva" with a link to https://openova.io
- **ShareAlike** — derivative works distribute under the same license
- **No additional restrictions** — don't apply legal terms or technological measures that legally restrict others from doing anything the license permits

## Usage rules

### DO

- Use `powered-by-openova.svg` verbatim in third-party Blueprint UIs that run on a Sovereign powered by OpenOva
- Use `openova-mark.svg` as the icon-position glyph in compact spaces
- Maintain the gradient `#3B82F6 → #818CF8` (the canonical OpenOva blue→indigo)
- Maintain min clear-space of 0.5× the mark height around the lockup
- Maintain min display size of 24px height for the lockup (legibility floor)

### DON'T

- Recolor the gradient
- Rotate / stretch / skew the mark
- Substitute a different wordmark font (the lockup uses system-ui-sans for portable rendering)
- Use the mark to imply OpenOva endorsement of a product OpenOva hasn't endorsed
- Embed in OEM-licensed products without contacting hello@openova.io for a commercial license

## Versions

| Version | Date | Changes |
|---|---|---|
| 1.0 | 2026-05-24 | Initial public brand-kit. Mark + powered-by lockup. CC-BY-SA-4.0. Filed for Wave 5.70 #2371 in response to chepherd v0.5 ask. |
| 1.1 | 2026-05-24 | Added stacked vertical lockup (`powered-by-openova-stacked.svg`) — completes the 3-variant set (icon-only / horizontal / stacked) per original #2371 scope. |

## Contact

For brand-kit additions, commercial-license inquiries, or partnership use cases beyond CC-BY-SA-4.0: brand@openova.io
