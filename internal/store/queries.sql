-- name: GetNight :one
SELECT id, night_of, log_path, log_sha256, cloud_onset_at, baseline_mode, ingested_at
FROM nights WHERE night_of = ?;

-- name: ListNights :many
SELECT id, night_of, log_path, log_sha256, cloud_onset_at, baseline_mode, ingested_at
FROM nights ORDER BY night_of DESC;

-- name: DeleteNight :exec
DELETE FROM nights WHERE night_of = ?;

-- name: CreateNight :exec
INSERT INTO nights (night_of, log_path, log_sha256, cloud_onset_at, baseline_mode, ingested_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertFrame :exec
INSERT INTO frames (night_id, at, class, exposure_sec, filter, target, detected_stars, hfr, index_pct)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListFrames :many
SELECT id, night_id, at, class, exposure_sec, filter, target, detected_stars, hfr, index_pct
FROM frames WHERE night_id = ? ORDER BY at;

-- name: InsertEvent :exec
INSERT INTO events (night_id, at, kind, detail, cause)
VALUES (?, ?, ?, ?, ?);

-- name: ListEvents :many
SELECT id, night_id, at, kind, detail, cause
FROM events WHERE night_id = ? ORDER BY at;

-- name: InsertSkySample :exec
INSERT INTO sky_samples (night_id, at, luminance, volatility, image_path)
VALUES (?, ?, ?, ?, ?);

-- name: ListSkySamples :many
SELECT id, night_id, at, luminance, volatility, image_path
FROM sky_samples WHERE night_id = ? ORDER BY at;

-- Clear-sky light frames for one (target, filter) across all nights: frames
-- before that night's cloud onset, or the whole night when no onset. Feeds
-- the rolling historical baseline.
-- name: ListClearLightFrames :many
SELECT f.detected_stars, n.night_of
FROM frames f
JOIN nights n ON n.id = f.night_id
WHERE f.class = 'light'
  AND f.target = ?
  AND f.filter = ?
  AND (n.cloud_onset_at IS NULL OR f.at < n.cloud_onset_at)
ORDER BY n.night_of, f.at;

-- name: UpsertBaseline :exec
INSERT INTO baselines (target, filter, median_stars, n_frames, n_nights, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (target, filter) DO UPDATE SET
    median_stars = excluded.median_stars,
    n_frames     = excluded.n_frames,
    n_nights     = excluded.n_nights,
    updated_at   = excluded.updated_at;

-- name: ListBaselines :many
SELECT target, filter, median_stars, n_frames, n_nights, updated_at
FROM baselines ORDER BY target, filter;
