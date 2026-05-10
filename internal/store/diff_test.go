package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davemorin/supacrawl/internal/postgres"
	"github.com/stretchr/testify/require"
)

func TestDiff_AddedTable(t *testing.T) {
	result := diffSnapshots(t,
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, nil, nil),
		snapshotWith(t, "demo", []postgres.Table{profilesTable(), auditLogTable()}, nil, nil),
	)

	require.Len(t, result.Tables.Added, 1)
	require.Equal(t, "audit_log", result.Tables.Added[0].Name)
	require.Empty(t, result.Tables.Removed)
	require.Empty(t, result.Tables.Changed)
}

func TestDiff_RemovedTable(t *testing.T) {
	result := diffSnapshots(t,
		snapshotWith(t, "demo", []postgres.Table{auditLogTable(), profilesTable()}, nil, nil),
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, nil, nil),
	)

	require.Len(t, result.Tables.Removed, 1)
	require.Equal(t, "audit_log", result.Tables.Removed[0].Name)
	require.Empty(t, result.Tables.Added)
	require.Empty(t, result.Tables.Changed)
}

func TestDiff_RLSDisabled(t *testing.T) {
	before := profilesTable()
	before.RLSEnabled = true
	after := before
	after.RLSEnabled = false

	result := diffSnapshots(t,
		snapshotWith(t, "demo", []postgres.Table{before}, nil, nil),
		snapshotWith(t, "demo", []postgres.Table{after}, nil, nil),
	)

	require.Len(t, result.Tables.Changed, 1)
	change := result.Tables.Changed[0]
	require.Equal(t, "public.profiles", change.Key)
	require.Equal(t, []string{"rls_enabled"}, change.ChangedFields)
	require.True(t, change.Before.RLSEnabled)
	require.False(t, change.After.RLSEnabled)
}

func TestDiff_PolicyUsingChanged(t *testing.T) {
	before := profilesPolicy()
	before.Using = "auth.uid() = id"
	after := before
	after.Using = "true"

	result := diffSnapshots(t,
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, []postgres.Policy{before}, nil),
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, []postgres.Policy{after}, nil),
	)

	require.Len(t, result.Policies.Changed, 1)
	change := result.Policies.Changed[0]
	require.Equal(t, "public.profiles.profiles_select", change.Key)
	require.Equal(t, []string{"using"}, change.ChangedFields)
	require.Equal(t, "auth.uid() = id", change.Before.Using)
	require.Equal(t, "true", change.After.Using)
}

func TestDiff_StorageBucketPublic(t *testing.T) {
	before := avatarsBucket()
	before.Public = false
	after := before
	after.Public = true

	result := diffSnapshots(t,
		snapshotWith(t, "demo", nil, nil, []postgres.StorageBucket{before}),
		snapshotWith(t, "demo", nil, nil, []postgres.StorageBucket{after}),
	)

	require.Len(t, result.StorageBuckets.Changed, 1)
	change := result.StorageBuckets.Changed[0]
	require.Equal(t, "avatars", change.Key)
	require.Equal(t, []string{"public"}, change.ChangedFields)
	require.False(t, change.Before.Public)
	require.True(t, change.After.Public)
}

func TestDiff_Identical(t *testing.T) {
	result := diffSnapshots(t,
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, []postgres.Policy{profilesPolicy()}, []postgres.StorageBucket{avatarsBucket()}),
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, []postgres.Policy{profilesPolicy()}, []postgres.StorageBucket{avatarsBucket()}),
	)

	require.NotNil(t, result.Tables.Added)
	require.NotNil(t, result.Tables.Removed)
	require.NotNil(t, result.Tables.Changed)
	require.Empty(t, result.Tables.Added)
	require.Empty(t, result.Tables.Removed)
	require.Empty(t, result.Tables.Changed)
	require.Empty(t, result.Policies.Added)
	require.Empty(t, result.Policies.Removed)
	require.Empty(t, result.Policies.Changed)
	require.Empty(t, result.StorageBuckets.Added)
	require.Empty(t, result.StorageBuckets.Removed)
	require.Empty(t, result.StorageBuckets.Changed)
}

func TestDiff_ProjectMismatch(t *testing.T) {
	result := diffSnapshots(t,
		snapshotWith(t, "staging", []postgres.Table{profilesTable()}, nil, nil),
		snapshotWith(t, "demo", []postgres.Table{profilesTable()}, nil, nil),
	)

	require.Equal(t, "demo", result.Current.ProjectID)
	require.Equal(t, "staging", result.Baseline.ProjectID)
	require.True(t, result.ProjectMismatch)
}

func TestOpenReadOnly_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

	_, err := OpenReadOnly(path)

	require.Error(t, err)
	require.Contains(t, err.Error(), "baseline archive not found: "+path)
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}

func TestOpenReadOnly_RejectsNonArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	st, err := OpenReadOnly(path)

	require.Nil(t, st)
	require.Error(t, err)
	require.Contains(t, err.Error(), "path is not a supacrawl archive: "+path)
}

func diffSnapshots(t *testing.T, baselineSnapshot, currentSnapshot postgres.Snapshot) DiffResult {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	baselinePath := filepath.Join(dir, "baseline.db")
	currentPath := filepath.Join(dir, "current.db")
	writeSnapshot(t, baselinePath, baselineSnapshot)
	writeSnapshot(t, currentPath, currentSnapshot)

	current, err := Open(currentPath)
	require.NoError(t, err)
	defer current.Close()
	baseline, err := OpenReadOnly(baselinePath)
	require.NoError(t, err)
	defer baseline.Close()

	result, err := current.Diff(ctx, baseline)
	require.NoError(t, err)
	return result
}

func writeSnapshot(t *testing.T, path string, snapshot postgres.Snapshot) {
	t.Helper()
	st, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, st.PutSnapshot(context.Background(), snapshot))
	require.NoError(t, st.Close())
}

func snapshotWith(t *testing.T, projectID string, tables []postgres.Table, policies []postgres.Policy, buckets []postgres.StorageBucket) postgres.Snapshot {
	t.Helper()
	return postgres.Snapshot{
		Project: postgres.ProjectInfo{
			ID:            projectID,
			DatabaseName:  "postgres",
			CurrentUser:   "postgres",
			ServerVersion: "16.0",
			CollectedAt:   time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
		},
		Tables:         tables,
		Policies:       policies,
		StorageBuckets: buckets,
	}
}

func profilesTable() postgres.Table {
	return postgres.Table{
		Schema:        "public",
		Name:          "profiles",
		Kind:          "table",
		Owner:         "postgres",
		Comment:       "User profiles",
		RLSEnabled:    true,
		RLSForced:     false,
		EstimatedRows: 42,
	}
}

func auditLogTable() postgres.Table {
	return postgres.Table{
		Schema:        "public",
		Name:          "audit_log",
		Kind:          "table",
		Owner:         "postgres",
		RLSEnabled:    true,
		RLSForced:     true,
		EstimatedRows: 10,
	}
}

func profilesPolicy() postgres.Policy {
	return postgres.Policy{
		Schema:    "public",
		TableName: "profiles",
		Name:      "profiles_select",
		Command:   "SELECT",
		Roles:     "{authenticated}",
		Using:     "auth.uid() = id",
		Check:     "",
	}
}

func avatarsBucket() postgres.StorageBucket {
	return postgres.StorageBucket{
		ID:               "avatars",
		Name:             "avatars",
		Public:           false,
		FileSizeLimit:    "1048576",
		AllowedMimeTypes: "{image/png,image/jpeg}",
		CreatedAt:        "2026-05-10T12:00:00Z",
		UpdatedAt:        "2026-05-10T12:00:00Z",
	}
}
