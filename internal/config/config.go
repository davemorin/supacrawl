package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const defaultDirName = ".supacrawl"

type Config struct {
	Version int          `toml:"version"`
	DBPath  string       `toml:"db_path"`
	LogDir  string       `toml:"log_dir"`
	Source  SourceConfig `toml:"source"`
	Search  SearchConfig `toml:"search"`
	Sync    SyncConfig   `toml:"sync"`
	Backup  BackupConfig `toml:"backup"`
}

type SourceConfig struct {
	ProjectID         string   `toml:"project_id"`
	DatabaseURL       string   `toml:"database_url"`
	DatabaseURLEnv    string   `toml:"database_url_env"`
	SupabaseURL       string   `toml:"supabase_url"`
	SupabaseURLEnv    string   `toml:"supabase_url_env"`
	SecretKeyEnv      string   `toml:"secret_key_env"`
	ServiceRoleKeyEnv string   `toml:"service_role_key_env,omitempty"`
	ExcludeSchemas    []string `toml:"exclude_schemas"`
}

type SearchConfig struct {
	DefaultLimit int `toml:"default_limit"`
}

type SyncConfig struct {
	ReadPolicy string `toml:"read_policy"`
	StaleAfter string `toml:"stale_after"`
}

type BackupConfig struct {
	RepoPath     string `toml:"repo_path"`
	Recipient    string `toml:"recipient"`
	RecipientEnv string `toml:"recipient_env"`
	IdentityPath string `toml:"identity_path"`
	IdentityEnv  string `toml:"identity_env"`
}

func Default() Config {
	base := "~/" + defaultDirName
	return Config{
		Version: 1,
		DBPath:  filepath.ToSlash(filepath.Join(base, "supacrawl.db")),
		LogDir:  filepath.ToSlash(filepath.Join(base, "logs")),
		Source: SourceConfig{
			DatabaseURLEnv: "SUPABASE_DB_URL",
			SupabaseURLEnv: "SUPABASE_URL",
			SecretKeyEnv:   "SUPABASE_SECRET_KEY",
			ExcludeSchemas: []string{
				"information_schema",
				"pg_catalog",
				"pg_toast",
				"pg_temp_%",
			},
		},
		Search: SearchConfig{
			DefaultLimit: 20,
		},
		Sync: SyncConfig{
			ReadPolicy: "auto",
			StaleAfter: "15m",
		},
		Backup: BackupConfig{
			RepoPath:     filepath.ToSlash(filepath.Join(base, "backups")),
			RecipientEnv: "SUPACRAWL_AGE_RECIPIENT",
			IdentityPath: filepath.ToSlash(filepath.Join(base, "age.key")),
			IdentityEnv:  "SUPACRAWL_AGE_IDENTITY",
		},
	}
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, defaultDirName, "config.toml"), nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Save(path string) error {
	if err := c.Normalize(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Config) Normalize() error {
	if c.Version == 0 {
		c.Version = 1
	}
	if strings.TrimSpace(c.DBPath) == "" {
		c.DBPath = Default().DBPath
	}
	if strings.TrimSpace(c.LogDir) == "" {
		c.LogDir = Default().LogDir
	}
	if strings.TrimSpace(c.Source.DatabaseURLEnv) == "" && strings.TrimSpace(c.Source.DatabaseURL) == "" {
		c.Source.DatabaseURLEnv = Default().Source.DatabaseURLEnv
	}
	if strings.TrimSpace(c.Source.SupabaseURLEnv) == "" && strings.TrimSpace(c.Source.SupabaseURL) == "" {
		c.Source.SupabaseURLEnv = Default().Source.SupabaseURLEnv
	}
	if strings.TrimSpace(c.Source.SecretKeyEnv) == "" && strings.TrimSpace(c.Source.ServiceRoleKeyEnv) == "" {
		c.Source.SecretKeyEnv = Default().Source.SecretKeyEnv
	}
	if len(c.Source.ExcludeSchemas) == 0 {
		c.Source.ExcludeSchemas = Default().Source.ExcludeSchemas
	}
	if c.Search.DefaultLimit <= 0 {
		c.Search.DefaultLimit = Default().Search.DefaultLimit
	}
	if strings.TrimSpace(c.Sync.ReadPolicy) == "" {
		c.Sync.ReadPolicy = Default().Sync.ReadPolicy
	}
	if strings.TrimSpace(c.Sync.StaleAfter) == "" {
		c.Sync.StaleAfter = Default().Sync.StaleAfter
	}
	if strings.TrimSpace(c.Backup.RepoPath) == "" {
		c.Backup.RepoPath = Default().Backup.RepoPath
	}
	if strings.TrimSpace(c.Backup.RecipientEnv) == "" {
		c.Backup.RecipientEnv = Default().Backup.RecipientEnv
	}
	if strings.TrimSpace(c.Backup.IdentityPath) == "" {
		c.Backup.IdentityPath = Default().Backup.IdentityPath
	}
	if strings.TrimSpace(c.Backup.IdentityEnv) == "" {
		c.Backup.IdentityEnv = Default().Backup.IdentityEnv
	}

	paths := []*string{&c.DBPath, &c.LogDir, &c.Backup.RepoPath, &c.Backup.IdentityPath}
	for _, candidate := range paths {
		expanded, err := ExpandPath(*candidate)
		if err != nil {
			return err
		}
		*candidate = expanded
	}

	for i := range c.Source.ExcludeSchemas {
		c.Source.ExcludeSchemas[i] = strings.TrimSpace(c.Source.ExcludeSchemas[i])
	}
	return nil
}

func (c Config) ResolveDatabaseURL() (string, string, error) {
	if strings.TrimSpace(c.Source.DatabaseURL) != "" {
		return strings.TrimSpace(c.Source.DatabaseURL), "config:source.database_url", nil
	}
	envName := strings.TrimSpace(c.Source.DatabaseURLEnv)
	if envName == "" {
		return "", "", errors.New("source.database_url_env is empty")
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", envName, fmt.Errorf("%s is not set", envName)
	}
	return value, envName, nil
}

func (c Config) ResolveBackupRecipient() (string, string, error) {
	if strings.TrimSpace(c.Backup.Recipient) != "" {
		return strings.TrimSpace(c.Backup.Recipient), "config:backup.recipient", nil
	}
	envName := strings.TrimSpace(c.Backup.RecipientEnv)
	if envName == "" {
		return "", "", errors.New("backup.recipient_env is empty")
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", envName, fmt.Errorf("%s is not set", envName)
	}
	return value, envName, nil
}

func (c Config) ResolveBackupIdentity() (string, string, error) {
	envName := strings.TrimSpace(c.Backup.IdentityEnv)
	if envName != "" {
		value := strings.TrimSpace(os.Getenv(envName))
		if value != "" {
			return value, envName, nil
		}
	}
	path := strings.TrimSpace(c.Backup.IdentityPath)
	if path == "" {
		return "", "", errors.New("backup.identity_path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", path, err
	}
	return strings.TrimSpace(string(data)), path, nil
}

func (c Config) ResolveSupabaseURL() (string, string, error) {
	if strings.TrimSpace(c.Source.SupabaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.Source.SupabaseURL), "/"), "config:source.supabase_url", nil
	}
	envNames := []string{strings.TrimSpace(c.Source.SupabaseURLEnv), "NEXT_PUBLIC_SUPABASE_URL"}
	for _, envName := range envNames {
		if envName == "" {
			continue
		}
		value := strings.TrimSpace(os.Getenv(envName))
		if value != "" {
			return strings.TrimRight(value, "/"), envName, nil
		}
	}
	return "", strings.Join(envNames, ","), fmt.Errorf("%s is not set", strings.Join(envNames, " or "))
}

func (c Config) ResolveSecretKey() (string, string, error) {
	envNames := uniqueNonEmpty(
		strings.TrimSpace(c.Source.SecretKeyEnv),
		strings.TrimSpace(c.Source.ServiceRoleKeyEnv),
		"SUPABASE_SECRET_KEY",
		"SUPABASE_SERVICE_ROLE_KEY",
	)
	if len(envNames) == 0 {
		return "", "", errors.New("source.secret_key_env is empty")
	}
	for _, envName := range envNames {
		value := strings.TrimSpace(os.Getenv(envName))
		if value != "" {
			return value, envName, nil
		}
	}
	return "", strings.Join(envNames, ","), fmt.Errorf("%s is not set", strings.Join(envNames, " or "))
}

func (c Config) ResolveServiceRoleKey() (string, string, error) {
	return c.ResolveSecretKey()
}

func uniqueNonEmpty(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Clean(path), nil
}
