# skyq

Imaging analysis for the observatory. skyq parses each night's N.I.N.A. log,
computes a normalised transparency index (star counts per target × filter
against a clear-sky baseline), attributes failures to their causes, charts
the night's PHD2 guiding, and writes a self-contained HTML morning report.

The sky itself belongs to [skycam](https://github.com/exploded/skycam), which
runs on the observatory Pi at allsky.local. skyq reads each night's cloud
cover, cloud onset and roof state from skycam's sky API, and pulls the
report's thumbnails from its `/images/` folder.

See `SPEC.md` for the full design and the validated acceptance numbers.

## What runs where

| Machine | Program | Owns |
|---|---|---|
| Pi (allsky.local) | skycam | The sky: capture, cloud, onset, roof state, the live view, and the Alpaca weather device N.I.N.A. reads |
| NUC | skyq | Imaging: what got through, the transparency index, failure attribution, autofocus and guiding |
| Linode | deepspaceplace.com | The published reports |

## Commands

```
skyq report    [--night 2026-09-02]   analyse one night, write + publish the report
skyq backfill  [--nights 30] [--rerun] report recent nights whose log is new or changed
```

All commands take `--config config.json` (default `./config.json`).

`backfill` skips a night already ingested from the same log, so it never
re-renders and republishes an old report for nothing. Pass `--rerun` to run
those nights again, for example after a change to the report.

## Setup on the NUC

1. Build: `go build` (the main package lives at the repo root)
2. `copy config.example.json config.json` and fill in the paths
   (`config.json` is gitignored — never commit it).
   - `allsky_username` and `allsky_password` are skycam's `images_user` and
     `images_password`. The same Basic auth covers `/images/` and the sky API.
   - `skycam_url` may be left empty: it defaults to the scheme and host of
     `allsky_base_url`, which is where skycam runs.
   - `phd2_log_dir` may be left empty: it defaults to PHD2's own
   `%USERPROFILE%\Documents\PHD2`. Set it if PHD2 writes its guide logs
   elsewhere; a directory that is not there costs the report its guiding
   chart and nothing else.
3. Test a night: `skyq.exe report --night 2026-09-02`
4. Task Scheduler: create a daily task at 09:00 running `run-report.bat`
   (it cds to its own folder and appends output to `skyq-report.log`).
   Run it as the same Windows user N.I.N.A. runs as, and tick "Run task as
   soon as possible after a scheduled start is missed". Or from an admin
   prompt:

   ```
   schtasks /Create /TN "skyq morning report" /TR "C:\apps\skyq\run-report.bat" /SC DAILY /ST 09:00
   ```

   Deployment is just `skyq.exe`, `config.json` and `run-report.bat` in one
   folder — templates and styles are embedded in the binary, and `cache\`,
   `skyq.db` and `reports\` are created next to it.

## Cloud and roof from skycam

Each `report` run asks skycam for the night:
`GET http://allsky.local/api/nights/<YYYYMMDD>/sky`. The reply gives each
frame's cloud cover and roof state, plus the cloud onset. skyq uses the onset
as given, so the onset rule lives in skycam alone.

- **Cloud unknown.** If skycam returns 404, the Pi can't be reached, or no
  frame in the imaging session has a cloud reading, the night's cloud is
  unknown. The report says so, attributes no failure to cloud, and stores no
  clear-sky baselines from it. Unknown is never read as clear.
- **Gaps in a known night.** A failure after the onset is blamed on cloud
  only if skycam had a cloud reading within 5 minutes of it. skycam has no
  reading while the roof isn't open, while the sun is above −12°, or for the
  first ten minutes or so of a session.
- **Nights already on record** keep their stored cloud verdict if skycam has
  nothing for them. Every night before skycam has an onset from skyq's
  retired luminance detector, and that onset carries forward.
- **Roof bands.** The report hatches the stretches skycam's roll-off
  controller reported the roof as CLOSED, OPENING, CLOSING or ERROR. For a
  night skycam can't serve, the bands fall back to N.I.N.A.'s `DomeVM.cs`
  lines. Those lines miss a roof opened by hand.
- **Thumbnails.** skycam keeps stills for 14 nights and frame rows for a
  year. A thumbnail whose still has gone is left out of the report. Stills
  that skyq fetches are kept in `cache/YYYYMMDD/`.

### N.I.N.A.'s weather device

N.I.N.A. already reads skycam's Alpaca ObservingConditions device at
allsky.local:11111. Its `SkyQuality` is skycam's approximate sky brightness
in mag/arcsec², not skyq's transparency index. skyq no longer runs a
weather device or a live page.

alpaca-switch no longer needs to share UDP discovery port 32227 with skyq.
The shareable bind it gained on 2026-09-03 does no harm, so it can stay.

Disable the old Task Scheduler task for `skyq serve` on the NUC. The
`serve` subcommand and `run-serve.bat` are gone.

## Publishing

Every report carries previous/next-night links and a link back to
`index.html`, which lists all nights by month with each verdict's opening
line. Both are rebuilt after each run: neighbouring reports are relinked in
place, and any that changed are uploaded along with the new report and the
index. Reports rendered before navigation existed have no link slots — run
`skyq backfill --rerun` to regenerate them. That re-renders every night in
range, so nights before skycam lose their luminance chart; their onsets are
kept.

With `publish.enabled: true`, the finished report, the regenerated index
and any relinked neighbours are copied by `scp` (OpenSSH, `BatchMode`) to
the Linode web root for deepspaceplace.com. A publish failure is logged but
never blocks the local report. One-time setup on the machine that runs skyq:

1. `ssh-keygen -t ed25519 -f %USERPROFILE%\.ssh\skyq_publish -N "" -C skyq-publish`
   and set `publish.identity_file` to that private key path.
2. Trust the host key once: `ssh-keyscan -p 2222 194.195.254.92 >> %USERPROFILE%\.ssh\known_hosts`
3. On the Linode (root): append the `.pub` to `/home/deploy/.ssh/authorized_keys`,
   and create the target dir writable by deploy:
   `mkdir -p /var/www/deepspaceplace/reports && chown deploy:www-data /var/www/deepspaceplace/reports && chmod 2775 /var/www/deepspaceplace/reports`,
   then `usermod -aG www-data deploy`. Only the *directory* need stay
   writable: uploads land under a temporary name and are renamed over the
   target, so `deploy` never has to own the file it replaces. That matters
   because `deploy-deepspaceplace` ends with
   `chown -R www-data:www-data /var/www/deepspaceplace`, which reclaims every
   report each time the site ships — the setgid group bit survives it.
4. Add a Caddy `handle_path`/root for `/reports` under the deepspaceplace.com
   site if the web root differs from `dest`, then `systemctl reload caddy`.

## Data

- SQLite database (`skyq.db`) accumulates frames, events and baselines so
  the index becomes comparable across nights once ≥ 5 clear nights exist for
  a target × filter pair. `nights.cloud_source` records where each night's
  onset came from: `skycam`, `luminance` (before skycam) or `unknown`.
- `legacy_sky_samples` holds the per-still luminance from before skycam,
  keyed by night. Nothing reads it; it is kept so no data was lost in the
  switch.
- Stills are cached under `cache/YYYYMMDD/` and can be pruned freely.
- PHD2 guide logs are read straight from `phd2_log_dir` each run and nothing
  from them is stored. A report only shows the guiding of a night whose
  guide log is still on disk, so `skyq backfill --rerun` reaches back exactly
  as far as PHD2's own log retention allows.
