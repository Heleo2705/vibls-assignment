package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/rs/zerolog"
)

// NewCloneStage creates a CloneFn that clones a Git repository using go-git.
// workspaceBase is the base directory for all workspaces (e.g., "/tmp/deploy-api").
// If workspaceBase is empty, defaults to "/tmp/deploy-api/workspaces".
func NewCloneStage(logger zerolog.Logger, workspaceBase string) CloneFn {
	if workspaceBase == "" {
		workspaceBase = "/tmp/deploy-api/workspaces"
	}

	return func(ctx context.Context, jobID, repoURL, branch, workspace string) error {
		if workspace == "" {
			workspace = filepath.Join(workspaceBase, jobID)
		}

		// Clean any leftover workspace from a prior attempt, then recreate
		if err := os.RemoveAll(workspace); err != nil {
			return fmt.Errorf("clean workspace: %w", err)
		}
		if err := os.MkdirAll(workspace, 0755); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}

		// Clone options
		cloneOpts := &git.CloneOptions{
			URL:          repoURL,
			Progress:     nil,
			Depth:        1, // shallow clone
			SingleBranch: true,
		}

		if branch != "" {
			cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(branch)
		}

		start := time.Now()
		logger.Info().Str("job_id", jobID).Str("repo", repoURL).Str("branch", branch).Msg("cloning repository")

		_, err := git.PlainCloneContext(ctx, workspace, false, cloneOpts)
		if err != nil {
			return fmt.Errorf("git clone: %w", err)
		}

		logger.Info().Str("job_id", jobID).Dur("duration", time.Since(start)).Msg("repository cloned")
		return nil
	}
}
