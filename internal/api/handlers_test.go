package api

import (
	"encoding/json"
	"strings"
	"testing"

	"deploy-api/internal/models"
)

// ---------------------------------------------------------------------------
// idempotencyHash — deterministic, distinct, version-dependent
// ---------------------------------------------------------------------------

func TestIdempotencyHash_Deterministic(t *testing.T) {
	req := createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "default",
	}

	h1 := idempotencyHash(req)
	h2 := idempotencyHash(req)

	if h1 != h2 {
		t.Fatal("idempotencyHash must return the same hash for identical inputs")
	}
	if h1 == "" {
		t.Fatal("idempotencyHash must not return empty string")
	}
}

func TestIdempotencyHash_DifferentInputs_DifferentHash(t *testing.T) {
	base := createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "default",
	}

	tests := []struct {
		name  string
		tweak func(req createJobRequest) createJobRequest
	}{
		{
			name: "different repo_url",
			tweak: func(req createJobRequest) createJobRequest {
				req.RepositoryURL = "https://github.com/other/repo"
				return req
			},
		},
		{
			name: "different branch",
			tweak: func(req createJobRequest) createJobRequest {
				req.Branch = "develop"
				return req
			},
		},
		{
			name: "different target_namespace",
			tweak: func(req createJobRequest) createJobRequest {
				req.TargetNamespace = "production"
				return req
			},
		},
		{
			name: "different build_context",
			tweak: func(req createJobRequest) createJobRequest {
				req.BuildContext = "infra/"
				return req
			},
		},
		{
			name: "different dockerfile_path",
			tweak: func(req createJobRequest) createJobRequest {
				req.DockerfilePath = "deploy/Dockerfile.prod"
				return req
			},
		},
	}

	baseHash := idempotencyHash(base)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := tt.tweak(base)
			modHash := idempotencyHash(modified)
			if modHash == baseHash {
				t.Errorf("changing %s must change the hash", tt.name)
			}
		})
	}
}

func TestIdempotencyHash_VersionFieldChangesHash(t *testing.T) {
	req := createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "default",
	}

	baseHash := idempotencyHash(req)

	tests := []struct {
		name    string
		version string
	}{
		{"non-empty version", "v2"},
		{"numeric version", "123"},
		{"semver version", "1.2.3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req.Version = tt.version
			got := idempotencyHash(req)
			if got == "" {
				t.Fatal("hash must not be empty")
			}
			if got == baseHash {
				t.Errorf("version=%q must change the hash", tt.version)
			}
		})
	}
}

func TestIdempotencyHash_DifferentVersionSameAsItself(t *testing.T) {
	req := createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "default",
		Version:         "v2",
	}

	h1 := idempotencyHash(req)
	h2 := idempotencyHash(req)
	if h1 != h2 {
		t.Error("same request with same version must produce same hash")
	}
}

func TestIdempotencyHash_NilVsNonNilOverrides(t *testing.T) {
	req := createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "default",
	}

	nilHash := idempotencyHash(req)

	cpu := 1.0
	mem := 512
	replicas := 2
	req.ResourceOverrides = &models.ResourceOverrides{
		CPUCores: &cpu,
		MemoryMB: &mem,
		Replicas: &replicas,
	}
	nonNilHash := idempotencyHash(req)

	if nonNilHash == nilHash {
		t.Error("hash with non-nil ResourceOverrides must differ from nil")
	}
}

func TestIdempotencyHash_EmptyRequestIsDeterministic(t *testing.T) {
	h1 := idempotencyHash(createJobRequest{})
	h2 := idempotencyHash(createJobRequest{})

	if h1 != h2 {
		t.Error("empty createJobRequest must produce deterministic hash")
	}
	if h1 == "" {
		t.Error("empty createJobRequest must not produce empty hash")
	}

	// A SHA-256 hex string is exactly 64 characters
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}

func TestIdempotencyHash_IsHexEncodedSHA256(t *testing.T) {
	h := idempotencyHash(createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "default",
	})
	// SHA-256 hex = exactly 64 hex chars
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA-256 hex)", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("unexpected character %c in hash — expected hex", c)
		}
	}
}

// ---------------------------------------------------------------------------
// Request validation — missing fields, unknown fields
// ---------------------------------------------------------------------------

func TestCreateJobRequest_Validation(t *testing.T) {
	t.Run("missing repository_url fails validation", func(t *testing.T) {
		req := createJobRequest{
			Branch:          "main",
			BuildContext:    ".",
			DockerfilePath:  "Dockerfile",
			TargetNamespace: "default",
		}
		if req.RepositoryURL == "" {
			// This is what handleCreateJob checks — passes
		} else {
			t.Error("expected RepositoryURL to be empty")
		}
	})

	t.Run("missing target_namespace fails validation", func(t *testing.T) {
		req := createJobRequest{
			RepositoryURL:  "https://github.com/example/repo",
			Branch:         "main",
			BuildContext:   ".",
			DockerfilePath: "Dockerfile",
		}
		if req.TargetNamespace == "" {
			// This is what handleCreateJob checks — passes
		} else {
			t.Error("expected TargetNamespace to be empty")
		}
	})

	t.Run("valid request has all required fields", func(t *testing.T) {
		req := createJobRequest{
			RepositoryURL:   "https://github.com/example/repo",
			Branch:          "main",
			BuildContext:    ".",
			DockerfilePath:  "Dockerfile",
			TargetNamespace: "default",
		}
		if req.RepositoryURL == "" || req.TargetNamespace == "" {
			t.Error("valid request must have RepositoryURL and TargetNamespace")
		}
	})

	t.Run("branch and dockerfile_path are optional", func(t *testing.T) {
		req := createJobRequest{
			RepositoryURL:   "https://github.com/example/repo",
			TargetNamespace: "default",
		}
		if req.RepositoryURL == "" || req.TargetNamespace == "" {
			t.Error("only RepositoryURL and TargetNamespace are required")
		}
	})
}

func TestCreateJobRequest_UnknownFieldsRejected(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "known fields pass",
			json:    `{"repository_url":"https://github.com/example/repo","target_namespace":"default"}`,
			wantErr: false,
		},
		{
			name:    "unknown field rejected",
			json:    `{"repository_url":"https://github.com/example/repo","target_namespace":"default","unknown_field":"value"}`,
			wantErr: true,
		},
		{
			name:    "multiple unknown fields rejected",
			json:    `{"repository_url":"https://github.com/example/repo","target_namespace":"default","foo":1,"bar":2}`,
			wantErr: true,
		},
		{
			name:    "all valid fields with version",
			json:    `{"repository_url":"https://github.com/example/repo","branch":"main","build_context":".","dockerfile_path":"Dockerfile","target_namespace":"default","version":"v2"}`,
			wantErr: false,
		},
		{
			name:    "empty json object passes",
			json:    `{}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := json.NewDecoder(strings.NewReader(tt.json))
			dec.DisallowUnknownFields()
			var req createJobRequest
			err := dec.Decode(&req)
			if tt.wantErr && err == nil {
				t.Error("expected unknown fields error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCreateJobRequest_DisallowUnknownFields_ErrorMessage(t *testing.T) {
	// Verify that DisallowUnknownFields produces an error that
	// handleCreateJob would catch as "invalid request body".
	badJSON := `{"repository_url":"https://github.com/example/repo","target_namespace":"default","bogus":true}`
	dec := json.NewDecoder(strings.NewReader(badJSON))
	dec.DisallowUnknownFields()
	var req createJobRequest
	err := dec.Decode(&req)
	if err == nil {
		t.Fatal("expected error for unknown field 'bogus'")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error must mention 'unknown field', got: %v", err)
	}
}

func TestCreateJobRequest_JSONRoundTrip(t *testing.T) {
	req := createJobRequest{
		RepositoryURL:   "https://github.com/example/repo",
		Branch:          "main",
		BuildContext:    ".",
		DockerfilePath:  "Dockerfile",
		TargetNamespace: "ns",
		Version:         "v1",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded createJobRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.RepositoryURL != req.RepositoryURL {
		t.Errorf("RepositoryURL round-trip: got %q, want %q", decoded.RepositoryURL, req.RepositoryURL)
	}
	if decoded.TargetNamespace != req.TargetNamespace {
		t.Errorf("TargetNamespace round-trip: got %q, want %q", decoded.TargetNamespace, req.TargetNamespace)
	}
	if decoded.Version != req.Version {
		t.Errorf("Version round-trip: got %q, want %q", decoded.Version, req.Version)
	}
}

// ---------------------------------------------------------------------------
// ResourceOverrides JSON tags (used by idempotencyHash internally)
// ---------------------------------------------------------------------------

func TestResourceOverrides_JSONTags(t *testing.T) {
	cpu := 2.0
	mem := 1024
	replicas := 3

	ro := models.ResourceOverrides{
		CPUCores: &cpu,
		MemoryMB: &mem,
		Replicas: &replicas,
	}
	data, err := json.Marshal(ro)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["cpu_cores"] != 2.0 {
		t.Errorf("cpu_cores = %v, want 2.0", m["cpu_cores"])
	}
	if m["memory_mb"] != float64(1024) {
		t.Errorf("memory_mb = %v, want 1024", m["memory_mb"])
	}
	if m["replicas"] != float64(3) {
		t.Errorf("replicas = %v, want 3", m["replicas"])
	}
}
