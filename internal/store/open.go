// Package store owns the SQLite database: schema, connection setup, and the
// sqlc-generated queries in the db subpackage.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Open opens (creating if needed) the skyq database and applies the schema.
// The schema is CREATE IF NOT EXISTS throughout, so this is idempotent.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serialises writers; a single connection avoids SQLITE_BUSY
	// surprises for this workload's tiny volumes.
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(schemaSQL); err != nil {
		d.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	return d, nil
}
