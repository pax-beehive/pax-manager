package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *Service) GetTeamMemexIndex(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	documents, err := s.store.ListTeamMemexDocuments(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"index": renderTeamMemexIndex(documents)}, nil
}

func (s *Service) ListTeamMemexDocuments(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	documents, err := s.store.ListTeamMemexDocuments(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"documents": teamMemexDocumentResponses(documents)}, nil
}

func (s *Service) GetTeamMemexDocument(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
	documentPath string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	documentPath = normalizeTeamMemexDocumentPath(documentPath)
	if documentPath == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "path is required"}
	}
	document, err := s.store.GetTeamMemexDocument(c, principal, teamID, documentPath)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{
		"document": teamMemexDocumentResponseFromDomain(document),
	}, nil
}

type teamMemexDocumentResponse struct {
	Path      string          `json:"path"`
	Title     string          `json:"title"`
	Summary   string          `json:"summary"`
	Tags      json.RawMessage `json:"tags,omitempty"`
	BodyMD    string          `json:"body_md"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func teamMemexDocumentResponses(
	documents []domain.TeamMemexDocument,
) []teamMemexDocumentResponse {
	out := make([]teamMemexDocumentResponse, 0, len(documents))
	for _, document := range documents {
		out = append(out, teamMemexDocumentResponseFromDomain(document))
	}
	return out
}

func teamMemexDocumentResponseFromDomain(
	document domain.TeamMemexDocument,
) teamMemexDocumentResponse {
	return teamMemexDocumentResponse{
		Path:      document.Path,
		Title:     document.Title,
		Summary:   document.Summary,
		Tags:      append(json.RawMessage(nil), document.Tags...),
		BodyMD:    document.BodyMD,
		UpdatedAt: document.UpdatedAt,
	}
}

func renderTeamMemexIndex(documents []domain.TeamMemexDocument) string {
	var builder strings.Builder
	builder.WriteString("# Team LLM Wiki\n")
	if len(documents) == 0 {
		return builder.String()
	}
	groups := make(map[string][]domain.TeamMemexDocument)
	sections := make([]string, 0)
	for _, doc := range documents {
		section := path.Dir(doc.Path)
		if section == "." || section == "/" {
			section = "root"
		}
		if _, ok := groups[section]; !ok {
			sections = append(sections, section)
		}
		groups[section] = append(groups[section], doc)
	}
	sort.Strings(sections)
	for _, section := range sections {
		builder.WriteString("\n## ")
		builder.WriteString(section)
		builder.WriteString("\n\n")
		sort.Slice(groups[section], func(i, j int) bool {
			return groups[section][i].Path < groups[section][j].Path
		})
		for _, doc := range groups[section] {
			builder.WriteString("- [")
			builder.WriteString(doc.Title)
			builder.WriteString("](")
			builder.WriteString(doc.Path)
			builder.WriteString(") - ")
			builder.WriteString(doc.Summary)
			builder.WriteString(". Updated ")
			builder.WriteString(doc.UpdatedAt.UTC().Format("2006-01-02"))
			builder.WriteString(".\n")
		}
	}
	return builder.String()
}

func normalizeTeamMemexDocumentPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return ""
	}
	return clean
}
