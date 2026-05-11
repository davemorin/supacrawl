package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultNormalizesPaths(t *testing.T) {
	cfg := Default()
	require.NoError(t, cfg.Normalize())
	require.True(t, filepath.IsAbs(cfg.DBPath))
	require.Equal(t, "SUPABASE_DB_URL", cfg.Source.DatabaseURLEnv)
	require.Equal(t, "SUPABASE_URL", cfg.Source.SupabaseURLEnv)
	require.Equal(t, "SUPABASE_SECRET_KEY", cfg.Source.SecretKeyEnv)
	require.Empty(t, cfg.Source.ServiceRoleKeyEnv)
	require.Equal(t, 20, cfg.Search.DefaultLimit)
	require.Equal(t, "auto", cfg.Sync.ReadPolicy)
	require.Equal(t, "15m", cfg.Sync.StaleAfter)
	require.True(t, filepath.IsAbs(cfg.Backup.RepoPath))
}

func TestResolveDatabaseURLPrefersConfigValue(t *testing.T) {
	cfg := Default()
	cfg.Source.DatabaseURL = "postgres://example"
	cfg.Source.DatabaseURLEnv = "SUPABASE_DB_URL"
	t.Setenv("SUPABASE_DB_URL", "postgres://env")

	value, source, err := cfg.ResolveDatabaseURL()
	require.NoError(t, err)
	require.Equal(t, "postgres://example", value)
	require.Equal(t, "config:source.database_url", source)
}

func TestSaveUsesPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Default()
	cfg.DBPath = filepath.Join(dir, "supacrawl.db")
	cfg.LogDir = filepath.Join(dir, "logs")

	require.NoError(t, cfg.Save(path))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestResolveSupabaseURLFallsBackToPublicEnv(t *testing.T) {
	cfg := Default()
	cfg.Source.SupabaseURLEnv = "SUPABASE_URL"
	t.Setenv("NEXT_PUBLIC_SUPABASE_URL", "https://example.supabase.co/")

	value, source, err := cfg.ResolveSupabaseURL()
	require.NoError(t, err)
	require.Equal(t, "https://example.supabase.co", value)
	require.Equal(t, "NEXT_PUBLIC_SUPABASE_URL", source)
}

func TestResolveSecretKeyPrefersCurrentEnv(t *testing.T) {
	cfg := Default()
	t.Setenv("SUPABASE_SECRET_KEY", "sb_secret_current")
	t.Setenv("SUPABASE_SERVICE_ROLE_KEY", "legacy-service-role")

	value, source, err := cfg.ResolveSecretKey()
	require.NoError(t, err)
	require.Equal(t, "sb_secret_current", value)
	require.Equal(t, "SUPABASE_SECRET_KEY", source)
}

func TestResolveSecretKeyFallsBackToLegacyServiceRoleEnv(t *testing.T) {
	cfg := Default()
	t.Setenv("SUPABASE_SERVICE_ROLE_KEY", "legacy-service-role")

	value, source, err := cfg.ResolveSecretKey()
	require.NoError(t, err)
	require.Equal(t, "legacy-service-role", value)
	require.Equal(t, "SUPABASE_SERVICE_ROLE_KEY", source)
}
