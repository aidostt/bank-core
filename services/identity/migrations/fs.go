// Package migrations embeds the SQL migrations, applied on service start
// (CLAUDE.md tech stack).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
