package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func stagePtr(s PipelineStage) *PipelineStage { return &s }

// ---------------------------------------------------------------------------
// ErrIdempotencyKeyExists sentinel
// ---------------------------------------------------------------------------

func TestErrIdempotencyKeyExists_IsSentinel(t *testing.T) {
	t.Run("direct match", func(t *testing.T) {
		if !errors.Is(ErrIdempotencyKeyExists, ErrIdempotencyKeyExists) {
			t.Error("errors.Is(ErrIdempotencyKeyExists, ErrIdempotencyKeyExists) must be true")
		}
	})

	t.Run("not equal to ErrNotFound", func(t *testing.T) {
		if errors.Is(ErrIdempotencyKeyExists, ErrNotFound) {
			t.Error("ErrIdempotencyKeyExists must not match ErrNotFound")
		}
	})

	t.Run("wrapped match", func(t *testing.T) {
		wrapped := fmt.Errorf("wrapped: %w", ErrIdempotencyKeyExists)
		if !errors.Is(wrapped, ErrIdempotencyKeyExists) {
			t.Error("errors.Is with wrapped error must match ErrIdempotencyKeyExists")
		}
	})

	t.Run("error message", func(t *testing.T) {
		want := "idempotency key already exists"
		if got := ErrIdempotencyKeyExists.Error(); got != want {
			t.Errorf("ErrIdempotencyKeyExists.Error() = %q, want %q", got, want)
		}
	})

	t.Run("double wrapping", func(t *testing.T) {
		outer := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrIdempotencyKeyExists))
		if !errors.Is(outer, ErrIdempotencyKeyExists) {
			t.Error("errors.Is must unwrap through multiple layers")
		}
	})
}

// ---------------------------------------------------------------------------
// JobEvent JSON serialization — from_stage / to_stage optional
// ---------------------------------------------------------------------------

func TestJobEvent_JSON_Serialization(t *testing.T) {
	cloning := stagePtr(StageCloning)
	building := stagePtr(StageBuilding)

	tests := []struct {
		name  string
		event JobEvent
		check func(t *testing.T, raw json.RawMessage)
	}{
		{
			name: "both stages present",
			event: JobEvent{
				ID:        "evt-001",
				JobID:     "job-001",
				FromStage: cloning,
				ToStage:   building,
				Status:    StatusRunning,
				Message:   "transitioning",
				CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			check: func(t *testing.T, raw json.RawMessage) {
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if m["from_stage"] != "CLONING" {
					t.Errorf("from_stage = %v, want CLONING", m["from_stage"])
				}
				if m["to_stage"] != "BUILDING" {
					t.Errorf("to_stage = %v, want BUILDING", m["to_stage"])
				}
				if m["id"] != "evt-001" {
					t.Errorf("id = %v, want evt-001", m["id"])
				}
				if m["status"] != "RUNNING" {
					t.Errorf("status = %v, want RUNNING", m["status"])
				}
			},
		},
		{
			name: "nil from_stage (initial event)",
			event: JobEvent{
				ID:      "evt-002",
				JobID:   "job-001",
				ToStage: building,
				Status:  StatusQueued,
				Message: "job created",
			},
			check: func(t *testing.T, raw json.RawMessage) {
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if _, exists := m["from_stage"]; exists {
					t.Error("from_stage should be omitted when nil")
				}
				if m["to_stage"] != "BUILDING" {
					t.Errorf("to_stage = %v, want BUILDING", m["to_stage"])
				}
			},
		},
		{
			name: "nil to_stage (terminal event)",
			event: JobEvent{
				ID:        "evt-003",
				JobID:     "job-001",
				FromStage: building,
				Status:    StatusCompleted,
				Message:   "deployment complete",
			},
			check: func(t *testing.T, raw json.RawMessage) {
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if _, exists := m["to_stage"]; exists {
					t.Error("to_stage should be omitted when nil")
				}
				if m["from_stage"] != "BUILDING" {
					t.Errorf("from_stage = %v, want BUILDING", m["from_stage"])
				}
			},
		},
		{
			name: "both stages nil (placeholder event)",
			event: JobEvent{
				ID:      "evt-004",
				JobID:   "job-001",
				Status:  StatusFailed,
				Message: "unexpected error",
			},
			check: func(t *testing.T, raw json.RawMessage) {
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if _, exists := m["from_stage"]; exists {
					t.Error("from_stage should be omitted when nil")
				}
				if _, exists := m["to_stage"]; exists {
					t.Error("to_stage should be omitted when nil")
				}
			},
		},
		{
			name: "empty message omitted",
			event: JobEvent{
				ID:     "evt-005",
				JobID:  "job-001",
				Status: StatusQueued,
			},
			check: func(t *testing.T, raw json.RawMessage) {
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if _, exists := m["message"]; exists {
					t.Error("message should be omitted when empty")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.event)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			tt.check(t, data)

			// Round-trip: unmarshal back and verify key fields
			var decoded JobEvent
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal round-trip: %v", err)
			}
			if decoded.ID != tt.event.ID {
				t.Errorf("round-trip ID = %q, want %q", decoded.ID, tt.event.ID)
			}
			if decoded.Status != tt.event.Status {
				t.Errorf("round-trip Status = %q, want %q", decoded.Status, tt.event.Status)
			}
		})
	}
}
