package migrations

import "embed"

// PlatformFS embeds all ordered PostgreSQL platform schema SQL scripts.
//
//go:embed platform/*.sql
var PlatformFS embed.FS
