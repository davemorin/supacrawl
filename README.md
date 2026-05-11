# supacrawl

`supacrawl` mirrors Supabase/Postgres project metadata into local SQLite so you can search, inspect, and query your backend without poking around the Supabase dashboard.

It follows the local-first shape of tools like `discrawl` and `slacrawl`: crawl once, keep the archive on your machine, use fast local search, and run read-only SQL against the snapshot.

`supacrawl` is part of the OpenClaw crawler family and is intended for teams,
maintainers, and agents that need fast local access to Supabase project shape,
policies, functions, Storage inventory, and copied table rows.

## Included

- local SQLite archive with FTS5 search
- Supabase/Postgres schema crawl over a normal Postgres connection URL
- schemas, tables, columns, indexes, constraints, RLS policies, functions, triggers, extensions
- optional full table row mirror into SQLite as schema-agnostic JSON rows
- optional row-copy mode without FTS for smaller archives
- local archive size reports
- automatic metadata refresh for read commands, with opt-out flags
- per-table JSONL/CSV export from the local copy
- Storage blob downloads from copied `storage.objects` rows
- encrypted local backup shards with age
- Supabase Storage buckets and object counts when the `storage` schema is visible
- `doctor` diagnostics for local archive and database connectivity
- `metadata --json`, `status --json`, `doctor --json`, and read-only `sql`
- JSON output for automation with `--json` or `--format json`

## Not Yet Included

- Supabase Management API crawl for edge functions, auth settings, branches, or secrets
- git-backed archive publish/subscribe
- terminal UI
- Homebrew/release packaging
- git-backed snapshot sharing through `crawlkit`
- terminal archive UI through `crawlkit`

## Requirements

- Go 1.26+
- a Supabase direct or pooler Postgres connection URL
- enough Postgres privileges to read catalog metadata

## Install

Homebrew:

```bash
brew install davemorin/tap/supacrawl
```

Build from source:

```bash
git clone https://github.com/davemorin/supacrawl.git
cd supacrawl
go build -o bin/supacrawl ./cmd/supacrawl
./bin/supacrawl version
```

## Quick Start

```bash
export SUPABASE_DB_URL="postgres://postgres.<ref>:<password>@aws-0-us-west-1.pooler.supabase.com:6543/postgres?sslmode=require"

go run ./cmd/supacrawl init --project-id <ref>
go run ./cmd/supacrawl doctor
go run ./cmd/supacrawl metadata --json
go run ./cmd/supacrawl sync
go run ./cmd/supacrawl sync --full
go run ./cmd/supacrawl sync --full --no-row-fts
go run ./cmd/supacrawl status
go run ./cmd/supacrawl status --sync never
go run ./cmd/supacrawl size
go run ./cmd/supacrawl search "auth policies"
go run ./cmd/supacrawl diff ~/.supacrawl/supacrawl-before.db
go run ./cmd/supacrawl export --type jsonl --out companies.jsonl public.companies
go run ./cmd/supacrawl storage pull --dir ./supabase-storage --limit 10
go run ./cmd/supacrawl backup keygen --out ~/.supacrawl/age.key
go run ./cmd/supacrawl backup init --recipient age1...
go run ./cmd/supacrawl backup push
go run ./cmd/supacrawl backup status
go run ./cmd/supacrawl sql "select schema_name, name, rls_enabled from tables order by schema_name, name limit 20"
go run ./cmd/supacrawl sql "select row_json from table_rows where schema_name = 'public' and table_name = 'companies' limit 5"
```

If you build the binary, replace `go run ./cmd/supacrawl` with `./bin/supacrawl`.

## Commands

- `init` creates `~/.supacrawl/config.toml`
- `doctor` checks the local SQLite archive and Supabase/Postgres connection
- `metadata` prints command/capability metadata for launchers and agents
- `version` prints the CLI version
- `sync` crawls metadata into the local archive
- `sync --data` or `sync --full` also copies base table rows into `table_rows`
- `status` prints archive counts
- `report` summarizes schemas and policy coverage
- `diff` compares the current archive to another archive file
- `size` reports archive file size and largest copied source tables
- `search` searches crawled tables, functions, storage buckets, and extensions
- `export` writes copied source rows for one table as JSONL or CSV
- `storage pull` downloads Storage blobs into a local directory
- `backup keygen` creates an age identity and prints the public recipient
- `backup init` stores local backup settings in config
- `backup push` writes encrypted JSONL gzip shards plus a manifest
- `backup status` prints backup manifest metadata
- `backup pull` decrypts backup shards into a local restore directory
- `sql` runs read-only SQL against the local SQLite archive

## Configuration

Default config path:

```text
~/.supacrawl/config.toml
```

Default archive path:

```text
~/.supacrawl/supacrawl.db
```

The default secret handling expects the connection string in `SUPABASE_DB_URL`. You can change that during init:

```bash
supacrawl init --database-url-env MY_SUPABASE_DB_URL
```

`--database-url` is available for local experiments, but the env-var flow is the safer default because the config file is durable.

Storage downloads also need:

```bash
export SUPABASE_URL="https://<ref>.supabase.co"
export SUPABASE_SECRET_KEY="sb_secret_..."
```

If `SUPABASE_URL` is not set, `supacrawl` will also try `NEXT_PUBLIC_SUPABASE_URL`.
If `SUPABASE_SECRET_KEY` is not set, `supacrawl` will also try the legacy
`SUPABASE_SERVICE_ROLE_KEY` name.

Supabase API keys are separate from the Postgres connection string. Use
`SUPABASE_DB_URL` for metadata and row sync. Use `SUPABASE_SECRET_KEY` only for
private Storage downloads.

## Read Freshness

Read commands refresh metadata automatically when the local snapshot is stale:

```bash
supacrawl status
supacrawl search "profiles"
supacrawl sql "select count(*) from tables"
```

The default policy is `auto` with a `15m` staleness window. If the database URL
is not available, `auto` continues with the local archive. Use explicit flags
when you need a deterministic read:

```bash
supacrawl status --sync never
supacrawl report --sync always
supacrawl search --stale-after 1h "auth.uid"
```

## Full Local Copy Mode

`sync --full` mirrors accessible base-table rows into SQLite:

```bash
supacrawl sync --full
```

Rows are stored as JSON in `table_rows`:

```sql
select
  schema_name,
  table_name,
  json_extract(row_json, '$.id') as id
from table_rows
where schema_name = 'public'
limit 20
```

This makes the first full-copy mode robust across arbitrary Supabase schemas. It is not yet a byte-for-byte Supabase backup: Edge Function source and project dashboard settings are still future work.

Use `--no-row-fts` when archive size matters more than full-text search over row JSON:

```bash
supacrawl sync --full --no-row-fts
```

Use `--exclude-table` for pathological tables you want to skip during row copy:

```bash
supacrawl sync --full --no-row-fts --exclude-table public.enrichments
```

## Diff

Compare the current archive against another SQLite archive file:

```bash
supacrawl diff <other-archive.db>
```

The first diff compares tables, policies, and Storage buckets, including RLS,
policy expressions, and public bucket changes.

## Export

```bash
supacrawl export --type jsonl --out companies.jsonl public.companies
supacrawl export --type csv --out companies.csv public.companies
```

## Storage Blobs

After a full sync has copied `storage.objects`, download blobs:

```bash
supacrawl storage pull --dir ./supabase-storage
```

The downloader preserves bucket/object paths under the target directory.

## Encrypted Backups

Generate a local age identity once:

```bash
supacrawl backup keygen --out ~/.supacrawl/age.key
```

Store the public recipient in config or in `SUPACRAWL_AGE_RECIPIENT`:

```bash
supacrawl backup init --recipient age1...
```

Create an encrypted backup from the SQLite archive:

```bash
supacrawl backup push
supacrawl backup status
```

Restore decrypted JSONL gzip shards to a directory:

```bash
supacrawl backup pull --out ./supacrawl-restore
```

`backup push` is local-only today. It writes encrypted shards under
`~/.supacrawl/backups` by default and does not push to a remote.

## OpenClaw Compatibility

`supacrawl` is designed to fit the OpenClaw local-first crawler family:

- `doctor` is the fastest live sanity check.
- `metadata --json`, `status --json`, and `doctor --json` are stable surfaces for launchers, agents, and CI.
- `sync` is metadata-first; `sync --full` is explicit for copying rows.
- `sync --full` prints per-table progress to stderr so JSON stdout stays parseable.
- read commands default to a bounded auto-refresh policy and accept `--sync never`.
- backups are encrypted local shards; private identities are read from env or disk.
- Secrets are resolved from environment variables and are not written into the archive by default.
- Analysis happens against local SQLite through read-only `sql`.

See [docs/openclaw.md](docs/openclaw.md) for agent rules and convention notes.

## Output Modes

```bash
supacrawl status --json
supacrawl --format json search "profiles"
supacrawl --format log sync
```

## Local Development

```bash
go test ./...
go vet ./...
go build -o bin/supacrawl ./cmd/supacrawl
./scripts/validate-local.sh
```
