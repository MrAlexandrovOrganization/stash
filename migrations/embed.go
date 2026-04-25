package migrations

import "embed"

// FS contains all SQL migration files embedded into the binary.
//
//go:embed *.sql
var FS embed.FS
