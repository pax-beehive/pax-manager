// Package search provides a private Elasticsearch projection of PG-owned data.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const requestTimeout = 10 * time.Second
const maxResponseBytes = 32 << 20
const maxDocumentBytes = 8 << 20

var ErrInvalidQuery = errors.New("invalid search query")

// Config contains backend-only connection settings. Empty URLs disable search at the application layer.
type Config struct {
	URL        string
	Index      string
	APIKey     string
	HTTPClient *http.Client
}

type Client struct {
	baseURL string
	index   string
	apiKey  string
	http    *http.Client
}

func ValidateConfig(rawURL, index string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" {
		return errors.New(
			"SEARCH_URL must be an HTTP(S) URL without credentials, query, or fragment",
		)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,199}$`).MatchString(index) {
		return errors.New("SEARCH_INDEX must be a lowercase index name, at most 200 characters")
	}
	return nil
}

func NewClient(cfg Config) (*Client, error) {
	if err := ValidateConfig(cfg.URL, cfg.Index); err != nil {
		return nil, err
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	// Redirects must not forward the API key to an unintended endpoint.
	cloned := *httpClient
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		baseURL: strings.TrimRight(cfg.URL, "/"),
		index:   cfg.Index,
		apiKey:  cfg.APIKey,
		http:    &cloned,
	}, nil
}

type backendError struct {
	status int
	kind   string
}

func (e *backendError) Error() string {
	return fmt.Sprintf("search backend returned HTTP %d", e.status)
}

func (c *Client) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, errors.New("cannot encode search request")
		}
	}
	if len(payload) > maxDocumentBytes {
		return nil, errors.New("search request is too large")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("cannot create search request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("search backend request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("cannot read search response")
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("search response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &detail)
		return nil, &backendError{status: resp.StatusCode, kind: detail.Error.Type}
	}
	return data, nil
}

func (c *Client) EnsureIndex(ctx context.Context) error {
	properties := map[string]any{}
	for _, name := range []string{"doc_id", "kind", "source_id", "owner_user_id", "agent_id", "session_id", "conversation_id", "role", "content_hash", "canonicalization_version"} {
		properties[name] = map[string]string{"type": "keyword"}
	}
	properties["body"] = map[string]string{"type": "wildcard"}
	properties["title"] = map[string]string{"type": "wildcard"}
	properties["created_at"] = map[string]string{"type": "date"}
	properties["deleted"] = map[string]string{"type": "boolean"}
	for _, name := range []string{"source_revision", "summary_revision", "session_seq"} {
		properties[name] = map[string]string{"type": "long"}
	}
	_, err := c.request(
		ctx,
		http.MethodPut,
		"/"+c.index,
		map[string]any{
			"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
			"mappings": map[string]any{"dynamic": "strict", "properties": properties},
		},
	)
	var backendErr *backendError
	if errors.As(err, &backendErr) && backendErr.status == 400 &&
		backendErr.kind == "resource_already_exists_exception" {
		return nil
	}
	return err
}

type record struct {
	DocID                   string    `json:"doc_id"`
	Kind                    string    `json:"kind"`
	SourceID                string    `json:"source_id"`
	OwnerUserID             string    `json:"owner_user_id"`
	AgentID                 string    `json:"agent_id,omitempty"`
	SessionID               string    `json:"session_id,omitempty"`
	ConversationID          string    `json:"conversation_id,omitempty"`
	Title                   string    `json:"title,omitempty"`
	Body                    string    `json:"body,omitempty"`
	Role                    string    `json:"role,omitempty"`
	SessionSeq              int64     `json:"session_seq,omitempty"`
	ContentHash             string    `json:"content_hash,omitempty"`
	CanonicalizationVersion string    `json:"canonicalization_version,omitempty"`
	Revision                int64     `json:"source_revision"`
	SummaryRevision         int64     `json:"summary_revision,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
	Deleted                 bool      `json:"deleted"`
}

func toRecord(d domain.SearchDocument) record {
	r := record{
		DocID:                   d.DocumentID(),
		Kind:                    d.Kind,
		SourceID:                d.SourceID,
		OwnerUserID:             d.OwnerUserID,
		AgentID:                 d.AgentID,
		SessionID:               d.SessionID,
		ConversationID:          d.ConversationID,
		Title:                   d.Title,
		Body:                    d.Body,
		Role:                    d.Role,
		SessionSeq:              d.SessionSeq,
		ContentHash:             d.ContentHash,
		CanonicalizationVersion: d.CanonicalizationVersion,
		Revision:                d.Revision,
		SummaryRevision:         d.SummaryRevision,
		CreatedAt:               d.CreatedAt,
		Deleted:                 d.Deleted,
	}
	if d.Deleted {
		return record{
			DocID:    r.DocID,
			Kind:     r.Kind,
			SourceID: r.SourceID,
			Revision: r.Revision,
			Deleted:  true,
		}
	}
	return r
}
func (r record) document() domain.SearchDocument {
	return domain.SearchDocument{
		Kind:                    r.Kind,
		SourceID:                r.SourceID,
		OwnerUserID:             r.OwnerUserID,
		AgentID:                 r.AgentID,
		SessionID:               r.SessionID,
		ConversationID:          r.ConversationID,
		Title:                   r.Title,
		Body:                    r.Body,
		Role:                    r.Role,
		SessionSeq:              r.SessionSeq,
		ContentHash:             r.ContentHash,
		CanonicalizationVersion: r.CanonicalizationVersion,
		Revision:                r.Revision,
		SummaryRevision:         r.SummaryRevision,
		CreatedAt:               r.CreatedAt,
		Deleted:                 r.Deleted,
	}
}

func (c *Client) Index(ctx context.Context, doc domain.SearchDocument) error {
	if doc.Kind == "" || doc.SourceID == "" || doc.Revision <= 0 ||
		(!doc.Deleted && doc.OwnerUserID == "") {
		return errors.New("search document requires identity, owner, and positive revision")
	}
	path := "/" + c.index + "/_doc/" + url.PathEscape(
		doc.DocumentID(),
	) + "?version_type=external&version=" + strconv.FormatInt(
		doc.Revision,
		10,
	)
	_, err := c.request(ctx, http.MethodPut, path, toRecord(doc))
	var backendErr *backendError
	if errors.As(err, &backendErr) && backendErr.status == http.StatusConflict &&
		backendErr.kind == "version_conflict_engine_exception" {
		return nil
	}
	return err
}
