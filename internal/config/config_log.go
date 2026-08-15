package config

import "log/slog"

// LogLevel is the minimum severity that is written.
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// SlogLevel maps the configured level onto slog's. An unrecognised value is
// info rather than an error: a typo in a log level should not stop the
// service from starting.
func (l LogLevel) SlogLevel() slog.Level {
	switch l {
	case LogLevelDebug:
		return slog.LevelDebug
	case LogLevelWarn:
		return slog.LevelWarn
	case LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LogFormat is how log records are written.
type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

// LogConfig configures the structured logger.
type LogConfig struct {
	Level  LogLevel  `env:"LEVEL" envDefault:"info"`
	Format LogFormat `env:"FORMAT" envDefault:"text"`
}
