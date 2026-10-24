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

skyq is the **morning report**: batch. Parse last night's N.I.N.A. and PHD2 logs, read the
night's cloud from skycam, write an HTML report, and store everything in SQLite so baselines
improve over time.

**The sky belongs to skycam** (`exploded/skycam`), the all-sky camera software on the
observatory Pi at `allsky.local`. Since 2026-10-24 it measures cloud from each frame's star
count, knows the roof state from the roll-off controller, finds the cloud onset, serves the
live view, and runs the ASCOM Alpaca ObservingConditions device N.I.N.A. reads. skyq's
former Phase 2 — a live monitor with its own Alpaca device — moved there and is gone from
this repo (§7).

---

## 2. Deployment context

| | |
|---|---|
| **Runs on** | The Windows 11 NUC (`nuc.local`) — the same machine that runs N.I.N.A. and `alpaca-switch.exe` |
| **Language** | Go |
| **Storage** | SQLite via modernc/sqlite (pure Go, no cgo), queries via [sqlc](https://sqlc.dev/) |
| **Web** | `html/template` + HTMX (self-hosted, no CDN) |
| **Sky data** | skycam on `allsky.local` (the observatory Pi): `GET /api/nights/<YYYYMMDD>/sky` for cloud, onset and roof per frame, and `/images/YYYYMMDD/image-YYYYMMDDHHMMSS.jpg` for stills, both behind the same HTTP Basic auth (§4.4) |
| **N.I.N.A. log** | Local file, `%LOCALAPPDATA%\NINA\Logs\` — open read-only |
| **Service mgmt** | Windows Task Scheduler: daily task at 09:00 for `skyq report` |
| **Publishing** | Finished reports pushed to the Linode, served at deepspaceplace.com (already behind Caddy) |

Running on the NUC makes the N.I.N.A. log a local read — the most awkward transport problem
in the prototype disappears.

**Getting the sky to the NUC.** skyq asks skycam for one night at a time over the LAN. The
night date is allsky's: frames before noon belong to the previous evening, the same
convention as skyq's night-of. `http://allsky.local/images/` and the sky API are (and should
stay) password protected — the client sends HTTP Basic auth on every request, with the
credentials in skyq's config file, never in the repo. skyq fetches only the stills the
report shows as thumbnails, into a local cache on the NUC. skycam keeps frame rows for 365
days but deletes the JPEGs after 14 nights, so a still may 404 while its frame row survives
(§10).

There is no existing service pattern on the NUC to copy — `alpaca-switch.exe` is run
manually as a console program. skyq must be better behaved: the scheduled task sets an
explicit "start in" directory, and the program logs to a file (console output is lost under
Task Scheduler).

Take the log path, skycam URL, skycam credentials and cache directory from config; do not
hard-code them. The config file holds secrets, so it must be excluded from git and
the repo ships a `config.example` instead (same discipline as `.env` in the other projects).

**Control surface.** The config file and the CLI subcommands (`report`, `backfill`) are the
only way to change skyq's behaviour — there is no settings or admin UI, by design. Config
changes take effect on the next scheduled `report` run.

---

## 3. Package layout

```
skyq/
├── main.go                   # package main at the root so a bare `go build` works
├── report.go                 # the report/backfill orchestration
├── internal/ninalog/         # log parser — pure, no I/O beyond an io.Reader
│   ├── parse.go
│   ├── types.go
│   └── parse_test.go
├── internal/skycam/          # skycam's sky API + thumbnail stills, cached locally
├── internal/phd2/            # PHD2 guide-log parser — pure, like ninalog
│   ├── parse.go
│   ├── types.go
│   └── parse_test.go
├── internal/analysis/        # normalisation, baselines, attribution, roof fallback, guiding
│   ├── index.go
│   ├── cloud.go              # where a night's cloud verdict came from
│   ├── attribute.go
│   ├── roof.go
│   ├── guide.go
│   └── index_test.go
├── internal/store/           # schema.sql, queries.sql, migrate.go
│   └── db/                   # sqlc output
├── internal/report/          # HTML report generation
├── internal/publish/         # push report + index to the Linode
├── design/                   # report look & feel — see §6
│   ├── report.css
│   ├── CHARTS.md
│   └── reference-report.html
└── testdata/
    ├── 20260902.log          # the validated fixture — see §9
    ├── phd2-guidelog.txt     # a small guide log in the real format — see §4.6
    ├── sky-20260902.json     # HAND-BUILT sky API reply for the validated night — see §9
    └── stills/               # a handful of real stills
```

`ninalog`, `phd2` and `analysis` must have **no dependency on the store, the network or the
web layer**. They are the reusable core and they are where the tests live.

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
    Filter       string   // H, O, S, L, R, G, B
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

"Clear" for baseline purposes = frames before the cloud onset (§4.4), or all frames if no
onset was detected. A night whose cloud is **unknown** (§4.4) has no clear frames as far as
other nights are concerned: its baselines serve its own index, but are not written to the
baselines table, and the night is not history for any pair.

**Too few frames to carry forward.** A baseline built from fewer than 3 clear frames still
serves its own night, but is not written to the baselines table, and a night with fewer than
3 clear frames for a pair does not count as history for it. With no onset detected, one frame
through cloud counts as clear: on 2026-06-28 a single NGC 346 H frame read 2 stars, and that
stored divisor put the live index for 2026-09-17 at 36250.

**A target that only begins after the onset** has no clear frames tonight, so the rule above
gives it no baseline and its frames no index (2026-09-03: onset 21:20, NGC 2070 started
23:50, 54 subs vanished from the chart). For such a `(target, filter)` pair, in order:

1. Any clear history on record — even one night — used as the rolling median above. The
   five-night minimum only governs *switching away from* a self baseline, not rescuing a
   pair that has none.
2. Failing that, the pair's own whole-night median, cloud included. That index is relative
   (how the sky changed during the target), not absolute; the report must say so, and the
   divisor is not written to the baselines table.

Pairs that do have clear frames tonight are unaffected — the §9 numbers do not move.

### 4.4 Cloud and roof from skycam

skyq does not measure the sky. For each night it reads skycam's sky API,
`GET /api/nights/<YYYYMMDD>/sky`, which returns every night-mode frame (sun below −6°) with:

- `time`, `image` (path under `/images/`), `stars`, `sky_level`
- `cloud` — 0–1, or null. skycam only judges cloud with the roof OPEN and the sun below −12°,
  and it leaves the first ~30 dark, roof-open frames (about 10 minutes) null while it builds
  its clear-sky baseline. A null early in a session is normal.
- `roof` — the roll-off controller's own state: OPEN, CLOSED, OPENING, CLOSING, ERROR, or
  `unknown` when the reading is stale. There is no MOVING.
- `rain`
- `onset` — the cloud onset, or null. skyq uses it as given rather than re-deriving it, so
  the rule lives in one place (skycam's `analysis.CloudOnset`: the first of 3 consecutive
  frames with cloud ≥ 0.5; a null breaks a run).

The API answers 404 when skycam has no frames for the night.

**Unknown is never clear and never cloudy** (§8 rule 3). A night's cloud is *unknown* when
skycam answers 404, the Pi is unreachable or the reply does not parse, or no frame inside the
imaging session (first frame to last frame + 30 min, extended to cover dawn flats) has a
cloud reading. An unknown night has no onset, attributes nothing to cloud (§4.5), stores no
baselines (§4.3), and its report says *Cloud unknown* before anything else.

**Stored verdicts carry forward.** If a night already in the database is rerun and skycam has
nothing for it, the stored onset and `cloud_source` stand rather than turning into unknown.
That covers every night before skycam — their onsets came from the retired luminance
detector below — and any rerun while the Pi is down. `backfill` skips a night whose log is
unchanged unless `--rerun` is given, so old reports are not re-rendered for nothing.

**Roof.** The all-sky camera is inside the observatory, so with the roof shut it photographs
the roof. skycam gates its own cloud on the roof being OPEN, so skyq needs no roof gate. The
roof still matters to the report, which hatches the stretches the roof was not open on the
cloud chart:

1. **skycam's per-frame roof** is the source. It comes from the controller, so it sees a roof
   opened by hand. CLOSED, OPENING, CLOSING and ERROR are banded; `unknown` is left plain,
   as is any gap of more than 5 minutes between frames.
2. **N.I.N.A.'s log is the fallback**, only for a night skycam cannot serve. `DomeVM.cs`
   `OpenShutter`/`CloseShutter` lines bracket every move N.I.N.A. commanded and state the
   shutter on each side of it. That record misses a roof opened by hand: the validated night
   has no open event at all, only "Shutter state before closing ShutterOpen" at 06:36. So
   each move's prior state carries **backwards** to the last known change and its resulting
   state **forwards** to the next one, and **unknown is not closed** — a night the log says
   nothing about gets no bands.

**Thumbnails** come from the frames' `image` paths, fetched into `cache/YYYYMMDD/`. skycam
deletes JPEGs after 14 nights while keeping frame rows for a year, so an `image` path can
404. That thumbnail is left out; it never stops the report.

#### Retired: the luminance detector

Until 2026-10-24 skyq measured cloud itself, and §9's "All-sky" numbers came from that: mean
luminance of a 6–30 % band of each AllSky still, downsampled to 64×16, with cloud onset where
the population standard deviation of the last 10 samples exceeded 6.0. On the validated night
it fired at **03:48**, with a peak luminance of **88.9** at **05:19** against a clear-sky
baseline of 25–30. The threshold was specific to that camera and gain, AllSky's
auto-exposure made it a detection metric rather than photometry, and it needed a roof gate
because a shut roof scores near-zero volatility. skycam replaced it; the band, volatility
settings and `skyq calibrate` are gone, and `legacy_sky_samples` keeps the old per-still
luminance (§5).

As a cross-check only, skycam replayed the cached 2026-09-02 stills with the roof stubbed
OPEN and found its onset at **03:45:52**, two minutes before the validated 03:48. §9 keeps
03:48.

### 4.5 Event attribution

Classify each failure so the report says something useful rather than listing errors:

- **PHD2 error within 10 minutes after a slew or target change** → *post-slew guide star
  acquisition*, not weather. On the validated night this correctly reclassified the single
  event that did not fit the cloud pattern (00:17:12, ten minutes after the 00:10:39 slew
  from IC 4628 to NGC 2070).
- **Any failure after cloud onset** → attribute to cloud — provided skycam had a cloud reading
  within 5 minutes of it. A failure in a stretch with no reading (the roof shut, say) is not
  blamed on cloud: unknown is never cloudy. A night whose verdict was carried forward has no
  per-frame readings to hand, so its onset covers the rest of the night as before.
- **Autofocus run whose fitted points had fewer than ~30 detected stars, or that logged
  `New focus point HFR ... significantly worse`** → flag as *untrustworthy*. On the validated
  night the 04:42 run fitted its curve on 3, 4, 6, 24 and 28 stars, rejected its own first
  answer, and took 7m38s against a typical 3m20s. It happened to land somewhere sane; that
  was luck, and the report should say so.
- **Anything else** → unexplained, and list it prominently. Unexplained failures are the
  ones worth a human's time.

---

### 4.6 PHD2 guiding

Guiding is the night's other failure mode, and the guide log is already on the NUC. PHD2
writes it to `%USERPROFILE%\Documents\PHD2\PHD2_GuideLog_YYYY-MM-DD_HHMMSS.txt` (config key
`phd2_log_dir`), one file per launch, containing a repeating header-plus-CSV block:

```
Guiding Begins at 2026-09-02 20:52:49
Pixel scale = 0.69 arc-sec/px, Binning = 1, Focal length = 1200 mm
Frame,Time,mount,dx,dy,RARawDistance,DECRawDistance,...
1,6.227,"Mount",0.458,1.028,0.304,1.116,...
Guiding Ends at 2026-09-02 21:04:05
```

Rules the parser must keep:

- **Read columns by name, never by position.** PHD2's log version has added columns over the
  years and will again. A calibration block writes its own CSV header with no
  `RARawDistance`, which is how those rows get rejected.
- `Time` is seconds since that block's `Guiding Begins`, not since the file opened.
- Raw distances are **pixels**; the block's own `Pixel scale` converts them to arcseconds. A
  header that omits it inherits the file's last known scale; a file with none anywhere yields
  no samples rather than a plot in the wrong unit.
- **`mount` = `DROP` is a discarded frame**, not a measurement of zero error. Count it and
  report it. On the validated night PHD2 dropped 1,227 frames against 3,001 kept.
- A file that ends without `Guiding Ends` — a crash, or a live tail — closes its last session
  at its last sample, never at the end of the night.
- One file can span two nights: the night's noon-to-noon window decides what belongs to it,
  not the filename that matched.

Reduction for the report (see `design/CHARTS.md`, "The guiding chart"): bin to one minute and
take each bin's **RMS about zero** — not about the bin's mean, as PHD2's live graph does,
because slow drift across a minute is error the morning report wants to see. Bins with fewer
than two frames are dropped.

Guiding is **supporting evidence, not an input to the index**. Nothing here may change a
transparency number, and a night with no guide log must produce the same report it produced
before, minus one card. On the validated night the split either side of the cloud onset —
0.85 arcsec RMS before, 2.40 after — corroborates the all-sky camera from a third instrument.

---

## 5. Data model

SQLite. `internal/store/schema.sql` is the source of truth, queries are in
`internal/store/queries.sql`, and sqlc generates into `internal/store/db`. `migrate.go` brings a
database made by an older build up to the schema, recording progress in `PRAGMA user_version`.

```sql
CREATE TABLE nights (
    id                INTEGER PRIMARY KEY,
    night_of          TEXT NOT NULL UNIQUE,   -- 'YYYY-MM-DD', the evening date
    log_path          TEXT NOT NULL,
    log_sha256        TEXT NOT NULL,          -- re-ingest guard, see below
    cloud_onset_at    TEXT,                   -- RFC3339, null if none detected
    cloud_source      TEXT NOT NULL,          -- 'skycam' | 'luminance' | 'unknown'
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
unchanged log and an unchanged cloud verdict is a no-op, and anything else replaces that
night's rows in a transaction. You will re-run this while developing and you must not end up
with doubled frames.

**Migration 1 (2026-10-24, skycam).** Adds `nights.cloud_source`; every night already on
record is tagged `'luminance'` and keeps counting as clear-sky history. skyq stores nothing
per frame from skycam: the Pi keeps a year of frame rows, and the carried-forward verdict
(§4.4) covers anything older. The old `sky_samples` table held the retired detector's
per-still luminance; its rows move to `legacy_sky_samples` (`night_of`, `at`, `luminance`,
`volatility`, `image_path`), keyed by night with no foreign key so re-ingesting a night cannot
cascade them away, and `sky_samples` is dropped. Nothing reads the legacy table.

---

## 6. Phase 1 — morning report

`skyq report [--night 2026-09-02]` — defaults to the night that just ended.

1. Locate the log for that night in `%LOCALAPPDATA%\NINA\Logs\`, hash it, ingest frames
   and events. Read the night's PHD2 guide logs over the same window (§4.6); a missing
   directory is a log line, never an error.
2. Read the night from skycam's sky API (§4.4): cloud onset, per-frame cloud and roof. A
   missing night or an unreachable Pi is a log line and unknown cloud, never an error.
3. Compute or load baselines, populate `index_pct`.
4. Attribute events (§4.5).
5. Render the HTML report to a configured output dir.
6. Publish: upload the self-contained report and a regenerated index page to the Linode
   over the existing deploy SSH access (`scp`/`sftp`, port 2222, user `deploy`) into the
   deepspaceplace.com web root — e.g. `/var/www/deepspaceplace/reports/YYYY-MM-DD.html`
   plus `index.html`. A publish failure must log and exit non-zero but **must not** stop
   the local report from being written — the analysis is the product, the upload is a copy.

Run it from a Windows Task Scheduler daily task at, say, 09:00 local — well after any
shutdown sequence and after skycam has processed the night at dawn.

`skyq backfill [--nights 30]` runs the same steps for recent nights, skipping any night
already ingested from an unchanged log. `--rerun` runs those too.

**The report should lead with a verdict, not a chart.** One or two sentences: how much of the
night was usable, when the sky went, what failed because of it, and anything unexplained.
Charts are supporting evidence.

Report contents, in order:

1. Verdict paragraph + stat row (index before/after onset, onset time, usable hours,
   frames kept vs lost).
2. Transparency index over time, one line per filter, with a dashed 100 baseline, autofocus
   bands shaded, target changes marked, failures as ticks on the base.
3. All-sky cloud cover from skycam over the same x-axis, with a dashed rule at the 50 %
   onset level and the stretches the roof was not open hatched.
4. PHD2 guiding over the same x-axis — per-minute RA and Dec RMS, autofocus bands, PHD2
   errors on the rail. Absent entirely on a night with no guide log.
5. All-sky thumbnails at the interesting moments — start, onset, most cloud, end. Any whose
   still skycam no longer keeps is left out.
6. Baseline table (the divisors — makes the index auditable).
7. Event timeline with attributed causes.
8. Collapsed raw frame table.

Charting: **inline SVG generated in Go**, no JS charting library. A self-contained file is the
point, so anything with thousands of points is reduced before it is plotted, never thinned by
dropping data. Separate charts sharing one x-axis — never a dual y-axis.

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

## 7. Live monitoring — moved to skycam

skyq's former Phase 2 (`skyq serve`: a stills poller, a darkness gate, a live HTMX page and an
Alpaca ObservingConditions device on `:11112`, sharing discovery port 32227 with
alpaca-switch) was removed on 2026-10-24. skycam on the Pi does all of it now:

- Its Alpaca ObservingConditions device is on `allsky.local:11111`, and N.I.N.A. already
  reads it. `CloudCover` comes from skycam's star counts, and `SkyQuality` is approximate sky
  brightness in mag/arcsec², not skyq's transparency index. skycam also passes
  OpenWeatherMap's ambient readings through.
- Its live view is on the Pi and, remotely, at skycam.mchugh.au.
- Staleness, twilight and roof-shut readings answer unavailable, never zero — the same rule
  §8 holds skyq to.

alpaca-switch no longer needs to share UDP 32227 with skyq; its shareable bind is harmless
and can stay. The Task Scheduler task for `skyq serve` is to be disabled.

---

## 8. Safety constraints — hard rules

These are not preferences. Get them wrong and the observatory pays for it.

1. **Transparency must never gate the roof.** Cloud is not a safety condition; rain is. The
   RG-11 safety monitor keeps sole ownership of the close decision. skyq exposes no device at
   all; the ObservingConditions device (advisory) lives in skycam and is **never** a
   SafetyMonitor.
2. **Advisory means skip work, not stop the observatory.** Legitimate uses: defer autofocus,
   hold off plate solving, pause a target and record the gap. Nothing that moves the roof or
   parks the mount.
3. **Unknown ≠ unsafe.** When data is stale or missing, report unknown. Consumers must treat
   unknown as "do not gate", so a dead camera never triggers observatory actions. In the
   report, unknown cloud is never clear and never cloudy (§4.4).
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

**The cloud onset is a recorded input: 03:48.** It was validated by hand from that night's
all-sky stills with the retired luminance detector (§4.4). skycam did not exist that night,
so the sky API has no data for it. The baseline, index and attribution tests take 03:48 as
given, and the end-to-end test feeds it through `testdata/sky-20260902.json` — a **hand-built**
sky API reply (synthetic cloud values, roof OPEN throughout, onset 03:48, stills only for the
four frames in `testdata/stills/`), not a recording.

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

**All-sky** — *retired.* The luminance detector's onset at 03:48, peak luminance 88.9 at 05:19
and clear-sky baseline 25–30 are kept in §4.4 as history. skyq no longer computes them, and
no test asserts them.

**Attribution** — the 00:17:12 PHD2 error classifies as `post_slew` (it follows the 00:10:39
slew by under 10 minutes); the other five classify as `cloud`; the 04:42 autofocus run flags
as untrustworthy.

`testdata/stills/` keeps four real stills from the night — clear, the onset and the cloudy
peak — so the report's thumbnails are tested on real pixels.

---

## 10. Open decisions

Settled: the stills pull mechanism, crop band, volatility threshold and exposure
normalisation all went with the luminance detector — skycam answers them on the Pi.

1. **Retention** — skycam keeps frame rows for 365 days and JPEGs for 14 nights. The NUC's
   stills cache now holds only thumbnails and stills cached before skycam; it needs its own
   pruning (a config value, e.g. 90 days) only if it grows. skyq's own rows are tiny; keep
   them indefinitely.

---

## Appendix — source material

All in `C:\Projects\cowork\nuc`:

- `20260902-201750-3.2.0.9001.8256-202609.log` — the fixture log
- `allsky-20260902.mp4`, `keogram-20260902.jpg` — that night's all-sky output
- `transparency-by-filter.html` — the validated reference report; match its structure
- `cloud-vs-errors.html`, `sky-vs-errors.html` — earlier iterations, kept only to show what
  the exposure-class and target confounds looked like before they were controlled. Do not
  use their numbers.
