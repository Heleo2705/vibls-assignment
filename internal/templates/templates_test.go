package templates_test

import (
	"fmt"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"

	"deploy-api/internal/templates"
	"deploy-api/internal/worker"
)

// TestManifestTemplates_AllRenderAndParse validates every embedded manifest template.
//
// It reads all .go.tmpl files from the embedded FS, renders each with a real
// ManifestData, and verifies:
//  1. No template parse error
//  2. No execution error
//  3. Output is valid YAML
//  4. Name and namespace fields appear correctly
//  5. Expected labels (app, managed-by) are present
func TestManifestTemplates_AllRenderAndParse(t *testing.T) {
	entries, err := templates.Manifests.ReadDir("manifests")
	if err != nil {
		t.Fatalf("failed to read manifests dir: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no manifest templates found")
	}

	// Build a canonical ManifestData the same way NewManifestStage does.
	data := worker.DefaultManifestData()
	data.Name = "app-test-job-123"
	data.Namespace = "test-namespace"
	// Override image so we can verify it renders in deployment.
	data.Image = "nginx:1.25"

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go.tmpl") {
			continue
		}

		t.Run(name, func(t *testing.T) {
			tmplPath := "manifests/" + name
			raw, err := templates.Manifests.ReadFile(tmplPath)
			if err != nil {
				t.Fatalf("read template %s: %v", name, err)
			}

			// 1. Template MUST parse
			tmpl, err := template.New(name).Parse(string(raw))
			if err != nil {
				t.Fatalf("parse template %s: %v", name, err)
			}

			// 2. Template MUST execute without error
			var buf strings.Builder
			if err := tmpl.Execute(&buf, data); err != nil {
				t.Fatalf("execute template %s: %v", name, err)
			}

			output := buf.String()
			if strings.TrimSpace(output) == "" {
				t.Fatal("rendered template produced empty output")
			}

			// 3. Output MUST be valid YAML
			var doc any
			if err := yaml.Unmarshal([]byte(output), &doc); err != nil {
				t.Fatalf("invalid YAML from template %s:\n%s\nparse error: %v", name, output, err)
			}

			// 4. Every template renders a top-level YAML mapping with apiVersion/kind
			root, ok := doc.(map[string]any)
			if !ok {
				t.Fatalf("template %s did not produce a YAML mapping", name)
			}

			// Verify apiVersion and kind are present
			if _, ok := root["apiVersion"]; !ok {
				t.Errorf("template %s: missing apiVersion", name)
			}
			if _, ok := root["kind"]; !ok {
				t.Errorf("template %s: missing kind", name)
			}

			// 5. Verify metadata
			metaRaw, ok := root["metadata"]
			if !ok {
				t.Fatalf("template %s: missing metadata", name)
			}
			meta, ok := metaRaw.(map[string]any)
			if !ok {
				t.Fatalf("template %s: metadata is not a mapping", name)
			}

			// The namespace.go.tmpl uses .Namespace as metadata.name and has no
			// metadata.namespace (Namespace resources are cluster-scoped).
			// All other templates use .Name (possibly with a suffix) and have .Namespace.
			if name == "namespace.go.tmpl" {
				if v, ok := meta["name"]; !ok || v != data.Namespace {
					t.Errorf("template %s: metadata.name = %v, want %q", name, v, data.Namespace)
				}
				if _, exists := meta["namespace"]; exists {
					t.Errorf("template %s: should not have metadata.namespace (Namespace is cluster-scoped)", name)
				}
			} else {
				assertMetaField(t, name, meta, "name", data.Name)
				assertMetaField(t, name, meta, "namespace", data.Namespace)
			}

			// 6. Check labels block
			labelsRaw, ok := meta["labels"]
			if !ok {
				t.Fatalf("template %s: missing labels in metadata", name)
			}
			labels, ok := labelsRaw.(map[string]any)
			if !ok {
				t.Fatalf("template %s: labels is not a mapping", name)
			}

			// Verify mandatory labels
			appLabel, ok := labels["app"]
			if !ok {
				t.Errorf("template %s: missing label 'app'", name)
			} else {
				expectedApp := data.Name
				if app, ok := appLabel.(string); !ok || app != expectedApp {
					t.Errorf("template %s: label 'app' = %v, want %q", name, appLabel, expectedApp)
				}
			}

			managedBy, ok := labels["managed-by"]
			if !ok {
				t.Errorf("template %s: missing label 'managed-by'", name)
			} else {
				if mb, ok := managedBy.(string); !ok || mb != "deploy-api" {
					t.Errorf("template %s: label 'managed-by' = %v, want 'deploy-api'", name, managedBy)
				}
			}
		})
	}
}

// assertMetaField checks that a metadata field has the expected value (or starts with it
// for templates that append a suffix to the name).
func assertMetaField(t *testing.T, tmplName string, meta map[string]any, key, prefix string) {
	t.Helper()

	valRaw, ok := meta[key]
	if !ok {
		t.Errorf("template %s: missing metadata.%s", tmplName, key)
		return
	}

	val, ok := valRaw.(string)
	if !ok {
		t.Errorf("template %s: metadata.%s is not a string (got %T)", tmplName, key, valRaw)
		return
	}

	if !strings.HasPrefix(val, prefix) {
		t.Errorf("template %s: metadata.%s = %q, want prefix %q", tmplName, key, val, prefix)
	}
}

// TestManifestTemplates_AllCount verifies there are exactly 12 template files.
func TestManifestTemplates_AllCount(t *testing.T) {
	entries, err := templates.Manifests.ReadDir("manifests")
	if err != nil {
		t.Fatalf("failed to read manifests dir: %v", err)
	}

	var tmplFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go.tmpl") {
			tmplFiles = append(tmplFiles, e.Name())
		}
	}

	if len(tmplFiles) != 12 {
		t.Fatalf("expected 12 manifest templates, got %d: %v", len(tmplFiles), tmplFiles)
	}

	// Verify we have the expected set
	expected := map[string]bool{
		"configmap.go.tmpl":      false,
		"deployment.go.tmpl":     false,
		"hpa.go.tmpl":            false,
		"ingress.go.tmpl":        false,
		"namespace.go.tmpl":      false,
		"networkpolicy.go.tmpl":  false,
		"pdb.go.tmpl":            false,
		"role.go.tmpl":           false,
		"rolebinding.go.tmpl":    false,
		"secret.go.tmpl":         false,
		"service.go.tmpl":        false,
		"serviceaccount.go.tmpl": false,
	}
	for _, f := range tmplFiles {
		expected[f] = true
	}
	var missing []string
	for name, found := range expected {
		if !found {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing expected templates: %v", missing)
	}
}

// TestManifestTemplates_EdgeCases tests templates with unusual but valid inputs.
func TestManifestTemplates_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		manifest  string
		namespace string
	}{
		{"short name", "a", "ns"},
		{"hyphenated", "app-my-service-v2", "prod-us-east"},
		{"numeric", "app-12345", "team-42"},
		{"max length name", fmt.Sprintf("app-%s", strings.Repeat("x", 62)), "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := templates.Manifests.ReadDir("manifests")
			if err != nil {
				t.Fatalf("read manifests dir: %v", err)
			}

			data := worker.DefaultManifestData()
			data.Name = tt.manifest
			data.Namespace = tt.namespace

			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go.tmpl") {
					continue
				}
				tmplPath := "manifests/" + entry.Name()
				raw, err := templates.Manifests.ReadFile(tmplPath)
				if err != nil {
					t.Fatalf("read template %s: %v", entry.Name(), err)
				}

				tmpl, err := template.New(entry.Name()).Parse(string(raw))
				if err != nil {
					t.Fatalf("parse template %s: %v", entry.Name(), err)
				}

				var buf strings.Builder
				if err := tmpl.Execute(&buf, data); err != nil {
					t.Fatalf("execute template %s with name %q: %v", entry.Name(), tt.manifest, err)
				}

				output := buf.String()
				if strings.TrimSpace(output) == "" {
					t.Fatalf("template %s produced empty output for name %q", entry.Name(), tt.manifest)
				}

				// Must be valid YAML
				var doc any
				if err := yaml.Unmarshal([]byte(output), &doc); err != nil {
					t.Fatalf("invalid YAML from template %s with name %q:\n%s\nerror: %v",
						entry.Name(), tt.manifest, output, err)
				}
			}
		})
	}
}

// TestManifestTemplates_ExactValues checks concrete expected output for a subset of templates.
func TestManifestTemplates_ExactValues(t *testing.T) {
	data := worker.DefaultManifestData()
	data.Name = "app-test-job-123"
	data.Namespace = "test-namespace"
	data.Image = "nginx:1.25"

	t.Run("service", func(t *testing.T) {
		raw, err := templates.Manifests.ReadFile("manifests/service.go.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		tmpl := template.Must(template.New("service.go.tmpl").Parse(string(raw)))
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatal(err)
		}
		output := buf.String()

		// Unmarshal and verify key values
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(output), &doc); err != nil {
			t.Fatalf("invalid YAML: %v", err)
		}
		meta := doc["metadata"].(map[string]any)
		if meta["name"] != "app-test-job-123-svc" {
			t.Errorf("service name = %q, want %q", meta["name"], "app-test-job-123-svc")
		}
		spec := doc["spec"].(map[string]any)
		if spec["type"] != "ClusterIP" {
			t.Errorf("service type = %q, want %q", spec["type"], "ClusterIP")
		}
		ports := spec["ports"].([]any)
		port0 := ports[0].(map[string]any)
		if port0["port"] != 80 {
			t.Errorf("service port = %v, want 80", port0["port"])
		}
		if port0["targetPort"] != 8080 {
			t.Errorf("service targetPort = %v, want 8080", port0["targetPort"])
		}
	})

	t.Run("deployment", func(t *testing.T) {
		raw, err := templates.Manifests.ReadFile("manifests/deployment.go.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		tmpl := template.Must(template.New("deployment.go.tmpl").Parse(string(raw)))
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatal(err)
		}
		output := buf.String()

		var doc map[string]any
		if err := yaml.Unmarshal([]byte(output), &doc); err != nil {
			t.Fatalf("invalid YAML: %v", err)
		}
		spec := doc["spec"].(map[string]any)
		if spec["replicas"] != 1 {
			t.Errorf("replicas = %v, want 1", spec["replicas"])
		}
		templateSpec := spec["template"].(map[string]any)["spec"].(map[string]any)
		containers := templateSpec["containers"].([]any)
		c0 := containers[0].(map[string]any)
		if c0["image"] != "nginx:1.25" {
			t.Errorf("container image = %q, want %q", c0["image"], "nginx:1.25")
		}
		if c0["name"] != "app-test-job-123" {
			t.Errorf("container name = %q, want %q", c0["name"], "app-test-job-123")
		}
	})

	t.Run("namespace", func(t *testing.T) {
		raw, err := templates.Manifests.ReadFile("manifests/namespace.go.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		tmpl := template.Must(template.New("namespace.go.tmpl").Parse(string(raw)))
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatal(err)
		}
		output := buf.String()

		var doc map[string]any
		if err := yaml.Unmarshal([]byte(output), &doc); err != nil {
			t.Fatalf("invalid YAML: %v", err)
		}
		// Namespace template uses .Namespace as the resource name, not .Name
		meta := doc["metadata"].(map[string]any)
		if meta["name"] != "test-namespace" {
			t.Errorf("namespace metadata.name = %q, want %q", meta["name"], "test-namespace")
		}
		if doc["kind"] != "Namespace" {
			t.Errorf("kind = %q, want %q", doc["kind"], "Namespace")
		}
	})
}
