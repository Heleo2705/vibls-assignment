package worker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// NewBuildStage creates a BuildFn that runs docker build via os/exec.
// The workspace directory is used as the working directory for the build,
// so relative build_context and dockerfile_path resolve against the clone root.
func NewBuildStage(logger zerolog.Logger) BuildFn {
	return func(ctx context.Context, jobID, buildCtx, dockerfile, tag string, workspace string) error {
		args := []string{"build"}

		if dockerfile != "" {
			args = append(args, "-f", dockerfile)
		}

		if tag != "" {
			args = append(args, "-t", tag)
		}

		args = append(args, buildCtx)

		start := time.Now()
		logger.Info().Str("job_id", jobID).Strs("args", args).Str("workdir", workspace).Msg("building docker image")

		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Dir = workspace
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker build failed: %w\noutput: %s", err, strings.TrimSpace(string(output)))
		}

		logger.Info().Str("job_id", jobID).Dur("duration", time.Since(start)).Msg("docker image built")
		return nil
	}
}
