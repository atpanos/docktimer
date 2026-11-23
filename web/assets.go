// Package webassets embeds the static frontend assets (CSS, HTMX, app.js).
package webassets

import "embed"

// FS contains the static directory.
//
//go:embed static
var FS embed.FS
