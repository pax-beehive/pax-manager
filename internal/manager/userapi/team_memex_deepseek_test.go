package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestDeepSeekTeamMemexExecutorRequestsJSONManifest(t *testing.T) {
	ctx := context.Background()
	var gotAuth string
	var gotReq deepSeekChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "{\"operations\":[{\"operation\":\"no_op\"}]}"
				}
			}]
		}`))
	}))
	defer srv.Close()

	executor, err := NewDeepSeekTeamMemexExecutor(DeepSeekTeamMemexConfig{
		APIKey:      "deepseek_test",
		BaseURL:     srv.URL,
		Model:       "deepseek-v4-flash",
		Timeout:     time.Second,
		MaxTokens:   1234,
		Temperature: 0.1,
		HTTPClient:  srv.Client(),
	})
	require.NoError(t, err)

	report := &domain.TeamMemexValidationReport{
		Retryable: true,
		Errors: []domain.TeamMemexValidationError{{
			Code:    "INDEX_IS_READ_ONLY",
			Path:    "index.md",
			Message: "index.md is generated and read-only",
		}},
		Constraints: domain.DefaultTeamMemexRunConstraints(),
	}
	previous := &domain.TeamMemexManifest{Operations: []domain.TeamMemexManifestOperation{{
		Operation: domain.TeamMemexOperationUpdateDoc,
		Path:      "index.md",
		Title:     "Index",
		Summary:   "Invalid",
		BodyMD:    "# Index\n",
	}}}
	manifest, err := executor.MaintainTeamMemex(ctx, TeamMemexExecutorInput{
		TeamID:           "team_1",
		IndexMD:          "# Team LLM Wiki\n",
		Constraints:      domain.DefaultTeamMemexRunConstraints(),
		Workspace:        staticTeamMemexWorkspace{index: "# Team LLM Wiki\n"},
		AttemptNumber:    2,
		PreviousManifest: previous,
		ValidationReport: report,
	})

	require.NoError(t, err)
	require.Equal(t, domain.TeamMemexOperationNoOp, manifest.Operations[0].Operation)
	require.Equal(t, "Bearer deepseek_test", gotAuth)
	require.Equal(t, "deepseek-v4-flash", gotReq.Model)
	require.Equal(t, "json_object", gotReq.ResponseFormat.Type)
	require.Equal(t, 1234, gotReq.MaxTokens)
	require.InDelta(t, 0.1, gotReq.Temperature, 0.001)
	require.Len(t, gotReq.Messages, 2)
	require.Contains(t, gotReq.Messages[0].Content, "Return only a JSON object")
	require.Contains(t, gotReq.Messages[1].Content, "Attempt number:\n2")
	require.Contains(t, gotReq.Messages[1].Content, "Previous invalid manifest JSON")
	require.Contains(t, gotReq.Messages[1].Content, "Validation report JSON to fix")
}

func TestDeepSeekTeamMemexExecutorReturnsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	executor, err := NewDeepSeekTeamMemexExecutor(DeepSeekTeamMemexConfig{
		APIKey:     "deepseek_test",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	require.NoError(t, err)

	_, err = executor.MaintainTeamMemex(context.Background(), TeamMemexExecutorInput{
		TeamID:      "team_1",
		IndexMD:     "# Team LLM Wiki\n",
		Constraints: domain.DefaultTeamMemexRunConstraints(),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "status 429")
	require.Contains(t, err.Error(), "rate limited")
}

func TestDeepSeekTeamMemexExecutorRequiresAPIKey(t *testing.T) {
	_, err := NewDeepSeekTeamMemexExecutor(DeepSeekTeamMemexConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "api key is required")
}

type staticTeamMemexWorkspace struct {
	index string
}

func (w staticTeamMemexWorkspace) ListIndex() string {
	return w.index
}

func (staticTeamMemexWorkspace) SearchDocs(string) ([]domain.TeamMemexDocument, error) {
	return nil, nil
}

func (staticTeamMemexWorkspace) ReadDoc(string) (domain.TeamMemexDocument, error) {
	return domain.TeamMemexDocument{}, domain.ErrNotFound
}
