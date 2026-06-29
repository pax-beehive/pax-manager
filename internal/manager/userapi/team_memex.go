package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (s *Service) CreateTeamMemexRun(
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
	if err := s.store.AuthorizeTeamMemexRun(c, principal, teamID); err != nil {
		return 0, nil, err
	}
	documents, err := s.store.ListTeamMemexDocuments(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	reservedPaths, err := s.store.ListTeamMemexDocumentPaths(c, principal, teamID)
	if err != nil {
		return 0, nil, err
	}
	runID, err := s.secrets.New("tmrun")
	if err != nil {
		return 0, nil, err
	}
	now := s.clock().UTC()
	constraints := domain.DefaultTeamMemexRunConstraints()
	indexMD := renderTeamMemexIndex(documents)
	executor := s.memexExecutor
	if executor == nil {
		executor = dryRunTeamMemexExecutor{}
	}
	executorType := teamMemexExecutorType(executor)
	run := domain.TeamMemexRun{
		RunID:             runID,
		TeamID:            teamID,
		RequestedByUserID: principal.User.UserID,
		ExecutorType:      executorType,
		Partial:           false,
		Constraints:       constraints,
		IndexMD:           indexMD,
		StartedAt:         now,
	}
	maxAttempts := teamMemexRunMaxAttempts(constraints)
	attempts := make([]domain.TeamMemexRunAttempt, 0, maxAttempts)
	var previousManifest *domain.TeamMemexManifest
	var previousReport *domain.TeamMemexValidationReport
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		attemptID, err := s.secrets.New("tmattempt")
		if err != nil {
			return 0, nil, err
		}
		attemptStartedAt := s.clock().UTC()
		manifest, err := executor.MaintainTeamMemex(c, TeamMemexExecutorInput{
			TeamID:           teamID,
			IndexMD:          indexMD,
			Constraints:      constraints,
			Workspace:        newTeamMemexWorkspace(documents, constraints),
			AttemptNumber:    attemptNumber,
			PreviousManifest: previousManifest,
			ValidationReport: previousReport,
		})
		attemptCompletedAt := s.clock().UTC()
		attempt := domain.TeamMemexRunAttempt{
			AttemptID:     attemptID,
			RunID:         runID,
			TeamID:        teamID,
			AttemptNumber: attemptNumber,
			ExecutorType:  executorType,
			Manifest:      cloneTeamMemexManifest(manifest),
			StartedAt:     attemptStartedAt,
			CompletedAt:   &attemptCompletedAt,
		}
		if err != nil {
			attempt.Status = domain.TeamMemexRunAttemptStatusProviderFailed
			attempt.Error = err.Error()
			attempts = append(attempts, attempt)
			run.Status = domain.TeamMemexRunStatusProviderFailed
			run.Error = err.Error()
			run.CompletedAt = &attemptCompletedAt
			run.Attempts = attempts
			created, createErr := s.store.CreateTeamMemexRun(c, principal, run)
			if createErr != nil {
				return 0, nil, createErr
			}
			return http.StatusOK, map[string]any{
				"run": teamMemexRunResponseFromDomain(created),
			}, nil
		}
		operations, report, err := s.validateTeamMemexManifest(
			manifest,
			documents,
			reservedPaths,
			constraints,
		)
		if err != nil {
			attempt.Status = domain.TeamMemexRunAttemptStatusValidationFailed
			attempt.ValidationReport = cloneTeamMemexValidationReport(report)
			attempt.Error = err.Error()
			attempts = append(attempts, attempt)
			if report != nil && report.Retryable && attemptNumber < maxAttempts {
				nextManifest := cloneTeamMemexManifest(manifest)
				previousManifest = &nextManifest
				previousReport = cloneTeamMemexValidationReport(report)
				continue
			}
			run.Status = domain.TeamMemexRunStatusValidationFailed
			run.ValidationReport = cloneTeamMemexValidationReport(report)
			run.Error = err.Error()
			run.CompletedAt = &attemptCompletedAt
			run.Attempts = attempts
			created, createErr := s.store.CreateTeamMemexRun(c, principal, run)
			if createErr != nil {
				return 0, nil, createErr
			}
			return http.StatusOK, map[string]any{
				"run": teamMemexRunResponseFromDomain(created),
			}, nil
		}
		attempt.Status = domain.TeamMemexRunAttemptStatusSucceeded
		attempts = append(attempts, attempt)
		run.Status = domain.TeamMemexRunStatusSucceeded
		run.CompletedAt = &attemptCompletedAt
		run.Attempts = attempts
		created, err := s.store.PublishTeamMemexRun(
			c,
			principal,
			run,
			operations,
			attemptCompletedAt,
		)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"run": teamMemexRunResponseFromDomain(created)}, nil
	}
	return 0, nil, errors.New("team memex run reached an unreachable terminal state")
}

func (s *Service) GetTeamMemexRun(
	c context.Context,
	meta auth.RequestMetadata,
	teamID string,
	runID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if teamID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "team_id is required"}
	}
	if strings.TrimSpace(runID) == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "run_id is required"}
	}
	run, err := s.store.GetTeamMemexRun(c, principal, teamID, runID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"run": teamMemexRunResponseFromDomain(run)}, nil
}

type teamMemexDocumentResponse struct {
	Path      string          `json:"path"`
	Title     string          `json:"title"`
	Summary   string          `json:"summary"`
	Tags      json.RawMessage `json:"tags,omitempty"`
	BodyMD    string          `json:"body_md"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type teamMemexRunResponse struct {
	RunID             string                            `json:"run_id"`
	TeamID            string                            `json:"team_id"`
	RequestedByUserID string                            `json:"requested_by_user_id"`
	ExecutorType      string                            `json:"executor_type"`
	Status            string                            `json:"status"`
	Partial           bool                              `json:"partial"`
	Constraints       domain.TeamMemexRunConstraints    `json:"constraints"`
	IndexMD           string                            `json:"index_md"`
	ValidationReport  *domain.TeamMemexValidationReport `json:"validation_report,omitempty"`
	Error             string                            `json:"error,omitempty"`
	StartedAt         time.Time                         `json:"started_at"`
	CompletedAt       *time.Time                        `json:"completed_at,omitempty"`
	Attempts          []teamMemexRunAttemptResponse     `json:"attempts,omitempty"`
}

type teamMemexRunAttemptResponse struct {
	AttemptID        string                            `json:"attempt_id"`
	RunID            string                            `json:"run_id"`
	TeamID           string                            `json:"team_id"`
	AttemptNumber    int                               `json:"attempt_number"`
	ExecutorType     string                            `json:"executor_type"`
	Status           string                            `json:"status"`
	Manifest         domain.TeamMemexManifest          `json:"manifest"`
	ValidationReport *domain.TeamMemexValidationReport `json:"validation_report,omitempty"`
	Error            string                            `json:"error,omitempty"`
	StartedAt        time.Time                         `json:"started_at"`
	CompletedAt      *time.Time                        `json:"completed_at,omitempty"`
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

func teamMemexRunResponseFromDomain(run domain.TeamMemexRun) teamMemexRunResponse {
	return teamMemexRunResponse{
		RunID:             run.RunID,
		TeamID:            run.TeamID,
		RequestedByUserID: run.RequestedByUserID,
		ExecutorType:      run.ExecutorType,
		Status:            run.Status,
		Partial:           run.Partial,
		Constraints:       cloneTeamMemexRunConstraints(run.Constraints),
		IndexMD:           run.IndexMD,
		ValidationReport:  cloneTeamMemexValidationReport(run.ValidationReport),
		Error:             run.Error,
		StartedAt:         run.StartedAt,
		CompletedAt:       run.CompletedAt,
		Attempts:          teamMemexRunAttemptResponses(run.Attempts),
	}
}

func teamMemexRunAttemptResponses(
	attempts []domain.TeamMemexRunAttempt,
) []teamMemexRunAttemptResponse {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]teamMemexRunAttemptResponse, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, teamMemexRunAttemptResponse{
			AttemptID:        attempt.AttemptID,
			RunID:            attempt.RunID,
			TeamID:           attempt.TeamID,
			AttemptNumber:    attempt.AttemptNumber,
			ExecutorType:     attempt.ExecutorType,
			Status:           attempt.Status,
			Manifest:         cloneTeamMemexManifest(attempt.Manifest),
			ValidationReport: cloneTeamMemexValidationReport(attempt.ValidationReport),
			Error:            attempt.Error,
			StartedAt:        attempt.StartedAt,
			CompletedAt:      attempt.CompletedAt,
		})
	}
	return out
}

func cloneTeamMemexRunConstraints(
	constraints domain.TeamMemexRunConstraints,
) domain.TeamMemexRunConstraints {
	constraints.AllowedOperations = append([]string(nil), constraints.AllowedOperations...)
	return constraints
}

func cloneTeamMemexValidationReport(
	report *domain.TeamMemexValidationReport,
) *domain.TeamMemexValidationReport {
	if report == nil {
		return nil
	}
	cloned := *report
	cloned.Errors = append([]domain.TeamMemexValidationError(nil), report.Errors...)
	cloned.Constraints = cloneTeamMemexRunConstraints(report.Constraints)
	return &cloned
}

func cloneTeamMemexManifest(manifest domain.TeamMemexManifest) domain.TeamMemexManifest {
	if len(manifest.Operations) == 0 {
		return domain.TeamMemexManifest{}
	}
	operations := make([]domain.TeamMemexManifestOperation, 0, len(manifest.Operations))
	for _, operation := range manifest.Operations {
		operation.Tags = append(json.RawMessage(nil), operation.Tags...)
		operations = append(operations, operation)
	}
	return domain.TeamMemexManifest{Operations: operations}
}

func teamMemexRunMaxAttempts(constraints domain.TeamMemexRunConstraints) int {
	if constraints.MaxRepairAttempts < 0 {
		return 1
	}
	return 1 + constraints.MaxRepairAttempts
}

func teamMemexExecutorType(executor TeamMemexExecutor) string {
	typed, ok := executor.(typedTeamMemexExecutor)
	if !ok {
		return domain.TeamMemexRunExecutorDryRun
	}
	value := strings.TrimSpace(typed.Type())
	if value == "" {
		return domain.TeamMemexRunExecutorDryRun
	}
	return value
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

type TeamMemexExecutor interface {
	MaintainTeamMemex(
		ctx context.Context,
		input TeamMemexExecutorInput,
	) (domain.TeamMemexManifest, error)
}

type typedTeamMemexExecutor interface {
	TeamMemexExecutor
	Type() string
}

type TeamMemexExecutorInput struct {
	TeamID           string
	IndexMD          string
	Constraints      domain.TeamMemexRunConstraints
	Workspace        TeamMemexWorkspace
	AttemptNumber    int
	PreviousManifest *domain.TeamMemexManifest
	ValidationReport *domain.TeamMemexValidationReport
}

type TeamMemexWorkspace interface {
	ListIndex() string
	SearchDocs(query string) ([]domain.TeamMemexDocument, error)
	ReadDoc(path string) (domain.TeamMemexDocument, error)
}

type dryRunTeamMemexExecutor struct{}

func (dryRunTeamMemexExecutor) MaintainTeamMemex(
	context.Context,
	TeamMemexExecutorInput,
) (domain.TeamMemexManifest, error) {
	return domain.TeamMemexManifest{
		Operations: []domain.TeamMemexManifestOperation{{
			Operation: domain.TeamMemexOperationNoOp,
		}},
	}, nil
}

func (dryRunTeamMemexExecutor) Type() string {
	return domain.TeamMemexRunExecutorDryRun
}

type teamMemexWorkspace struct {
	indexMD          string
	documents        map[string]domain.TeamMemexDocument
	orderedPaths     []string
	maxDocsRead      int
	maxCharsRead     int
	docsRead         int
	charsRead        int
	returnedReadDocs map[string]struct{}
}

func newTeamMemexWorkspace(
	documents []domain.TeamMemexDocument,
	constraints domain.TeamMemexRunConstraints,
) *teamMemexWorkspace {
	workspace := &teamMemexWorkspace{
		indexMD:          renderTeamMemexIndex(documents),
		documents:        make(map[string]domain.TeamMemexDocument, len(documents)),
		maxDocsRead:      constraints.MaxDocsReadPerRun,
		maxCharsRead:     constraints.MaxDocCharsReadPerRun,
		returnedReadDocs: make(map[string]struct{}),
	}
	for _, document := range documents {
		workspace.documents[document.Path] = document
		workspace.orderedPaths = append(workspace.orderedPaths, document.Path)
	}
	sort.Strings(workspace.orderedPaths)
	return workspace
}

func (w *teamMemexWorkspace) ListIndex() string {
	return w.indexMD
}

func (w *teamMemexWorkspace) SearchDocs(query string) ([]domain.TeamMemexDocument, error) {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(terms) == 0 {
		return nil, nil
	}
	var matches []domain.TeamMemexDocument
	for _, path := range w.orderedPaths {
		doc := w.documents[path]
		haystack := strings.ToLower(strings.Join([]string{
			doc.Path,
			doc.Title,
			doc.Summary,
			doc.BodyMD,
		}, "\n"))
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			match := teamMemexDocumentResponseSource(doc)
			match.BodyMD = ""
			matches = append(matches, match)
		}
	}
	return matches, nil
}

func (w *teamMemexWorkspace) ReadDoc(value string) (domain.TeamMemexDocument, error) {
	documentPath := normalizeTeamMemexDocumentPath(value)
	if documentPath == "" {
		return domain.TeamMemexDocument{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "path is required",
		}
	}
	doc, ok := w.documents[documentPath]
	if !ok {
		return domain.TeamMemexDocument{}, domain.ErrNotFound
	}
	if _, seen := w.returnedReadDocs[documentPath]; !seen {
		if w.maxDocsRead > 0 && w.docsRead+1 > w.maxDocsRead {
			return domain.TeamMemexDocument{}, errors.New("team memex doc read limit exceeded")
		}
		if w.maxCharsRead > 0 && w.charsRead+len(doc.BodyMD) > w.maxCharsRead {
			return domain.TeamMemexDocument{}, errors.New("team memex doc char read limit exceeded")
		}
		w.returnedReadDocs[documentPath] = struct{}{}
		w.docsRead++
		w.charsRead += len(doc.BodyMD)
	}
	return teamMemexDocumentResponseSource(doc), nil
}

func teamMemexDocumentResponseSource(doc domain.TeamMemexDocument) domain.TeamMemexDocument {
	doc.Tags = append(json.RawMessage(nil), doc.Tags...)
	return doc
}

func (s *Service) validateTeamMemexManifest(
	manifest domain.TeamMemexManifest,
	documents []domain.TeamMemexDocument,
	reservedPaths []string,
	constraints domain.TeamMemexRunConstraints,
) ([]domain.TeamMemexDocumentOperation, *domain.TeamMemexValidationReport, error) {
	validator := teamMemexManifestValidator{
		constraints:    constraints,
		activeDocs:     make(map[string]domain.TeamMemexDocument, len(documents)),
		reservedPaths:  make(map[string]struct{}, len(reservedPaths)),
		operationPaths: make(map[string]struct{}),
	}
	for _, document := range documents {
		validator.activeDocs[document.Path] = document
	}
	for _, documentPath := range reservedPaths {
		validator.reservedPaths[documentPath] = struct{}{}
	}
	return validator.validate(s.secrets, manifest)
}

type teamMemexManifestValidator struct {
	constraints    domain.TeamMemexRunConstraints
	activeDocs     map[string]domain.TeamMemexDocument
	reservedPaths  map[string]struct{}
	operationPaths map[string]struct{}
	errors         []domain.TeamMemexValidationError
	operations     []domain.TeamMemexDocumentOperation
}

func (v *teamMemexManifestValidator) validate(
	secrets SecretIssuer,
	manifest domain.TeamMemexManifest,
) ([]domain.TeamMemexDocumentOperation, *domain.TeamMemexValidationReport, error) {
	if len(manifest.Operations) > v.constraints.MaxOutputDocs {
		v.addError(
			"TOO_MANY_OPERATIONS",
			"",
			fmt.Sprintf("manifest has more than %d operations", v.constraints.MaxOutputDocs),
		)
	}
	allowed := make(map[string]struct{}, len(v.constraints.AllowedOperations))
	for _, operation := range v.constraints.AllowedOperations {
		allowed[operation] = struct{}{}
	}
	for i, operation := range manifest.Operations {
		v.validateOperation(secrets, allowed, i, operation)
	}
	if len(v.errors) > 0 {
		report := &domain.TeamMemexValidationReport{
			Retryable:   true,
			Errors:      append([]domain.TeamMemexValidationError(nil), v.errors...),
			Constraints: cloneTeamMemexRunConstraints(v.constraints),
		}
		return nil, report, errors.New("team memex manifest validation failed")
	}
	return append([]domain.TeamMemexDocumentOperation(nil), v.operations...), nil, nil
}

func (v *teamMemexManifestValidator) validateOperation(
	secrets SecretIssuer,
	allowed map[string]struct{},
	index int,
	operation domain.TeamMemexManifestOperation,
) {
	if _, ok := allowed[operation.Operation]; !ok {
		v.addError(
			"OPERATION_NOT_ALLOWED",
			operation.Path,
			fmt.Sprintf("operation %q is not allowed", operation.Operation),
		)
		return
	}
	if operation.Operation == domain.TeamMemexOperationNoOp {
		return
	}
	documentPath := normalizeTeamMemexDocumentPath(operation.Path)
	if documentPath == "" {
		v.addError("INVALID_PATH", operation.Path, "path is invalid")
		return
	}
	if documentPath == "index.md" {
		v.addError("INDEX_IS_READ_ONLY", documentPath, "index.md is generated and read-only")
		return
	}
	if _, ok := v.operationPaths[documentPath]; ok {
		v.addError(
			"DUPLICATE_PATH",
			documentPath,
			"manifest includes multiple operations for the same path",
		)
		return
	}
	v.operationPaths[documentPath] = struct{}{}
	switch operation.Operation {
	case domain.TeamMemexOperationCreateDoc:
		v.validateCreate(secrets, documentPath, operation)
	case domain.TeamMemexOperationUpdateDoc:
		v.validateUpdate(documentPath, operation)
	case domain.TeamMemexOperationArchiveDoc:
		v.validateArchive(documentPath, operation)
	default:
		v.addError(
			"OPERATION_NOT_SUPPORTED",
			documentPath,
			fmt.Sprintf("operation %q is not supported at index %d", operation.Operation, index),
		)
	}
}

func (v *teamMemexManifestValidator) validateCreate(
	secrets SecretIssuer,
	documentPath string,
	operation domain.TeamMemexManifestOperation,
) {
	if _, exists := v.reservedPaths[documentPath]; exists {
		v.addError("PATH_ALREADY_EXISTS", documentPath, "create_doc path already exists")
		return
	}
	if !v.validateWritableDocFields(documentPath, operation) {
		return
	}
	documentID, err := secrets.New("tmdoc")
	if err != nil {
		v.addError("DOCUMENT_ID_FAILED", documentPath, err.Error())
		return
	}
	v.operations = append(v.operations, domain.TeamMemexDocumentOperation{
		Operation:  domain.TeamMemexOperationCreateDoc,
		DocumentID: documentID,
		Path:       documentPath,
		Title:      strings.TrimSpace(operation.Title),
		Summary:    strings.TrimSpace(operation.Summary),
		Tags:       normalizeTeamMemexTags(operation.Tags),
		BodyMD:     operation.BodyMD,
	})
}

func (v *teamMemexManifestValidator) validateUpdate(
	documentPath string,
	operation domain.TeamMemexManifestOperation,
) {
	if _, exists := v.activeDocs[documentPath]; !exists {
		v.addError("DOC_NOT_ACTIVE", documentPath, "update_doc can update active documents only")
		return
	}
	if !v.validateWritableDocFields(documentPath, operation) {
		return
	}
	v.operations = append(v.operations, domain.TeamMemexDocumentOperation{
		Operation: domain.TeamMemexOperationUpdateDoc,
		Path:      documentPath,
		Title:     strings.TrimSpace(operation.Title),
		Summary:   strings.TrimSpace(operation.Summary),
		Tags:      normalizeTeamMemexTags(operation.Tags),
		BodyMD:    operation.BodyMD,
	})
}

func (v *teamMemexManifestValidator) validateArchive(
	documentPath string,
	operation domain.TeamMemexManifestOperation,
) {
	if _, exists := v.activeDocs[documentPath]; !exists {
		v.addError("DOC_NOT_ACTIVE", documentPath, "archive_doc can archive active documents only")
		return
	}
	v.operations = append(v.operations, domain.TeamMemexDocumentOperation{
		Operation: domain.TeamMemexOperationArchiveDoc,
		Path:      documentPath,
	})
}

func (v *teamMemexManifestValidator) validateWritableDocFields(
	documentPath string,
	operation domain.TeamMemexManifestOperation,
) bool {
	ok := true
	if strings.TrimSpace(operation.Title) == "" {
		v.addError("TITLE_REQUIRED", documentPath, "title is required")
		ok = false
	}
	if strings.TrimSpace(operation.Summary) == "" {
		v.addError("SUMMARY_REQUIRED", documentPath, "summary is required")
		ok = false
	}
	if strings.TrimSpace(operation.BodyMD) == "" {
		v.addError("BODY_REQUIRED", documentPath, "body_md is required")
		ok = false
	}
	if v.constraints.MaxOutputDocCharsEach > 0 &&
		len(operation.BodyMD) > v.constraints.MaxOutputDocCharsEach {
		v.addError("DOC_BODY_TOO_LARGE", documentPath, "body_md exceeds max_output_doc_chars_each")
		ok = false
	}
	if len(operation.Tags) > 0 && !json.Valid(operation.Tags) {
		v.addError("TAGS_INVALID", documentPath, "tags must be valid JSON")
		ok = false
	}
	return ok
}

func (v *teamMemexManifestValidator) addError(code string, path string, message string) {
	v.errors = append(v.errors, domain.TeamMemexValidationError{
		Code:    code,
		Path:    path,
		Message: message,
	})
}

func normalizeTeamMemexTags(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`[]`)
	}
	return append(json.RawMessage(nil), raw...)
}
