package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// AIVerdict holds the result from the AI judge.
type AIVerdict struct {
	Passed     bool     `json:"passed"`
	Score      int      `json:"score"` // 0-100
	Reasoning  string   `json:"reasoning"`
	Violations []string `json:"violations,omitempty"`
}

// AIJudge evaluates manifests using the DeepSeek API.
type AIJudge struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	logger  zerolog.Logger
}

// AIJudgeConfig holds configuration for the AI judge.
type AIJudgeConfig struct {
	APIKey  string        // DeepSeek API key (defaults to DEEPSEEK_API_KEY env var)
	Model   string        // Model name (default: "deepseek-chat")
	BaseURL string        // API base URL (default: "https://api.deepseek.com/v1")
	Timeout time.Duration // Request timeout (default: 30s)
}

// NewAIJudge creates a new AIJudge with the given config.
func NewAIJudge(logger zerolog.Logger, cfg AIJudgeConfig) *AIJudge {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if cfg.Model == "" {
		cfg.Model = "deepseek-chat"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com/v1"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &AIJudge{
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		baseURL: cfg.BaseURL,
		client:  &http.Client{Timeout: cfg.Timeout},
		logger:  logger,
	}
}

// Evaluate sends manifests to the DeepSeek API and returns the verdict.
// manifests is a map of filename -> content for all generated YAML files.
func (j *AIJudge) Evaluate(ctx context.Context, manifests map[string]string) *AIVerdict {
	// Check if API key is available
	if j.apiKey == "" {
		j.logger.Warn().Msg("DEEPSEEK_API_KEY not set, skipping AI judge")
		return nil
	}

	// Build the prompt
	prompt := buildPrompt(manifests)

	// Call DeepSeek API
	result, err := j.callDeepSeek(ctx, prompt)
	if err != nil {
		j.logger.Error().Err(err).Msg("DeepSeek API call failed, falling back to heuristic-only")
		return nil
	}

	return result
}

// buildPrompt constructs the system + user prompt for manifest review.
func buildPrompt(manifests map[string]string) string {
	var sb strings.Builder
	sb.WriteString("You are a Kubernetes expert. Review the following manifests for security, reliability, and best practices.\n")
	sb.WriteString("Respond with a JSON object containing:\n")
	sb.WriteString("- \"passed\": boolean\n")
	sb.WriteString("- \"score\": integer 0-100\n")
	sb.WriteString("- \"reasoning\": string explaining key findings\n")
	sb.WriteString("- \"violations\": array of strings listing specific issues\n\n")
	sb.WriteString("Manifests:\n\n")

	for name, content := range manifests {
		sb.WriteString("--- " + name + " ---\n")
		sb.WriteString(content + "\n")
	}

	return sb.String()
}

// deepseekRequest is the API request body.
type deepseekRequest struct {
	Model    string            `json:"model"`
	Messages []deepseekMessage `json:"messages"`
}

type deepseekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// deepseekResponse is the API response body.
type deepseekResponse struct {
	Choices []struct {
		Message deepseekMessage `json:"message"`
	} `json:"choices"`
}

// callDeepSeek sends the prompt to the DeepSeek API and parses the response.
func (j *AIJudge) callDeepSeek(ctx context.Context, prompt string) (*AIVerdict, error) {
	reqBody := deepseekRequest{
		Model: j.model,
		Messages: []deepseekMessage{
			{Role: "system", Content: "You are a Kubernetes manifest reviewer. Always respond with valid JSON."},
			{Role: "user", Content: prompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", j.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+j.apiKey)

	resp, err := j.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var dsResp deepseekResponse
	if err := json.Unmarshal(respBody, &dsResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(dsResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response choices")
	}

	// Parse the AI response JSON from the message content
	var verdict AIVerdict
	content := dsResp.Choices[0].Message.Content
	if err := json.Unmarshal([]byte(content), &verdict); err != nil {
		// Response wasn't clean JSON; try to extract score with basic heuristic
		j.logger.Warn().Err(err).Msg("failed to parse AI response as JSON, using heuristic fallback")
		return nil, fmt.Errorf("parse AI verdict: %w", err)
	}

	return &verdict, nil
}
