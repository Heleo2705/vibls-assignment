package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"sigs.k8s.io/yaml"
)

// HeuristicVerdict holds the results of heuristic checks.
type HeuristicVerdict struct {
	Passed  bool             `json:"passed"`
	Score   int              `json:"score"` // 0-100
	Checks  []HeuristicCheck `json:"checks"`
	Summary string           `json:"summary"`
}

// HeuristicCheck represents a single check result.
type HeuristicCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// VerificationResult holds the complete verification output.
type VerificationResult struct {
	Heuristic   HeuristicVerdict `json:"heuristic"`
	AIVerdict   *AIVerdict       `json:"ai_verdict,omitempty"`
	OverallPass bool             `json:"overall_pass"`
}

// RunHeuristicVerification runs all heuristic checks on the generated manifests directory.
// manifestDir should contain the .yaml files output by the manifest stage.
// imageTag is the docker image tag to scan with Trivy.
func RunHeuristicVerification(ctx context.Context, logger zerolog.Logger, manifestDir, imageTag string) HeuristicVerdict {
	checks := make([]HeuristicCheck, 0, 4)

	checkKubeconform(ctx, logger, manifestDir, &checks)
	checkOPAPipeline(ctx, logger, manifestDir, &checks)
	checkTrivyScan(ctx, logger, imageTag, &checks)
	checkResourceBounds(ctx, logger, manifestDir, &checks)

	passed := 0
	total := 0
	for _, c := range checks {
		if strings.Contains(c.Detail, "not installed") || strings.Contains(c.Detail, "skipping") {
			continue // tool not available — don't penalize
		}
		total++
		if c.Passed {
			passed++
		}
	}
	if total == 0 {
		total = 1 // avoid division by zero
	}

	score := passed * 100 / total
	if score > 100 {
		score = 100
	}
	passedOverall := score >= 50

	var summary string
	if passedOverall {
		summary = fmt.Sprintf("passed %d/%d checks (score: %d/100)", passed, total, score)
	} else {
		summary = fmt.Sprintf("failed %d/%d checks (score: %d/100)", total-passed, total, score)
	}

	return HeuristicVerdict{
		Passed:  passedOverall,
		Score:   score,
		Checks:  checks,
		Summary: summary,
	}
}

// checkKubeconform runs kubeconform validation on generated manifests.
func checkKubeconform(ctx context.Context, logger zerolog.Logger, manifestDir string, checks *[]HeuristicCheck) {
	start := time.Now()

	// Check if kubeconform is installed
	if _, err := exec.LookPath("kubeconform"); err != nil {
		*checks = append(*checks, HeuristicCheck{
			Name:   "kubeconform",
			Passed: false,
			Detail: "kubeconform not installed, skipping",
		})
		logger.Warn().Dur("duration", time.Since(start)).Msg("kubeconform not installed")
		return
	}

	manifests, err := collectYAMLFiles(manifestDir)
	if err != nil || len(manifests) == 0 {
		*checks = append(*checks, HeuristicCheck{
			Name:   "kubeconform",
			Passed: false,
			Detail: fmt.Sprintf("no manifest files found in %s", manifestDir),
		})
		return
	}

	cmd := exec.CommandContext(ctx, "kubeconform", manifests...)
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err == nil {
		*checks = append(*checks, HeuristicCheck{
			Name:   "kubeconform",
			Passed: true,
			Detail: fmt.Sprintf("all manifests validated successfully (%s)", elapsed),
		})
		logger.Info().Dur("duration", elapsed).Msg("kubeconform check passed")
	} else {
		detail := string(output)
		if detail == "" {
			detail = err.Error()
		}
		*checks = append(*checks, HeuristicCheck{
			Name:   "kubeconform",
			Passed: false,
			Detail: fmt.Sprintf("validation failed: %s", strings.TrimSpace(detail)),
		})
		logger.Warn().Err(err).Dur("duration", elapsed).Msg("kubeconform check failed")
	}
}

// checkOPAPipeline checks that required annotations/labels are present on each manifest.
// Reads policies/pipeline/ rego files if they exist; for now performs a simple file content check
// ensuring "app" and "managed-by" labels are set on each YAML's metadata.
func checkOPAPipeline(ctx context.Context, logger zerolog.Logger, manifestDir string, checks *[]HeuristicCheck) {
	start := time.Now()
	logger.Debug().Msg("running OPA pipeline check")

	manifests, err := collectYAMLFiles(manifestDir)
	if err != nil || len(manifests) == 0 {
		*checks = append(*checks, HeuristicCheck{
			Name:   "opa_pipeline",
			Passed: false,
			Detail: fmt.Sprintf("no manifest files found in %s", manifestDir),
		})
		return
	}

	// Required labels for pipeline governance
	requiredLabels := []struct {
		path  string
		value string
	}{
		{path: "app", value: ""},                  // present, any value
		{path: "managed-by", value: "deploy-api"}, // present with expected value
	}

	var failures []string
	for _, mf := range manifests {
		data, err := os.ReadFile(mf)
		if err != nil {
			failures = append(failures, fmt.Sprintf("cannot read %s: %v", filepath.Base(mf), err))
			continue
		}

		// Handle multi-document YAML (separated by ---)
		docs := splitYAMLDocuments(data)
		for docIdx, docData := range docs {
			var doc map[string]interface{}
			if err := yaml.Unmarshal(docData, &doc); err != nil {
				failures = append(failures, fmt.Sprintf("%s doc %d: cannot parse: %v", filepath.Base(mf), docIdx, err))
				continue
			}
			if len(doc) == 0 {
				continue // skip empty documents
			}

			metadata, ok := doc["metadata"].(map[string]interface{})
			if !ok {
				failures = append(failures, fmt.Sprintf("%s doc %d: missing metadata section", filepath.Base(mf), docIdx))
				continue
			}

			labels, ok := metadata["labels"].(map[string]interface{})
			if !ok {
				failures = append(failures, fmt.Sprintf("%s doc %d: missing metadata.labels", filepath.Base(mf), docIdx))
				continue
			}

			for _, rl := range requiredLabels {
				val, hasLabel := labels[rl.path]
				if !hasLabel {
					failures = append(failures, fmt.Sprintf("%s doc %d: missing label %q", filepath.Base(mf), docIdx, rl.path))
					continue
				}
				if rl.value != "" {
					valStr, ok := val.(string)
					if !ok || valStr != rl.value {
						failures = append(failures, fmt.Sprintf("%s doc %d: label %q expected %q, got %q", filepath.Base(mf), docIdx, rl.path, rl.value, valStr))
					}
				}
			}
		}
	}

	elapsed := time.Since(start)
	if len(failures) == 0 {
		*checks = append(*checks, HeuristicCheck{
			Name:   "opa_pipeline",
			Passed: true,
			Detail: fmt.Sprintf("all manifests have required labels (%s)", elapsed),
		})
		logger.Info().Dur("duration", elapsed).Msg("OPA pipeline check passed")
	} else {
		detail := fmt.Sprintf("%d label issues found: %s (%s)", len(failures), strings.Join(failures, "; "), elapsed)
		*checks = append(*checks, HeuristicCheck{
			Name:   "opa_pipeline",
			Passed: false,
			Detail: detail,
		})
		logger.Warn().Int("issues", len(failures)).Dur("duration", elapsed).Msg("OPA pipeline check failed")
	}
}

// --- Trivy scan types ---

type trivyOutput struct {
	Results []trivyResult `json:"Results"`
}

type trivyResult struct {
	Vulnerabilities []trivyVuln `json:"Vulnerabilities"`
}

type trivyVuln struct {
	VulnerabilityID string `json:"VulnerabilityID"`
	Severity        string `json:"Severity"`
}

// checkTrivyScan runs a Trivy image scan on the given image tag and counts CRITICAL/HIGH vulnerabilities.
func checkTrivyScan(ctx context.Context, logger zerolog.Logger, imageTag string, checks *[]HeuristicCheck) {
	start := time.Now()
	logger.Debug().Str("image", imageTag).Msg("running Trivy scan")

	if imageTag == "" {
		*checks = append(*checks, HeuristicCheck{
			Name:   "trivy_scan",
			Passed: false,
			Detail: "no image tag provided, skipping",
		})
		return
	}

	if _, err := exec.LookPath("trivy"); err != nil {
		logger.Warn().Err(err).Msg("trivy not found, skipping check")
		*checks = append(*checks, HeuristicCheck{
			Name:   "trivy_scan",
			Passed: false,
			Detail: "trivy not installed (optional dependency)",
		})
		return
	}

	cmd := exec.CommandContext(ctx, "trivy", "image",
		"--severity=CRITICAL,HIGH",
		"--no-progress",
		"--format=json",
		imageTag,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		*checks = append(*checks, HeuristicCheck{
			Name:   "trivy_scan",
			Passed: false,
			Detail: fmt.Sprintf("scan failed: %s", detail),
		})
		logger.Warn().Err(err).Dur("duration", time.Since(start)).Msg("trivy scan failed")
		return
	}

	var output trivyOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		*checks = append(*checks, HeuristicCheck{
			Name:   "trivy_scan",
			Passed: false,
			Detail: fmt.Sprintf("failed to parse Trivy output: %v", err),
		})
		return
	}

	totalVulns := 0
	for _, result := range output.Results {
		totalVulns += len(result.Vulnerabilities)
	}

	elapsed := time.Since(start)
	if totalVulns == 0 {
		*checks = append(*checks, HeuristicCheck{
			Name:   "trivy_scan",
			Passed: true,
			Detail: fmt.Sprintf("no critical or high vulnerabilities found (%s)", elapsed),
		})
		logger.Info().Dur("duration", elapsed).Msg("trivy scan passed")
	} else {
		*checks = append(*checks, HeuristicCheck{
			Name:   "trivy_scan",
			Passed: false,
			Detail: fmt.Sprintf("found %d critical/high vulnerabilities (%s)", totalVulns, elapsed),
		})
		logger.Warn().Int("vulnerabilities", totalVulns).Dur("duration", elapsed).Msg("trivy scan found vulnerabilities")
	}
}

// --- Resource bounds types ---

// resourceDoc is a partial K8s resource document for resource-bound validation.
type resourceDoc struct {
	Kind     string            `json:"kind"`
	Metadata resourceMetadata  `json:"metadata"`
	Spec     resourcePodSpec   `json:"spec"`
}

type resourceMetadata struct {
	Name string `json:"name"`
}

type resourceSpec struct {
	Template resourcePodTemplate `json:"template"`
}

type resourcePodTemplate struct {
	Spec resourcePodSpec `json:"spec"`
}

type resourcePodSpec struct {
	Template resourceTemplate `json:"template"`
}

type resourceTemplate struct {
	Spec resourceTemplatePodSpec `json:"spec"`
}

type resourceTemplatePodSpec struct {
	Containers []resourceContainer `json:"containers"`
}

type resourceContainer struct {
	Name      string               `json:"name"`
	Resources resourceRequirements `json:"resources"`
}

type resourceRequirements struct {
	Requests resourceValues `json:"requests"`
	Limits   resourceValues `json:"limits"`
}

type resourceValues struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// checkResourceBounds reads manifest YAML files and verifies that
// CPU request ≤ CPU limit and memory request ≤ memory limit.
func checkResourceBounds(ctx context.Context, logger zerolog.Logger, manifestDir string, checks *[]HeuristicCheck) {
	start := time.Now()
	logger.Debug().Msg("running resource bounds check")

	manifests, err := collectYAMLFiles(manifestDir)
	if err != nil || len(manifests) == 0 {
		*checks = append(*checks, HeuristicCheck{
			Name:   "resource_bounds",
			Passed: false,
			Detail: fmt.Sprintf("no manifest files found in %s", manifestDir),
		})
		return
	}

	var failures []string
	for _, mf := range manifests {
		data, err := os.ReadFile(mf)
		if err != nil {
			failures = append(failures, fmt.Sprintf("cannot read %s: %v", filepath.Base(mf), err))
			continue
		}

		// Handle multi-document YAML
		docs := splitYAMLDocuments(data)
		for _, docData := range docs {
			var doc resourceDoc
			if err := yaml.Unmarshal(docData, &doc); err != nil {
				continue // skip docs that don't match deployment structure
			}
			if doc.Kind != "" && doc.Kind != "Deployment" && doc.Kind != "StatefulSet" && doc.Kind != "DaemonSet" {
				continue // only check workload types with spec.template.spec.containers
			}
			if doc.Metadata.Name == "" && doc.Kind == "" {
				continue // not a recognized resource doc
			}

			for _, container := range doc.Spec.Template.Spec.Containers {
			if container.Resources.Requests.CPU == "" && container.Resources.Requests.Memory == "" &&
				container.Resources.Limits.CPU == "" && container.Resources.Limits.Memory == "" {
				continue
			}

			// Check CPU: request must not exceed limit
			if container.Resources.Requests.CPU != "" && container.Resources.Limits.CPU != "" {
				reqCPU, err1 := parseCPU(container.Resources.Requests.CPU)
				limCPU, err2 := parseCPU(container.Resources.Limits.CPU)
				if err1 == nil && err2 == nil && reqCPU > limCPU {
					failures = append(failures, fmt.Sprintf("%s/%s: cpu request (%s) exceeds limit (%s)",
						doc.Metadata.Name, container.Name,
						container.Resources.Requests.CPU, container.Resources.Limits.CPU))
				}
			}

			// Check Memory: request must not exceed limit
			if container.Resources.Requests.Memory != "" && container.Resources.Limits.Memory != "" {
				reqMem, err1 := parseMemory(container.Resources.Requests.Memory)
				limMem, err2 := parseMemory(container.Resources.Limits.Memory)
				if err1 == nil && err2 == nil && reqMem > limMem {
					failures = append(failures, fmt.Sprintf("%s/%s: memory request (%s) exceeds limit (%s)",
						doc.Metadata.Name, container.Name,
						container.Resources.Requests.Memory, container.Resources.Limits.Memory))
				}
			} // close memory if
		} // close container range
		} // close docs range
	} // close manifests range

	elapsed := time.Since(start)
	if len(failures) == 0 {
		*checks = append(*checks, HeuristicCheck{
			Name:   "resource_bounds",
			Passed: true,
			Detail: fmt.Sprintf("all resource bounds are valid (%s)", elapsed),
		})
		logger.Info().Dur("duration", elapsed).Msg("resource bounds check passed")
	} else {
		detail := fmt.Sprintf("%d resource bound violations: %s (%s)", len(failures), strings.Join(failures, "; "), elapsed)
		*checks = append(*checks, HeuristicCheck{
			Name:   "resource_bounds",
			Passed: false,
			Detail: detail,
		})
		logger.Warn().Int("violations", len(failures)).Dur("duration", elapsed).Msg("resource bounds check failed")
	}
}

// parseCPU converts a Kubernetes CPU value to millicores for numeric comparison.
//
// Supported formats:
//   - "500m" → 500
//   - "1"    → 1000
//   - "0.5"  → 500
func parseCPU(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty CPU value")
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid CPU millicore value: %s", s)
		}
		return n, nil
	}
	// Whole or fractional cores (e.g., "1" or "0.5")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CPU value: %s", s)
	}
	return int64(math.Round(v * 1000)), nil
}

// parseMemory converts a Kubernetes memory value to bytes for numeric comparison.
//
// Supported suffixes: Ei, Pi, Ti, Gi, Mi, Ki (binary), E, P, T, G, M, K (SI).
// Plain numbers are treated as bytes.
func parseMemory(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty memory value")
	}

	type suffixEntry struct {
		suffix     string
		multiplier int64
	}

	// Try longest suffixes first (Ei before E, etc.)
	suffixes := []suffixEntry{
		{"Ei", 1 << 60},
		{"Pi", 1 << 50},
		{"Ti", 1 << 40},
		{"Gi", 1 << 30},
		{"Mi", 1 << 20},
		{"Ki", 1 << 10},
		{"E", 1_000_000_000_000_000_000},
		{"P", 1_000_000_000_000_000},
		{"T", 1_000_000_000_000},
		{"G", 1_000_000_000},
		{"M", 1_000_000},
		{"K", 1_000},
		{"k", 1_000},
	}

	for _, se := range suffixes {
		if strings.HasSuffix(s, se.suffix) {
			numStr := strings.TrimSuffix(s, se.suffix)
			v, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid memory value: %s", s)
			}
			return int64(math.Round(v * float64(se.multiplier))), nil
		}
	}

	// Plain number — treat as bytes
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Attempt float parse for edge cases
		vf, err2 := strconv.ParseFloat(s, 64)
		if err2 != nil {
			return 0, fmt.Errorf("invalid memory value: %s", s)
		}
		return int64(vf), nil
	}
	return v, nil
}

// collectYAMLFiles returns all .yaml and .yml files in the given directory.
func collectYAMLFiles(dir string) ([]string, error) {
	var files []string
	for _, pattern := range []string{"*.yaml", "*.yml"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	return files, nil
}

// splitYAMLDocuments splits a multi-document YAML by the "---" separator.
// Returns each document as a separate byte slice.
func splitYAMLDocuments(data []byte) [][]byte {
	var docs [][]byte
	for _, doc := range bytes.Split(data, []byte("\n---\n")) {
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) == 0 {
			continue
		}
		docs = append(docs, trimmed)
	}
	if len(docs) == 0 {
		docs = append(docs, bytes.TrimSpace(data))
	}
	return docs
}
