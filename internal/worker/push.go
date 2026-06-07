package worker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// NewPushStage creates a PushFn that runs docker push via os/exec.
func NewPushStage(logger zerolog.Logger) PushFn {
	return func(ctx context.Context, tag string) error {
		if tag == "" {
			return fmt.Errorf("tag is required for docker push")
		}

		start := time.Now()
		logger.Info().Str("tag", tag).Msg("pushing docker image")

		cmd := exec.CommandContext(ctx, "docker", "push", tag)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker push failed: %w\noutput: %s", err, strings.TrimSpace(string(output)))
		}

		logger.Info().Str("tag", tag).Dur("duration", time.Since(start)).Msg("docker image pushed")
		return nil
	}
}
