package models

import (
	"encoding/json"
	"time"
)

// JobEvent represents a single state transition or notable event in a job's
// lifecycle. Events are appended to provide a full audit trail for debugging
// and observability.
type JobEvent struct {
	ID        string           `json:"id"`
	JobID     string           `json:"job_id"`
	FromStage *PipelineStage   `json:"from_stage,omitempty"`
	ToStage   *PipelineStage   `json:"to_stage,omitempty"`
	Status    JobStatus        `json:"status"`
	Message   string           `json:"message,omitempty"`
	Metadata  json.RawMessage  `json:"metadata,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}
