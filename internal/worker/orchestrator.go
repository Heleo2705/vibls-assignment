package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"deploy-api/internal/models"
	"deploy-api/internal/verification"
)

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// Orchestrator coordinates the deployment pipeline stages.
type Orchestrator struct {
	repo          *models.Repository
	cloneFn       CloneFn
	buildFn       BuildFn
	pushFn        PushFn
	manifestFn    ManifestFn
	verifyFn      VerifyFn
	applyFn       ApplyFn
	healthCheckFn HealthCheckFn
}

// CloneFn clones a repository for a deployment job.
type CloneFn func(ctx context.Context, jobID, repoURL, branch, workspace string) error

// BuildFn builds a Docker image for a deployment job.
type BuildFn func(ctx context.Context, jobID, buildCtx, dockerfile, tag string, workspace string) error

// PushFn pushes a Docker image to a registry.
type PushFn func(ctx context.Context, tag string) error

// ManifestFn generates Kubernetes manifests for a deployment job.
type ManifestFn func(ctx context.Context, jobID, namespace, image string, overrides *models.ResourceOverrides, workspace string) error

// VerifyFn runs verification checks on generated manifests and container image.
type VerifyFn func(ctx context.Context, manifestDir, imageTag string) (*verification.VerificationResult, error)

// ApplyFn applies Kubernetes manifests to a cluster.
type ApplyFn func(ctx context.Context, namespace, manifestDir string) error

// HealthCheckFn checks deployment health after apply.
type HealthCheckFn func(ctx context.Context, namespace string) *DeploymentHealth

// NewOrchestrator creates a new Orchestrator with all stage functions.
func NewOrchestrator(
	repo *models.Repository,
	cloneFn CloneFn,
	buildFn BuildFn,
	pushFn PushFn,
	manifestFn ManifestFn,
	verifyFn VerifyFn,
	applyFn ApplyFn,
	healthCheckFn HealthCheckFn,
) *Orchestrator {
	return &Orchestrator{
		repo:          repo,
		cloneFn:       cloneFn,
		buildFn:       buildFn,
		pushFn:        pushFn,
		manifestFn:    manifestFn,
		verifyFn:      verifyFn,
		applyFn:       applyFn,
		healthCheckFn: healthCheckFn,
	}
}

// Execute runs the full deployment pipeline for a job.
// Stages: Clone → Build → Push → ManifestGen → Verify → Apply → HealthCheck
// On any error, the job status is updated to Failed with the error message.
func (o *Orchestrator) Execute(ctx context.Context, job *models.Job) error {
	registry := getEnv("CONTAINER_REGISTRY", "registry:5000")
	workspace := fmt.Sprintf("/tmp/workspaces/%s", job.ID)
	manifestDir := filepath.Join(workspace, "manifests", job.ID)
	imageTag := fmt.Sprintf("%s/%s:%s", registry, job.TargetNamespace, job.ID)

	// Stage 1: Clone
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StageCloning, ""); err != nil {
		return fmt.Errorf("update job status to running/cloning: %w", err)
	}
	if err := o.cloneFn(ctx, job.ID, job.RepositoryURL, job.Branch, workspace); err != nil {
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageCloning, err.Error())
		return fmt.Errorf("clone stage: %w", err)
	}

	// Stage 2: Build
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StageBuilding, ""); err != nil {
		return fmt.Errorf("update job status to building: %w", err)
	}
	if err := o.buildFn(ctx, job.ID, job.BuildContext, job.DockerfilePath, imageTag, workspace); err != nil {
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageBuilding, err.Error())
		return fmt.Errorf("build stage: %w", err)
	}

	// Stage 3: Push
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StagePushing, ""); err != nil {
		return fmt.Errorf("update job status to pushing: %w", err)
	}
	if err := o.pushFn(ctx, imageTag); err != nil {
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StagePushing, err.Error())
		return fmt.Errorf("push stage: %w", err)
	}

	// Stage 4: Manifest Generation
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StageManifestGenerating, ""); err != nil {
		return fmt.Errorf("update job status to manifest generating: %w", err)
	}
	if err := o.manifestFn(ctx, job.ID, job.TargetNamespace, imageTag, job.ResourceOverrides, workspace); err != nil {
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageManifestGenerating, err.Error())
		return fmt.Errorf("manifest generation stage: %w", err)
	}

	// Stage 5: Verification
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StageVerifying, ""); err != nil {
		return fmt.Errorf("update job status to verifying: %w", err)
	}
	result, err := o.verifyFn(ctx, manifestDir, imageTag)
	if err != nil {
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageVerifying, err.Error())
		return fmt.Errorf("verify stage: %w", err)
	}
	if !result.OverallPass {
		msg := fmt.Sprintf("verification failed: %s", result.Heuristic.Summary)
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageVerifying, msg)
		return fmt.Errorf("verify stage: %s", msg)
	}

	// Stage 6: Apply
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StageApplying, ""); err != nil {
		return fmt.Errorf("update job status to applying: %w", err)
	}
	if err := o.applyFn(ctx, job.TargetNamespace, manifestDir); err != nil {
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageApplying, err.Error())
		return fmt.Errorf("apply stage: %w", err)
	}

	// Stage 7: Health Check
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusRunning, models.StageHealthChecking, ""); err != nil {
		return fmt.Errorf("update job status to health checking: %w", err)
	}
	health := o.healthCheckFn(ctx, job.TargetNamespace)
	if !health.Ready {
		msg := fmt.Sprintf("health check failed: %d/%d pods ready, service up=%v", health.PodsReady, health.PodsTotal, health.ServiceUp)
		_ = o.repo.UpdateJobStatus(ctx, job.ID, models.StatusFailed, models.StageHealthChecking, msg)
		return fmt.Errorf("health check stage: %s", msg)
	}

	// All stages completed successfully
	if err := o.repo.UpdateJobStatus(ctx, job.ID, models.StatusCompleted, models.StageHealthChecking, ""); err != nil {
		return fmt.Errorf("update job status to completed: %w", err)
	}

	return nil
}
