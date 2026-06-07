package learning

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"deploy-api/internal/models"
)

// Recommendation holds a learning recommendation for resource defaults.
type Recommendation struct {
	SuggestedCPUCores  float64          `json:"suggested_cpu_cores"`
	SuggestedMemoryMB  int              `json:"suggested_memory_mb"`
	FailurePatterns    []FailurePattern `json:"failure_patterns,omitempty"`
	TotalJobsAnalyzed  int              `json:"total_jobs_analyzed"`
}

// FailurePattern describes a recurring failure in the pipeline.
type FailurePattern struct {
	Stage        string `json:"stage"`
	Count        int    `json:"count"`
	ExampleError string `json:"example_error,omitempty"`
}

// Analyzer computes learning recommendations from completed jobs.
type Analyzer struct {
	repo   *models.Repository
	logger zerolog.Logger
}

// NewAnalyzer creates a new Analyzer.
func NewAnalyzer(repo *models.Repository, logger zerolog.Logger) *Analyzer {
	return &Analyzer{repo: repo, logger: logger}
}

// AnalyzeCompletedJobs queries completed jobs and computes resource waste.
// It returns a Recommendation with optimized defaults and failure patterns.
func (a *Analyzer) AnalyzeCompletedJobs(ctx context.Context, since time.Time) (*Recommendation, error) {
	jobs, err := a.repo.ListCompletedJobs(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("list completed jobs: %w", err)
	}

	if len(jobs) == 0 {
		return &Recommendation{
			SuggestedCPUCores: 0.5,
			SuggestedMemoryMB: 256,
			TotalJobsAnalyzed: 0,
		}, nil
	}

	// Analyze resource overrides used vs defaults
	totalCPU := 0.0
	totalMemory := 0.0
	cpuCount := 0
	memoryCount := 0
	stageFailures := make(map[string]int)
	stageErrors := make(map[string]string)

	for _, job := range jobs {
		if job.Status == models.StatusFailed {
			if job.CurrentStage != "" {
				stageFailures[string(job.CurrentStage)]++
				if job.ErrorMessage != "" {
					stageErrors[string(job.CurrentStage)] = job.ErrorMessage
				}
			}
		}

		if job.ResourceOverrides != nil {
			if job.ResourceOverrides.CPUCores != nil {
				totalCPU += *job.ResourceOverrides.CPUCores
				cpuCount++
			}
			if job.ResourceOverrides.MemoryMB != nil {
				totalMemory += float64(*job.ResourceOverrides.MemoryMB)
				memoryCount++
			}
		}
	}

	// Build recommendation
	rec := &Recommendation{
		TotalJobsAnalyzed: len(jobs),
		SuggestedCPUCores: 0.5, // default
		SuggestedMemoryMB: 256,  // default in MB
	}

	if cpuCount > 0 {
		avgCPU := totalCPU / float64(cpuCount)
		rec.SuggestedCPUCores = avgCPU
	}

	if memoryCount > 0 {
		avgMem := int(totalMemory / float64(memoryCount))
		if avgMem > 0 {
			rec.SuggestedMemoryMB = avgMem
		}
	}

	// Build failure patterns
	for stage, count := range stageFailures {
		pattern := FailurePattern{
			Stage: stage,
			Count: count,
		}
		if errMsg, ok := stageErrors[stage]; ok {
			pattern.ExampleError = errMsg
		}
		rec.FailurePatterns = append(rec.FailurePatterns, pattern)
	}

	a.logger.Info().
		Int("jobs_analyzed", len(jobs)).
		Float64("suggested_cpu", rec.SuggestedCPUCores).
		Int("suggested_memory_mb", rec.SuggestedMemoryMB).
		Int("failure_patterns", len(rec.FailurePatterns)).
		Msg("learning analysis complete")

	return rec, nil
}
