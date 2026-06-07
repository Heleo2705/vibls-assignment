package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// authMiddleware is a chi middleware that enforces ABAC authorization.
// It extracts token from Authorization header, maps to role, and evaluates OPA policy.
func authMiddleware(deps Dependencies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract the action from the request method + route pattern
			action := resolveAction(r)
			if action == "" {
				http.Error(w, `{"error":"unknown action"}`, http.StatusBadRequest)
				return
			}

			// Extract namespace from query param (default: "sandbox")
			// Dev note: In production, namespace should come from token-bound identity, not user params.
			namespace := r.URL.Query().Get("namespace")
			if namespace == "" {
				namespace = "sandbox"
			}

			// Extract role from Authorization header (simple Bearer token mapping)
			role := resolveRole(r)
			if role == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// Nil guard: ensure RegoEngine was initialized
			if deps.RegoEngine == nil {
				deps.Logger.Error().Msg("RegoEngine not initialized")
				http.Error(w, `{"error":"authorization misconfigured"}`, http.StatusInternalServerError)
				return
			}

			// Evaluate OPA policy
			input := AuthzInput{
				User:   AuthzUser{Role: role},
				Action: action,
				Resource: AuthzResource{Namespace: namespace},
			}

			allowed, err := deps.RegoEngine.Authorize(r.Context(), input)
			if err != nil {
				deps.Logger.Error().Err(err).Msg("authorization error")
				http.Error(w, `{"error":"authorization failed"}`, http.StatusInternalServerError)
				return
			}

			if !allowed {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// resolveAction maps HTTP method + route pattern to an action name.
// Uses chi.RouteContext for pattern-aware matching.
func resolveAction(r *http.Request) string {
	// Use chi route context to get matched pattern, fall back to path heuristics
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		pattern := rctx.RoutePattern()
		switch {
		case r.Method == "POST" && pattern == "/api/v1/jobs":
			return "create_job"
		case r.Method == "GET" && pattern == "/api/v1/jobs":
			return "list_jobs"
		case r.Method == "GET" && pattern == "/api/v1/jobs/{id}":
			return "view_job"
		}
	}

	// Fallback heuristic (when route context is unavailable, e.g., during testing)
	if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/jobs") {
		return "create_job"
	}
	if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/jobs") {
		return "list_jobs"
	}
	if r.Method == "GET" && strings.Count(r.URL.Path, "/") >= 4 && strings.Contains(r.URL.Path, "/jobs/") {
		return "view_job"
	}
	return ""
}

// resolveRole extracts the user role from the request.
// Uses a simple Bearer token -> role mapping for local development.
func resolveRole(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}

	// Simple token mapping for local dev
	token := strings.TrimPrefix(auth, "Bearer ")
	switch token {
	case "admin-token":
		return "admin"
	case "dev-token":
		return "developer"
	case "view-token":
		return "viewer"
	default:
		return ""
	}
}
