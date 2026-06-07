package models

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a job is not found.
var ErrNotFound = pgx.ErrNoRows

// ErrIdempotencyKeyExists is returned by CreateIdempotencyKeyTx when the key
// already exists (ON CONFLICT DO NOTHING inserted zero rows).
var ErrIdempotencyKeyExists = fmt.Errorf("idempotency key already exists")

// IdempotencyKey represents a cached idempotent request.
type IdempotencyKey struct {
	Hash      string          `json:"hash"`
	JobID     string          `json:"job_id"`
	Response  json.RawMessage `json:"response"`
	CreatedAt time.Time       `json:"created_at"`
}

// Repository handles all database operations for deployment jobs.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new Repository with the given connection pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// BeginTx starts a new PostgreSQL transaction.
func (r *Repository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// CreateJob inserts a new job and returns its ID.
func (r *Repository) CreateJob(ctx context.Context, job *Job) (string, error) {
	query := `
		INSERT INTO jobs (
			repo_url, branch, build_context, dockerfile_path, target_namespace,
			status, current_stage, resource_overrides, stage_timestamps,
			error_message, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12
		) RETURNING id
	`

	var resourceOverridesJSON []byte
	if job.ResourceOverrides != nil {
		var err error
		resourceOverridesJSON, err = json.Marshal(job.ResourceOverrides)
		if err != nil {
			return "", fmt.Errorf("marshal resource_overrides: %w", err)
		}
	}

	var stageTimestampsJSON []byte
	if job.StageTimestamps != nil {
		var err error
		stageTimestampsJSON, err = json.Marshal(job.StageTimestamps)
		if err != nil {
			return "", fmt.Errorf("marshal stage_timestamps: %w", err)
		}
	}

	var id string
	err := r.pool.QueryRow(ctx, query,
		job.RepositoryURL, job.Branch, job.BuildContext, job.DockerfilePath, job.TargetNamespace,
		job.Status, job.CurrentStage,
		resourceOverridesJSON, stageTimestampsJSON,
		job.ErrorMessage, job.CreatedAt, job.UpdatedAt,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}

	return id, nil
}

// GetIdempotencyKey retrieves a cached idempotency key by hash.
// Returns pgx.ErrNoRows if not found.
func (r *Repository) GetIdempotencyKey(ctx context.Context, hash string) (*IdempotencyKey, error) {
	query := `
		SELECT hash, job_id, response, created_at
		FROM idempotency_keys
		WHERE hash = $1
	`

	var key IdempotencyKey
	err := r.pool.QueryRow(ctx, query, hash).Scan(
		&key.Hash, &key.JobID, &key.Response, &key.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &key, nil
}

// CreateIdempotencyKeyTx inserts a new idempotency key within the provided
// transaction. Uses ON CONFLICT DO NOTHING so concurrent duplicate inserts
// return ErrIdempotencyKeyExists.
func (r *Repository) CreateIdempotencyKeyTx(ctx context.Context, hash, jobID string, response json.RawMessage, tx pgx.Tx) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (hash, job_id, response)
		VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO NOTHING
	`, hash, jobID, response)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrIdempotencyKeyExists
	}
	return nil
}

// DeleteExpiredIdempotencyKeys deletes idempotency keys older than the
// given age. Returns the number of deleted rows.
func (r *Repository) DeleteExpiredIdempotencyKeys(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-age)
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM idempotency_keys WHERE created_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// GetActiveDeployment checks if there is an active (QUEUED or RUNNING)
// deployment for the given repo, branch, and namespace. Returns the job
// if found, or pgx.ErrNoRows if none exists.
func (r *Repository) GetActiveDeployment(ctx context.Context, repoURL, branch, namespace string) (*Job, error) {
	query := `
		SELECT id, status FROM jobs
		WHERE repo_url = $1 AND branch = $2 AND target_namespace = $3
		  AND status IN ('QUEUED', 'RUNNING')
		LIMIT 1
	`

	var job Job
	err := r.pool.QueryRow(ctx, query, repoURL, branch, namespace).Scan(
		&job.ID, &job.Status,
	)
	if err != nil {
		return nil, err
	}

	return &job, nil
}

// CreateJobTx inserts a new job within the provided transaction and returns
// its ID. Equivalent to CreateJob but uses the supplied transaction instead
// of the connection pool directly.
func (r *Repository) CreateJobTx(ctx context.Context, job *Job, tx pgx.Tx) (string, error) {
	query := `
		INSERT INTO jobs (
			repo_url, branch, build_context, dockerfile_path, target_namespace,
			status, current_stage, resource_overrides, stage_timestamps,
			error_message, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12
		) RETURNING id
	`

	var resourceOverridesJSON []byte
	if job.ResourceOverrides != nil {
		var err error
		resourceOverridesJSON, err = json.Marshal(job.ResourceOverrides)
		if err != nil {
			return "", fmt.Errorf("marshal resource_overrides: %w", err)
		}
	}

	var stageTimestampsJSON []byte
	if job.StageTimestamps != nil {
		var err error
		stageTimestampsJSON, err = json.Marshal(job.StageTimestamps)
		if err != nil {
			return "", fmt.Errorf("marshal stage_timestamps: %w", err)
		}
	}

	var id string
	err := tx.QueryRow(ctx, query,
		job.RepositoryURL, job.Branch, job.BuildContext, job.DockerfilePath, job.TargetNamespace,
		job.Status, job.CurrentStage,
		resourceOverridesJSON, stageTimestampsJSON,
		job.ErrorMessage, job.CreatedAt, job.UpdatedAt,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}

	return id, nil
}

// CreateJobEvent inserts an audit trail event for a job.
func (r *Repository) CreateJobEvent(ctx context.Context, event *JobEvent) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO job_events (job_id, from_stage, to_stage, status, message, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, event.JobID, event.FromStage, event.ToStage, event.Status, event.Message, event.Metadata)
	return err
}

// ListJobEvents returns all audit trail events for a job, ordered
// chronologically.
func (r *Repository) ListJobEvents(ctx context.Context, jobID string) ([]*JobEvent, error) {
	query := `
		SELECT id, job_id, from_stage, to_stage, status, message, metadata, created_at
		FROM job_events
		WHERE job_id = $1
		ORDER BY created_at ASC
	`

	rows, err := r.pool.Query(ctx, query, jobID)
	if err != nil {
		return nil, fmt.Errorf("list job events: %w", err)
	}
	defer rows.Close()

	var events []*JobEvent
	for rows.Next() {
		var ev JobEvent
		var rawFromStage, rawToStage *string
		var rawMetadata json.RawMessage

		if err := rows.Scan(
			&ev.ID, &ev.JobID,
			&rawFromStage, &rawToStage,
			&ev.Status, &ev.Message,
			&rawMetadata, &ev.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan job event: %w", err)
		}

		if rawFromStage != nil {
			s := PipelineStage(*rawFromStage)
			ev.FromStage = &s
		}
		if rawToStage != nil {
			s := PipelineStage(*rawToStage)
			ev.ToStage = &s
		}
		if rawMetadata != nil {
			ev.Metadata = rawMetadata
		}

		events = append(events, &ev)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job events: %w", err)
	}

	if events == nil {
		events = []*JobEvent{}
	}

	return events, nil
}

// GetJob retrieves a single job by ID. Returns ErrNotFound if not found.
func (r *Repository) GetJob(ctx context.Context, id string) (*Job, error) {
	query := `
		SELECT id, repo_url, branch, build_context, dockerfile_path, target_namespace,
		       status, current_stage, resource_overrides, stage_timestamps,
		       error_message, created_at, updated_at
		FROM jobs
		WHERE id = $1
	`

	var job Job
	var rawResourceOverrides, rawStageTimestamps json.RawMessage

	err := r.pool.QueryRow(ctx, query, id).Scan(
		&job.ID, &job.RepositoryURL, &job.Branch, &job.BuildContext, &job.DockerfilePath, &job.TargetNamespace,
		&job.Status, &job.CurrentStage,
		&rawResourceOverrides, &rawStageTimestamps,
		&job.ErrorMessage, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if rawResourceOverrides != nil {
		var ro ResourceOverrides
		if err := json.Unmarshal(rawResourceOverrides, &ro); err != nil {
			return nil, fmt.Errorf("unmarshal resource_overrides: %w", err)
		}
		job.ResourceOverrides = &ro
	}

	if rawStageTimestamps != nil {
		var timestamps map[PipelineStage]time.Time
		if err := json.Unmarshal(rawStageTimestamps, &timestamps); err != nil {
			return nil, fmt.Errorf("unmarshal stage_timestamps: %w", err)
		}
		job.StageTimestamps = timestamps
	}

	return &job, nil
}

// ListJobsOptions holds pagination and filter parameters.
type ListJobsOptions struct {
	Status        *JobStatus `json:"status,omitempty"`
	Namespace     *string    `json:"namespace,omitempty"`
	CreatedAfter  *time.Time `json:"created_after,omitempty"`
	CreatedBefore *time.Time `json:"created_before,omitempty"`
	Limit         int        `json:"limit,omitempty"`
	Offset        int        `json:"offset,omitempty"`
}

// ListJobsResult holds the paginated results and total count.
type ListJobsResult struct {
	Jobs  []*Job `json:"jobs"`
	Total int    `json:"total"`
}

// ListJobs returns paginated jobs with optional filters.
func (r *Repository) ListJobs(ctx context.Context, opts ListJobsOptions) (*ListJobsResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	var conditions []string
	var args []any
	argIdx := 1

	if opts.Status != nil {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *opts.Status)
		argIdx++
	}
	if opts.Namespace != nil {
		conditions = append(conditions, fmt.Sprintf("target_namespace = $%d", argIdx))
		args = append(args, *opts.Namespace)
		argIdx++
	}
	if opts.CreatedAfter != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argIdx))
		args = append(args, *opts.CreatedAfter)
		argIdx++
	}
	if opts.CreatedBefore != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argIdx))
		args = append(args, *opts.CreatedBefore)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count query
	countQuery := "SELECT COUNT(*) FROM jobs " + whereClause
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}

	// Data query
	dataQuery := fmt.Sprintf(`
		SELECT id, repo_url, branch, build_context, dockerfile_path, target_namespace,
		       status, current_stage, resource_overrides, stage_timestamps,
		       error_message, created_at, updated_at
		FROM jobs %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`,
		whereClause, argIdx, argIdx+1,
	)
	args = append(args, opts.Limit, opts.Offset)

	rows, err := r.pool.Query(ctx, dataQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		var job Job
		var rawResourceOverrides, rawStageTimestamps json.RawMessage

		if err := rows.Scan(
			&job.ID, &job.RepositoryURL, &job.Branch, &job.BuildContext, &job.DockerfilePath, &job.TargetNamespace,
			&job.Status, &job.CurrentStage,
			&rawResourceOverrides, &rawStageTimestamps,
			&job.ErrorMessage, &job.CreatedAt, &job.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}

		if rawResourceOverrides != nil {
			var ro ResourceOverrides
			if err := json.Unmarshal(rawResourceOverrides, &ro); err != nil {
				return nil, fmt.Errorf("unmarshal resource_overrides: %w", err)
			}
			job.ResourceOverrides = &ro
		}

		if rawStageTimestamps != nil {
			var timestamps map[PipelineStage]time.Time
			if err := json.Unmarshal(rawStageTimestamps, &timestamps); err != nil {
				return nil, fmt.Errorf("unmarshal stage_timestamps: %w", err)
			}
			job.StageTimestamps = timestamps
		}

		jobs = append(jobs, &job)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}

	if jobs == nil {
		jobs = []*Job{}
	}

	return &ListJobsResult{
		Jobs:  jobs,
		Total: total,
	}, nil
}

// UpdateJobStatus updates a job's status, current stage, and optional error message.
func (r *Repository) UpdateJobStatus(ctx context.Context, id string, status JobStatus, stage PipelineStage, errorMsg string) error {
	query := `
		UPDATE jobs
		SET status = $1, current_stage = $2, error_message = COALESCE($3, error_message), updated_at = now()
		WHERE id = $4
	`

	tag, err := r.pool.Exec(ctx, query, status, stage, errorMsg, id)
	if err != nil {
		return fmt.Errorf("update job status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	// Audit trail (best-effort) — record this state transition
	_, _ = r.pool.Exec(ctx, `
		INSERT INTO job_events (job_id, to_stage, status, message)
		VALUES ($1, $2, $3, $4)
	`, id, stage, status, errorMsg)

	return nil
}

// ListCompletedJobs returns terminal-state jobs (COMPLETED or FAILED) updated after the given time.
func (r *Repository) ListCompletedJobs(ctx context.Context, since time.Time) ([]*Job, error) {
	query := `
		SELECT id, repo_url, branch, build_context, dockerfile_path, target_namespace,
		       status, current_stage, resource_overrides, stage_timestamps,
		       error_message, created_at, updated_at
		FROM jobs
		WHERE status IN ('COMPLETED', 'FAILED') AND updated_at >= $1
		ORDER BY updated_at DESC
	`

	rows, err := r.pool.Query(ctx, query, since)
	if err != nil {
		return nil, fmt.Errorf("list completed jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		var job Job
		var rawResourceOverrides, rawStageTimestamps json.RawMessage

		if err := rows.Scan(
			&job.ID, &job.RepositoryURL, &job.Branch, &job.BuildContext, &job.DockerfilePath, &job.TargetNamespace,
			&job.Status, &job.CurrentStage,
			&rawResourceOverrides, &rawStageTimestamps,
			&job.ErrorMessage, &job.CreatedAt, &job.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}

		if rawResourceOverrides != nil {
			var ro ResourceOverrides
			if err := json.Unmarshal(rawResourceOverrides, &ro); err != nil {
				return nil, fmt.Errorf("unmarshal resource_overrides: %w", err)
			}
			job.ResourceOverrides = &ro
		}

		if rawStageTimestamps != nil {
			var timestamps map[PipelineStage]time.Time
			if err := json.Unmarshal(rawStageTimestamps, &timestamps); err != nil {
				return nil, fmt.Errorf("unmarshal stage_timestamps: %w", err)
			}
			job.StageTimestamps = timestamps
		}

		jobs = append(jobs, &job)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate completed jobs: %w", err)
	}

	if jobs == nil {
		jobs = []*Job{}
	}

	return jobs, nil
}
