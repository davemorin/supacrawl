package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/davemorin/supacrawl/internal/postgres"
)

type DiffResult struct {
	Current         ArchiveRef        `json:"current"`
	Baseline        ArchiveRef        `json:"baseline"`
	ProjectMismatch bool              `json:"project_mismatch"`
	Tables          TableDiff         `json:"tables"`
	Policies        PolicyDiff        `json:"policies"`
	StorageBuckets  StorageBucketDiff `json:"storage_buckets"`
}

type ArchiveRef struct {
	Path        string    `json:"path"`
	ProjectID   string    `json:"project_id"`
	CollectedAt time.Time `json:"collected_at"`
}

type TableDiff struct {
	Added   []postgres.Table `json:"added"`
	Removed []postgres.Table `json:"removed"`
	Changed []TableChange    `json:"changed"`
}

type TableChange struct {
	Key           string         `json:"key"`
	Before        postgres.Table `json:"before"`
	After         postgres.Table `json:"after"`
	ChangedFields []string       `json:"changed_fields"`
}

type PolicyDiff struct {
	Added   []postgres.Policy `json:"added"`
	Removed []postgres.Policy `json:"removed"`
	Changed []PolicyChange    `json:"changed"`
}

type PolicyChange struct {
	Key           string          `json:"key"`
	Before        postgres.Policy `json:"before"`
	After         postgres.Policy `json:"after"`
	ChangedFields []string        `json:"changed_fields"`
}

type StorageBucketDiff struct {
	Added   []postgres.StorageBucket `json:"added"`
	Removed []postgres.StorageBucket `json:"removed"`
	Changed []StorageBucketChange    `json:"changed"`
}

type StorageBucketChange struct {
	Key           string                 `json:"key"`
	Before        postgres.StorageBucket `json:"before"`
	After         postgres.StorageBucket `json:"after"`
	ChangedFields []string               `json:"changed_fields"`
}

func (s *Store) Diff(ctx context.Context, baseline *Store) (DiffResult, error) {
	currentRef, err := s.archiveRef(ctx)
	if err != nil {
		return DiffResult{}, err
	}
	baselineRef, err := baseline.archiveRef(ctx)
	if err != nil {
		return DiffResult{}, err
	}

	currentTables, err := s.loadTables(ctx)
	if err != nil {
		return DiffResult{}, err
	}
	baselineTables, err := baseline.loadTables(ctx)
	if err != nil {
		return DiffResult{}, err
	}
	currentPolicies, err := s.loadPolicies(ctx)
	if err != nil {
		return DiffResult{}, err
	}
	baselinePolicies, err := baseline.loadPolicies(ctx)
	if err != nil {
		return DiffResult{}, err
	}
	currentBuckets, err := s.loadStorageBuckets(ctx)
	if err != nil {
		return DiffResult{}, err
	}
	baselineBuckets, err := baseline.loadStorageBuckets(ctx)
	if err != nil {
		return DiffResult{}, err
	}

	result := DiffResult{
		Current:         currentRef,
		Baseline:        baselineRef,
		ProjectMismatch: currentRef.ProjectID != baselineRef.ProjectID,
		Tables:          diffTables(currentTables, baselineTables),
		Policies:        diffPolicies(currentPolicies, baselinePolicies),
		StorageBuckets:  diffStorageBuckets(currentBuckets, baselineBuckets),
	}
	return result, nil
}

func (s *Store) archiveRef(ctx context.Context) (ArchiveRef, error) {
	ref := ArchiveRef{Path: s.path}
	var collectedAt string
	err := s.db.QueryRowContext(ctx, `select project_id, collected_at from project_info where id = 'default'`).Scan(&ref.ProjectID, &collectedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ref, nil
		}
		return ArchiveRef{}, err
	}
	if collectedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, collectedAt)
		if err == nil {
			ref.CollectedAt = parsed
		}
	}
	return ref, nil
}

func (s *Store) loadTables(ctx context.Context) ([]postgres.Table, error) {
	rows, err := s.db.QueryContext(ctx, `
select schema_name, name, kind, owner, comment, rls_enabled, rls_forced, estimated_rows
from tables
order by schema_name, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []postgres.Table
	for rows.Next() {
		var row postgres.Table
		var rlsEnabled, rlsForced int
		if err := rows.Scan(&row.Schema, &row.Name, &row.Kind, &row.Owner, &row.Comment, &rlsEnabled, &rlsForced, &row.EstimatedRows); err != nil {
			return nil, err
		}
		row.RLSEnabled = intBool(rlsEnabled)
		row.RLSForced = intBool(rlsForced)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) loadPolicies(ctx context.Context) ([]postgres.Policy, error) {
	rows, err := s.db.QueryContext(ctx, `
select schema_name, table_name, name, command, roles, using_expr, check_expr
from policies
order by schema_name, table_name, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []postgres.Policy
	for rows.Next() {
		var row postgres.Policy
		if err := rows.Scan(&row.Schema, &row.TableName, &row.Name, &row.Command, &row.Roles, &row.Using, &row.Check); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) loadStorageBuckets(ctx context.Context) ([]postgres.StorageBucket, error) {
	rows, err := s.db.QueryContext(ctx, `
select id, name, public, file_size_limit, allowed_mime_types, created_at, updated_bucket_at
from storage_buckets
order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []postgres.StorageBucket
	for rows.Next() {
		var row postgres.StorageBucket
		var public int
		if err := rows.Scan(&row.ID, &row.Name, &public, &row.FileSizeLimit, &row.AllowedMimeTypes, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Public = intBool(public)
		out = append(out, row)
	}
	return out, rows.Err()
}

func diffTables(current, baseline []postgres.Table) TableDiff {
	diff := TableDiff{
		Added:   make([]postgres.Table, 0),
		Removed: make([]postgres.Table, 0),
		Changed: make([]TableChange, 0),
	}
	currentByKey := make(map[string]postgres.Table, len(current))
	for _, row := range current {
		currentByKey[tableKey(row)] = row
	}
	baselineByKey := make(map[string]postgres.Table, len(baseline))
	for _, row := range baseline {
		baselineByKey[tableKey(row)] = row
	}

	for _, row := range current {
		key := tableKey(row)
		before, ok := baselineByKey[key]
		if !ok {
			diff.Added = append(diff.Added, row)
			continue
		}
		fields := changedTableFields(before, row)
		if len(fields) > 0 {
			diff.Changed = append(diff.Changed, TableChange{Key: key, Before: before, After: row, ChangedFields: fields})
		}
	}
	for _, row := range baseline {
		if _, ok := currentByKey[tableKey(row)]; !ok {
			diff.Removed = append(diff.Removed, row)
		}
	}

	sort.Slice(diff.Added, func(i, j int) bool { return tableKey(diff.Added[i]) < tableKey(diff.Added[j]) })
	sort.Slice(diff.Removed, func(i, j int) bool { return tableKey(diff.Removed[i]) < tableKey(diff.Removed[j]) })
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].Key < diff.Changed[j].Key })
	return diff
}

func diffPolicies(current, baseline []postgres.Policy) PolicyDiff {
	diff := PolicyDiff{
		Added:   make([]postgres.Policy, 0),
		Removed: make([]postgres.Policy, 0),
		Changed: make([]PolicyChange, 0),
	}
	currentByKey := make(map[string]postgres.Policy, len(current))
	for _, row := range current {
		currentByKey[policyKey(row)] = row
	}
	baselineByKey := make(map[string]postgres.Policy, len(baseline))
	for _, row := range baseline {
		baselineByKey[policyKey(row)] = row
	}

	for _, row := range current {
		key := policyKey(row)
		before, ok := baselineByKey[key]
		if !ok {
			diff.Added = append(diff.Added, row)
			continue
		}
		fields := changedPolicyFields(before, row)
		if len(fields) > 0 {
			diff.Changed = append(diff.Changed, PolicyChange{Key: key, Before: before, After: row, ChangedFields: fields})
		}
	}
	for _, row := range baseline {
		if _, ok := currentByKey[policyKey(row)]; !ok {
			diff.Removed = append(diff.Removed, row)
		}
	}

	sort.Slice(diff.Added, func(i, j int) bool { return policyKey(diff.Added[i]) < policyKey(diff.Added[j]) })
	sort.Slice(diff.Removed, func(i, j int) bool { return policyKey(diff.Removed[i]) < policyKey(diff.Removed[j]) })
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].Key < diff.Changed[j].Key })
	return diff
}

func diffStorageBuckets(current, baseline []postgres.StorageBucket) StorageBucketDiff {
	diff := StorageBucketDiff{
		Added:   make([]postgres.StorageBucket, 0),
		Removed: make([]postgres.StorageBucket, 0),
		Changed: make([]StorageBucketChange, 0),
	}
	currentByKey := make(map[string]postgres.StorageBucket, len(current))
	for _, row := range current {
		currentByKey[storageBucketKey(row)] = row
	}
	baselineByKey := make(map[string]postgres.StorageBucket, len(baseline))
	for _, row := range baseline {
		baselineByKey[storageBucketKey(row)] = row
	}

	for _, row := range current {
		key := storageBucketKey(row)
		before, ok := baselineByKey[key]
		if !ok {
			diff.Added = append(diff.Added, row)
			continue
		}
		fields := changedStorageBucketFields(before, row)
		if len(fields) > 0 {
			diff.Changed = append(diff.Changed, StorageBucketChange{Key: key, Before: before, After: row, ChangedFields: fields})
		}
	}
	for _, row := range baseline {
		if _, ok := currentByKey[storageBucketKey(row)]; !ok {
			diff.Removed = append(diff.Removed, row)
		}
	}

	sort.Slice(diff.Added, func(i, j int) bool { return storageBucketKey(diff.Added[i]) < storageBucketKey(diff.Added[j]) })
	sort.Slice(diff.Removed, func(i, j int) bool { return storageBucketKey(diff.Removed[i]) < storageBucketKey(diff.Removed[j]) })
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].Key < diff.Changed[j].Key })
	return diff
}

func changedTableFields(before, after postgres.Table) []string {
	fields := make([]string, 0)
	if before.RLSEnabled != after.RLSEnabled {
		fields = append(fields, "rls_enabled")
	}
	if before.RLSForced != after.RLSForced {
		fields = append(fields, "rls_forced")
	}
	if before.Comment != after.Comment {
		fields = append(fields, "comment")
	}
	if before.Kind != after.Kind {
		fields = append(fields, "kind")
	}
	return fields
}

func changedPolicyFields(before, after postgres.Policy) []string {
	fields := make([]string, 0)
	if before.Command != after.Command {
		fields = append(fields, "command")
	}
	if before.Roles != after.Roles {
		fields = append(fields, "roles")
	}
	if before.Using != after.Using {
		fields = append(fields, "using")
	}
	if before.Check != after.Check {
		fields = append(fields, "check")
	}
	return fields
}

func changedStorageBucketFields(before, after postgres.StorageBucket) []string {
	fields := make([]string, 0)
	if before.Public != after.Public {
		fields = append(fields, "public")
	}
	if before.FileSizeLimit != after.FileSizeLimit {
		fields = append(fields, "file_size_limit")
	}
	if before.AllowedMimeTypes != after.AllowedMimeTypes {
		fields = append(fields, "allowed_mime_types")
	}
	return fields
}

func tableKey(row postgres.Table) string {
	return row.Schema + "." + row.Name
}

func policyKey(row postgres.Policy) string {
	return row.Schema + "." + row.TableName + "." + row.Name
}

func storageBucketKey(row postgres.StorageBucket) string {
	return row.ID
}

func intBool(value int) bool {
	return value != 0
}
