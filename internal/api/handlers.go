package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"deploy-api/internal/models"
	"deploy-api/internal/queue"
)

// createJobRequest is the JSON body for creating a deployment job.
type createJobRequest struct {
	RepositoryURL     string                    `json:"repository_url"`
	Branch            string                    `json:"branch"`
	BuildContext      string                    `json:"build_context"`
	DockerfilePath    string                    `json:"dockerfile_path"`
	TargetNamespace   string                    `json:"target_namespace"`
	ResourceOverrides *models.ResourceOverrides `json:"resource_overrides,omitempty"`
	Version           string                    `json:"version,omitempty"`
}

// idempotencyHash computes a deterministic SHA-256 hash from the deployment
// identity fields. Include version so clients can force a fresh deployment.
func idempotencyHash(req createJobRequest) string {
	scope := struct {
		Repo   string                    `json:"repo_url"`
		Branch string                    `json:"branch"`
		NS     string                    `json:"target_namespace"`
		Ctx    string                    `json:"build_context"`
		DfPath string                    `json:"dockerfile_path"`
		Over   *models.ResourceOverrides `json:"resource_overrides,omitempty"`
		Ver    string                    `json:"version,omitempty"`
	}{
		Repo:   req.RepositoryURL,
		Branch: req.Branch,
		NS:     req.TargetNamespace,
		Ctx:    req.BuildContext,
		DfPath: req.DockerfilePath,
		Over:   req.ResourceOverrides,
		Ver:    req.Version,
	}
	data, _ := json.Marshal(scope)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// handleCreateJob handles POST /api/v1/jobs
func (h *Handler) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	// Limit request body to 1 MB to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req createJobRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.RepositoryURL == "" {
		http.Error(w, `{"error":"repository_url is required"}`, http.StatusBadRequest)
		return
	}
	if req.TargetNamespace == "" {
		http.Error(w, `{"error":"target_namespace is required"}`, http.StatusBadRequest)
		return
	}

	// 1. Idempotency check — return cached response if this exact request
	//    has been processed before.
	hash := idempotencyHash(req)
	existingKey, err := h.deps.Repo.GetIdempotencyKey(r.Context(), hash)
	if err == nil {
		// Found cached key — replay the same 201 response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write(existingKey.Response)
		return
	}

	// 2. Active deployment guard — prevent duplicate active deployments for
	//    the same target (repo + branch + namespace).
	activeJob, err := h.deps.Repo.GetActiveDeployment(r.Context(), req.RepositoryURL, req.Branch, req.TargetNamespace)
	if err == nil && activeJob != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":            "active deployment already exists for this target",
			"existing_job_id":  activeJob.ID,
			"existing_status":  activeJob.Status,
		})
		return
	}

	// 3. Transactional job creation — create the job and idempotency key
	//    atomically so a crash between insert and key-write cannot orphan a job.
	tx, err := h.deps.Repo.BeginTx(r.Context())
	if err != nil {
		h.deps.Logger.Error().Err(err).Msg("failed to begin transaction")
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	now := time.Now().UTC()
	job := models.Job{
		RepositoryURL:     req.RepositoryURL,
		Branch:            req.Branch,
		BuildContext:      req.BuildContext,
		DockerfilePath:    req.DockerfilePath,
		TargetNamespace:   req.TargetNamespace,
		Status:            models.StatusQueued,
		ResourceOverrides: req.ResourceOverrides,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	id, err := h.deps.Repo.CreateJobTx(r.Context(), &job, tx)
	if err != nil {
		h.deps.Logger.Error().Err(err).Msg("failed to create job")
		http.Error(w, `{"error":"failed to create job"}`, http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"id":     id,
		"status": models.StatusQueued,
	}
	respJSON, _ := json.Marshal(response)

	if err := h.deps.Repo.CreateIdempotencyKeyTx(r.Context(), hash, id, respJSON, tx); err != nil {
		tx.Rollback(r.Context())
		if errors.Is(err, models.ErrIdempotencyKeyExists) {
			// Another request created this key first. Return the original response.
			existingKey, getErr := h.deps.Repo.GetIdempotencyKey(r.Context(), hash)
			if getErr == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write(existingKey.Response)
				return
			}
			h.deps.Logger.Error().Err(getErr).Msg("idempotency conflict recovery failed")
			http.Error(w, `{"error":"conflict"}`, http.StatusConflict)
			return
		}
		h.deps.Logger.Error().Err(err).Msg("failed to create idempotency key")
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		h.deps.Logger.Error().Err(err).Msg("failed to commit transaction")
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}

	// 4. Enqueue the deploy job for asynchronous processing
	payload, err := json.Marshal(queue.DeployJobPayload{JobID: id})
	if err != nil {
		h.deps.Logger.Error().Err(err).Msg("failed to marshal queue payload")
		http.Error(w, `{"error":"failed to enqueue job"}`, http.StatusInternalServerError)
		return
	}

	if _, err := h.deps.QueueClient.EnqueueDeployJob(r.Context(), id, payload); err != nil {
		h.deps.Logger.Error().Err(err).Str("job_id", id).Msg("failed to enqueue deploy job")
		http.Error(w, `{"error":"failed to enqueue job"}`, http.StatusInternalServerError)
		return
	}

	// 5. Respond with the created job
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

// handleListJobs handles GET /api/v1/jobs
func (h *Handler) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	opts := models.ListJobsOptions{
		Limit:  20,
		Offset: 0,
	}

	if s := q.Get("status"); s != "" {
		status := models.JobStatus(s)
		opts.Status = &status
	}

	if ns := q.Get("namespace"); ns != "" {
		opts.Namespace = &ns
	}

	if limitStr := q.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}

	if offsetStr := q.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			opts.Offset = offset
		}
	}

	result, err := h.deps.Repo.ListJobs(r.Context(), opts)
	if err != nil {
		h.deps.Logger.Error().Err(err).Msg("failed to list jobs")
		http.Error(w, `{"error":"failed to list jobs"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}

// handleGetJob handles GET /api/v1/jobs/{id}
func (h *Handler) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, `{"error":"job id is required"}`, http.StatusBadRequest)
		return
	}

	job, err := h.deps.Repo.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
			return
		}
		h.deps.Logger.Error().Err(err).Str("job_id", id).Msg("failed to get job")
		http.Error(w, `{"error":"failed to get job"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(job)
}
