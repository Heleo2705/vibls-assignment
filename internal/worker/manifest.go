package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/rs/zerolog"

	"deploy-api/internal/models"
	"deploy-api/internal/templates"
)

// ManifestData holds all data for K8s manifest templates.
type ManifestData struct {
	Name                 string
	Namespace            string
	Image                string
	Replicas             int
	ContainerPort        int
	ServicePort          int
	Host                 string
	CPURequest           string
	CPULimit             string
	MemoryRequest        string
	MemoryLimit          string
	MinReplicas          int
	MaxReplicas          int
	TargetCPUUtilization int
}

// DefaultManifestData returns a ManifestData with sensible defaults.
func DefaultManifestData() ManifestData {
	return ManifestData{
		Replicas:            1,
		ContainerPort:       8080,
		ServicePort:         80,
		Host:                "example.local",
		CPURequest:          "100m",
		CPULimit:            "500m",
		MemoryRequest:       "128Mi",
		MemoryLimit:         "256Mi",
		MinReplicas:         1,
		MaxReplicas:         5,
		TargetCPUUtilization: 80,
	}
}

// NewManifestStage creates a ManifestFn that generates Kubernetes YAML from embedded templates.
func NewManifestStage(logger zerolog.Logger, outputBase string) ManifestFn {
	if outputBase == "" {
		outputBase = "/tmp/deploy-api/manifests"
	}

	return func(ctx context.Context, jobID, namespace, image string, overrides *models.ResourceOverrides, workspace string) error {
		data := DefaultManifestData()
		// Prefix with "app-" to ensure DNS-1035 compliance (UUIDs can start with a digit)
		data.Name = "app-" + jobID
		data.Namespace = namespace
		data.Image = image

		// Use local copy of outputBase to avoid data race on closure capture
		base := outputBase
		if workspace != "" {
			base = filepath.Join(workspace, "manifests")
		}

		if overrides != nil {
			if overrides.Replicas != nil {
				data.Replicas = *overrides.Replicas
			}
			if overrides.CPUCores != nil {
				millicores := int(*overrides.CPUCores * 1000)
				data.CPURequest = fmt.Sprintf("%dm", millicores)
				data.CPULimit = fmt.Sprintf("%dm", millicores)
			}
			if overrides.MemoryMB != nil {
				data.MemoryRequest = fmt.Sprintf("%dMi", *overrides.MemoryMB)
				data.MemoryLimit = fmt.Sprintf("%dMi", *overrides.MemoryMB)
			}
		}

		outputDir := filepath.Join(base, jobID)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("create manifest output dir: %w", err)
		}

		// Load and render each template from the embedded filesystem.
		// Templates are embedded via internal/templates/embed.go under the
		// "manifests/" directory prefix.
		entries, err := templates.Manifests.ReadDir("manifests")
		if err != nil {
			return fmt.Errorf("read template dir: %w", err)
		}

		start := time.Now()
		logger.Info().Str("job_id", jobID).Msg("generating kubernetes manifests")

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			tmplPath := "manifests/" + entry.Name()
			tmplContent, err := templates.Manifests.ReadFile(tmplPath)
			if err != nil {
				return fmt.Errorf("read template %s: %w", entry.Name(), err)
			}

			tmpl, err := template.New(entry.Name()).Parse(string(tmplContent))
			if err != nil {
				return fmt.Errorf("parse template %s: %w", entry.Name(), err)
			}

			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				return fmt.Errorf("execute template %s: %w", entry.Name(), err)
			}

			outName := strings.Replace(entry.Name(), ".go.tmpl", ".yaml", 1)
			outputFile := filepath.Join(outputDir, outName)
			if err := os.WriteFile(outputFile, buf.Bytes(), 0644); err != nil {
				return fmt.Errorf("write manifest %s: %w", outName, err)
			}
		}

		logger.Info().
			Str("job_id", jobID).
			Str("output_dir", outputDir).
			Dur("duration", time.Since(start)).
			Msg("manifests generated")
		return nil
	}
}
