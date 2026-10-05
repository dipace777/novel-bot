package config

import "fmt"

type DatabaseConfig struct {
	DatabaseURL string
	DBMaxConns  int32
}

// LoadDatabase also serves the migration executable; no authentication or browser settings are required.
func LoadDatabase() (DatabaseConfig, error) {
	cfg := DatabaseConfig{DatabaseURL: value("DATABASE_URL", "")}
	if cfg.DatabaseURL == "" {
		return DatabaseConfig{}, fmt.Errorf("DATABASE_URL is required")
	}
	n, err := integer("DB_MAX_CONNS", 20, 2147483647)
	if err != nil {
		return DatabaseConfig{}, err
	}
	cfg.DBMaxConns = int32(n)
	return cfg, nil
}
