// Package docrender is the document renderer of the chargeback BSS (EPIC
// #6867): a stateless service that turns a structured document — invoice,
// credit note, statement or quote — into a PDF (and an HTML rendition of the
// same document) without a browser.
//
// This root package holds only the embedded assets every rendition shares:
// the locale catalogs under locales/ (every label a document shows goes
// through the i18n `t` function; English ships, the structure takes more)
// and the html/template files under templates/ (one shared layout plus one
// content file per document kind).
package docrender

import (
	"embed"
	"io/fs"
)

// Locales holds locales/<lang>.json — one flat key → label catalog per
// language. `en` is required; any other file present is offered as a locale.
//
//go:embed locales/*.json
var Locales embed.FS

// Templates holds the html/template sources: _layout.html and one
// <template>.html per document kind (invoice, credit-note, statement, quote).
//
//go:embed templates/*.html
var Templates embed.FS

// assets serves both trees through one fs.FS, which is what the server takes
// so a test can substitute a fixture tree wholesale.
//
//go:embed locales/*.json templates/*.html
var assets embed.FS

// Assets is the embedded locales/ + templates/ tree.
func Assets() fs.FS { return assets }
