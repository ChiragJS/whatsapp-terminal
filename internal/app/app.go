package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chirag/whatsapp-terminal/internal/config"
	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
	"github.com/chirag/whatsapp-terminal/internal/transport/demo"
	"github.com/chirag/whatsapp-terminal/internal/transport/whatsapp"
	"github.com/chirag/whatsapp-terminal/internal/ui"
)

func Run(ctx context.Context, cfg config.Config) error {
	if cfg.CheckKeybindings {
		return checkKeybindings(cfg)
	}

	logOutput, closeLog, err := openLogSink(cfg)
	if err != nil {
		return err
	}
	defer closeLog()

	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("app starting", "data_dir", cfg.DataDir, "demo", cfg.Demo, "stress", cfg.Stress)

	if cfg.ResetCache {
		if err := resetLocalCache(cfg.DataDir); err != nil {
			return fmt.Errorf("reset cache: %w", err)
		}
		logger.Info("local cache cleared", "data_dir", cfg.DataDir)
	}

	repo, err := appstore.New(filepath.Join(cfg.DataDir, "app.db"))
	if err != nil {
		return err
	}
	defer repo.Close()

	transport := chooseTransport(cfg, repo, logger)
	if err := transport.Start(ctx); err != nil {
		return err
	}
	defer func() {
		if err := transport.Stop(); err != nil {
			logger.Warn("stop transport", "error", err)
		}
	}()

	themeSlug := cfg.Theme
	if themeSlug == "" {
		themeSlug = ui.LoadPersistedThemeName(cfg.DataDir)
	}
	model := ui.NewModel(repo, transport).
		WithDownloadDir(filepath.Join(cfg.DataDir, "downloads")).
		WithQuitAfterNavigation(cfg.ArmQuitAfterNavigation).
		WithForceRepaint(cfg.NoAltScreen).
		WithDataDir(cfg.DataDir).
		WithKeymap(cfg.DataDir).
		WithTheme(themeSlug).
		WithChatListLimit(cfg.ChatListLimit).
		WithLogger(logger.With("component", "ui"))
	options := []tea.ProgramOption{}
	if !cfg.NoAltScreen {
		options = append(options, tea.WithAltScreen())
	}
	options = append(options, tea.WithMouseCellMotion())
	program := tea.NewProgram(model, options...)
	// Disable terminal autowrap (DECAWM) while the TUI runs. Real chat data
	// contains graphemes whose measured width disagrees with the terminal's
	// rendered width (Devanagari matras, emoji variation/skin-tone
	// sequences). With autowrap on, one over-wide line inserts a physical
	// newline and permanently desynchronizes Bubble Tea's relative-cursor
	// repaints, corrupting the whole screen; with it off, the line clips
	// harmlessly at the last column.
	fmt.Fprint(os.Stdout, "\x1b[?7l")
	defer fmt.Fprint(os.Stdout, "\x1b[?7h")
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}

// checkKeybindings validates the keybindings file and reports the result on
// stdout. It returns a non-nil error when problems exist so the process exits
// non-zero.
func checkKeybindings(cfg config.Config) error {
	_, problems := ui.LoadKeymap(cfg.DataDir)
	path := filepath.Join(cfg.DataDir, "keybindings.json")
	if len(problems) == 0 {
		fmt.Printf("keybindings OK (%s)\n", path)
		return nil
	}
	for _, p := range problems {
		fmt.Println(p)
	}
	return fmt.Errorf("keybindings validation failed: %d problem(s)", len(problems))
}

func chooseTransport(cfg config.Config, repo *appstore.Store, logger *slog.Logger) domain.Transport {
	if cfg.Demo {
		return demo.New(repo, logger).WithStress(cfg.Stress)
	}
	return whatsapp.NewAdapter(cfg, repo, logger)
}

// resetLocalCache removes the SQLite app.db and its WAL/journal sidecars
// from dataDir. The whatsmeow session db, theme preference, and downloads
// directory are intentionally left in place so the user stays paired and
// keeps their settings.
func resetLocalCache(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	base := filepath.Join(dataDir, "app.db")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		path := base + suffix
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}

func openLogSink(cfg config.Config) (io.Writer, func(), error) {
	if !cfg.Debug {
		return io.Discard, func() {}, nil
	}
	path := filepath.Join(cfg.DataDir, "debug.log")
	// #nosec G304 -- path is constrained to the configured application data directory.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open debug log: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}
