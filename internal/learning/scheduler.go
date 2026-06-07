package learning

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rs/zerolog"
)

// Scheduler manages periodic learning analysis tasks.
type Scheduler struct {
	analyzer *Analyzer
	logger   zerolog.Logger
	client   *asynq.Client
}

// NewScheduler creates a new Scheduler.
func NewScheduler(analyzer *Analyzer, logger zerolog.Logger, client *asynq.Client) *Scheduler {
	return &Scheduler{
		analyzer: analyzer,
		logger:   logger,
		client:   client,
	}
}

// RunAnalysis performs a one-time learning analysis and returns the recommendation.
func (s *Scheduler) RunAnalysis(ctx context.Context) (*Recommendation, error) {
	// Analyze jobs from the last 7 days
	since := time.Now().Add(-7 * 24 * time.Hour)
	rec, err := s.analyzer.AnalyzeCompletedJobs(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("analysis failed: %w", err)
	}

	s.logger.Info().
		Int("total_jobs", rec.TotalJobsAnalyzed).
		Float64("suggested_cpu", rec.SuggestedCPUCores).
		Int("suggested_memory_mb", rec.SuggestedMemoryMB).
		Int("failure_patterns", len(rec.FailurePatterns)).
		Msg("learning analysis completed")

	// Log any failure patterns found
	for _, fp := range rec.FailurePatterns {
		s.logger.Warn().
			Str("stage", fp.Stage).
			Int("failures", fp.Count).
			Str("example", fp.ExampleError).
			Msg("recurring failure pattern detected")
	}

	return rec, nil
}
