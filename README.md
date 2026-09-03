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
skyq serve                            Phase 2 live monitor (not yet implemented)
```

All commands take `--config config.json` (default `./config.json`).

## Setup on the NUC

1. Build: `go build` (the main package lives at the repo root)
2. `copy config.example.json config.json` and fill in the AllSky credentials
   (`config.json` is gitignored — never commit it).
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

## Publishing

With `publish.enabled: true`, the finished report and a regenerated index
page are copied by `scp` (OpenSSH, `BatchMode`) to the Linode web root for
deepspaceplace.com. A publish failure is logged but never blocks the local
report. One-time setup on the machine that runs skyq:

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
