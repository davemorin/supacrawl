# supacrawl

Use this skill when the user wants to inspect, mirror, search, export, or back
up a Supabase project into a local SQLite archive.

## Safety

- Never print secrets. Redact Postgres URLs, passwords, service role keys, JWTs,
  and anon keys.
- Do not write to the remote Supabase project.
- Prefer temporary archives under `/private/tmp` for trials unless the user asks
  for a durable archive.
- Start with `doctor` before a live sync.
- Use `sync` before `sync --full` on unfamiliar projects.
- Use `storage pull --limit` before downloading a large bucket.
- Use `status --sync never` when the user wants an archive-only read.
- Use encrypted backups only with age recipients or identities supplied through
  config, files, or environment variables.

## Quick Workflow

```bash
supacrawl init --project-id <label>
supacrawl doctor --json
supacrawl sync --json
supacrawl status --json
supacrawl diff /path/to/older-archive.db --json
supacrawl size --json
```

Use diff against a copy of the archive made before the sync you want to compare
against.

Read commands auto-refresh stale metadata by default. Override the policy when
needed:

```bash
supacrawl status --sync never --json
supacrawl report --sync always --json
supacrawl search --stale-after 1h "auth.uid"
```

For a full local database copy:

```bash
supacrawl sync --full --batch-size 2000
```

For a smaller archive:

```bash
supacrawl sync --full --no-row-fts --batch-size 2000
```

Export one table:

```bash
supacrawl export --type jsonl --out companies.jsonl public.companies
```

Download Storage blobs after a full sync:

```bash
supacrawl storage pull --dir ./supabase-storage --limit 10
```

Create and inspect an encrypted local backup:

```bash
supacrawl backup keygen --out ~/.supacrawl/age.key
supacrawl backup init --recipient age1...
supacrawl backup push --json
supacrawl backup status --json
supacrawl backup pull --out ./supacrawl-restore --json
```

## Local Analysis

Use read-only SQL against the archive:

```bash
supacrawl sql "select schema_name, name, rls_enabled from tables order by schema_name, name"
supacrawl sql "select json_extract(row_json, '$.name') from table_rows where schema_name='public' and table_name='companies' limit 20"
```

## Expected Archive Tables

- `schemas`
- `tables`
- `columns`
- `indexes`
- `constraints`
- `policies`
- `functions`
- `triggers`
- `storage_buckets`
- `storage_object_stats`
- `crawl_runs`
- `data_copy_runs`
- `search_docs`
- `table_rows`

## Validation

Before declaring the tool healthy, run:

```bash
./scripts/validate-local.sh
```

If `SUPABASE_DB_URL` is set, the validator also runs live `doctor` and metadata
sync checks. Set `SUPACRAWL_VALIDATE_FULL=1` only when a full data copy is safe.
