// Package ui embeds the built front-end (ui/dist). Lane B replaces the
// placeholder index.html with the Vite build; the Go binary serves whatever
// is in dist at / with an index.html fallback for browser routing.
package ui

import "embed"

// Dist holds the built assets.
//
//go:embed dist
var Dist embed.FS
