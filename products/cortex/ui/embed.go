// Package ui holds the cortex screens. web carries the tracked index.html and styles.css and,
// after npm run build in this folder, the compiled scripts; a binary that serves the screens
// embeds the folder.
package ui

import "embed"

// Web is the folder the browser loads.
//
//go:embed web
var Web embed.FS
