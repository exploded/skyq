# skyq — sky transparency analyser

Answers, from data the observatory already produces: how much of a normal
night got through, and which failures were the sky's fault?

**SPEC.md is the contract.** The analysis was validated by hand against the
night of 2026-09-02/03 and §9 of the spec lists exact numbers the code must
reproduce — the tests encode them. Do not change thresholds, medians or
quartile conventions without re-reading §9.

## Deployment

Runs on the Windows 11 NUC (`nuc.local`) next to N.I.N.A. and
alpaca-switch.exe. Reads the N.I.N.A. log locally, pulls all-sky stills over
HTTP (basic auth) from `allsky.local`, publishes finished reports to the
Linode (deepspaceplace.com) by scp. Task Scheduler runs `skyq report` daily
at 09:00. No settings UI — config.json (gitignored, secrets) + CLI only.

## Layout

- `main.go` / `report.go` / `serve.go` (repo root) — subcommands: report,
  backfill, calibrate, serve. Root main package so `go build` works in the
  folder, like alpaca-switch.
- `internal/live` — Phase 2 engine: stills poller, darkness gate (riseset),
  staleness, live index via log tailing
- `internal/alpaca` — Alpaca ObservingConditions on :11112; discovery
  shares UDP 32227 with alpaca-switch via SO_REUSEADDR (both repos changed
  2026-09-03 — needs the shareable alpaca-switch build); management API
  included
- `internal/server` — read-only HTMX 4 live page on :8996
- `internal/ninalog` — log parser, pure; patterns validated on N.I.N.A. 3.2.0.9001
- `internal/allsky` — luminance (pure) + stills fetch/scan
- `internal/analysis` — baselines, index, cloud detection, event attribution
- `internal/store` — SQLite via sqlc (modernc, no CGO); schema.sql is the source of truth
- `internal/report` — self-contained HTML report; design/ is the look's source of truth
- `internal/publish` — scp to the Linode; failure never blocks the local report

## Gotchas

- **Frame state latches at capture time, not detection time** — the filter
  wheel moves for the next exposure before the previous frame's detection
  line appears.
- **Only completed autofocus runs exclude frames.** Frames of cancelled AF
  runs are the validated "centering" class (§9's 18) — do not "fix" this.
- **Two median conventions on purpose**: baselines use median_high; index
  stats use Python-statistics median + exclusive (R-6) quartiles. §9 breaks
  otherwise.
- `internal/report/report.css` must stay byte-identical to
  `design/report.css` (a test enforces it). Chart geometry lives in
  `design/CHARTS.md` — read it before touching svg.go.
- `.local/` holds real sample data (N.I.N.A. logs, AF JSONs, PHD2 logs) used
  by the robustness tests; it is gitignored, do not commit it.

## Safety (SPEC §8 — hard rules)

Transparency is advisory. This tool exposes ObservingConditions, never
SafetyMonitor; nothing here may gate the roof or park the mount. Unknown ≠
unsafe: stale data reports unavailable, never zero.

## Workflow

- `go test ./...` — includes the §9 acceptance suite and a sweep over
  `.local/nina-logs`
- `sqlc generate` after editing store/queries.sql or schema.sql
- Repo: `exploded/skyq`, branch `main`. No CI deploy — built and run on
  the NUC (see SPEC §2).
