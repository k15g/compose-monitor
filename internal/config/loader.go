package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Load reads the configuration from the environment, after loading a .env
// file if one is present. A missing .env is not an error: in a container the
// environment is supplied directly, and the file is a local-development
// convenience.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parsing configuration from environment: %w", err)
	}
	return cfg, nil
}

// MustLoad is Load, panicking on failure. It is for cmd/, where a
// configuration that cannot be read means the process has nothing to do.
func MustLoad() *Config {
	cfg, err := Load()
	if err != nil {
		panic(err)
	}
	return cfg
}
