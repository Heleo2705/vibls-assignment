package api

import (
	"context"
	"fmt"

	"github.com/open-policy-agent/opa/rego"
	"github.com/rs/zerolog"
)

// AuthzInput is the input structure for ABAC policy evaluation.
type AuthzInput struct {
	User     AuthzUser     `json:"user"`
	Action   string        `json:"action"`
	Resource AuthzResource `json:"resource"`
}

// AuthzUser represents the authenticated user.
type AuthzUser struct {
	Role string `json:"role"`
}

// AuthzResource represents the resource being accessed.
type AuthzResource struct {
	Namespace string `json:"namespace"`
}

// RegoEngine evaluates OPA Rego policies for authorization decisions.
type RegoEngine struct {
	partial *rego.PreparedEvalQuery
	logger  zerolog.Logger
}

// NewRegoEngine creates a RegonEngine by compiling the given Rego policy content.
// The policyData should contain the full authz.rego source as a string.
func NewRegoEngine(logger zerolog.Logger, policyData string) (*RegoEngine, error) {
	query, err := rego.New(
		rego.Query("data.deploy.authz.allow"),
		rego.Module("deploy.authz", policyData),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to prepare rego query: %w", err)
	}

	return &RegoEngine{partial: &query, logger: logger}, nil
}

// Authorize evaluates the policy for the given input and returns true if allowed.
func (e *RegoEngine) Authorize(ctx context.Context, input AuthzInput) (bool, error) {
	results, err := e.partial.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return false, fmt.Errorf("rego eval error: %w", err)
	}

	if len(results) == 0 || len(results[0].Expressions) == 0 {
		return false, nil
	}

	allowed, ok := results[0].Expressions[0].Value.(bool)
	if !ok {
		return false, nil
	}

	return allowed, nil
}
