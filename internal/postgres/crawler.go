package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Crawler struct {
	DatabaseURL    string
	ProjectID      string
	ExcludeSchemas []string
	ExcludeTables  []string
}

type DoctorReport struct {
	DatabaseName  string `json:"database_name"`
	CurrentUser   string `json:"current_user"`
	ServerVersion string `json:"server_version"`
	CanConnect    bool   `json:"can_connect"`
}

func (c Crawler) Doctor(ctx context.Context) (DoctorReport, error) {
	conn, err := pgx.Connect(ctx, c.DatabaseURL)
	if err != nil {
		return DoctorReport{}, err
	}
	defer conn.Close(ctx)

	var report DoctorReport
	err = conn.QueryRow(ctx, `select current_database(), current_user, current_setting('server_version')`).
		Scan(&report.DatabaseName, &report.CurrentUser, &report.ServerVersion)
	if err != nil {
		return DoctorReport{}, err
	}
	report.CanConnect = true
	return report, nil
}

func (c Crawler) Crawl(ctx context.Context) (Snapshot, error) {
	conn, err := pgx.Connect(ctx, c.DatabaseURL)
	if err != nil {
		return Snapshot{}, err
	}
	defer conn.Close(ctx)

	snapshot := Snapshot{}
	if err := conn.QueryRow(ctx, `select current_database(), current_user, current_setting('server_version'), now()`).
		Scan(&snapshot.Project.DatabaseName, &snapshot.Project.CurrentUser, &snapshot.Project.ServerVersion, &snapshot.Project.CollectedAt); err != nil {
		return Snapshot{}, err
	}
	snapshot.Project.ID = strings.TrimSpace(c.ProjectID)
	if snapshot.Project.ID == "" {
		snapshot.Project.ID = snapshot.Project.DatabaseName
	}
	if snapshot.Project.CollectedAt.IsZero() {
		snapshot.Project.CollectedAt = time.Now().UTC()
	}

	if snapshot.Schemas, err = c.loadSchemas(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Tables, err = c.loadTables(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Columns, err = c.loadColumns(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Indexes, err = c.loadIndexes(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Constraints, err = c.loadConstraints(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Policies, err = c.loadPolicies(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Functions, err = c.loadFunctions(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Triggers, err = c.loadTriggers(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Extensions, err = c.loadExtensions(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.StorageBuckets, err = c.loadStorageBuckets(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	if snapshot.StorageObjectStats, err = c.loadStorageObjectStats(ctx, conn); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (c Crawler) CrawlData(ctx context.Context, tables []Table, batchSize int, emit func([]TableRow) error, progress func(DataCopyProgress)) (DataCopyStats, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	conn, err := pgx.Connect(ctx, c.DatabaseURL)
	if err != nil {
		return DataCopyStats{}, err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `set statement_timeout = 0`); err != nil {
		return DataCopyStats{}, fmt.Errorf("disable statement_timeout: %w", err)
	}

	var stats DataCopyStats
	for _, table := range tables {
		if !isCopyableTableKind(table.Kind) {
			continue
		}
		if c.isExcludedTable(table) {
			continue
		}
		if progress != nil {
			progress(DataCopyProgress{Schema: table.Schema, TableName: table.Name})
		}
		tableStats, err := c.crawlTableRows(ctx, conn, table, batchSize, emit, progress)
		if err != nil {
			return stats, err
		}
		if progress != nil {
			progress(DataCopyProgress{Schema: table.Schema, TableName: table.Name, Rows: tableStats.Rows, Done: true})
		}
		stats.Tables++
		stats.Rows += tableStats.Rows
	}
	return stats, nil
}

func (c Crawler) crawlTableRows(ctx context.Context, conn *pgx.Conn, table Table, batchSize int, emit func([]TableRow) error, progress func(DataCopyProgress)) (DataCopyStats, error) {
	query := fmt.Sprintf("select to_jsonb(t)::text from %s as t", qualifiedName(table.Schema, table.Name))
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return DataCopyStats{}, fmt.Errorf("copy %s.%s: %w", table.Schema, table.Name, err)
	}
	defer rows.Close()

	batch := make([]TableRow, 0, batchSize)
	var rowNumber int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return DataCopyStats{}, fmt.Errorf("copy %s.%s scan row: %w", table.Schema, table.Name, err)
		}
		rowNumber++
		batch = append(batch, TableRow{
			Schema:    table.Schema,
			TableName: table.Name,
			RowNumber: rowNumber,
			JSON:      raw,
		})
		if len(batch) >= batchSize {
			if err := emit(batch); err != nil {
				return DataCopyStats{}, err
			}
			batch = batch[:0]
			if progress != nil && rowNumber%10000 == 0 {
				progress(DataCopyProgress{Schema: table.Schema, TableName: table.Name, Rows: rowNumber})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return DataCopyStats{}, fmt.Errorf("copy %s.%s rows: %w", table.Schema, table.Name, err)
	}
	if len(batch) > 0 {
		if err := emit(batch); err != nil {
			return DataCopyStats{}, err
		}
	}
	return DataCopyStats{Tables: 1, Rows: rowNumber}, nil
}

func (c Crawler) isExcludedTable(table Table) bool {
	name := strings.ToLower(table.Schema + "." + table.Name)
	shortName := strings.ToLower(table.Name)
	for _, excluded := range c.ExcludeTables {
		excluded = strings.ToLower(strings.TrimSpace(excluded))
		if excluded == "" {
			continue
		}
		if excluded == name || excluded == shortName {
			return true
		}
	}
	return false
}

func isCopyableTableKind(kind string) bool {
	switch kind {
	case "table", "partitioned_table", "foreign_table":
		return true
	default:
		return false
	}
}

func qualifiedName(schema, name string) string {
	return quoteIdent(schema) + "." + quoteIdent(name)
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (c Crawler) schemaAllowed(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "information_schema" || strings.HasPrefix(name, "pg_") {
		return false
	}
	for _, pattern := range c.ExcludeSchemas {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "%") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "%")) {
			return false
		}
		if name == pattern {
			return false
		}
	}
	return true
}

func (c Crawler) loadSchemas(ctx context.Context, conn *pgx.Conn) ([]Schema, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       pg_catalog.pg_get_userbyid(n.nspowner),
       case
         when n.nspname in ('auth','storage','realtime','graphql_public','vault','extensions') then 'supabase'
         else 'user'
       end
from pg_catalog.pg_namespace n
order by n.nspname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Schema
	for rows.Next() {
		var row Schema
		if err := rows.Scan(&row.Name, &row.Owner, &row.Type); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Name) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadTables(ctx context.Context, conn *pgx.Conn) ([]Table, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       c.relname,
       case c.relkind
         when 'r' then 'table'
         when 'p' then 'partitioned_table'
         when 'v' then 'view'
         when 'm' then 'materialized_view'
         when 'f' then 'foreign_table'
         else c.relkind::text
       end,
       pg_catalog.pg_get_userbyid(c.relowner),
       coalesce(pg_catalog.obj_description(c.oid, 'pg_class'), ''),
       c.relrowsecurity,
       c.relforcerowsecurity,
       c.reltuples::bigint
from pg_catalog.pg_class c
join pg_catalog.pg_namespace n on n.oid = c.relnamespace
where c.relkind in ('r','p','v','m','f')
order by n.nspname, c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Table
	for rows.Next() {
		var row Table
		if err := rows.Scan(&row.Schema, &row.Name, &row.Kind, &row.Owner, &row.Comment, &row.RLSEnabled, &row.RLSForced, &row.EstimatedRows); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Schema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadColumns(ctx context.Context, conn *pgx.Conn) ([]Column, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       cls.relname,
       a.attname,
       a.attnum::int,
       pg_catalog.format_type(a.atttypid, a.atttypmod),
       not a.attnotnull,
       coalesce(pg_catalog.pg_get_expr(ad.adbin, ad.adrelid), ''),
       coalesce(pg_catalog.col_description(a.attrelid, a.attnum), '')
from pg_catalog.pg_attribute a
join pg_catalog.pg_class cls on cls.oid = a.attrelid
join pg_catalog.pg_namespace n on n.oid = cls.relnamespace
left join pg_catalog.pg_attrdef ad on ad.adrelid = a.attrelid and ad.adnum = a.attnum
where cls.relkind in ('r','p','v','m','f')
  and a.attnum > 0
  and not a.attisdropped
order by n.nspname, cls.relname, a.attnum`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Column
	for rows.Next() {
		var row Column
		if err := rows.Scan(&row.TableSchema, &row.TableName, &row.Name, &row.Ordinal, &row.DataType, &row.IsNullable, &row.Default, &row.Comment); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.TableSchema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadIndexes(ctx context.Context, conn *pgx.Conn) ([]Index, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       tbl.relname,
       idx.relname,
       pg_catalog.pg_get_indexdef(i.indexrelid)
from pg_catalog.pg_index i
join pg_catalog.pg_class tbl on tbl.oid = i.indrelid
join pg_catalog.pg_class idx on idx.oid = i.indexrelid
join pg_catalog.pg_namespace n on n.oid = tbl.relnamespace
order by n.nspname, tbl.relname, idx.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Index
	for rows.Next() {
		var row Index
		if err := rows.Scan(&row.Schema, &row.TableName, &row.Name, &row.Definition); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Schema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadConstraints(ctx context.Context, conn *pgx.Conn) ([]Constraint, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       cls.relname,
       con.conname,
       case con.contype
         when 'p' then 'primary_key'
         when 'f' then 'foreign_key'
         when 'u' then 'unique'
         when 'c' then 'check'
         when 'x' then 'exclusion'
         else con.contype::text
       end,
       pg_catalog.pg_get_constraintdef(con.oid, true)
from pg_catalog.pg_constraint con
join pg_catalog.pg_class cls on cls.oid = con.conrelid
join pg_catalog.pg_namespace n on n.oid = con.connamespace
order by n.nspname, cls.relname, con.conname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Constraint
	for rows.Next() {
		var row Constraint
		if err := rows.Scan(&row.Schema, &row.TableName, &row.Name, &row.Type, &row.Definition); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Schema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadPolicies(ctx context.Context, conn *pgx.Conn) ([]Policy, error) {
	rows, err := conn.Query(ctx, `
select schemaname,
       tablename,
       policyname,
       cmd,
       coalesce(roles::text, ''),
       coalesce(qual, ''),
       coalesce(with_check, '')
from pg_catalog.pg_policies
order by schemaname, tablename, policyname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Policy
	for rows.Next() {
		var row Policy
		if err := rows.Scan(&row.Schema, &row.TableName, &row.Name, &row.Command, &row.Roles, &row.Using, &row.Check); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Schema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadFunctions(ctx context.Context, conn *pgx.Conn) ([]Function, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       p.proname,
       pg_catalog.pg_get_function_identity_arguments(p.oid),
       pg_catalog.pg_get_function_result(p.oid),
       l.lanname,
       p.prosecdef,
       coalesce(pg_catalog.pg_get_functiondef(p.oid), '')
from pg_catalog.pg_proc p
join pg_catalog.pg_namespace n on n.oid = p.pronamespace
join pg_catalog.pg_language l on l.oid = p.prolang
where p.prokind in ('f','p')
order by n.nspname, p.proname, pg_catalog.pg_get_function_identity_arguments(p.oid)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Function
	for rows.Next() {
		var row Function
		if err := rows.Scan(&row.Schema, &row.Name, &row.IdentityArgs, &row.Returns, &row.Language, &row.SecurityDefiner, &row.Definition); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Schema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadTriggers(ctx context.Context, conn *pgx.Conn) ([]Trigger, error) {
	rows, err := conn.Query(ctx, `
select n.nspname,
       cls.relname,
       t.tgname,
       case
         when (t.tgtype & 2) <> 0 then 'before'
         when (t.tgtype & 64) <> 0 then 'instead_of'
         else 'after'
       end,
       concat_ws(',',
         case when (t.tgtype & 4) <> 0 then 'insert' end,
         case when (t.tgtype & 8) <> 0 then 'delete' end,
         case when (t.tgtype & 16) <> 0 then 'update' end,
         case when (t.tgtype & 32) <> 0 then 'truncate' end
       ),
       pn.nspname || '.' || p.proname,
       pg_catalog.pg_get_triggerdef(t.oid, true)
from pg_catalog.pg_trigger t
join pg_catalog.pg_class cls on cls.oid = t.tgrelid
join pg_catalog.pg_namespace n on n.oid = cls.relnamespace
join pg_catalog.pg_proc p on p.oid = t.tgfoid
join pg_catalog.pg_namespace pn on pn.oid = p.pronamespace
where not t.tgisinternal
order by n.nspname, cls.relname, t.tgname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Trigger
	for rows.Next() {
		var row Trigger
		if err := rows.Scan(&row.Schema, &row.TableName, &row.Name, &row.Timing, &row.Events, &row.FunctionName, &row.Definition); err != nil {
			return nil, err
		}
		if c.schemaAllowed(row.Schema) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func (c Crawler) loadExtensions(ctx context.Context, conn *pgx.Conn) ([]Extension, error) {
	rows, err := conn.Query(ctx, `
select e.extname,
       n.nspname,
       e.extversion,
       coalesce(obj_description(e.oid, 'pg_extension'), '')
from pg_catalog.pg_extension e
join pg_catalog.pg_namespace n on n.oid = e.extnamespace
order by e.extname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Extension
	for rows.Next() {
		var row Extension
		if err := rows.Scan(&row.Name, &row.Schema, &row.Version, &row.Comment); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c Crawler) loadStorageBuckets(ctx context.Context, conn *pgx.Conn) ([]StorageBucket, error) {
	exists, err := relationExists(ctx, conn, "storage.buckets")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := conn.Query(ctx, `
select id,
       name,
       public,
       coalesce(file_size_limit::text, ''),
       coalesce(allowed_mime_types::text, ''),
       coalesce(created_at::text, ''),
       coalesce(updated_at::text, '')
from storage.buckets
order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StorageBucket
	for rows.Next() {
		var row StorageBucket
		if err := rows.Scan(&row.ID, &row.Name, &row.Public, &row.FileSizeLimit, &row.AllowedMimeTypes, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c Crawler) loadStorageObjectStats(ctx context.Context, conn *pgx.Conn) ([]StorageObjectStat, error) {
	exists, err := relationExists(ctx, conn, "storage.objects")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := conn.Query(ctx, `
select bucket_id,
       count(*)::bigint,
       coalesce(sum(coalesce((metadata->>'size')::bigint, 0)), 0)::bigint
from storage.objects
group by bucket_id
order by bucket_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StorageObjectStat
	for rows.Next() {
		var row StorageObjectStat
		if err := rows.Scan(&row.BucketID, &row.ObjectCount, &row.TotalBytes); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func relationExists(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	var value *string
	if err := conn.QueryRow(ctx, `select to_regclass($1)::text`, name).Scan(&value); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check relation %s: %w", name, err)
	}
	return value != nil && *value != "", nil
}
