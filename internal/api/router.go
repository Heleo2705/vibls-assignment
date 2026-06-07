package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"deploy-api/internal/models"
	"deploy-api/internal/queue"
)

// Dependencies holds all dependencies that handlers need.
type Dependencies struct {
	Repo        *models.Repository
	QueueClient *queue.QueueClient
	Logger      zerolog.Logger
	RegoEngine  *RegoEngine
}

// Handler holds dependencies and provides HTTP handler methods.
type Handler struct {
	deps Dependencies
}

// NewHandler creates a new Handler with the given dependencies.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{deps: deps}
}

// MountRoutes attaches all API routes to the given router.
func (h *Handler) MountRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.With(authMiddleware(h.deps)).Post("/jobs", h.handleCreateJob)
		r.With(authMiddleware(h.deps)).Get("/jobs", h.handleListJobs)
		r.With(authMiddleware(h.deps)).Get("/jobs/{id}", h.handleGetJob)
	})
}

