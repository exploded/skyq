package store

import (
	"database/sql"
	"fmt"
)

// migrations bring a database made by an older build up to schema.sql.
// Each runs once, in order, inside a transaction; PRAGMA user_version
// records how many have run. schema.sql has already been applied when they
// run, so every step must also be a no-op on a fresh database.
var migrations = []func(*sql.Tx) error{
	migrateSkycamCloud,
}

func migrate(d *sql.DB) error {
	var version int
	if err := d.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		if err := migrations[i](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA takes no bound parameters.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// migrateSkycamCloud moves cloud detection to skycam (2026-10-24).
//
// Every night already on record got its onset from skyq's own luminance
// detector, so it is tagged 'luminance' and keeps counting as clear-sky
// history.
//
// sky_samples held that detector's per-still luminance. Nothing reads it
// any more, but it is the only record of those nights' sky, so its rows are
// copied to legacy_sky_samples before the table goes. The copy is keyed by
// night_of with no foreign key: re-ingesting a night deletes and recreates
// its nights row, and a cascade would take the legacy rows with it.
func migrateSkycamCloud(tx *sql.Tx) error {
	has, err := hasColumn(tx, "nights", "cloud_source")
	if err != nil {
		return err
	}
	if !has {
		if _, err := tx.Exec(`ALTER TABLE nights ADD COLUMN cloud_source TEXT NOT NULL DEFAULT 'luminance'`); err != nil {
			return err
		}
	}

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sky_samples'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS legacy_sky_samples (
		    night_of   TEXT NOT NULL,
		    at         TEXT NOT NULL,
		    luminance  REAL NOT NULL,
		    volatility REAL,
		    image_path TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS legacy_sky_night_at ON legacy_sky_samples(night_of, at)`,
		`INSERT INTO legacy_sky_samples (night_of, at, luminance, volatility, image_path)
		 SELECT n.night_of, s.at, s.luminance, s.volatility, s.image_path
		 FROM sky_samples s JOIN nights n ON n.id = s.night_id
		 ORDER BY n.night_of, s.at`,
		`DROP TABLE sky_samples`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
