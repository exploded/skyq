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

1. Build: `go build -o skyq.exe ./cmd/skyq`
2. `copy config.example.json config.json` and fill in the AllSky credentials
   (`config.json` is gitignored — never commit it).
3. Test a night: `skyq.exe report --night 2026-09-02`
4. Task Scheduler: create a daily task at 09:00 running `skyq.exe report`
   with **Start in** set to this directory, and redirect output to a log
   file (console output is lost under the scheduler):
   `cmd /c skyq.exe report >> skyq-report.log 2>&1`

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
