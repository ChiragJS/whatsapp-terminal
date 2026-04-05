package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	DataDir     string
	SessionName string
	LogLevel    string
	Debug       bool
	Demo        bool
	NoAltScreen bool
}

func Parse() (Config, error) {
	defaultDir, err := defaultDataDir()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{}
	flag.StringVar(&cfg.DataDir, "data-dir", defaultDir, "directory for local config, cache, and session data")
	flag.StringVar(&cfg.SessionName, "session-name", "default", "name of the WhatsApp session profile")
	flag.StringVar(&cfg.LogLevel, "log-level", "info", "application log level")
	flag.BoolVar(&cfg.Debug, "debug", false, "enable debug logging to a file under the data directory")
	flag.BoolVar(&cfg.Demo, "demo", false, "run in offline demo mode with seeded data")
	flag.BoolVar(&cfg.NoAltScreen, "no-alt-screen", false, "disable terminal alt-screen mode")
	flag.Parse()

	if cfg.SessionName == "" {
		return Config{}, fmt.Errorf("session-name cannot be empty")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return Config{}, fmt.Errorf("create data dir: %w", err)
	}
	return cfg, nil
}

func defaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("discover config dir: %w", err)
	}
	return filepath.Join(base, "whatsapp-terminal"), nil
}
