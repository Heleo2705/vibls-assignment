package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"deploy-api/internal/api"
	"deploy-api/internal/migrations"
	"deploy-api/internal/models"
	"deploy-api/internal/queue"
	"deploy-api/internal/tracing"
)

func readPolicy(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func main() {
	// Logger
	log := zerolog.New(os.Stderr).With().Timestamp().Logger()

	// Tracing
	tp, err := tracing.InitTracerProvider(context.Background(), "deploy-api", otlpEndpoint())
	if err != nil {
		log.Warn().Err(err).Msg("failed to init tracing, continuing without")
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tp.Shutdown(ctx)
		}()
	}

	// Configuration from env (with defaults)
	dbURL := getEnv("DATABASE_URL", "postgres://deployer:secret@localhost:5432/jobs?sslmode=disable")

	// Run database migrations (idempotent — safe to call every startup)
	if err := migrations.Run(dbURL); err != nil {
		log.Fatal().Err(err).Msg("failed to run database migrations")
	}
	log.Info().Msg("database migrations applied")
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	port := getEnv("PORT", "8080")

	// Database
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()
	repo := models.NewRepository(pool)

	// Queue client
	queueClient, err := queue.NewQueueClient(redisAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create queue client")
	}
	defer queueClient.Close()

	// Router
	r := chi.NewRouter()

	// Middleware (metrics first to capture all requests)
	r.Use(api.MetricsMiddleware)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Health check
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Prometheus metrics endpoint
	r.Handle("/metrics", api.MetricsHandler())

	// Rego engine for ABAC
	policyPath := getEnv("POLICY_PATH", "policies/abac/authz.rego")
	policyData, err := readPolicy(policyPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", policyPath).Msg("failed to read ABAC policy")
	}
	regoEngine, err := api.NewRegoEngine(log, policyData)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize Rego engine")
	}

	// API handler
	h := api.NewHandler(api.Dependencies{
		Repo:        repo,
		QueueClient: queueClient,
		Logger:      log,
		RegoEngine:  regoEngine,
	})
	h.MountRoutes(r)

	// Server
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		log.Info().Str("port", port).Msg("API server starting")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal().Err(err).Msg("server forced to shutdown")
	}
	log.Info().Msg("server exited")
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func otlpEndpoint() string {
	if e := os.Getenv("OTLP_ENDPOINT"); e != "" {
		return e
	}
	return ""
}
