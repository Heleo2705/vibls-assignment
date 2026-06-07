package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rs/zerolog"
)

// DeployJobPayload is the payload for a deploy job task.
type DeployJobPayload struct {
	JobID string `json:"job_id"`
}

// DeployJobHandler processes a deploy job task.
// Implement this interface for the actual deployment logic.
type DeployJobHandler interface {
	HandleDeployJob(ctx context.Context, payload DeployJobPayload) error
}

// DeployJobHandlerFunc is a function adapter for DeployJobHandler.
type DeployJobHandlerFunc func(ctx context.Context, payload DeployJobPayload) error

// HandleDeployJob implements DeployJobHandler.
func (f DeployJobHandlerFunc) HandleDeployJob(ctx context.Context, payload DeployJobPayload) error {
	return f(ctx, payload)
}

// Middleware is a function that wraps a handler with cross-cutting concerns.
type Middleware func(asynq.Handler) asynq.Handler

// chainMiddleware applies middlewares in order (first is outermost).
func chainMiddleware(handler asynq.Handler, middlewares ...Middleware) asynq.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// RegisterDeployJobHandler registers a deploy job handler with middleware.
func (s *QueueServer) RegisterDeployJobHandler(handler DeployJobHandler, middlewares ...Middleware) {
	wrapped := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		var payload DeployJobPayload
		if err := parsePayload(task, &payload); err != nil {
			return err
		}
		return handler.HandleDeployJob(ctx, payload)
	})
	s.mux.Handle(TypeDeployJob, chainMiddleware(wrapped, middlewares...))
}

// parsePayload unmarshals the task payload into the given value.
func parsePayload(task *asynq.Task, v interface{}) error {
	return json.Unmarshal(task.Payload(), v)
}

// LoggingMiddleware logs task processing start and completion (with duration).
func LoggingMiddleware(logger zerolog.Logger) Middleware {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			start := time.Now()
			logger.Info().Str("task_type", task.Type()).Msg("task started")
			err := next.ProcessTask(ctx, task)
			if err != nil {
				logger.Error().Err(err).Str("task_type", task.Type()).Dur("duration", time.Since(start)).Msg("task failed")
			} else {
				logger.Info().Str("task_type", task.Type()).Dur("duration", time.Since(start)).Msg("task completed")
			}
			return err
		})
	}
}

// RecoveryMiddleware catches panics from handlers and logs them.
func RecoveryMiddleware(logger zerolog.Logger) Middleware {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) (err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error().Interface("panic", r).Str("task_type", task.Type()).Msg("task panicked")
					err = fmt.Errorf("task panicked: %v", r)
				}
			}()
			return next.ProcessTask(ctx, task)
		})
	}
}

// MetricsMiddleware wraps handler with Prometheus metrics instrumentation.
// (Stub — metrics will be implemented in Phase 7.)
func MetricsMiddleware() Middleware {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			return next.ProcessTask(ctx, task)
		})
	}
}
