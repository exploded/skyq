# skyq

Sky transparency analyser for the observatory. Parses each night's N.I.N.A.
log and the all-sky camera's stills, computes a normalised transparency
index (star counts per target × filter against a clear-sky baseline),
detects cloud onset independently of N.I.N.A., attributes failures to their
causes, and writes a self-contained HTML morning report.

See `SPEC.md` for the full design and the validated acceptance numbers.

## Commands

```
skyq report    [--night 2026-09-02]   analyse one night, write + publish the report
skyq backfill  [--nights 30]          run report for recent nights with logs
skyq calibrate [--night 2026-09-02]   suggest a volatility threshold from a clear night
skyq serve                            Phase 2 live monitor (Alpaca device + live page)
```

All commands take `--config config.json` (default `./config.json`).

## Setup on the NUC

1. Build: `go build` (the main package lives at the repo root)
2. `copy config.example.json config.json` and fill in the paths and
   coordinates (`config.json` is gitignored — never commit it). The AllSky
   `/images/` listing is open HTTP; if it ever gains Basic auth, add
   `allsky_username` / `allsky_password` back to config.json.
3. Test a night: `skyq.exe report --night 2026-09-02`
4. Task Scheduler: create a daily task at 09:00 running `run-report.bat`
   (it cds to its own folder and appends output to `skyq-report.log`).
   Run it as the same Windows user N.I.N.A. runs as, and tick "Run task as
   soon as possible after a scheduled start is missed". Or from an admin
   prompt:

   ```
   schtasks /Create /TN "skyq morning report" /TR "C:\skyq\run-report.bat" /SC DAILY /ST 09:00
   ```

   Deployment is just `skyq.exe`, `config.json` and `run-report.bat` in one
   folder — templates and styles are embedded in the binary, and `cache\`,
   `skyq.db` and `reports\` are created next to it.

## Live monitor (Phase 2)

`skyq serve` runs all night: it polls allsky.local for new stills (shared
cache with the morning report), gates cloud detection on astronomical
darkness (set `latitude`/`longitude` in config) and data freshness, tails
tonight's N.I.N.A. log for a live transparency index, and exposes:

- **Alpaca ObservingConditions** on `alpaca_addr` (default `:11112`).
  N.I.N.A. finds it by standard Alpaca UDP discovery: skyq and
  alpaca-switch share port 32227 with SO_REUSEADDR (per the Alpaca spec —
  needs an alpaca-switch build from 2026-09-03 or later; with an older
  build skyq logs a warning and only auto-discovery is lost). Discovery
  replies carry the machine's LAN address, so the API binds all
  interfaces, not loopback. Sensors: CloudCover
  (volatility vs threshold, %), SkyBrightness (instrumental luminance),
  SkyQuality (live transparency index), TimeSinceLastUpdate. Stale or
  twilight readings answer with an Alpaca error — unknown never reads as
  clear — and everything is advisory: this is never a SafetyMonitor and
  nothing here may gate the roof.
- **Live page** on `live_addr` (default `:8996`, LAN — expect one Windows
  Firewall prompt): current state, sparkline, latest still, tonight's
  events. Read-only; refreshes every 30 s.

N.I.N.A. connects one weather device at a time, so switching it from
OpenWeatherMap to skyq would normally drop temperature, humidity,
pressure, dew point and wind from the FITS headers. Set
`openweathermap_api_key` (the same key N.I.N.A. uses) and skyq passes
those through: it polls OpenWeatherMap every 10 minutes and serves the
ambient sensors alongside its own sky signals. OpenWeatherMap's cloud
cover is deliberately ignored — the all-sky volatility is the cloud
signal. With no key, the ambient sensors report not-implemented and
N.I.N.A. greys them out.

Task Scheduler: schedule `run-serve.bat` at startup with
"restart the task if it fails" enabled, same user as N.I.N.A.
Gating in sequences: use Sequencer Powerups weather expressions (e.g.
`If CloudCover > 80` → skip autofocus) — skyq only reports.

## Publishing

Every report carries previous/next-night links and a link back to
`index.html`, which lists all nights by month with each verdict's opening
line. Both are rebuilt after each run: neighbouring reports are relinked in
place, and any that changed are uploaded along with the new report and the
index. Reports rendered before navigation existed have no link slots — run
`skyq backfill` to regenerate them.

With `publish.enabled: true`, the finished report, the regenerated index
and any relinked neighbours are copied by `scp` (OpenSSH, `BatchMode`) to
the Linode web root for deepspaceplace.com. A publish failure is logged but
never blocks the local report. One-time setup on the machine that runs skyq:

1. `ssh-keygen -t ed25519 -f %USERPROFILE%\.ssh\skyq_publish -N "" -C skyq-publish`
   and set `publish.identity_file` to that private key path.
2. Trust the host key once: `ssh-keyscan -p 2222 172.105.178.43 >> %USERPROFILE%\.ssh\known_hosts`
3. On the Linode (root): append the `.pub` to `/home/deploy/.ssh/authorized_keys`,
   and create the target dir writable by deploy:
   `mkdir -p /var/www/deepspaceplace/reports && chown deploy:www-data /var/www/deepspaceplace/reports`
4. Add a Caddy `handle_path`/root for `/reports` under the deepspaceplace.com
   site if the web root differs from `dest`, then `systemctl reload caddy`.

## Data

- SQLite database (`skyq.db`) accumulates frames, events, sky samples and
  baselines so the index becomes comparable across nights once ≥ 5 clear
  nights exist for a target × filter pair.
- Stills are cached under `cache/YYYYMMDD/` and can be pruned freely;
  derived samples live in the database.
