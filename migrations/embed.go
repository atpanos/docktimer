// Package migrations embeds the SQL schema migrations into the binary.
package migrations

import "embed"

// FS contains all numbered migration files.
//
//go:embed *.sql
var FS embed.FS
