# skyq — sky transparency analyser

Implementation spec. Hand this to Claude in VS Code.

---

## 1. What this is

A tool that answers, from data you already produce: **how much of a normal night got
through, and which failures were the sky's fault?**

It was prototyped by hand against the night of 2–3 September 2026 and the analysis is
validated — every algorithm and threshold below was checked against real data, and §9
gives the exact numbers to test against. This spec is the port of that prototype to a
maintainable Go program.

Two phases:

- **Phase 1 — morning report.** Batch. Parse last night's N.I.N.A. log plus the all-sky
  stills, write an HTML report, store everything in SQLite so baselines improve over time.
- **Phase 2 — live transparency monitor.** Compute the same metric during the night from
  stills as they land, expose it as an ASCOM Alpaca ObservingConditions device so N.I.N.A.
  can skip work that is going to fail.

Phase 1 first. Phase 2 reuses its parsing and baseline layers, so factor them as packages
from the start, not as a `main.go` monolith.

---

## 2. Deployment context

| | |
|---|---|
| **Runs on** | The Windows 11 NUC (`nuc.local`) — the same machine that runs N.I.N.A. and `alpaca-switch.exe` |
| **Language** | Go |
| **Storage** | SQLite via modernc/sqlite (pure Go, no cgo), queries via [sqlc](https://sqlc.dev/) |
| **Web** | `html/template` + HTMX (self-hosted, no CDN) |
| **All-sky stills** | On `allsky.local` (Debian box running AllSky), `~/allsky/images/YYYYMMDD/image-YYYYMMDDHHMMSS.jpg` — skyq pulls them over the LAN and caches locally |
| **N.I.N.A. log** | Local file, `%LOCALAPPDATA%\NINA\Logs\` — open read-only |
| **Service mgmt** | Windows Task Scheduler: daily task at 09:00 for `skyq report`, at-startup task with restart-on-failure for `skyq serve` |
| **Publishing** | Finished reports pushed to the Linode, served at deepspaceplace.com (already behind Caddy) |

Running on the NUC makes the N.I.N.A. log a local read — the most awkward transport problem
in the prototype disappears — and puts the Phase 2 Alpaca device on the same machine as its
only consumer. The capture time of each still is in the filename, so no video timing
calibration is needed regardless of how the stills arrive.

**Getting the stills to the NUC.** Pull directly from `allsky.local`, not via S3 (AllSky's
S3 upload only carries keograms, star trails and videos, and adding stills would cost upload
latency that Phase 2 can't afford). Preferred: HTTP from the AllSky web server, which already
serves the images directory; fallback: a read-only SMB export of `~/allsky/images`. Verify
which is exposed on the real box before coding. `http://allsky.local/images/` is (and should
stay) password protected — the fetcher sends HTTP Basic auth on every request, with the
credentials in skyq's config file, never in the repo. Fetched stills land in a local cache
directory on the NUC; derived samples outlive the cache (§10).

There is no existing service pattern on the NUC to copy — `alpaca-switch.exe` is run
manually as a console program. skyq must be better behaved: both scheduled tasks set an
explicit "start in" directory, and the program logs to a file (console output is lost under
Task Scheduler).

Take the log path, stills source URL, stills credentials and cache directory from config;
do not hard-code them. The config file holds secrets, so it must be excluded from git and
the repo ships a `config.example` instead (same discipline as `.env` in the other projects).

**Control surface.** The config file and the CLI subcommands (`report`, `backfill`, `serve`,
`calibrate`) are the only way to change skyq's behaviour — there is no settings or admin UI,
by design. Config changes take effect on the next scheduled `report` run, or after restarting
`skyq serve`.

---

## 3. Package layout

```
skyq/
├── main.go                   # package main at the root so a bare `go build` works
├── report.go                 # the report/backfill/calibrate orchestration
├── internal/ninalog/         # log parser — pure, no I/O beyond an io.Reader
│   ├── parse.go
│   ├── types.go
│   └── parse_test.go
├── internal/allsky/          # still fetching + luminance
│   ├── fetch.go              # list + download new stills from allsky.local into the cache
│   ├── scan.go               # walk the local cache, parse filename timestamps
│   ├── luminance.go          # pure — operates on an io.Reader/bytes, no network
│   └── luminance_test.go
├── internal/analysis/        # normalisation, baselines, cloud detection
│   ├── index.go
│   ├── cloud.go
│   └── index_test.go
├── internal/store/           # sqlc-generated code + migrations
│   ├── migrations/
│   ├── queries/
│   └── db/                   # sqlc output
├── internal/report/          # HTML report generation (Phase 1)
├── internal/publish/         # push report + index to the Linode (Phase 1)
├── internal/alpaca/          # ObservingConditions HTTP API (Phase 2)
├── internal/server/          # HTMX live view (Phase 2)
├── templates/
├── design/                   # report look & feel — see §6
│   ├── report.css
│   ├── CHARTS.md
│   └── reference-report.html
└── testdata/
    ├── 20260902.log          # the validated fixture — see §9
    └── stills/               # a handful of real stills
```

`ninalog`, `allsky` and `analysis` must have **no dependency on the store or the web
layer**. They are the reusable core and they are where the tests live.

---

## 4. The validated analysis

### 4.1 N.I.N.A. log parsing

Pipe-delimited: `TIMESTAMP|LEVEL|SOURCE|MEMBER|LINE|MESSAGE`. Timestamps are **local time,
no zone**, with 4 fractional digits. Parse the first 23 characters with layout
`2006-01-02T15:04:05.000`. Store as UTC using a configured location; keep local time for
display.

The log is large (1.5 MB, ~10k lines for one night) and full of multi-line stack traces —
stream it line by line, never load it whole.

These patterns are verified against a real log. Keep them in one file with a comment naming
the N.I.N.A. version they were validated on (3.2.0.9001):

```go
// Star detection result — the transparency measurement.
reStars = `^(\S+)\|INFO\|HocusFocusStarDetection\.cs\|BuildStarDetectionResult\|\d+\|` +
          `Average HFR: ([\d.]+), HFR MAD: ([\d.]+), Detected Stars (\d+)`

// Exposure start — gives the exposure length for the NEXT star-detection line.
reCapture = `^(\S+)\|INFO\|CameraVM\.cs\|Capture\|\d+\|Starting Exposure - Exposure Time: ([\d.]+)s`

// Filter changes — the Filter field on the capture line is often EMPTY for long
// exposures, so filter must be tracked from these instead.
reFilter = `^(\S+)\|INFO\|FilterWheelVM\.cs\|ChangeFilter\|\d+\|Moving to Filter (\w+)`

// Target changes.
reSlew = `Item: SlewScopeToRaDec, Coordinates: RA: ([0-9:]+); Dec: ([^;]+);`
reTargetName = `Target: ([^ ]+(?: [^ ]+)?) RA: ([0-9:]+); Dec: ([^;]+);`

// Autofocus run boundaries — frames inside these windows must be excluded.
reAFStart = `FocuserMediator\.cs\|BroadcastAutoFocusRunStarting`
reAFEnd   = `HocusFocusVM\.cs\|AutoFocusEngine_Completed\|\d+\|AutoFocus completed with ` +
            `focuser starting at (\d+) and ending at (\d+)`
reAFTemp  = `BroadcastSuccessfulAutoFocusRun\|\d+\|Autofocus notification received - Temperature ([\d.]+)`
reAFReject = `ValidateCalculatedFocusPosition\|\d+\|New focus point HFR ([\d.]+) is significantly worse`

// Failures.
reSolveOK   = `\|ImageSolver\.cs\|Solve\|54\|Platesolve successful`
reSolveFail = `\|ASTAPSolver\.cs\|ReadResult\|\d+\|ASTAP - Plate solve failed`
rePHD2Err   = `\|PHD2Guider\.cs\|ProcessEvent\|\d+\|PHD2 error:(.*)$`
reDomeRefuse = `\|DomeVM\.cs\|OpenShutter\|\d+\|Dome shutter ordered to open but the mount is unparked`
```

**Noise to ignore.** The Touch-N-Stars plugin emits hundreds of `SettingsController` JSON
errors and PHD2 polling timeouts per night. They are not observatory events. Filter on
source file, not on log level — `ERROR` alone is far too noisy to be a signal.

**Parser state machine.** Walk lines in order holding: current filter, current target,
last exposure length, whether inside an autofocus window. Emit a `Frame` on each star-detection
line, tagged with all of that state. Emit `Event` records for failures.

```go
type Frame struct {
    At           time.Time
    ExposureSec  float64
    Filter       string   // H, O, S, L
    Target       string   // "IC 4628", "NGC 2070"
    DetectedStars int
    HFR          float64
    HFRMad       float64
    InAutofocus  bool
}

type Event struct {
    At      time.Time
    Kind    EventKind // SolveFailed, PHD2Error, AutofocusRejected, DomeRefused
    Detail  string
}
```

### 4.2 Frame classification — this is the part that matters most

The prototype's first answer was wrong because it mixed exposure classes. Classify every
frame and only use one class for transparency:

| Class | Rule | Use |
|---|---|---|
| **Light** | `ExposureSec >= 60` and not in an autofocus window | **The transparency measure.** |
| **Centering** | `ExposureSec < 60`, filter `L`, not in an autofocus window | Plate-solve health only. |
| **Autofocus** | inside an AF window | Discard. Defocused on purpose. |

Excluding autofocus frames is not optional — an AF sweep deliberately produces near-zero
star counts and will fake a cloud event every 40 minutes.

The Light/Centering split is itself a finding worth surfacing in the report: on the validated
night, 300 s narrowband subs kept finding stars through thin cloud while 3–5 s centering
frames did not. That is exactly why plate solving failed while imaging continued.

### 4.3 Normalisation — target × filter

Raw star counts are meaningless across filters *and* across targets. On the validated night
O III found **716** stars on IC 4628 and **228** on NGC 2070 — a 3× difference with nothing
to do with the sky.

```
index(frame) = 100 * frame.DetectedStars / baseline(frame.Target, frame.Filter)
```

`baseline` is the median detected-star count for that `(target, filter)` pair over frames
recorded in clear conditions.

**Bootstrapping.** With no history, use the current night's own pre-cloud median (this is
what the prototype did, and it produced a flat 100 baseline with IQR 86–107 — good enough).
Once ≥ 5 clear nights exist for a `(target, filter)` pair, use a rolling median across those
nights instead, so the index becomes comparable night to night. Store which mode produced
each night's baseline; the report must say which it used.

"Clear" for baseline purposes = frames before the all-sky cloud onset (§4.4), or all frames
if no onset was detected.

### 4.4 All-sky luminance and cloud detection

Per still:

1. Parse the timestamp from the filename (`image-YYYYMMDDHHMMSS.jpg`) — local time.
2. Decode, crop a horizontal band of upper sky: full width, vertical range **6%–30%** of
   image height. This avoids both the telescope silhouette (centre) and the horizon glow
   and moon (bottom). Make it configurable — it depends on camera orientation.
3. Downsample to 64×16 and take the mean luminance, 0–255.

Cloud detection, validated:

```
volatility = population stddev of the last 10 luminance samples
cloud onset when volatility > 6.0
```

On the validated night this fired at **03:48** — 18 minutes before the first plate-solve
failure and about an hour before PHD2 began struggling. That lead time is the whole point of
Phase 2.

**Two caveats to carry into the code as comments:**

- The threshold `6.0` is specific to this camera, its gain, and moonlit cloud. Make it
  configurable, and add a `skyq calibrate` path that derives it from the spread of a known
  clear night rather than shipping a magic number.
- AllSky uses **auto-exposure**, so luminance is partly compensated: under cloud the sky got
  brighter *and* the exposure dropped from 5.0 s to 2.1 s. Both moved together, which
  amplified the signal — convenient, but it means this is a detection metric, not a
  photometric one. If the exposure is recoverable per frame (AllSky overlay config, EXIF, or
  a JSON sidecar), record it and consider normalising. Do not present the raw number as sky
  brightness in physical units.

Absolute level is also useful context but is dominated by the moon: on the validated night
the baseline was 25–30, rising gently to ~34 with moonrise, then spiking to a peak of 88.9
at 05:19 under moonlit cloud.

### 4.5 Event attribution

Classify each failure so the report says something useful rather than listing errors:

- **PHD2 error within 10 minutes after a slew or target change** → *post-slew guide star
  acquisition*, not weather. On the validated night this correctly reclassified the single
  event that did not fit the cloud pattern (00:17:12, ten minutes after the 00:10:39 slew
  from IC 4628 to NGC 2070).
- **Any failure after cloud onset** → attribute to cloud.
- **Autofocus run whose fitted points had fewer than ~30 detected stars, or that logged
  `New focus point HFR ... significantly worse`** → flag as *untrustworthy*. On the validated
  night the 04:42 run fitted its curve on 3, 4, 6, 24 and 28 stars, rejected its own first
  answer, and took 7m38s against a typical 3m20s. It happened to land somewhere sane; that
  was luck, and the report should say so.
- **Anything else** → unexplained, and list it prominently. Unexplained failures are the
  ones worth a human's time.

---

## 5. Data model

SQLite. Migrations in `internal/store/migrations`, queries in `internal/store/queries`,
sqlc generating into `internal/store/db`.

```sql
CREATE TABLE nights (
    id                INTEGER PRIMARY KEY,
    night_of          TEXT NOT NULL UNIQUE,   -- 'YYYY-MM-DD', the evening date
    log_path          TEXT NOT NULL,
    log_sha256        TEXT NOT NULL,          -- re-ingest guard, see below
    cloud_onset_at    TEXT,                   -- RFC3339, null if none detected
    baseline_mode     TEXT NOT NULL,          -- 'self' | 'historical'
    ingested_at       TEXT NOT NULL
);

CREATE TABLE frames (
    id             INTEGER PRIMARY KEY,
    night_id       INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at             TEXT NOT NULL,
    class          TEXT NOT NULL,   -- 'light' | 'centering' | 'autofocus'
    exposure_sec   REAL NOT NULL,
    filter         TEXT NOT NULL,
    target         TEXT NOT NULL,
    detected_stars INTEGER NOT NULL,
    hfr            REAL NOT NULL,
    index_pct      REAL             -- null until a baseline exists
);
CREATE INDEX frames_night_at ON frames(night_id, at);
CREATE INDEX frames_baseline ON frames(target, filter, class);

CREATE TABLE events (
    id       INTEGER PRIMARY KEY,
    night_id INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at       TEXT NOT NULL,
    kind     TEXT NOT NULL,
    detail   TEXT NOT NULL,
    cause    TEXT NOT NULL   -- 'cloud' | 'post_slew' | 'unexplained'
);

CREATE TABLE sky_samples (
    id        INTEGER PRIMARY KEY,
    night_id  INTEGER NOT NULL REFERENCES nights(id) ON DELETE CASCADE,
    at        TEXT NOT NULL,
    luminance REAL NOT NULL,
    volatility REAL,
    image_path TEXT NOT NULL
);
CREATE INDEX sky_night_at ON sky_samples(night_id, at);

CREATE TABLE baselines (
    target      TEXT NOT NULL,
    filter      TEXT NOT NULL,
    median_stars REAL NOT NULL,
    n_frames    INTEGER NOT NULL,
    n_nights    INTEGER NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (target, filter)
);
```

**Re-ingesting must be idempotent.** Key on `log_sha256`; re-running the morning job on an
unchanged log is a no-op, and on a changed log replaces that night's rows in a transaction.
You will re-run this while developing and you must not end up with doubled frames.

---

## 6. Phase 1 — morning report

`skyq report [--night 2026-09-02]` — defaults to the night that just ended.

1. Locate the log for that night in `%LOCALAPPDATA%\NINA\Logs\`, hash it, ingest frames
   and events.
2. Sync last night's stills from `allsky.local` into the local cache (skip files already
   present), then compute luminance samples and detect cloud onset.
3. Compute or load baselines, populate `index_pct`.
4. Attribute events (§4.5).
5. Render the HTML report to a configured output dir.
6. Publish: upload the self-contained report and a regenerated index page to the Linode
   over the existing deploy SSH access (`scp`/`sftp`, port 2222, user `deploy`) into the
   deepspaceplace.com web root — e.g. `/var/www/deepspaceplace/reports/YYYY-MM-DD.html`
   plus `index.html`. A publish failure must log and exit non-zero but **must not** stop
   the local report from being written — the analysis is the product, the upload is a copy.

Run it from a Windows Task Scheduler daily task at, say, 09:00 local — well after any
shutdown sequence and any timelapse generation on the AllSky box.

**The report should lead with a verdict, not a chart.** One or two sentences: how much of the
night was usable, when the sky went, what failed because of it, and anything unexplained.
Charts are supporting evidence.

Report contents, in order:

1. Verdict paragraph + stat row (index before/after onset, onset time, usable hours,
   frames kept vs lost).
2. Transparency index over time, one line per filter, with a dashed 100 baseline, autofocus
   bands shaded, target changes marked, failures as ticks on the base.
3. All-sky luminance over the same x-axis.
4. All-sky thumbnails at the interesting moments — onset, worst, any recovery.
5. Baseline table (the divisors — makes the index auditable).
6. Event timeline with attributed causes.
7. Collapsed raw frame table.

Charting: **inline SVG generated in Go**, no JS charting library. The data is small (order 100
points) and a self-contained file is the point. Two separate charts sharing an x-axis —
never a dual y-axis.

### Getting the look right

Do not redesign the report, and do not infer the styling from prose. Three files in
`design/` are the source of truth:

| File | What it is |
|---|---|
| `design/report.css` | The complete stylesheet, tokenised and commented. Embed it verbatim inside a `<style>` block in the generated report. |
| `design/CHARTS.md` | Exact SVG geometry, draw order, gap handling, event rail, hover layer, and the rules that must not be broken. Read before writing any chart code. |
| `design/reference-report.html` | The validated original with real 2026-09-02 data. Open it in a browser; the generated report should be indistinguishable apart from the numbers. |

The palette in `report.css` passed a colour-blindness and contrast validator for both light
and dark surfaces **in that exact slot order**. Substituting a hex or reordering the slots
invalidates it. Likewise the light/dark token structure: light values on bare `:root`, dark
redefined under both `prefers-color-scheme` and `[data-theme="dark"]`.

Reports are emailed, copied to phones and opened from disk, so every report is one
self-contained file — CSS inlined, thumbnails as `data:` URIs, no external requests.

---

## 7. Phase 2 — live monitor

`skyq serve` — long-running, Windows Task Scheduler at-startup task with restart-on-failure.

**Watcher.** The stills are remote, so this is a poller, not fsnotify: every 30–60 s, list
today's image directory on `allsky.local` (HTTP index or SMB), fetch any stills newer than
the last seen, and remember the AllSky directory rolls over at local noon. On each new
still: compute luminance, append a sample, recompute rolling volatility.

**Alpaca ObservingConditions device.** Implement the standard endpoints under
`/api/v1/observingconditions/0/`. **No discovery responder** (config flag, default off):
`alpaca-switch` on this same machine already binds UDP 32227 and dies with `log.Fatalf` if
that bind fails, so a second responder is a fight nobody wins. N.I.N.A. is local — add the
device manually as `127.0.0.1:11112`, once. Bind the Alpaca API to `127.0.0.1` by default
(also avoids Windows Firewall prompts) on a port distinct from alpaca-switch's 11111. Map:

- `CloudCover` → 0–100, derived from volatility against the configured threshold
- `SkyQuality` → the transparency index if a current one is available
- `SkyBrightness` → mean luminance (document the units honestly as instrumental, not mag/arcsec²)
- `TimeSinceLastUpdate` → seconds since the last still

N.I.N.A. reads this as weather data, and Sequencer Powerups exposes weather values as
expression symbols — so gating becomes an `If` expression in the sequence, which is already
a familiar pattern in this observatory.

**Staleness is not zero.** If the newest still is older than a configured max age (default
5 minutes), the device must report values as unavailable rather than returning a stale or
zeroed reading. A monitor that silently reports "clear" because the camera died is worse
than no monitor. Now that the stills cross the LAN, this rule covers network and AllSky-box
failures too, not just a dead camera — same behaviour, more ways to trigger it.

**HTMX live page** at `/`, on its own listener so it can bind LAN-wide while the Alpaca API
stays on loopback. The page is strictly read-only — it renders state and never mutates it:
no forms, no buttons, no POST handlers. A control that could gate or un-gate N.I.N.A. from a
LAN page would be an interlock nobody designed (§8). One page: current index, current volatility, a luminance
sparkline, the last still, and tonight's events. Follow the project's Go+HTMX conventions —
parse templates once at startup with the clone-per-page pattern, `hx-get` with
`hx-trigger="every 30s"` on a stable container with a `min-height` so nothing jumps,
self-hosted `htmx.min.js` under `static/js/`. No CDN.

---

## 8. Safety constraints — hard rules

These are not preferences. Get them wrong and the observatory pays for it.

1. **Transparency must never gate the roof.** Cloud is not a safety condition; rain is. The
   RG-11 safety monitor keeps sole ownership of the close decision. This tool exposes
   ObservingConditions (advisory), **never** SafetyMonitor.
2. **Advisory means skip work, not stop the observatory.** Legitimate uses: defer autofocus,
   hold off plate solving, pause a target and record the gap. Nothing that moves the roof or
   parks the mount.
3. **Unknown ≠ unsafe.** When data is stale or missing, report unknown. Consumers must treat
   unknown as "do not gate", so a dead camera never triggers observatory actions.
4. **Read-only on N.I.N.A.'s files.** Open the log read-only. Never write into N.I.N.A.'s
   directories.

Context for rule 1: N.I.N.A.'s `RefuseUnsafeShutterMove` already refuses to open the shutter
whenever the mount reports unparked — and a Paramount ME always reports unparked after power
on, because it has no absolute encoders and must be homed first. That interlock has already
cost one night. Do not add a second interlock in the same path.

---

## 9. Acceptance tests

`testdata/20260902.log` is the validated fixture. These values come from the hand-checked
prototype and the parser must reproduce them exactly.

**Parsing**

| Assertion | Expected |
|---|---|
| Light frames (≥60 s, outside AF) | 79 |
| Centering frames (<60 s, L, outside AF) | 18 |
| Autofocus runs | 14 |
| Light frames by filter | H 32, O 25, S 22 |
| Targets detected | `IC 4628`, then `NGC 2070` from 00:10:39 |
| Plate-solve failures | 6 |
| PHD2 errors | 6 |

**Baselines** (median detected stars per 300 s sub, before 03:48)

| Target | H | O | S |
|---|---|---|---|
| IC 4628 | 332 | 716 | 872 |
| NGC 2070 | 310 | 228 | 579 |

**Index**

| Window | Median | IQR | n |
|---|---|---|---|
| Before 03:48 | 100 | 86–107 | 63 |
| After 03:48 | 50 | 13–75 | 16 |

The tight pre-cloud IQR is the regression test that matters. If normalisation breaks, that
baseline stops being flat — under raw counts the same clear hours ranged 79 to 232.

**All-sky** — cloud onset at **03:48**, peak luminance **88.9** at **05:19**, clear-sky
baseline 25–30.

**Attribution** — the 00:17:12 PHD2 error classifies as `post_slew` (it follows the 00:10:39
slew by under 10 minutes); the other five classify as `cloud`; the 04:42 autofocus run flags
as untrustworthy.

Keep a handful of real stills in `testdata/stills/` covering clear, cloudy and the onset, so
the luminance and volatility code is tested on real pixels rather than synthetic ones.

---

## 10. Open decisions to settle first

1. **Stills pull mechanism** — HTTP from the AllSky web server or a read-only SMB export
   (§2). Confirm what `allsky.local` actually exposes before coding — including whether the
   web server's auth is Basic or Digest, and whether it serves a directory listing the
   poller can parse; affects config shape and the Phase 2 poller. (Log transport, the previous decision here, is resolved: the log
   is a local file on the NUC.)
2. **Crop band** — 6%–30% of height worked for the prototype's camera orientation. Verify
   against a real still before hard-coding the default.
3. **Volatility threshold** — 6.0 is validated on one night. Decide whether to ship it as a
   default or require `skyq calibrate` against a known clear night before Phase 2 gates
   anything.
4. **Exposure normalisation** — determine whether AllSky exposes per-frame exposure time in a
   machine-readable form on `allsky.local`. If it does, the luminance metric gets meaningfully
   better (§4.4).
5. **Retention** — AllSky's `DAYS_TO_KEEP` deletes old image directories on `allsky.local`,
   and the NUC's stills cache needs its own pruning (a config value, e.g. 14 days). skyq's
   derived samples are tiny; keep them indefinitely and let the images go.

---

## Appendix — source material

All in `C:\Projects\cowork\nuc`:

- `20260902-201750-3.2.0.9001.8256-202609.log` — the fixture log
- `allsky-20260902.mp4`, `keogram-20260902.jpg` — that night's all-sky output
- `transparency-by-filter.html` — the validated reference report; match its structure
- `cloud-vs-errors.html`, `sky-vs-errors.html` — earlier iterations, kept only to show what
  the exposure-class and target confounds looked like before they were controlled. Do not
  use their numbers.
