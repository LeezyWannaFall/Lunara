// Package migrations embeds versioned SQL migrations in the executable.
package migrations

import (
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed *.sql
var sqlFiles embed.FS

// New uses a session lock so concurrent migration commands cannot race.
// The caller owns db and must close it after using the provider.
func New(db *sql.DB) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, db, sqlFiles, goose.WithSessionLocker(locker))
}
