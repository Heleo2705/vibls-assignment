package models

import (
	"encoding/json"
	"time"
)

// JobStatus represents the overall status of a deployment job.
type JobStatus string

const (
	StatusQueued    JobStatus = "QUEUED"
	StatusRunning   JobStatus = "RUNNING"
	StatusCompleted JobStatus = "COMPLETED"
	StatusFailed    JobStatus = "FAILED"
)

// PipelineStage represents a specific stage in the deployment pipeline.
type PipelineStage string

const (
	StageCloning            PipelineStage = "CLONING"
	StageBuilding           PipelineStage = "BUILDING"
	StagePushing            PipelineStage = "PUSHING"
	StageManifestGenerating PipelineStage = "MANIFEST_GENERATING"
	StageVerifying          PipelineStage = "VERIFYING"
	StageApplying           PipelineStage = "APPLYING"
	StageHealthChecking     PipelineStage = "HEALTH_CHECKING"
)

// ResourceOverrides holds optional resource overrides for the deployment.
type ResourceOverrides struct {
	CPUCores *float64 `json:"cpu_cores,omitempty"`
	MemoryMB *int     `json:"memory_mb,omitempty"`
	Replicas *int     `json:"replicas,omitempty"`
}

// Job represents a deployment job with full pipeline tracking.
type Job struct {
	ID                string                    `json:"id" db:"id"`
	RepositoryURL     string                    `json:"repository_url" db:"repo_url"`
	Branch            string                    `json:"branch" db:"branch"`
	BuildContext      string                    `json:"build_context" db:"build_context"`
	DockerfilePath    string                    `json:"dockerfile_path" db:"dockerfile_path"`
	TargetNamespace   string                    `json:"target_namespace" db:"target_namespace"`
	Status            JobStatus                 `json:"status" db:"status"`
	CurrentStage      PipelineStage             `json:"current_stage" db:"current_stage"`
	ResourceOverrides *ResourceOverrides         `json:"resource_overrides,omitempty" db:"resource_overrides"`
	StageTimestamps   map[PipelineStage]time.Time `json:"stage_timestamps,omitempty" db:"stage_timestamps"`
	ErrorMessage      string                    `json:"error_message,omitempty" db:"error_message"`
	CreatedAt         time.Time                 `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at" db:"updated_at"`
}

// validTransitions defines the allowed forward transitions between pipeline stages.
// Each entry maps a stage to the only valid next stage in the pipeline sequence.
var validTransitions = map[PipelineStage]PipelineStage{
	StageCloning:            StageBuilding,
	StageBuilding:           StagePushing,
	StagePushing:            StageManifestGenerating,
	StageManifestGenerating: StageVerifying,
	StageVerifying:          StageApplying,
	StageApplying:           StageHealthChecking,
}

// ValidTransition checks whether moving from current to next is a valid forward
// stage transition in the deployment pipeline.
// The pipeline follows the strict ordering:
// Cloning → Building → Pushing → ManifestGenerating → Verifying → Applying → HealthChecking
func ValidTransition(current, next PipelineStage) bool {
	expected, ok := validTransitions[current]
	if !ok {
		return false
	}
	return expected == next
}

// IsTerminal returns true if the job status is a terminal state
// (COMPLETED or FAILED).
func IsTerminal(status JobStatus) bool {
	return status == StatusCompleted || status == StatusFailed
}

// MarshalJSON implements json.Marshaler for Job.
func (j *Job) MarshalJSON() ([]byte, error) {
	type Alias Job
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(j),
	})
}
