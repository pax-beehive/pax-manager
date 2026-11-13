package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type SecretaryControlService struct {
	principal  PrincipalResolver
	guidance   domain.SecretaryGuidanceStore
	operations domain.SecretaryOperationStore
}

func NewSecretaryControlService(principal PrincipalResolver, guidance domain.SecretaryGuidanceStore,
	operations domain.SecretaryOperationStore) *SecretaryControlService {
	return &SecretaryControlService{
		principal:  principal,
		guidance:   guidance,
		operations: operations,
	}
}

func (s *SecretaryControlService) owner(
	ctx context.Context,
	meta auth.RequestMetadata,
) (string, error) {
	if s == nil || s.principal == nil {
		return "", domain.ErrUnauthorized
	}
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return "", err
	}
	if principal.User.UserID == "" {
		return "", domain.ErrUnauthorized
	}
	return principal.User.UserID, nil
}

func validGuidanceScope(scope, source string) bool {
	return domain.ValidateSecretaryGuidanceScope(scope, source) == nil
}

func validControlID(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "/\\\x00\r\n\t ")
}

func (s *SecretaryControlService) GetGuidance(ctx context.Context, meta auth.RequestMetadata,
	scope, source string) (domain.SecretaryGuidance, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	if !validGuidanceScope(scope, source) {
		return domain.SecretaryGuidance{}, searchHTTPError(
			http.StatusBadRequest,
			"Select user or session guidance",
		)
	}
	if s.guidance == nil {
		return domain.SecretaryGuidance{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary guidance is unavailable",
		)
	}
	result, err := s.guidance.GetSecretaryGuidance(ctx, owner, scope, source)
	return result, secretaryControlError(err)
}

func (s *SecretaryControlService) UpdateGuidance(ctx context.Context, meta auth.RequestMetadata,
	scope, source string, update domain.SecretaryGuidanceUpdate) (domain.SecretaryGuidance, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	instruction, instructionErr := domain.NormalizeSecretaryInstruction(update.Instruction)
	if !validGuidanceScope(scope, source) || update.ExpectedRevision < 0 || instructionErr != nil {
		return domain.SecretaryGuidance{}, searchHTTPError(
			http.StatusBadRequest,
			"Provide valid guidance within 8192 bytes and 4096 characters, and its revision",
		)
	}
	update.Instruction = instruction
	if s.guidance == nil {
		return domain.SecretaryGuidance{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary guidance is unavailable",
		)
	}
	result, err := s.guidance.UpdateSecretaryGuidance(ctx, owner, scope, source, update)
	return result, secretaryControlError(err)
}

func (s *SecretaryControlService) Prepare(ctx context.Context, meta auth.RequestMetadata,
	request domain.SecretaryOperationPrepare) (domain.SecretaryOperation, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if request.Scope == "" {
		request.Scope = "pending"
	}
	if !validGuidanceScope(domain.SecretaryGuidanceSession, request.SourceID) ||
		!validOperationKind(request.Kind) ||
		request.Scope != "pending" && request.Scope != "all" && request.Scope != "selected" ||
		request.Scope == "selected" && len(request.MessageIDs) == 0 ||
		!validControlIDs(request.MessageIDs) ||
		request.Kind == "human_edit" && !validControlID(request.SummaryID) {
		return domain.SecretaryOperation{}, searchHTTPError(
			http.StatusBadRequest,
			"Select a valid session, operation and complete message range",
		)
	}
	if s.operations == nil {
		return domain.SecretaryOperation{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary operations are unavailable",
		)
	}
	result, err := s.operations.PrepareSecretaryOperation(ctx, owner, request)
	return result, secretaryControlError(err)
}

func validOperationKind(kind string) bool {
	return kind == "guided_summary" || kind == "human_summary" || kind == "human_edit"
}

func validControlIDs(ids []string) bool {
	if len(ids) > domain.SecretaryOperationTargetLimit {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validControlID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *SecretaryControlService) GetOperation(ctx context.Context, meta auth.RequestMetadata,
	id string) (domain.SecretaryOperation, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if !validControlID(id) {
		return domain.SecretaryOperation{}, searchHTTPError(
			http.StatusBadRequest,
			"Select a valid operation",
		)
	}
	if s.operations == nil {
		return domain.SecretaryOperation{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary operations are unavailable",
		)
	}
	result, err := s.operations.GetSecretaryOperation(ctx, owner, id)
	return result, secretaryControlError(err)
}

func (s *SecretaryControlService) Submit(ctx context.Context, meta auth.RequestMetadata,
	id string, request domain.SecretaryOperationSubmit) (domain.SecretaryOperation, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	instruction, instructionErr := domain.NormalizeSecretaryInstruction(request.RunInstruction)
	if !validControlID(id) || !validControlID(request.RequestID) || len(request.RequestID) > 256 ||
		request.ExpectedRevision < 1 ||
		request.ExpectedSummaryRevision < 0 ||
		!validControlIDs(request.MessageIDs) ||
		len(request.CoverageMessageIDs) > 512 ||
		!validControlIDs(request.CoverageMessageIDs) ||
		instructionErr != nil ||
		len(request.Title) > 4096 ||
		!utf8.ValidString(request.Title) ||
		len(request.Body) > 64*1024 ||
		!utf8.ValidString(request.Body) ||
		strings.ContainsRune(request.Title, 0) ||
		strings.ContainsRune(request.Body, 0) {
		return domain.SecretaryOperation{}, searchHTTPError(
			http.StatusBadRequest,
			"Provide a valid operation, revision, message selection and bounded summary",
		)
	}
	request.RunInstruction = instruction
	if s.operations == nil {
		return domain.SecretaryOperation{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary operations are unavailable",
		)
	}
	result, err := s.operations.SubmitSecretaryOperation(ctx, owner, id, request)
	return result, secretaryControlError(err)
}

func secretaryControlError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return domain.ErrNotFound
	case errors.Is(err, domain.ErrUnauthorized):
		return domain.ErrUnauthorized
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrSecretaryFenced),
		errors.Is(err, domain.ErrSummaryVersionMismatch), errors.Is(err, domain.ErrSourceVersionUnavailable),
		errors.Is(err, domain.ErrSecretarySourceIncomplete):
		return searchHTTPError(
			http.StatusConflict,
			"The source, instructions or task changed. Refresh the selection before submitting again.",
		)
	default:
		return searchHTTPError(
			http.StatusInternalServerError,
			"Secretary control could not be completed. Refresh before trying again.",
		)
	}
}
