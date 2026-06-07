package logger

import (
	"context"
	"os"

	"github.com/rs/zerolog"
)

// contextKey is used for storing logger in context.
type contextKey struct{}

var loggerKey = contextKey{}

// New creates a new zerolog.Logger writing to stderr with timestamp.
func New(level string) zerolog.Logger {
	l := zerolog.New(os.Stderr).With().Timestamp().Logger()

	switch level {
	case "debug":
		l = l.Level(zerolog.DebugLevel)
	case "warn":
		l = l.Level(zerolog.WarnLevel)
	case "error":
		l = l.Level(zerolog.ErrorLevel)
	default:
		l = l.Level(zerolog.InfoLevel)
	}

	return l
}

// WithJobID enriches the logger with a job_id field.
func WithJobID(logger zerolog.Logger, jobID string) zerolog.Logger {
	return logger.With().Str("job_id", jobID).Logger()
}

// WithComponent enriches the logger with a component field.
func WithComponent(logger zerolog.Logger, component string) zerolog.Logger {
	return logger.With().Str("component", component).Logger()
}

// FromContext extracts a logger from context, or returns a default.
func FromContext(ctx context.Context) zerolog.Logger {
	if l, ok := ctx.Value(loggerKey).(zerolog.Logger); ok {
		return l
	}
	return New("info")
}

// ToContext stores a logger in context.
func ToContext(ctx context.Context, logger zerolog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}
