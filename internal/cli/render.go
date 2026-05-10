package cli

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/davemorin/supacrawl/internal/postgres"
	"github.com/davemorin/supacrawl/internal/storage"
	"github.com/davemorin/supacrawl/internal/store"
)

func renderText(w io.Writer, title string, value any) error {
	fmt.Fprintf(w, "%s\n", title)
	switch v := value.(type) {
	case store.Status:
		renderStatus(w, v)
	case store.Report:
		renderReport(w, v)
	case store.DiffResult:
		renderDiff(w, v)
	case []store.SearchResult:
		renderSearchResults(w, v)
	case store.SQLResult:
		renderSQL(w, v)
	case store.ArchiveSize:
		renderArchiveSize(w, v)
	case storage.DownloadStats:
		renderDownloadStats(w, v)
	default:
		renderAny(w, value)
	}
	return nil
}

func renderStatus(w io.Writer, status store.Status) {
	if status.ProjectID != "" {
		fmt.Fprintf(w, "  project: %s\n", status.ProjectID)
	}
	if status.DatabaseName != "" {
		fmt.Fprintf(w, "  database: %s\n", status.DatabaseName)
	}
	if !status.CollectedAt.IsZero() {
		fmt.Fprintf(w, "  collected: %s\n", status.CollectedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "  schemas: %d\n", status.Schemas)
	fmt.Fprintf(w, "  tables: %d\n", status.Tables)
	fmt.Fprintf(w, "  columns: %d\n", status.Columns)
	fmt.Fprintf(w, "  policies: %d\n", status.Policies)
	fmt.Fprintf(w, "  functions: %d\n", status.Functions)
	fmt.Fprintf(w, "  triggers: %d\n", status.Triggers)
	fmt.Fprintf(w, "  extensions: %d\n", status.Extensions)
	fmt.Fprintf(w, "  storage buckets: %d\n", status.StorageBuckets)
	fmt.Fprintf(w, "  storage objects: %d\n", status.StorageObjects)
	fmt.Fprintf(w, "  data tables: %d\n", status.DataTables)
	fmt.Fprintf(w, "  data rows: %d\n", status.DataRows)
}

func renderReport(w io.Writer, report store.Report) {
	renderStatus(w, report.Status)
	if len(report.SchemaTables) > 0 {
		fmt.Fprintln(w, "\n  schemas")
		for _, row := range report.SchemaTables {
			fmt.Fprintf(w, "    %s: %d tables\n", row.Schema, row.Tables)
		}
	}
	if len(report.PolicyTables) > 0 {
		fmt.Fprintln(w, "\n  policy tables")
		for _, row := range report.PolicyTables {
			fmt.Fprintf(w, "    %s.%s: %d policies\n", row.Schema, row.Table, row.Policies)
		}
	}
}

func renderDiff(w io.Writer, diff store.DiffResult) {
	fmt.Fprintf(w, "  current: %s\n", renderArchiveRef(diff.Current))
	fmt.Fprintf(w, "  baseline: %s\n", renderArchiveRef(diff.Baseline))
	if diff.ProjectMismatch {
		fmt.Fprintf(w, "  warning: current and baseline archives are from different projects (current=%s, baseline=%s)\n", emptyUnknown(diff.Current.ProjectID), emptyUnknown(diff.Baseline.ProjectID))
	}
	renderTableDiff(w, diff.Tables)
	renderPolicyDiff(w, diff.Policies)
	renderStorageBucketDiff(w, diff.StorageBuckets)
}

func renderTableDiff(w io.Writer, diff store.TableDiff) {
	if len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.Changed) == 0 {
		fmt.Fprintln(w, "\n  tables: no changes")
		return
	}
	fmt.Fprintf(w, "\n  tables: +%d added, -%d removed, ~%d changed\n", len(diff.Added), len(diff.Removed), len(diff.Changed))
	if len(diff.Added) > 0 {
		fmt.Fprintln(w, "    added:")
		for _, row := range diff.Added {
			fmt.Fprintf(w, "      + %s\n", tableLabel(row))
		}
	}
	if len(diff.Removed) > 0 {
		fmt.Fprintln(w, "    removed:")
		for _, row := range diff.Removed {
			fmt.Fprintf(w, "      - %s\n", tableLabel(row))
		}
	}
	if len(diff.Changed) > 0 {
		fmt.Fprintln(w, "    changed:")
		for _, change := range diff.Changed {
			fmt.Fprintf(w, "      ~ %s%s\n", change.Key, tableChangeSummary(change))
		}
	}
}

func renderPolicyDiff(w io.Writer, diff store.PolicyDiff) {
	if len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.Changed) == 0 {
		fmt.Fprintln(w, "\n  policies: no changes")
		return
	}
	fmt.Fprintf(w, "\n  policies: +%d added, -%d removed, ~%d changed\n", len(diff.Added), len(diff.Removed), len(diff.Changed))
	if len(diff.Added) > 0 {
		fmt.Fprintln(w, "    added:")
		for _, row := range diff.Added {
			fmt.Fprintf(w, "      + %s\n", policyLabel(row))
		}
	}
	if len(diff.Removed) > 0 {
		fmt.Fprintln(w, "    removed:")
		for _, row := range diff.Removed {
			fmt.Fprintf(w, "      - %s\n", policyLabel(row))
		}
	}
	if len(diff.Changed) > 0 {
		fmt.Fprintln(w, "    changed:")
		for _, change := range diff.Changed {
			fmt.Fprintf(w, "      ~ %s%s\n", change.Key, policyChangeSummary(change))
		}
	}
}

func renderStorageBucketDiff(w io.Writer, diff store.StorageBucketDiff) {
	if len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.Changed) == 0 {
		fmt.Fprintln(w, "\n  storage_buckets: no changes")
		return
	}
	fmt.Fprintf(w, "\n  storage_buckets: +%d added, -%d removed, ~%d changed\n", len(diff.Added), len(diff.Removed), len(diff.Changed))
	if len(diff.Added) > 0 {
		fmt.Fprintln(w, "    added:")
		for _, row := range diff.Added {
			fmt.Fprintf(w, "      + %s\n", storageBucketLabel(row))
		}
	}
	if len(diff.Removed) > 0 {
		fmt.Fprintln(w, "    removed:")
		for _, row := range diff.Removed {
			fmt.Fprintf(w, "      - %s\n", storageBucketLabel(row))
		}
	}
	if len(diff.Changed) > 0 {
		fmt.Fprintln(w, "    changed:")
		for _, change := range diff.Changed {
			fmt.Fprintf(w, "      ~ %s%s\n", change.Key, storageBucketChangeSummary(change))
		}
	}
}

func renderSearchResults(w io.Writer, results []store.SearchResult) {
	if len(results) == 0 {
		fmt.Fprintln(w, "  no matches")
		return
	}
	for _, result := range results {
		fmt.Fprintf(w, "  [%s] %s\n", result.Kind, result.Title)
		if strings.TrimSpace(result.Snippet) != "" {
			fmt.Fprintf(w, "    %s\n", compactWhitespace(result.Snippet))
		}
	}
}

func renderSQL(w io.Writer, result store.SQLResult) {
	if len(result.Columns) == 0 {
		fmt.Fprintln(w, "  no columns")
		return
	}
	fmt.Fprintf(w, "  %s\n", strings.Join(result.Columns, "\t"))
	for _, row := range result.Rows {
		fmt.Fprintf(w, "  %s\n", strings.Join(row, "\t"))
	}
}

func renderArchiveSize(w io.Writer, size store.ArchiveSize) {
	fmt.Fprintf(w, "  db path: %s\n", size.DBPath)
	fmt.Fprintf(w, "  file bytes: %s\n", humanBytes(size.FileBytes))
	if size.WALBytes > 0 {
		fmt.Fprintf(w, "  wal bytes: %s\n", humanBytes(size.WALBytes))
	}
	fmt.Fprintf(w, "  sqlite bytes: %s\n", humanBytes(size.SQLiteBytes))
	fmt.Fprintf(w, "  row json bytes: %s\n", humanBytes(size.RowJSONBytes))
	if len(size.SourceTables) > 0 {
		fmt.Fprintln(w, "\n  largest source tables")
		for _, row := range size.SourceTables {
			fmt.Fprintf(w, "    %s.%s: %s across %d rows\n", row.Schema, row.Table, humanBytes(row.RowJSONBytes), row.Rows)
		}
	}
}

func renderDownloadStats(w io.Writer, stats storage.DownloadStats) {
	fmt.Fprintf(w, "  objects: %d\n", stats.Objects)
	fmt.Fprintf(w, "  bytes: %s\n", humanBytes(stats.Bytes))
	fmt.Fprintf(w, "  skipped: %d\n", stats.Skipped)
}

func renderAny(w io.Writer, value any) {
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Map {
		iter := rv.MapRange()
		for iter.Next() {
			fmt.Fprintf(w, "  %v: %v\n", iter.Key(), iter.Value())
		}
		return
	}
	fmt.Fprintf(w, "  %v\n", value)
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return strconv.FormatInt(value, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func renderArchiveRef(ref store.ArchiveRef) string {
	collected := "unknown"
	if !ref.CollectedAt.IsZero() {
		collected = ref.CollectedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s (project=%s, collected=%s)", ref.Path, emptyUnknown(ref.ProjectID), collected)
}

func tableChangeSummary(change store.TableChange) string {
	parts := make([]string, 0, len(change.ChangedFields))
	for _, field := range change.ChangedFields {
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", field, tableFieldValue(change.Before, field), tableFieldValue(change.After, field)))
	}
	return renderChangeParts(parts)
}

func policyChangeSummary(change store.PolicyChange) string {
	parts := make([]string, 0, len(change.ChangedFields))
	for _, field := range change.ChangedFields {
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", field, policyFieldValue(change.Before, field), policyFieldValue(change.After, field)))
	}
	return renderChangeParts(parts)
}

func storageBucketChangeSummary(change store.StorageBucketChange) string {
	parts := make([]string, 0, len(change.ChangedFields))
	for _, field := range change.ChangedFields {
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", field, storageBucketFieldValue(change.Before, field), storageBucketFieldValue(change.After, field)))
	}
	return renderChangeParts(parts)
}

func renderChangeParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func tableFieldValue(row postgres.Table, field string) string {
	switch field {
	case "rls_enabled":
		return strconv.FormatBool(row.RLSEnabled)
	case "rls_forced":
		return strconv.FormatBool(row.RLSForced)
	case "comment":
		return row.Comment
	case "kind":
		return row.Kind
	default:
		return ""
	}
}

func policyFieldValue(row postgres.Policy, field string) string {
	switch field {
	case "command":
		return row.Command
	case "roles":
		return row.Roles
	case "using":
		return row.Using
	case "check":
		return row.Check
	default:
		return ""
	}
}

func storageBucketFieldValue(row postgres.StorageBucket, field string) string {
	switch field {
	case "public":
		return strconv.FormatBool(row.Public)
	case "file_size_limit":
		return row.FileSizeLimit
	case "allowed_mime_types":
		return row.AllowedMimeTypes
	default:
		return ""
	}
}

func tableLabel(row postgres.Table) string {
	return row.Schema + "." + row.Name
}

func policyLabel(row postgres.Policy) string {
	return row.Schema + "." + row.TableName + "." + row.Name
}

func storageBucketLabel(row postgres.StorageBucket) string {
	return row.ID
}

func emptyUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
