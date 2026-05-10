# Changelog

All notable changes to `supacrawl` will be documented here.

The format follows a simple human-readable style. This project has not made a
stable release yet.

## Unreleased

### Added

- Initial Supabase/Postgres metadata crawler.
- Local SQLite archive with FTS search.
- Optional full table-row mirror into `table_rows`.
- Local archive size reporting and per-table export.
- Supabase Storage blob download support from copied `storage.objects` rows.
- Read-only local SQL command.
- diff command for comparing two archive files (tables, policies, storage buckets).
- Read command auto-refresh policy.
- Encrypted local backup shards using age.
- OpenClaw-style `doctor`, `metadata`, `status`, JSON output, and validation
  surfaces.
