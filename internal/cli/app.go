package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/davemorin/supacrawl/internal/backup"
	"github.com/davemorin/supacrawl/internal/config"
	"github.com/davemorin/supacrawl/internal/postgres"
	"github.com/davemorin/supacrawl/internal/storage"
	"github.com/davemorin/supacrawl/internal/store"
)

type App struct {
	Stdout io.Writer
	Stderr io.Writer
}

type OutputFormat string

const (
	FormatText OutputFormat = "text"
	FormatJSON OutputFormat = "json"
	FormatLog  OutputFormat = "log"
)

func New() *App {
	return &App{Stdout: os.Stdout, Stderr: os.Stderr}
}

func (a *App) Run(ctx context.Context, args []string) error {
	args = hoistGlobalFlags(args)
	global := flag.NewFlagSet("supacrawl", flag.ContinueOnError)
	global.SetOutput(a.Stderr)
	global.Usage = func() {}
	configPath := global.String("config", "", "config path")
	format := global.String("format", string(FormatText), "output format: text|json|log")
	jsonOut := global.Bool("json", false, "json output")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.printHelp()
			return nil
		}
		return err
	}

	rest := global.Args()
	if len(rest) == 0 {
		a.printHelp()
		return nil
	}
	if *configPath == "" {
		path, err := config.DefaultConfigPath()
		if err != nil {
			return err
		}
		*configPath = path
	}
	outputFormat, err := resolveOutputFormat(*format, *jsonOut)
	if err != nil {
		return err
	}

	switch rest[0] {
	case "init":
		return a.runInit(*configPath, rest[1:], outputFormat)
	case "doctor":
		return a.runDoctor(ctx, *configPath, outputFormat)
	case "metadata":
		return a.runMetadata(*configPath, outputFormat)
	case "version":
		return a.writeOutput("Version", map[string]any{"version": Version}, outputFormat)
	case "sync":
		return a.runSync(ctx, *configPath, rest[1:], outputFormat)
	case "status":
		return a.runStatus(ctx, *configPath, rest[1:], outputFormat)
	case "report":
		return a.runReport(ctx, *configPath, rest[1:], outputFormat)
	case "diff":
		return a.runDiff(ctx, *configPath, rest[1:], outputFormat)
	case "search":
		return a.runSearch(ctx, *configPath, rest[1:], outputFormat)
	case "sql":
		return a.runSQL(ctx, *configPath, rest[1:], outputFormat)
	case "size":
		return a.runSize(ctx, *configPath, rest[1:], outputFormat)
	case "export":
		return a.runExport(ctx, *configPath, rest[1:], outputFormat)
	case "storage":
		return a.runStorage(ctx, *configPath, rest[1:], outputFormat)
	case "backup":
		return a.runBackup(ctx, *configPath, rest[1:], outputFormat)
	default:
		return fmt.Errorf("unknown command: %s", rest[0])
	}
}

func hoistGlobalFlags(args []string) []string {
	var globals []string
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			globals = append(globals, arg)
		case arg == "--config" || arg == "--format":
			globals = append(globals, arg)
			if i+1 < len(args) {
				i++
				globals = append(globals, args[i])
			}
		case strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "--format="):
			globals = append(globals, arg)
		default:
			rest = append(rest, arg)
		}
	}
	return append(globals, rest...)
}

func resolveOutputFormat(value string, jsonOut bool) (OutputFormat, error) {
	if jsonOut {
		return FormatJSON, nil
	}
	switch OutputFormat(strings.ToLower(strings.TrimSpace(value))) {
	case "", FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	case FormatLog:
		return FormatLog, nil
	default:
		return "", fmt.Errorf("unsupported format %q: use text, json, or log", value)
	}
}

func (a *App) runInit(configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	dbPath := fs.String("db", "", "database path")
	projectID := fs.String("project-id", "", "Supabase project id or local archive label")
	databaseURL := fs.String("database-url", "", "Postgres connection URL; prefer --database-url-env for secrets")
	databaseURLEnv := fs.String("database-url-env", "", "environment variable containing the Postgres connection URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.Default()
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *projectID != "" {
		cfg.Source.ProjectID = *projectID
	}
	if *databaseURL != "" {
		cfg.Source.DatabaseURL = *databaseURL
	}
	if *databaseURLEnv != "" {
		cfg.Source.DatabaseURLEnv = *databaseURLEnv
	}
	if err := cfg.Save(configPath); err != nil {
		return err
	}
	return a.writeOutput("Init", map[string]any{
		"config_path":      configPath,
		"db_path":          cfg.DBPath,
		"database_url_env": cfg.Source.DatabaseURLEnv,
		"project_id":       cfg.Source.ProjectID,
	}, format)
}

func (a *App) runDoctor(ctx context.Context, configPath string, format OutputFormat) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	status, err := st.Status(ctx)
	if err != nil {
		return err
	}
	url, source, err := cfg.ResolveDatabaseURL()
	if err != nil {
		return a.writeOutput("Doctor", map[string]any{
			"db_path":             cfg.DBPath,
			"local_archive_ready": true,
			"database_url_source": source,
			"database_ready":      false,
			"database_error":      err.Error(),
			"status":              status,
		}, format)
	}
	diag, err := postgres.Crawler{DatabaseURL: url}.Doctor(ctx)
	if err != nil {
		return a.writeOutput("Doctor", map[string]any{
			"db_path":             cfg.DBPath,
			"local_archive_ready": true,
			"database_url_source": source,
			"database_ready":      false,
			"database_error":      err.Error(),
			"status":              status,
		}, format)
	}
	return a.writeOutput("Doctor", map[string]any{
		"db_path":             cfg.DBPath,
		"local_archive_ready": true,
		"database_url_source": source,
		"database_ready":      true,
		"database":            diag,
		"status":              status,
	}, format)
}

func (a *App) runSync(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var excludeTables stringListFlag
	projectID := fs.String("project-id", "", "override project id for this sync")
	includeData := fs.Bool("data", false, "copy source table rows into local SQLite as JSON")
	full := fs.Bool("full", false, "copy metadata and source table rows")
	batchSize := fs.Int("batch-size", 1000, "row batch size for data copies")
	noRowFTS := fs.Bool("no-row-fts", false, "skip FTS indexing for copied table rows to reduce archive size")
	noProgress := fs.Bool("no-progress", false, "disable per-table progress on stderr")
	fs.Var(&excludeTables, "exclude-table", "skip a table during data copy; repeatable, accepts table or schema.table")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	url, source, err := cfg.ResolveDatabaseURL()
	if err != nil {
		return err
	}
	if *projectID != "" {
		cfg.Source.ProjectID = *projectID
	}
	result, err := a.syncArchive(ctx, cfg, url, source, *includeData || *full, *batchSize, !*noRowFTS, *noProgress, format, excludeTables)
	if err != nil {
		return err
	}
	return a.writeOutput("Sync", result, format)
}

func (a *App) syncArchive(ctx context.Context, cfg config.Config, url string, source string, includeData bool, batchSize int, includeRowFTS bool, noProgress bool, format OutputFormat, excludeTables []string) (map[string]any, error) {
	crawler := postgres.Crawler{
		DatabaseURL:    url,
		ProjectID:      cfg.Source.ProjectID,
		ExcludeSchemas: cfg.Source.ExcludeSchemas,
		ExcludeTables:  excludeTables,
	}
	snapshot, err := crawler.Crawl(ctx)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if err := st.PutSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	result := map[string]any{
		"database_url_source": source,
		"project":             snapshot.Project,
		"counts":              snapshot.Counts(),
		"db_path":             cfg.DBPath,
	}
	if len(excludeTables) > 0 {
		result["excluded_tables"] = excludeTables
	}
	if includeData {
		if err := st.BeginDataCopy(ctx, includeRowFTS); err != nil {
			return nil, err
		}
		progress := a.dataProgress(format, noProgress)
		stats, copyErr := crawler.CrawlData(ctx, snapshot.Tables, batchSize, func(rows []postgres.TableRow) error {
			return st.PutDataRows(ctx, rows, includeRowFTS)
		}, progress)
		if err := st.FinishDataCopy(ctx, stats, copyErr); err != nil {
			return nil, err
		}
		result["data_counts"] = stats
		result["row_fts"] = includeRowFTS
	}
	return result, nil
}

func (a *App) dataProgress(format OutputFormat, disabled bool) func(postgres.DataCopyProgress) {
	if disabled || a.Stderr == nil {
		return nil
	}
	return func(progress postgres.DataCopyProgress) {
		table := progress.Schema + "." + progress.TableName
		if progress.Done {
			if format == FormatLog {
				fmt.Fprintf(a.Stderr, "copy table=%s rows=%d done=true\n", table, progress.Rows)
			} else {
				fmt.Fprintf(a.Stderr, "copied %s: %d rows\n", table, progress.Rows)
			}
			return
		}
		if progress.Rows > 0 {
			if format == FormatLog {
				fmt.Fprintf(a.Stderr, "copy table=%s rows=%d done=false\n", table, progress.Rows)
			} else {
				fmt.Fprintf(a.Stderr, "copying %s: %d rows...\n", table, progress.Rows)
			}
			return
		}
		if format == FormatLog {
			fmt.Fprintf(a.Stderr, "copy table=%s done=false\n", table)
		} else {
			fmt.Fprintf(a.Stderr, "copying %s...\n", table)
		}
	}
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

type readSyncOverrides struct {
	Policy        string
	StaleAfter    string
	PolicySet     bool
	StaleAfterSet bool
}

type readSyncOptions struct {
	Policy     string
	StaleAfter string
}

func parseReadSyncArgs(args []string) ([]string, readSyncOverrides, error) {
	var out []string
	var overrides readSyncOverrides
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--sync":
			if i+1 >= len(args) {
				return nil, readSyncOverrides{}, errors.New("--sync requires a value")
			}
			i++
			overrides.Policy = args[i]
			overrides.PolicySet = true
		case strings.HasPrefix(arg, "--sync="):
			overrides.Policy = strings.TrimPrefix(arg, "--sync=")
			overrides.PolicySet = true
		case arg == "--stale-after":
			if i+1 >= len(args) {
				return nil, readSyncOverrides{}, errors.New("--stale-after requires a value")
			}
			i++
			overrides.StaleAfter = args[i]
			overrides.StaleAfterSet = true
		case strings.HasPrefix(arg, "--stale-after="):
			overrides.StaleAfter = strings.TrimPrefix(arg, "--stale-after=")
			overrides.StaleAfterSet = true
		default:
			out = append(out, arg)
		}
	}
	return out, overrides, nil
}

func defaultReadSyncOptions(cfg config.Config) readSyncOptions {
	return readSyncOptions{
		Policy:     cfg.Sync.ReadPolicy,
		StaleAfter: cfg.Sync.StaleAfter,
	}
}

func mergeReadSyncOptions(defaults readSyncOptions, overrides readSyncOverrides) readSyncOptions {
	if overrides.PolicySet {
		defaults.Policy = overrides.Policy
	}
	if overrides.StaleAfterSet {
		defaults.StaleAfter = overrides.StaleAfter
	}
	return defaults
}

func (a *App) ensureFresh(ctx context.Context, cfg config.Config, options readSyncOptions) error {
	policy := strings.ToLower(strings.TrimSpace(options.Policy))
	if policy == "" {
		policy = "auto"
	}
	switch policy {
	case "never", "none", "off":
		return nil
	case "auto", "always":
	default:
		return fmt.Errorf("unsupported --sync value %q: use auto, always, or never", options.Policy)
	}

	shouldSync := policy == "always"
	if policy == "auto" {
		staleAfter, err := time.ParseDuration(strings.TrimSpace(options.StaleAfter))
		if err != nil {
			return fmt.Errorf("invalid --stale-after value %q", options.StaleAfter)
		}
		st, err := store.Open(cfg.DBPath)
		if err != nil {
			return err
		}
		status, statusErr := st.Status(ctx)
		closeErr := st.Close()
		if statusErr != nil {
			return statusErr
		}
		if closeErr != nil {
			return closeErr
		}
		shouldSync = status.CollectedAt.IsZero() || time.Since(status.CollectedAt) > staleAfter
	}
	if !shouldSync {
		return nil
	}

	url, source, err := cfg.ResolveDatabaseURL()
	if err != nil {
		if policy == "auto" {
			return nil
		}
		return err
	}
	if a.Stderr != nil {
		fmt.Fprintln(a.Stderr, "refreshing local archive metadata")
	}
	_, err = a.syncArchive(ctx, cfg, url, source, false, 1000, true, true, FormatText, nil)
	return err
}

func (a *App) runMetadata(configPath string, format OutputFormat) error {
	payload := map[string]any{
		"name":        "supacrawl",
		"version":     Version,
		"config_path": configPath,
		"commands": []string{
			"init",
			"doctor",
			"metadata",
			"version",
			"sync",
			"status",
			"report",
			"diff",
			"size",
			"search",
			"export",
			"storage pull",
			"backup keygen",
			"backup init",
			"backup push",
			"backup status",
			"backup pull",
			"sql",
		},
		"archive_tables": []string{
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
		},
		"capabilities": []string{
			"metadata-crawl",
			"row-copy",
			"storage-pull",
			"encrypted-backup",
			"auto-sync",
			"archive-diff",
			"fts-search",
			"read-only-sql",
			"json-output",
		},
	}
	return a.writeOutput("Metadata", payload, format)
}

func (a *App) runStatus(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: supacrawl status [--sync auto|always|never] [--stale-after duration]")
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	status, err := st.Status(ctx)
	if err != nil {
		return err
	}
	return a.writeOutput("Status", status, format)
}

func (a *App) runReport(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: supacrawl report [--sync auto|always|never] [--stale-after duration]")
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	report, err := st.Report(ctx)
	if err != nil {
		return err
	}
	return a.writeOutput("Report", report, format)
}

func (a *App) runDiff(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	// Sync policy applies only to the current archive; the baseline is immutable.
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: supacrawl diff <baseline-path> [--sync auto|always|never] [--stale-after duration]")
	}
	baselinePath, err := config.ExpandPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	current, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer current.Close()
	baseline, err := store.OpenReadOnly(baselinePath)
	if err != nil {
		return err
	}
	defer baseline.Close()
	diff, err := current.Diff(ctx, baseline)
	if err != nil {
		return err
	}
	return a.writeOutput("diff", diff, format)
}

func (a *App) runSearch(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	kind, limit, query, err := parseSearchArgs(args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(query) == "" {
		return errors.New("usage: supacrawl search [--kind table] <query>")
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	if limit <= 0 {
		limit = cfg.Search.DefaultLimit
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	results, err := st.Search(ctx, query, kind, limit)
	if err != nil {
		return err
	}
	return a.writeOutput("Search", results, format)
}

func parseSearchArgs(args []string) (string, int, string, error) {
	var kind string
	var limit int
	var query []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--kind":
			if i+1 >= len(args) {
				return "", 0, "", errors.New("--kind requires a value")
			}
			i++
			kind = args[i]
		case strings.HasPrefix(arg, "--kind="):
			kind = strings.TrimPrefix(arg, "--kind=")
		case arg == "--limit":
			if i+1 >= len(args) {
				return "", 0, "", errors.New("--limit requires a value")
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return "", 0, "", fmt.Errorf("invalid --limit value %q", args[i])
			}
			limit = value
		case strings.HasPrefix(arg, "--limit="):
			raw := strings.TrimPrefix(arg, "--limit=")
			value, err := strconv.Atoi(raw)
			if err != nil {
				return "", 0, "", fmt.Errorf("invalid --limit value %q", raw)
			}
			limit = value
		default:
			query = append(query, arg)
		}
	}
	return kind, limit, strings.Join(query, " "), nil
}

func (a *App) runSQL(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || strings.TrimSpace(strings.Join(args, " ")) == "" {
		return errors.New(`usage: supacrawl sql "select * from tables limit 5"`)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	result, err := st.QueryReadOnly(ctx, strings.Join(args, " "))
	if err != nil {
		return err
	}
	return a.writeOutput("SQL", result, format)
}

func (a *App) runSize(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("size", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	limit := fs.Int("limit", 20, "number of largest source tables to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	size, err := st.Size(ctx, cfg.DBPath, *limit)
	if err != nil {
		return err
	}
	return a.writeOutput("Size", size, format)
}

func (a *App) runExport(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	args, overrides, err := parseReadSyncArgs(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	outPath := fs.String("out", "", "output file path; stdout when empty")
	exportType := fs.String("type", "jsonl", "export type: jsonl|csv")
	limit := fs.Int("limit", 0, "maximum rows to export")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: supacrawl export [--type jsonl|csv] [--out path] <schema.table>")
	}
	schemaName, tableName, err := splitTableName(fs.Arg(0))
	if err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := a.ensureFresh(ctx, cfg, mergeReadSyncOptions(defaultReadSyncOptions(cfg), overrides)); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.ExportRows(ctx, schemaName, tableName, *limit)
	if err != nil {
		return err
	}
	var out io.Writer = a.Stdout
	var file *os.File
	if *outPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
			return err
		}
		file, err = os.Create(*outPath)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}
	switch strings.ToLower(strings.TrimSpace(*exportType)) {
	case "jsonl", "json":
		for _, row := range rows {
			if _, err := fmt.Fprintln(out, row.JSON); err != nil {
				return err
			}
		}
	case "csv":
		writer := csv.NewWriter(out)
		if err := writer.Write([]string{"row_number", "row_json"}); err != nil {
			return err
		}
		for _, row := range rows {
			if err := writer.Write([]string{strconv.FormatInt(row.RowNumber, 10), row.JSON}); err != nil {
				return err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported export type %q: use jsonl or csv", *exportType)
	}
	if *outPath != "" {
		return a.writeOutput("Export", map[string]any{
			"table": schemaName + "." + tableName,
			"rows":  len(rows),
			"path":  *outPath,
		}, format)
	}
	return nil
}

func (a *App) runStorage(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	if len(args) == 0 {
		return errors.New("usage: supacrawl storage pull --dir path [--bucket name] [--limit n]")
	}
	switch args[0] {
	case "pull", "download":
		return a.runStoragePull(ctx, configPath, args[1:], format)
	default:
		return fmt.Errorf("unknown storage command: %s", args[0])
	}
}

func (a *App) runStoragePull(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("storage pull", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	dir := fs.String("dir", "", "directory for downloaded blobs")
	bucket := fs.String("bucket", "", "download only one bucket")
	limit := fs.Int("limit", 0, "maximum objects to download")
	overwrite := fs.Bool("overwrite", false, "overwrite existing local files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dir) == "" {
		return errors.New("storage pull requires --dir")
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	baseURL, _, err := cfg.ResolveSupabaseURL()
	if err != nil {
		return err
	}
	key, _, err := cfg.ResolveSecretKey()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	objects, err := st.ListStorageObjects(ctx, *bucket, *limit)
	if err != nil {
		return err
	}
	downloader := storage.Downloader{BaseURL: baseURL, AuthToken: key, UseCurl: true}
	stats, err := downloader.DownloadObjects(ctx, objects, *dir, *overwrite)
	if err != nil {
		return err
	}
	return a.writeOutput("Storage", stats, format)
}

func (a *App) runBackup(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	if len(args) == 0 {
		return errors.New("usage: supacrawl backup <keygen|init|push|status|pull>")
	}
	switch args[0] {
	case "keygen":
		return a.runBackupKeygen(args[1:], format)
	case "init":
		return a.runBackupInit(configPath, args[1:], format)
	case "push":
		return a.runBackupPush(ctx, configPath, args[1:], format)
	case "status":
		return a.runBackupStatus(configPath, args[1:], format)
	case "pull":
		return a.runBackupPull(ctx, configPath, args[1:], format)
	default:
		return fmt.Errorf("unknown backup command: %s", args[0])
	}
}

func (a *App) runBackupKeygen(args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("backup keygen", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	outPath := fs.String("out", "", "path for the private age identity file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*outPath) == "" {
		return errors.New("backup keygen requires --out")
	}
	expandedOutPath, err := config.ExpandPath(*outPath)
	if err != nil {
		return err
	}
	identity, err := backup.GenerateIdentity()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(expandedOutPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(expandedOutPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		return err
	}
	return a.writeOutput("Backup Keygen", map[string]any{
		"identity_path": expandedOutPath,
		"recipient":     identity.Recipient().String(),
	}, format)
}

func (a *App) runBackupInit(configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("backup init", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	repoPath := fs.String("repo", "", "backup repository path")
	recipient := fs.String("recipient", "", "age recipient")
	recipientEnv := fs.String("recipient-env", "", "environment variable containing the age recipient")
	identityPath := fs.String("identity-path", "", "path to private age identity")
	identityAlias := fs.String("identity", "", "path to private age identity")
	identityEnv := fs.String("identity-env", "", "environment variable containing the private age identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(configPath)
	if err != nil {
		return err
	}
	if *repoPath != "" {
		cfg.Backup.RepoPath = *repoPath
	}
	if *recipient != "" {
		cfg.Backup.Recipient = *recipient
	}
	if *recipientEnv != "" {
		cfg.Backup.RecipientEnv = *recipientEnv
	}
	if *identityPath == "" && *identityAlias != "" {
		*identityPath = *identityAlias
	}
	if *identityPath != "" {
		cfg.Backup.IdentityPath = *identityPath
	}
	if *identityEnv != "" {
		cfg.Backup.IdentityEnv = *identityEnv
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Backup.RepoPath, 0o755); err != nil {
		return err
	}
	if err := cfg.Save(configPath); err != nil {
		return err
	}
	return a.writeOutput("Backup Init", map[string]any{
		"config_path":   configPath,
		"repo_path":     cfg.Backup.RepoPath,
		"recipient_env": cfg.Backup.RecipientEnv,
		"identity_path": cfg.Backup.IdentityPath,
		"identity_env":  cfg.Backup.IdentityEnv,
	}, format)
}

func (a *App) runBackupPush(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("backup push", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	repoPath := fs.String("repo", "", "backup repository path")
	recipient := fs.String("recipient", "", "age recipient")
	recipientEnv := fs.String("recipient-env", "", "environment variable containing the age recipient")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if *repoPath != "" {
		cfg.Backup.RepoPath = *repoPath
	}
	if *recipientEnv != "" {
		cfg.Backup.RecipientEnv = *recipientEnv
		cfg.Backup.Recipient = ""
	}
	if *recipient != "" {
		cfg.Backup.Recipient = *recipient
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	resolvedRecipient, source, err := cfg.ResolveBackupRecipient()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	result, err := backup.Writer{RepoPath: cfg.Backup.RepoPath, Recipient: resolvedRecipient}.Write(ctx, st)
	if err != nil {
		return err
	}
	return a.writeOutput("Backup Push", map[string]any{
		"repo_path":        result.RepoPath,
		"manifest_path":    result.ManifestPath,
		"created_at":       result.CreatedAt,
		"shards":           result.Shards,
		"rows":             result.Rows,
		"recipient_source": source,
	}, format)
}

func (a *App) runBackupStatus(configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("backup status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	repoPath := fs.String("repo", "", "backup repository path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if *repoPath != "" {
		cfg.Backup.RepoPath = *repoPath
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	manifest, manifestPath, err := backup.ReadManifest(cfg.Backup.RepoPath)
	if err != nil {
		return err
	}
	var rows int64
	for _, shard := range manifest.Shards {
		rows += shard.Rows
	}
	return a.writeOutput("Backup Status", map[string]any{
		"repo_path":     cfg.Backup.RepoPath,
		"manifest_path": manifestPath,
		"created_at":    manifest.CreatedAt,
		"shards":        len(manifest.Shards),
		"rows":          rows,
	}, format)
}

func (a *App) runBackupPull(ctx context.Context, configPath string, args []string, format OutputFormat) error {
	fs := flag.NewFlagSet("backup pull", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	repoPath := fs.String("repo", "", "backup repository path")
	outDir := fs.String("out", "", "directory for decrypted JSONL gzip shards")
	identityPath := fs.String("identity-path", "", "path to private age identity")
	identityAlias := fs.String("identity", "", "path to private age identity")
	identityEnv := fs.String("identity-env", "", "environment variable containing the private age identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*outDir) == "" {
		return errors.New("backup pull requires --out")
	}
	expandedOutDir, err := config.ExpandPath(*outDir)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if *repoPath != "" {
		cfg.Backup.RepoPath = *repoPath
	}
	if *identityPath == "" && *identityAlias != "" {
		*identityPath = *identityAlias
	}
	if *identityPath != "" {
		cfg.Backup.IdentityPath = *identityPath
		cfg.Backup.IdentityEnv = ""
	}
	if *identityEnv != "" {
		cfg.Backup.IdentityEnv = *identityEnv
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	identity, source, err := cfg.ResolveBackupIdentity()
	if err != nil {
		return err
	}
	result, err := backup.Puller{RepoPath: cfg.Backup.RepoPath, Identity: identity}.Pull(ctx, expandedOutDir)
	if err != nil {
		return err
	}
	return a.writeOutput("Backup Pull", map[string]any{
		"repo_path":       result.RepoPath,
		"out_dir":         result.OutDir,
		"shards":          result.Shards,
		"rows":            result.Rows,
		"identity_source": source,
	}, format)
}

func loadConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if os.IsNotExist(err) {
		return config.Config{}, fmt.Errorf("config not found at %s; run `supacrawl init` first", path)
	}
	return config.Config{}, err
}

func loadConfigOrDefault(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if os.IsNotExist(err) {
		cfg = config.Default()
		return cfg, cfg.Normalize()
	}
	return config.Config{}, err
}

func splitTableName(value string) (string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("table must be schema.table, got %q", value)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func (a *App) writeOutput(title string, value any, format OutputFormat) error {
	switch format {
	case FormatJSON:
		encoder := json.NewEncoder(a.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	case FormatLog:
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.Stdout, "%s %s\n", strings.ToLower(title), payload)
		return err
	default:
		return renderText(a.Stdout, title, value)
	}
}

func (a *App) printHelp() {
	fmt.Fprintln(a.Stdout, `supacrawl mirrors Supabase/Postgres metadata into local SQLite.

Usage:
  supacrawl [--config path] [--format text|json|log] <command>

Commands:
  init       create a starter config
  doctor     check local archive and database connectivity
  metadata   print command/capability metadata for launchers and agents
  version    print the supacrawl version
  sync       crawl Supabase/Postgres metadata into SQLite; add --full for rows
  status     print archive counts
  report     summarize schemas and policy coverage
  diff       compare current archive to another archive file
  size       show local archive size breakdown
  search     search tables, functions, storage buckets, extensions
  export     export copied table rows as jsonl or csv
  storage    download Storage blobs from copied storage.objects rows
  backup     create and restore encrypted local archive snapshots
  sql        run read-only SQL against the local archive

Quick start:
  export SUPABASE_DB_URL="postgres://postgres.<ref>:<password>@aws-...pooler.supabase.com:6543/postgres?sslmode=require"
  supacrawl init --project-id <ref>
  supacrawl doctor
  supacrawl metadata --json
  supacrawl sync
  supacrawl sync --full --no-row-fts
  supacrawl sync --full --no-row-fts --exclude-table public.enrichments
  supacrawl status --sync auto --stale-after 15m
  supacrawl diff /path/to/older-archive.db --json
  supacrawl size
  supacrawl export --type jsonl --out companies.jsonl public.companies
  supacrawl storage pull --dir ./supabase-storage
  supacrawl backup keygen --out ~/.supacrawl/age.key
  supacrawl backup push
  supacrawl search "auth policies"`)
}
