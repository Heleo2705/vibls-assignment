package queue

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rs/zerolog"
)

// Task type constants
const (
	TypeDeployJob        = "job:deploy"
	TypeLearningAnalysis = "learning:analyze"
)

// QueueClient wraps asynq.Client for enqueuing tasks.
type QueueClient struct {
	client *asynq.Client
}

// QueueServer wraps asynq.Server for processing tasks.
type QueueServer struct {
	server *asynq.Server
	mux    *asynq.ServeMux
	logger zerolog.Logger
}

// zerologAdapter adapts zerolog.Logger to asynq.Logger interface.
type zerologAdapter struct {
	logger zerolog.Logger
}

func (a *zerologAdapter) Debug(args ...interface{}) {
	a.logger.Debug().Msg(fmt.Sprint(args...))
}

func (a *zerologAdapter) Info(args ...interface{}) {
	a.logger.Info().Msg(fmt.Sprint(args...))
}

func (a *zerologAdapter) Warn(args ...interface{}) {
	a.logger.Warn().Msg(fmt.Sprint(args...))
}

func (a *zerologAdapter) Error(args ...interface{}) {
	a.logger.Error().Msg(fmt.Sprint(args...))
}

func (a *zerologAdapter) Fatal(args ...interface{}) {
	a.logger.Fatal().Msg(fmt.Sprint(args...))
}

// parseRedisURL extracts the host:port and password from a Redis URL.
// Handles "redis://" prefix and defaults port to :6379.
func parseRedisURL(redisURL string) (addr string, password string) {
	// If the URL contains "://", try to parse it as a URL.
	if strings.Contains(redisURL, "://") {
		u, err := url.Parse(redisURL)
		if err == nil {
			host := u.Hostname()
			port := u.Port()
			if port == "" {
				port = "6379"
			}
			if u.User != nil {
				if pw, ok := u.User.Password(); ok {
					password = pw
				}
			}
			return host + ":" + port, password
		}
	}

	// No scheme found, treat as host:port or just host.
	host := redisURL
	port := "6379"
	if host == "" {
		host = "localhost"
	}
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		port = host[idx+1:]
		host = host[:idx]
		if host == "" {
			host = "localhost"
		}
	}
	return host + ":" + port, password
}

// NewQueueClient creates a new QueueClient connected to the given Redis URL.
// Example redisURL: "redis://localhost:6379" or "localhost:6379".
func NewQueueClient(redisURL string) (*QueueClient, error) {
	addr, password := parseRedisURL(redisURL)
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     addr,
		Password: password,
	})
	return &QueueClient{client: client}, nil
}

// NewQueueServer creates a new QueueServer with the given configuration.
// concurrency determines how many tasks can run simultaneously.
// logger is optional; if the zero value, a default stderr logger is used.
func NewQueueServer(redisURL string, concurrency int, logger zerolog.Logger) (*QueueServer, error) {
	addr, password := parseRedisURL(redisURL)

	// Use default stderr logger if none provided
	if logger.GetLevel() == zerolog.NoLevel {
		logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}

	server := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr:     addr,
			Password: password,
		},
		asynq.Config{
			Concurrency:     concurrency,
			ShutdownTimeout: 10 * time.Second,
			Logger:          &zerologAdapter{logger: logger},
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
				logger.Error().Err(err).Str("task_type", task.Type()).Msg("task processing error")
			}),
		},
	)
	mux := asynq.NewServeMux()
	return &QueueServer{server: server, mux: mux, logger: logger}, nil
}

// EnqueueDeployJob enqueues a deploy job task with the given payload.
// Returns the task info and any error.
func (c *QueueClient) EnqueueDeployJob(ctx context.Context, jobID string, payload []byte) (*asynq.TaskInfo, error) {
	task := asynq.NewTask(
		TypeDeployJob,
		payload,
		asynq.MaxRetry(3),
		asynq.Timeout(30*time.Minute),
		asynq.TaskID(jobID),
	)
	return c.client.EnqueueContext(ctx, task)
}

// EnqueueLearningTask enqueues a learning analysis task.
func (c *QueueClient) EnqueueLearningTask(ctx context.Context) (*asynq.TaskInfo, error) {
	task := asynq.NewTask(TypeLearningAnalysis, nil)
	return c.client.EnqueueContext(ctx, task)
}

// RegisterHandler registers a handler function for the given task type.
// The handler receives context and the asynq task, returns error on failure.
func (s *QueueServer) RegisterHandler(taskType string, handler asynq.HandlerFunc) {
	s.mux.Handle(taskType, handler)
}

// Start begins processing tasks. Blocks until the context is cancelled.
func (s *QueueServer) Start(ctx context.Context) error {
	if err := s.server.Start(s.mux); err != nil {
		return err
	}

	<-ctx.Done()
	s.server.Shutdown()
	return nil
}

// Close gracefully shuts down the server.
func (s *QueueServer) Close() {
	s.server.Shutdown()
}

// Close closes the client connection.
func (c *QueueClient) Close() error {
	return c.client.Close()
}
