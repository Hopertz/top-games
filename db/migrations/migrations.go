// Package migrations holds the database migrations, the same files sqlc reads.
//
// To change the schema add a new numbered pair of files, for example
// 000002_add_username.up.sql and 000002_add_username.down.sql.
// The bot applies the ones not yet applied when it starts.
package migrations

import "embed"

// Files are all the migration files, built into the binary
//
//go:embed *.sql
var Files embed.FS
