package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"deploy-api/internal/learning"
	"deploy-api/internal/migrations"
	"deploy-api/internal/models"
	"deploy-api/internal/queue"
	"deploy-api/internal/tracing"
	"deploy-api/internal/worker"
)

func main() {
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()

	// --- Tracing ---
	tp, err := tracing.InitTracerProvider(context.Background(), "deploy-worker", otlpEndpoint())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to init tracing, continuing without")
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tp.Shutdown(ctx)
		}()
	}

	// --- Database ---
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Fatal().Msg("DATABASE_URL is required")
	}

	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid DATABASE_URL")
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create database pool")
	}
	defer pool.Close()

	repo := models.NewRepository(pool)

	// Run database migrations
	if err := migrations.Run(databaseURL); err != nil {
		logger.Fatal().Err(err).Msg("failed to run database migrations")
	}
	logger.Info().Msg("database migrations applied")

	// --- Redis / Asynq ---
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	concurrency := 4
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	queueServer, err := queue.NewQueueServer(redisAddr, concurrency, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create queue server")
	}
	defer queueServer.Close()

	// --- Pipeline stages ---
	cloneStage := worker.NewCloneStage(logger, "")
	buildStage := worker.NewBuildStage(logger)
	pushStage := worker.NewPushStage(logger)
	manifestStage := worker.NewManifestStage(logger, "")
	verifyStage := worker.NewVerifyStage(logger)
	applyStage, err := worker.NewApplyStage(logger, "")
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create apply stage")
	}
	healthStage := worker.NewHealthCheckStage(logger, 2*time.Minute)

	// --- Learning analyzer ---
	learner := learning.NewAnalyzer(repo, logger)
	scheduler := learning.NewScheduler(learner, logger, nil)

	// Start periodic learning analysis
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		// Run once on startup
		rec, err := scheduler.RunAnalysis(context.Background())
		if err != nil {
			logger.Error().Err(err).Msg("initial learning analysis failed")
		} else {
			logger.Info().Interface("recommendation", rec).Msg("initial learning analysis completed")
		}
		for {
			select {
			case <-ticker.C:
				rec, err := scheduler.RunAnalysis(context.Background())
				if err != nil {
					logger.Error().Err(err).Msg("scheduled learning analysis failed")
				} else {
					logger.Info().Interface("recommendation", rec).Msg("scheduled learning analysis completed")
				}
			}
		}
	}()

	// Register deploy job handler
	queueServer.RegisterHandler(queue.TypeDeployJob, func(ctx context.Context, t *asynq.Task) error {
		var payload queue.DeployJobPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal payload: %w", err)
		}

		job, err := repo.GetJob(ctx, payload.JobID)
		if err != nil {
			return fmt.Errorf("get job %s: %w", payload.JobID, err)
		}

		logger.Info().Str("job_id", job.ID).Msg("processing deploy job")

		orch := worker.NewOrchestrator(repo, cloneStage, buildStage, pushStage, manifestStage, verifyStage, applyStage, healthStage)
		if err := orch.Execute(ctx, job); err != nil {
			logger.Error().Err(err).Str("job_id", job.ID).Msg("deploy job failed")
			return err
		}

		logger.Info().Str("job_id", job.ID).Msg("deploy job completed")
		return nil
	})

	// Wait for shutdown signal
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Idempotency key cleanup (runs every hour, deletes keys older than 24h)
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, err := repo.DeleteExpiredIdempotencyKeys(ctx, 24*time.Hour)
				if err != nil {
					logger.Error().Err(err).Msg("idempotency key cleanup failed")
				} else if deleted > 0 {
					logger.Info().Int64("deleted", deleted).Msg("expired idempotency keys cleaned")
				}
			}
		}
	}()

	logger.Info().Int("concurrency", concurrency).Msg("starting worker")

	if err := queueServer.Start(ctx); err != nil {
		logger.Fatal().Err(err).Msg("queue server error")
	}
}

func otlpEndpoint() string {
	if e := os.Getenv("OTLP_ENDPOINT"); e != "" {
		return e
	}
	return ""
}
