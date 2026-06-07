package worker

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	jobDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "job_duration_seconds",
			Help:    "Duration of deployment jobs in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)

	pipelineStageDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "pipeline_stage_duration_seconds",
			Help:    "Duration of each pipeline stage",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"stage", "status"},
	)

	jobErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_errors_total",
			Help: "Total number of job errors by stage",
		},
		[]string{"stage"},
	)
)

func init() {
	prometheus.MustRegister(jobDurationSeconds, pipelineStageDuration, jobErrorsTotal)
}
