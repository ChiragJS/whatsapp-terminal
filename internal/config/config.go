package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	DataDir                string
	SessionName            string
	LogLevel               string
	Theme                  string
	ChatListLimit          int
	Debug                  bool
	Demo                   bool
	Stress                 bool
	ResetCache             bool
	NoAltScreen            bool
	ArmQuitAfterNavigation bool
	ShowVersion            bool
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
	flag.BoolVar(&cfg.ArmQuitAfterNavigation, "arm-quit-after-navigation", false, "allow q to quit after esc leaves search, compose, or thread views")
	flag.StringVar(&cfg.Theme, "theme", "", "TUI color theme: phosphor, sunset, ocean, plum, forest, paper (empty = use saved or default)")
	flag.IntVar(&cfg.ChatListLimit, "chat-list-limit", 0, "max chats fetched per inbox query (0 = built-in default of 500)")
	flag.BoolVar(&cfg.Stress, "stress", false, "seed an oversized demo dataset to stress-test the TUI (requires --demo)")
	flag.BoolVar(&cfg.ResetCache, "reset-cache", false, "delete the local message cache (app.db) before startup; preserves pairing, theme, and downloads")
	flag.BoolVar(&cfg.ShowVersion, "version", false, "print version information and exit")
	flag.Parse()

	if cfg.ShowVersion {
		return cfg, nil
	}
	if cfg.SessionName == "" {
		return Config{}, fmt.Errorf("session-name cannot be empty")
	}
	if cfg.ChatListLimit < 0 {
		return Config{}, fmt.Errorf("chat-list-limit must be >= 0")
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
