package worker

import (
	"context"

	"github.com/rs/zerolog"

	"deploy-api/internal/verification"
)

// NewVerifyStage creates a VerifyFn that runs heuristic checks on manifests and images.
func NewVerifyStage(logger zerolog.Logger) VerifyFn {
	return func(ctx context.Context, manifestDir, imageTag string) (*verification.VerificationResult, error) {
		heuristic := verification.RunHeuristicVerification(ctx, logger, manifestDir, imageTag)
		return &verification.VerificationResult{
			Heuristic:   heuristic,
			OverallPass: heuristic.Passed,
		}, nil
	}
}
