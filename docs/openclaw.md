# OpenClaw Compatibility

`supacrawl` follows the local-first crawler shape used by OpenClaw crawler
tools:

- config and runtime files live under `~/.supacrawl/` by default
- `doctor` is the fastest sanity check
- `status --json` and `metadata --json` are stable launcher/agent surfaces
- `sync` is metadata-first and `sync --full` is explicit for row copies
- read commands auto-refresh stale metadata and allow `--sync never` for strict
  local-only reads
- source credentials are read from environment variables, not persisted by
  default
- copied data is local SQLite and exposed through read-only SQL
- Storage blobs are pulled by an explicit `storage pull` command
- backups are local encrypted JSONL gzip shards and an unsecret manifest

## Agent Rules

- Do not print connection strings, service role keys, or JWT secrets.
- Do not print age private identities.
- Use `doctor` before a live `sync`.
- Use `sync` before `sync --full` when inspecting a new project.
- Prefer `sync --full --no-row-fts` for large projects when row search is not
  needed.
- Use `size` before running expensive exports or blob downloads.
- Use `storage pull --limit` for the first Storage validation.
- Use `status --sync never` when you need to inspect only the archive already
  on disk.
- Use `backup keygen`, `backup init`, `backup push`, `backup status`, and
  `backup pull` for local encrypted backup checks.
- Use read-only `sql` against the local archive for analysis.

## Runtime Paths

- config: `~/.supacrawl/config.toml`
- database: `~/.supacrawl/supacrawl.db`
- logs: `~/.supacrawl/logs/`
- backups: `~/.supacrawl/backups/`
- age identity: `~/.supacrawl/age.key`

## Environment Variables

- `SUPABASE_DB_URL` for Postgres metadata and row sync
- `SUPABASE_URL` or `NEXT_PUBLIC_SUPABASE_URL` for Storage downloads
- `SUPABASE_SECRET_KEY` for private Storage downloads
- `SUPABASE_SERVICE_ROLE_KEY` is still supported as a legacy fallback
- `SUPACRAWL_AGE_RECIPIENT` for backup encryption
- `SUPACRAWL_AGE_IDENTITY` for backup restore
