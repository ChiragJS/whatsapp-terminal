package whatsapp

import (
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

type bridgeLogger struct {
	logger *slog.Logger
}

var _ waLog.Logger = (*bridgeLogger)(nil)

func newBridgeLogger(logger *slog.Logger) waLog.Logger {
	if logger == nil {
		return waLog.Noop
	}
	return &bridgeLogger{logger: logger}
}

func (b *bridgeLogger) Warnf(msg string, args ...interface{}) {
	b.logger.Warn(formatf(msg, args...))
}

func (b *bridgeLogger) Errorf(msg string, args ...interface{}) {
	b.logger.Error(formatf(msg, args...))
}

func (b *bridgeLogger) Infof(msg string, args ...interface{}) {
	b.logger.Info(formatf(msg, args...))
}

func (b *bridgeLogger) Debugf(msg string, args ...interface{}) {
	b.logger.Debug(formatf(msg, args...))
}

func (b *bridgeLogger) Sub(module string) waLog.Logger {
	return &bridgeLogger{logger: b.logger.With("wa_module", module)}
}
