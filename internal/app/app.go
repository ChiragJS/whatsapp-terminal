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
	logOutput, closeLog, err := openLogSink(cfg)
	if err != nil {
		return err
	}
	defer closeLog()

	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelDebug}))

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

	model := ui.NewModelWithRuntimeOptions(repo, transport, nil, nil, nil, filepath.Join(cfg.DataDir, "downloads"), cfg.NoAltScreen)
	options := []tea.ProgramOption{}
	if !cfg.NoAltScreen {
		options = append(options, tea.WithAltScreen())
	}
	options = append(options, tea.WithMouseCellMotion())
	program := tea.NewProgram(model, options...)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}

func chooseTransport(cfg config.Config, repo *appstore.Store, logger *slog.Logger) domain.Transport {
	if cfg.Demo {
		return demo.New(repo, logger)
	}
	return whatsapp.NewAdapter(cfg, repo, logger)
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
