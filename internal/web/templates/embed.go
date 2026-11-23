// Package templates embeds the HTML templates.
package templates

import "embed"

// FS contains the layout, partials and pages.
//
//go:embed layout.html partials.html pages/*.html
var FS embed.FS
