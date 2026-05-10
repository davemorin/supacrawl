package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davemorin/supacrawl/internal/postgres"
	"github.com/davemorin/supacrawl/internal/search"
	_ "modernc.org/sqlite"
)

var ArchiveTableNames = []string{
	"project_info",
	"schemas",
	"tables",
	"columns",
	"indexes",
	"constraints",
	"policies",
	"functions",
	"triggers",
	"extensions",
	"storage_buckets",
	"storage_object_stats",
	"crawl_runs",
	"data_copy_runs",
	"search_docs",
	"table_rows",
}

const schema = `
pragma foreign_keys = on;
pragma journal_mode = wal;
pragma busy_timeout = 5000;

create table if not exists project_info (
  id text primary key check (id = 'default'),
  project_id text not null,
  database_name text not null,
  current_user text not null,
  server_version text not null,
  collected_at text not null
);

create table if not exists schemas (
  name text primary key,
  owner text not null,
  type text not null,
  raw_json text not null,
  updated_at text not null
);

create table if not exists tables (
  schema_name text not null,
  name text not null,
  kind text not null,
  owner text not null,
  comment text not null,
  rls_enabled integer not null,
  rls_forced integer not null,
  estimated_rows integer not null,
  raw_json text not null,
  updated_at text not null,
  primary key (schema_name, name)
);

create table if not exists columns (
  table_schema text not null,
  table_name text not null,
  name text not null,
  ordinal integer not null,
  data_type text not null,
  is_nullable integer not null,
  default_value text not null,
  comment text not null,
  raw_json text not null,
  updated_at text not null,
  primary key (table_schema, table_name, name)
);

create table if not exists indexes (
  schema_name text not null,
  table_name text not null,
  name text not null,
  definition text not null,
  raw_json text not null,
  updated_at text not null,
  primary key (schema_name, table_name, name)
);

create table if not exists constraints (
  schema_name text not null,
  table_name text not null,
  name text not null,
  type text not null,
  definition text not null,
  raw_json text not null,
  updated_at text not null,
  primary key (schema_name, table_name, name)
);

create table if not exists policies (
  schema_name text not null,
  table_name text not null,
  name text not null,
  command text not null,
  roles text not null,
  using_expr text not null,
  check_expr text not null,
  raw_json text not null,
  updated_at text not null,
  primary key (schema_name, table_name, name)
);

create table if not exists functions (
  schema_name text not null,
  name text not null,
  identity_args text not null,
  returns text not null,
  language text not null,
  security_definer integer not null,
  definition text not null,
  raw_json text not null,
  updated_at text not null,
  primary key (schema_name, name, identity_args)
);

create table if not exists triggers (
  schema_name text not null,
  table_name text not null,
  name text not null,
  timing text not null,
  events text not null,
  function_name text not null,
  definition text not null,
  raw_json text not null,
  updated_at text not null,
  primary key (schema_name, table_name, name)
);

create table if not exists extensions (
  name text primary key,
  schema_name text not null,
  version text not null,
  comment text not null,
  raw_json text not null,
  updated_at text not null
);

create table if not exists storage_buckets (
  id text primary key,
  name text not null,
  public integer not null,
  file_size_limit text not null,
  allowed_mime_types text not null,
  created_at text not null,
  updated_bucket_at text not null,
  raw_json text not null,
  updated_at text not null
);

create table if not exists storage_object_stats (
  bucket_id text primary key,
  object_count integer not null,
  total_bytes integer not null,
  raw_json text not null,
  updated_at text not null
);

create table if not exists crawl_runs (
  id integer primary key autoincrement,
  source_name text not null,
  database_name text not null,
  started_at text not null,
  finished_at text not null,
  error text not null
);

create table if not exists table_rows (
  row_key text primary key,
  schema_name text not null,
  table_name text not null,
  row_number integer not null,
  row_json text not null,
  copied_at text not null
);

create virtual table if not exists table_row_fts using fts5(row_key unindexed, schema_name unindexed, table_name unindexed, row_json);

create table if not exists data_copy_runs (
  id integer primary key autoincrement,
  started_at text not null,
  finished_at text not null,
  table_count integer not null,
  row_count integer not null,
  error text not null
);

create table if not exists search_docs (
  doc_key text primary key,
  kind text not null,
  title text not null,
  body text not null,
  updated_at text not null
);

create virtual table if not exists search_fts using fts5(doc_key unindexed, kind unindexed, title, body);

create index if not exists idx_tables_schema_kind on tables(schema_name, kind);
create index if not exists idx_columns_table on columns(table_schema, table_name, ordinal);
create index if not exists idx_policies_table on policies(schema_name, table_name);
create index if not exists idx_functions_schema on functions(schema_name, name);
create index if not exists idx_table_rows_table on table_rows(schema_name, table_name, row_number);
`

type Store struct {
	db   *sql.DB
	path string
}

type Status struct {
	ProjectID      string    `json:"project_id"`
	DatabaseName   string    `json:"database_name"`
	CollectedAt    time.Time `json:"collected_at"`
	Schemas        int       `json:"schemas"`
	Tables         int       `json:"tables"`
	Columns        int       `json:"columns"`
	Indexes        int       `json:"indexes"`
	Constraints    int       `json:"constraints"`
	Policies       int       `json:"policies"`
	Functions      int       `json:"functions"`
	Triggers       int       `json:"triggers"`
	Extensions     int       `json:"extensions"`
	StorageBuckets int       `json:"storage_buckets"`
	StorageObjects int64     `json:"storage_objects"`
	DataTables     int       `json:"data_tables"`
	DataRows       int64     `json:"data_rows"`
}

type SearchResult struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

type SQLResult struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

type Report struct {
	Status       Status        `json:"status"`
	SchemaTables []SchemaCount `json:"schema_tables"`
	PolicyTables []PolicyCount `json:"policy_tables"`
}

type ArchiveSize struct {
	DBPath        string            `json:"db_path"`
	FileBytes     int64             `json:"file_bytes"`
	WALBytes      int64             `json:"wal_bytes"`
	SHMBytes      int64             `json:"shm_bytes"`
	SQLiteBytes   int64             `json:"sqlite_bytes"`
	PageCount     int64             `json:"page_count"`
	PageSize      int64             `json:"page_size"`
	FreelistPages int64             `json:"freelist_pages"`
	RowJSONBytes  int64             `json:"row_json_bytes"`
	SourceTables  []SourceTableSize `json:"source_tables"`
}

type SourceTableSize struct {
	Schema       string `json:"schema"`
	Table        string `json:"table"`
	Rows         int64  `json:"rows"`
	RowJSONBytes int64  `json:"row_json_bytes"`
}

type ExportRow struct {
	RowNumber int64  `json:"row_number"`
	JSON      string `json:"json"`
}

type StorageObject struct {
	BucketID string `json:"bucket_id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
}

type SchemaCount struct {
	Schema string `json:"schema"`
	Tables int    `json:"tables"`
}

type PolicyCount struct {
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Policies int    `json:"policies"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) PutSnapshot(ctx context.Context, snapshot postgres.Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	started := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert into crawl_runs(source_name, database_name, started_at, finished_at, error) values(?,?,?,?,?)`, "postgres", snapshot.Project.DatabaseName, started, "", ""); err != nil {
		return err
	}

	for _, table := range []string{
		"project_info", "schemas", "tables", "columns", "indexes", "constraints", "policies",
		"functions", "triggers", "extensions", "storage_buckets", "storage_object_stats",
		"search_docs", "search_fts",
	} {
		if _, err := tx.ExecContext(ctx, "delete from "+table); err != nil {
			return err
		}
	}

	collectedAt := snapshot.Project.CollectedAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert into project_info(id, project_id, database_name, current_user, server_version, collected_at) values('default',?,?,?,?,?)`,
		snapshot.Project.ID, snapshot.Project.DatabaseName, snapshot.Project.CurrentUser, snapshot.Project.ServerVersion, collectedAt); err != nil {
		return err
	}
	for _, row := range snapshot.Schemas {
		if _, err := execJSON(ctx, tx, `insert into schemas(name, owner, type, raw_json, updated_at) values(?,?,?,?,?)`, row, row.Name, row.Owner, row.Type, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Tables {
		if _, err := execJSON(ctx, tx, `insert into tables(schema_name, name, kind, owner, comment, rls_enabled, rls_forced, estimated_rows, raw_json, updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
			row, row.Schema, row.Name, row.Kind, row.Owner, row.Comment, boolInt(row.RLSEnabled), boolInt(row.RLSForced), row.EstimatedRows, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Columns {
		if _, err := execJSON(ctx, tx, `insert into columns(table_schema, table_name, name, ordinal, data_type, is_nullable, default_value, comment, raw_json, updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
			row, row.TableSchema, row.TableName, row.Name, row.Ordinal, row.DataType, boolInt(row.IsNullable), row.Default, row.Comment, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Indexes {
		if _, err := execJSON(ctx, tx, `insert into indexes(schema_name, table_name, name, definition, raw_json, updated_at) values(?,?,?,?,?,?)`,
			row, row.Schema, row.TableName, row.Name, row.Definition, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Constraints {
		if _, err := execJSON(ctx, tx, `insert into constraints(schema_name, table_name, name, type, definition, raw_json, updated_at) values(?,?,?,?,?,?,?)`,
			row, row.Schema, row.TableName, row.Name, row.Type, row.Definition, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Policies {
		if _, err := execJSON(ctx, tx, `insert into policies(schema_name, table_name, name, command, roles, using_expr, check_expr, raw_json, updated_at) values(?,?,?,?,?,?,?,?,?)`,
			row, row.Schema, row.TableName, row.Name, row.Command, row.Roles, row.Using, row.Check, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Functions {
		if _, err := execJSON(ctx, tx, `insert into functions(schema_name, name, identity_args, returns, language, security_definer, definition, raw_json, updated_at) values(?,?,?,?,?,?,?,?,?)`,
			row, row.Schema, row.Name, row.IdentityArgs, row.Returns, row.Language, boolInt(row.SecurityDefiner), row.Definition, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Triggers {
		if _, err := execJSON(ctx, tx, `insert into triggers(schema_name, table_name, name, timing, events, function_name, definition, raw_json, updated_at) values(?,?,?,?,?,?,?,?,?)`,
			row, row.Schema, row.TableName, row.Name, row.Timing, row.Events, row.FunctionName, row.Definition, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Extensions {
		if _, err := execJSON(ctx, tx, `insert into extensions(name, schema_name, version, comment, raw_json, updated_at) values(?,?,?,?,?,?)`,
			row, row.Name, row.Schema, row.Version, row.Comment, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.StorageBuckets {
		if _, err := execJSON(ctx, tx, `insert into storage_buckets(id, name, public, file_size_limit, allowed_mime_types, created_at, updated_bucket_at, raw_json, updated_at) values(?,?,?,?,?,?,?,?,?)`,
			row, row.ID, row.Name, boolInt(row.Public), row.FileSizeLimit, row.AllowedMimeTypes, row.CreatedAt, row.UpdatedAt, ts(now)); err != nil {
			return err
		}
	}
	for _, row := range snapshot.StorageObjectStats {
		if _, err := execJSON(ctx, tx, `insert into storage_object_stats(bucket_id, object_count, total_bytes, raw_json, updated_at) values(?,?,?,?,?)`,
			row, row.BucketID, row.ObjectCount, row.TotalBytes, ts(now)); err != nil {
			return err
		}
	}
	if err := insertSearchDocs(ctx, tx, snapshot, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update crawl_runs set finished_at = ? where id = (select max(id) from crawl_runs)`, ts(now)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginDataCopy(ctx context.Context, includeRowFTS bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"table_rows", "table_row_fts"} {
		if _, err := tx.ExecContext(ctx, "delete from "+table); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into data_copy_runs(started_at, finished_at, table_count, row_count, error) values(?,?,?,?,?)`, ts(time.Now().UTC()), "", 0, 0, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutDataRows(ctx context.Context, rows []postgres.TableRow, includeRowFTS bool) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	copiedAt := ts(time.Now().UTC())
	for _, row := range rows {
		key := fmt.Sprintf("%s.%s#%d", row.Schema, row.TableName, row.RowNumber)
		if _, err := tx.ExecContext(ctx, `insert into table_rows(row_key, schema_name, table_name, row_number, row_json, copied_at) values(?,?,?,?,?,?)`,
			key, row.Schema, row.TableName, row.RowNumber, row.JSON, copiedAt); err != nil {
			return err
		}
		if includeRowFTS {
			if _, err := tx.ExecContext(ctx, `insert into table_row_fts(row_key, schema_name, table_name, row_json) values(?,?,?,?)`,
				key, row.Schema, row.TableName, row.JSON); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) FinishDataCopy(ctx context.Context, stats postgres.DataCopyStats, copyErr error) error {
	errText := ""
	if copyErr != nil {
		errText = copyErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `
update data_copy_runs
set finished_at = ?, table_count = ?, row_count = ?, error = ?
where id = (select max(id) from data_copy_runs)`, ts(time.Now().UTC()), stats.Tables, stats.Rows, errText)
	if err != nil {
		return err
	}
	return copyErr
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	var collectedAt string
	row := s.db.QueryRowContext(ctx, `select project_id, database_name, collected_at from project_info where id = 'default'`)
	if err := row.Scan(&status.ProjectID, &status.DatabaseName, &collectedAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Status{}, err
	}
	if collectedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, collectedAt)
		if err == nil {
			status.CollectedAt = parsed
		}
	}
	counts := map[string]*int{
		"schemas":         &status.Schemas,
		"tables":          &status.Tables,
		"columns":         &status.Columns,
		"indexes":         &status.Indexes,
		"constraints":     &status.Constraints,
		"policies":        &status.Policies,
		"functions":       &status.Functions,
		"triggers":        &status.Triggers,
		"extensions":      &status.Extensions,
		"storage_buckets": &status.StorageBuckets,
	}
	for table, dest := range counts {
		if err := s.db.QueryRowContext(ctx, "select count(*) from "+table).Scan(dest); err != nil {
			return Status{}, err
		}
	}
	if err := s.db.QueryRowContext(ctx, `select coalesce(sum(object_count), 0) from storage_object_stats`).Scan(&status.StorageObjects); err != nil {
		return Status{}, err
	}
	if err := s.db.QueryRowContext(ctx, `select count(distinct schema_name || '.' || table_name) from table_rows`).Scan(&status.DataTables); err != nil {
		return Status{}, err
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from table_rows`).Scan(&status.DataRows); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (s *Store) Report(ctx context.Context) (Report, error) {
	status, err := s.Status(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Status: status}
	rows, err := s.db.QueryContext(ctx, `select schema_name, count(*) from tables group by schema_name order by schema_name`)
	if err != nil {
		return Report{}, err
	}
	for rows.Next() {
		var row SchemaCount
		if err := rows.Scan(&row.Schema, &row.Tables); err != nil {
			rows.Close()
			return Report{}, err
		}
		report.SchemaTables = append(report.SchemaTables, row)
	}
	if err := rows.Close(); err != nil {
		return Report{}, err
	}

	rows, err = s.db.QueryContext(ctx, `select schema_name, table_name, count(*) from policies group by schema_name, table_name order by count(*) desc, schema_name, table_name limit 20`)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row PolicyCount
		if err := rows.Scan(&row.Schema, &row.Table, &row.Policies); err != nil {
			return Report{}, err
		}
		report.PolicyTables = append(report.PolicyTables, row)
	}
	return report, rows.Err()
}

func (s *Store) Size(ctx context.Context, dbPath string, limit int) (ArchiveSize, error) {
	if limit <= 0 {
		limit = 20
	}
	size := ArchiveSize{DBPath: dbPath}
	size.FileBytes = fileSize(dbPath)
	size.WALBytes = fileSize(dbPath + "-wal")
	size.SHMBytes = fileSize(dbPath + "-shm")
	if err := s.db.QueryRowContext(ctx, `select page_count from pragma_page_count()`).Scan(&size.PageCount); err != nil {
		return ArchiveSize{}, err
	}
	if err := s.db.QueryRowContext(ctx, `select page_size from pragma_page_size()`).Scan(&size.PageSize); err != nil {
		return ArchiveSize{}, err
	}
	if err := s.db.QueryRowContext(ctx, `select freelist_count from pragma_freelist_count()`).Scan(&size.FreelistPages); err != nil {
		return ArchiveSize{}, err
	}
	size.SQLiteBytes = size.PageCount * size.PageSize
	if err := s.db.QueryRowContext(ctx, `select coalesce(sum(length(row_json)), 0) from table_rows`).Scan(&size.RowJSONBytes); err != nil {
		return ArchiveSize{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
select schema_name, table_name, count(*) as rows, coalesce(sum(length(row_json)), 0) as bytes
from table_rows
group by schema_name, table_name
order by bytes desc, rows desc
limit ?`, limit)
	if err != nil {
		return ArchiveSize{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row SourceTableSize
		if err := rows.Scan(&row.Schema, &row.Table, &row.Rows, &row.RowJSONBytes); err != nil {
			return ArchiveSize{}, err
		}
		size.SourceTables = append(size.SourceTables, row)
	}
	return size, rows.Err()
}

func (s *Store) ExportRows(ctx context.Context, schemaName, tableName string, limit int) ([]ExportRow, error) {
	query := `select row_number, row_json from table_rows where schema_name = ? and table_name = ? order by row_number`
	args := []any{schemaName, tableName}
	if limit > 0 {
		query += ` limit ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportRow
	for rows.Next() {
		var row ExportRow
		if err := rows.Scan(&row.RowNumber, &row.JSON); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) ListStorageObjects(ctx context.Context, bucket string, limit int) ([]StorageObject, error) {
	query := `
select
  coalesce(json_extract(row_json, '$.bucket_id'), ''),
  coalesce(json_extract(row_json, '$.name'), ''),
  cast(coalesce(json_extract(row_json, '$.metadata.size'), 0) as integer)
from table_rows
where schema_name = 'storage'
  and table_name = 'objects'
  and (? = '' or json_extract(row_json, '$.bucket_id') = ?)
order by row_number`
	args := []any{bucket, bucket}
	if limit > 0 {
		query += ` limit ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StorageObject
	for rows.Next() {
		var row StorageObject
		if err := rows.Scan(&row.BucketID, &row.Name, &row.Size); err != nil {
			return nil, err
		}
		if row.BucketID != "" && row.Name != "" {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (s *Store) Search(ctx context.Context, query string, kind string, limit int) ([]SearchResult, error) {
	ftsQuery := search.BuildFTSQuery(query)
	if ftsQuery == "" {
		return nil, errors.New("search query is empty")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
select kind, doc_key, title, snippet(search_fts, 3, '', '', '...', 12)
from search_fts
where search_fts match ?
  and (? = '' or kind = ?)
order by rank
limit ?`, ftsQuery, kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		var row SearchResult
		if err := rows.Scan(&row.Kind, &row.Key, &row.Title, &row.Snippet); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) QueryReadOnly(ctx context.Context, query string) (SQLResult, error) {
	if !isReadOnlyQuery(query) {
		return SQLResult{}, errors.New("only read-only select, with, and pragma queries are allowed")
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return SQLResult{}, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return SQLResult{}, err
	}
	result := SQLResult{Columns: columns}
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return SQLResult{}, err
		}
		row := make([]string, len(columns))
		for i, value := range values {
			if value.Valid {
				row[i] = value.String
			}
		}
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}

func (s *Store) WriteTableJSONL(ctx context.Context, table string, w io.Writer) (int64, error) {
	if !isArchiveTable(table) {
		return 0, fmt.Errorf("unsupported archive table %q", table)
	}
	rows, err := s.db.QueryContext(ctx, "select * from "+table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return count, err
		}
		obj := make(map[string]any, len(columns))
		for i, col := range columns {
			obj[col] = normalizeSQLiteValue(values[i])
		}
		line, err := json.Marshal(obj)
		if err != nil {
			return count, err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func execJSON(ctx context.Context, tx *sql.Tx, query string, raw any, args ...any) (sql.Result, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	allArgs := make([]any, 0, len(args)+1)
	if len(args) == 0 {
		allArgs = append(allArgs, string(data))
	} else {
		allArgs = append(allArgs, args[:len(args)-1]...)
		allArgs = append(allArgs, string(data), args[len(args)-1])
	}
	return tx.ExecContext(ctx, query, allArgs...)
}

func insertSearchDocs(ctx context.Context, tx *sql.Tx, snapshot postgres.Snapshot, updatedAt time.Time) error {
	for _, doc := range buildDocs(snapshot, updatedAt) {
		if _, err := tx.ExecContext(ctx, `insert into search_docs(doc_key, kind, title, body, updated_at) values(?,?,?,?,?)`, doc.Key, doc.Kind, doc.Title, doc.Body, ts(updatedAt)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into search_fts(doc_key, kind, title, body) values(?,?,?,?)`, doc.Key, doc.Kind, doc.Title, doc.Body); err != nil {
			return err
		}
	}
	return nil
}

type doc struct {
	Key   string
	Kind  string
	Title string
	Body  string
}

func buildDocs(snapshot postgres.Snapshot, updatedAt time.Time) []doc {
	var docs []doc
	for _, row := range snapshot.Schemas {
		docs = append(docs, doc{
			Key:   "schema:" + row.Name,
			Kind:  "schema",
			Title: row.Name,
			Body:  strings.Join([]string{"schema", row.Name, row.Owner, row.Type}, "\n"),
		})
	}
	for _, row := range snapshot.Tables {
		title := row.Schema + "." + row.Name
		body := []string{"table", title, row.Kind, row.Owner, row.Comment, fmt.Sprintf("rls_enabled=%t", row.RLSEnabled), fmt.Sprintf("estimated_rows=%d", row.EstimatedRows)}
		for _, col := range snapshot.Columns {
			if col.TableSchema == row.Schema && col.TableName == row.Name {
				body = append(body, fmt.Sprintf("column %s %s nullable=%t default=%s %s", col.Name, col.DataType, col.IsNullable, col.Default, col.Comment))
			}
		}
		for _, policy := range snapshot.Policies {
			if policy.Schema == row.Schema && policy.TableName == row.Name {
				body = append(body, fmt.Sprintf("policy %s command=%s roles=%s using=%s check=%s", policy.Name, policy.Command, policy.Roles, policy.Using, policy.Check))
			}
		}
		for _, constraint := range snapshot.Constraints {
			if constraint.Schema == row.Schema && constraint.TableName == row.Name {
				body = append(body, fmt.Sprintf("constraint %s %s %s", constraint.Name, constraint.Type, constraint.Definition))
			}
		}
		for _, index := range snapshot.Indexes {
			if index.Schema == row.Schema && index.TableName == row.Name {
				body = append(body, fmt.Sprintf("index %s %s", index.Name, index.Definition))
			}
		}
		for _, trigger := range snapshot.Triggers {
			if trigger.Schema == row.Schema && trigger.TableName == row.Name {
				body = append(body, fmt.Sprintf("trigger %s %s %s %s", trigger.Name, trigger.Timing, trigger.Events, trigger.FunctionName))
			}
		}
		docs = append(docs, doc{
			Key:   "table:" + title,
			Kind:  "table",
			Title: title,
			Body:  strings.Join(body, "\n"),
		})
	}
	for _, row := range snapshot.Functions {
		title := row.Schema + "." + row.Name + "(" + row.IdentityArgs + ")"
		docs = append(docs, doc{
			Key:   "function:" + title,
			Kind:  "function",
			Title: title,
			Body:  strings.Join([]string{"function", title, row.Returns, row.Language, fmt.Sprintf("security_definer=%t", row.SecurityDefiner), row.Definition}, "\n"),
		})
	}
	for _, row := range snapshot.StorageBuckets {
		title := "storage." + row.ID
		docs = append(docs, doc{
			Key:   "storage_bucket:" + row.ID,
			Kind:  "storage_bucket",
			Title: title,
			Body:  strings.Join([]string{"storage bucket", row.ID, row.Name, fmt.Sprintf("public=%t", row.Public), row.FileSizeLimit, row.AllowedMimeTypes}, "\n"),
		})
	}
	for _, row := range snapshot.Extensions {
		title := "extension." + row.Name
		docs = append(docs, doc{
			Key:   "extension:" + row.Name,
			Kind:  "extension",
			Title: title,
			Body:  strings.Join([]string{"extension", row.Name, row.Schema, row.Version, row.Comment}, "\n"),
		})
	}
	return docs
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ts(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func isArchiveTable(table string) bool {
	for _, allowed := range ArchiveTableNames {
		if table == allowed {
			return true
		}
	}
	return false
}

func normalizeSQLiteValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func isReadOnlyQuery(query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" || strings.Contains(trimmed, ";") {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "select ") ||
		strings.HasPrefix(lower, "with ") ||
		strings.HasPrefix(lower, "pragma ")
}
