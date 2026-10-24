package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The schema as it stood before skycam took over cloud detection.
const preSkycamSchema = `
CREATE TABLE nights (
    id INTEGER PRIMARY KEY, night_of TEXT NOT NULL UNIQUE, log_path TEXT NOT NULL,
    log_sha256 TEXT NOT NULL, cloud_onset_at TEXT, baseline_mode TEXT NOT NULL,
    ingested_at TEXT NOT NULL);
CREATE TABLE sky_samples (
    id INTEGER PRIMARY KEY,
    night_id INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at TEXT NOT NULL, luminance REAL NOT NULL, volatility REAL, image_path TEXT NOT NULL);
INSERT INTO nights VALUES (1, '2026-09-02', 'a.log', 'abc', '2026-09-02T17:48:00Z', 'self', '2026-09-03T00:00:00Z');
INSERT INTO sky_samples VALUES (1, 1, '2026-09-02T13:24:14Z', 27.5, NULL, '');
INSERT INTO sky_samples VALUES (2, 1, '2026-09-02T19:19:12Z', 88.9, 7.1, '');
`

func TestMigrateKeepsLuminanceNights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(preSkycamSchema); err != nil {
		t.Fatal(err)
	}
	old.Close()

	// Open twice: the second run must find nothing left to do.
	for i := 0; i < 2; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		var source string
		if err := d.QueryRow(`SELECT cloud_source FROM nights WHERE night_of = '2026-09-02'`).Scan(&source); err != nil {
			t.Fatal(err)
		}
		if source != "luminance" {
			t.Errorf("cloud_source = %q, want luminance", source)
		}
		var n int
		var peak float64
		if err := d.QueryRow(`SELECT COUNT(*), MAX(luminance) FROM legacy_sky_samples WHERE night_of = '2026-09-02'`).Scan(&n, &peak); err != nil {
			t.Fatal(err)
		}
		if n != 2 || peak != 88.9 {
			t.Errorf("legacy samples = %d, peak %v; want 2, 88.9", n, peak)
		}
		var tables int
		d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'sky_samples'`).Scan(&tables)
		if tables != 0 {
			t.Error("sky_samples still present")
		}
		d.Close()
	}

	// Re-ingesting a night replaces its nights row; the legacy rows must
	// survive it.
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`DELETE FROM nights WHERE night_of = '2026-09-02'`); err != nil {
		t.Fatal(err)
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM legacy_sky_samples`).Scan(&n)
	if n != 2 {
		t.Errorf("legacy samples after deleting the night = %d, want 2", n)
	}
}

func TestMigrateFreshDatabase(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var version, legacy int
	d.QueryRow(`PRAGMA user_version`).Scan(&version)
	d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'legacy_sky_samples'`).Scan(&legacy)
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d", version, len(migrations))
	}
	if legacy != 0 {
		t.Error("fresh database got a legacy_sky_samples table")
	}
}
