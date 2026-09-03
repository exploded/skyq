-- skyq schema. Single source of truth; store.Open runs this at startup
-- (CREATE IF NOT EXISTS throughout). Times are RFC3339 TEXT in UTC, which
-- compares correctly as text.

CREATE TABLE IF NOT EXISTS nights (
    id                INTEGER PRIMARY KEY,
    night_of          TEXT NOT NULL UNIQUE,   -- 'YYYY-MM-DD', the evening date
    log_path          TEXT NOT NULL,
    log_sha256        TEXT NOT NULL,          -- re-ingest guard
    cloud_onset_at    TEXT,                   -- RFC3339, null if none detected
    baseline_mode     TEXT NOT NULL,          -- 'self' | 'historical'
    ingested_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS frames (
    id             INTEGER PRIMARY KEY,
    night_id       INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at             TEXT NOT NULL,
    class          TEXT NOT NULL,   -- 'light' | 'centering' | 'autofocus' | 'other'
    exposure_sec   REAL NOT NULL,
    filter         TEXT NOT NULL,
    target         TEXT NOT NULL,
    detected_stars INTEGER NOT NULL,
    hfr            REAL NOT NULL,
    index_pct      REAL             -- null until a baseline exists
);
CREATE INDEX IF NOT EXISTS frames_night_at ON frames(night_id, at);
CREATE INDEX IF NOT EXISTS frames_baseline ON frames(target, filter, class);

CREATE TABLE IF NOT EXISTS events (
    id       INTEGER PRIMARY KEY,
    night_id INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at       TEXT NOT NULL,
    kind     TEXT NOT NULL,
    detail   TEXT NOT NULL,
    cause    TEXT NOT NULL   -- 'cloud' | 'post_slew' | 'unexplained'
);

CREATE TABLE IF NOT EXISTS sky_samples (
    id         INTEGER PRIMARY KEY,
    night_id   INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at         TEXT NOT NULL,
    luminance  REAL NOT NULL,
    volatility REAL,
    image_path TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sky_night_at ON sky_samples(night_id, at);

CREATE TABLE IF NOT EXISTS baselines (
    target       TEXT NOT NULL,
    filter       TEXT NOT NULL,
    median_stars REAL NOT NULL,
    n_frames     INTEGER NOT NULL,
    n_nights     INTEGER NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (target, filter)
);
