package userapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-v4-flash"
)

type DeepSeekTeamMemexConfig struct {
	APIKey      string
	BaseURL     string
	Model       string
	Timeout     time.Duration
	MaxTokens   int
	Temperature float64
	HTTPClient  *http.Client
}

type deepSeekTeamMemexExecutor struct {
	cfg    DeepSeekTeamMemexConfig
	client *http.Client
}

func NewDeepSeekTeamMemexExecutor(
	cfg DeepSeekTeamMemexConfig,
) (TeamMemexExecutor, error) {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, errors.New("deepseek api key is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultDeepSeekBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = defaultDeepSeekModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 384000
	}
	if cfg.Temperature < 0 {
		cfg.Temperature = 0.2
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &deepSeekTeamMemexExecutor{cfg: cfg, client: client}, nil
}

func (e *deepSeekTeamMemexExecutor) Type() string {
	return domain.TeamMemexRunExecutorDeepSeek
}

func (e *deepSeekTeamMemexExecutor) MaintainTeamMemex(
	ctx context.Context,
	input TeamMemexExecutorInput,
) (domain.TeamMemexManifest, error) {
	reqBody := deepSeekChatCompletionRequest{
		Model: e.cfg.Model,
		Messages: []deepSeekChatMessage{
			{Role: "system", Content: teamMemexDeepSeekSystemPrompt()},
			{Role: "user", Content: teamMemexDeepSeekUserPrompt(input)},
		},
		ResponseFormat: deepSeekResponseFormat{Type: "json_object"},
		Temperature:    e.cfg.Temperature,
		MaxTokens:      e.cfg.MaxTokens,
	}
	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	endpoint, err := deepSeekEndpoint(e.cfg.BaseURL)
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(rawReq),
	)
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return domain.TeamMemexManifest{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return domain.TeamMemexManifest{}, fmt.Errorf(
			"deepseek chat completion failed: status %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}
	var chatResp deepSeekChatCompletionResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return domain.TeamMemexManifest{}, err
	}
	if len(chatResp.Choices) == 0 {
		return domain.TeamMemexManifest{}, errors.New("deepseek response has no choices")
	}
	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	if content == "" {
		return domain.TeamMemexManifest{}, errors.New("deepseek response content is empty")
	}
	var manifest domain.TeamMemexManifest
	if err := json.Unmarshal([]byte(content), &manifest); err != nil {
		return domain.TeamMemexManifest{}, fmt.Errorf("decode deepseek manifest: %w", err)
	}
	return manifest, nil
}

type deepSeekChatCompletionRequest struct {
	Model          string                 `json:"model"`
	Messages       []deepSeekChatMessage  `json:"messages"`
	ResponseFormat deepSeekResponseFormat `json:"response_format"`
	Temperature    float64                `json:"temperature"`
	MaxTokens      int                    `json:"max_tokens"`
}

type deepSeekChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekResponseFormat struct {
	Type string `json:"type"`
}

type deepSeekChatCompletionResponse struct {
	Choices []deepSeekChatCompletionChoice `json:"choices"`
}

type deepSeekChatCompletionChoice struct {
	Message deepSeekChatMessage `json:"message"`
}

func deepSeekEndpoint(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("deepseek base url is invalid: %q", baseURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return u.String(), nil
}

func teamMemexDeepSeekSystemPrompt() string {
	return strings.Join([]string{
		"You maintain a team LLM wiki by returning a JSON manifest.",
		"Return only a JSON object with one top-level field: operations.",
		"Each operation must be one of create_doc, update_doc, archive_doc, or no_op.",
		"Use no_op when there is no clear new wiki change.",
		"Never write index.md. It is generated and read-only.",
		"Do not delete documents. Archive obsolete active documents instead.",
		"Write concise Markdown bodies. Do not include secrets, credentials, or tokens.",
		"Do not wrap the JSON in Markdown fences.",
	}, "\n")
}

func teamMemexDeepSeekUserPrompt(input TeamMemexExecutorInput) string {
	constraints := mustMarshalTeamMemexJSON(input.Constraints)
	var builder strings.Builder
	builder.WriteString("Team ID:\n")
	builder.WriteString(input.TeamID)
	builder.WriteString("\n\nAttempt number:\n")
	_, _ = fmt.Fprintf(&builder, "%d", input.AttemptNumber)
	builder.WriteString("\n\nConstraints JSON:\n")
	builder.WriteString(constraints)
	builder.WriteString("\n\nGenerated index.md:\n")
	if input.Workspace != nil {
		builder.WriteString(input.Workspace.ListIndex())
	} else {
		builder.WriteString(input.IndexMD)
	}
	if input.PreviousManifest != nil {
		builder.WriteString("\n\nPrevious invalid manifest JSON:\n")
		builder.WriteString(mustMarshalTeamMemexJSON(input.PreviousManifest))
	}
	if input.ValidationReport != nil {
		builder.WriteString("\n\nValidation report JSON to fix:\n")
		builder.WriteString(mustMarshalTeamMemexJSON(input.ValidationReport))
	}
	builder.WriteString("\n\nReturn a JSON object shaped like this:\n")
	builder.WriteString(`{"operations":[{"operation":"no_op"}]}`)
	return builder.String()
}

func mustMarshalTeamMemexJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
