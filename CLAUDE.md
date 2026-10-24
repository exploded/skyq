# skyq — sky transparency analyser

Answers, from data the observatory already produces: how much of a normal
night got through, and which failures were the sky's fault?

skyq does imaging analysis only. **skycam** (`C:\Projects\go\skycam`,
`exploded/skycam`) on the observatory Pi owns the sky: cloud, onset, roof
state, the live view and the Alpaca weather device. skyq reads skycam's
`GET /api/nights/<YYYYMMDD>/sky` and never measures stills itself.

**SPEC.md is the contract.** The analysis was validated by hand against the
night of 2026-09-02/03 and §9 of the spec lists exact numbers the code must
reproduce — the tests encode them. Do not change thresholds, medians or
quartile conventions without re-reading §9.

## Deployment

Runs on the Windows 11 NUC (`nuc.local`) next to N.I.N.A. Reads the
N.I.N.A. and PHD2 logs locally, reads cloud, onset and roof from skycam's sky
API on `allsky.local` (basic auth), fetches only the report's thumbnail
stills, and publishes finished reports to the Linode (deepspaceplace.com) by
scp. Task Scheduler runs `skyq report` daily at 09:00. No settings UI —
config.json (gitignored, secrets) + CLI only.

## Layout

- `main.go` / `report.go` (repo root) — subcommands: report, backfill
  (`--rerun`). Root main package so `go build` works in the folder.
- `internal/skycam` — sky API client, still fetch into the cache, roof spans
  from skycam's controller states
- `internal/ninalog` — log parser, pure; patterns validated on N.I.N.A. 3.2.0.9001
- `internal/phd2` — PHD2 guide-log parser, pure; feeds the report's guiding
  chart only (SPEC §4.6)
- `internal/analysis` — baselines, index, event attribution, roof timeline
  from DomeVM lines (fallback only), guide-error binning
- `internal/store` — SQLite via sqlc (modernc, no CGO); schema.sql is the
  source of truth, and migrate.go (`PRAGMA user_version`) upgrades old
  databases
- `internal/report` — self-contained HTML report; design/ is the look's source of truth
- `internal/publish` — scp to the Linode; failure never blocks the local report

## Gotchas

- **Frame state latches at capture time, not detection time** — the filter
  wheel moves for the next exposure before the previous frame's detection
  line appears.
- **Only completed autofocus runs exclude frames.** Frames of cancelled AF
  runs are the validated "centering" class (§9's 18) — do not "fix" this.
- **Cloud unknown is never clear.** A 404, an unreachable Pi, or no cloud
  reading across the session makes the night's cloud `unknown`: no onset,
  nothing blamed on cloud, no baselines stored, not clear history. A failure
  after the onset with no skycam reading within 5 min is not cloud either.
  Use skycam's `onset` as given — never re-derive it.
- **Stored verdicts carry forward.** A night on record keeps its
  `cloud_source` and onset when skycam has nothing for it — every pre-skycam
  night is `luminance`. Don't "fix" those to unknown.
- **Roof:** skycam's per-frame roof (controller: OPEN, CLOSED, OPENING,
  CLOSING, ERROR, or `unknown` when stale) draws the report's bands;
  anything but OPEN is "not open", `unknown` is unbanded. N.I.N.A.'s
  `DomeVM.cs` lines are the fallback only for nights skycam can't serve,
  and they miss a roof opened by hand.
- **Guide RMS is taken about zero, not about the mean.** PHD2's own live
  graph de-means it; skyq does not, because drift across a minute is the
  error the morning report is looking for. A `DROP` row is a frame PHD2
  discarded, never a measurement of zero error. A night with no guide log
  gets no guiding card — never a flat line at zero.
- **Two median conventions on purpose**: baselines use median_high; index
  stats use Python-statistics median + exclusive (R-6) quartiles. §9 breaks
  otherwise.
- `internal/report/report.css` must stay byte-identical to
  `design/report.css` (a test enforces it). Chart geometry lives in
  `design/CHARTS.md` — read it before touching svg.go.
- `.local/` holds real sample data (N.I.N.A. logs, AF JSONs, PHD2 guide logs
  under `.local/PHD2/`) used by the robustness tests and the report previews;
  it is gitignored, do not commit it.
- `testdata/sky-20260902.json` is **hand-built**, not recorded: skycam did
  not exist on the validated night. Its onset is §9's recorded 03:48.

## Safety (SPEC §8 — hard rules)

Transparency is advisory. Nothing here may gate the roof or park the mount.
Unknown ≠ unsafe, and unknown is never clear or cloudy. (The Alpaca
ObservingConditions device lives in skycam now; it is never a
SafetyMonitor.)

## Workflow

- `go test ./...` — includes the §9 acceptance suite (end to end against a
  fake skycam in `report_test.go`) and sweeps over `.local/nina-logs` and
  `.local/PHD2`
- `sqlc generate` after editing store/queries.sql or schema.sql
- Repo: `exploded/skyq`, branch `main`. No CI deploy — built and run on
  the NUC (see SPEC §2).
