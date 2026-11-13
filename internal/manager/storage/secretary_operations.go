package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type memorySecretaryOperation struct {
	value     domain.SecretaryOperation
	owner     string
	requestID string
	hash      string
	completed map[string]bool
}

func validateSecretaryOperationPrepare(input domain.SecretaryOperationPrepare) error {
	if err := domain.ValidateSecretaryGuidanceScope(domain.SecretaryGuidanceSession, input.SourceID); err != nil {
		return err
	}
	switch input.Kind {
	case "human_edit":
		if input.SummaryID == "" || len(input.MessageIDs) != 0 {
			return ErrConflict
		}
	case "guided_summary", "human_summary":
		if input.SummaryID != "" {
			return ErrConflict
		}
		switch input.Scope {
		case "pending", "all":
			if len(input.MessageIDs) != 0 {
				return ErrConflict
			}
		case "selected":
			if len(input.MessageIDs) == 0 {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	if len(input.MessageIDs) > domain.SecretaryOperationTargetLimit {
		return ErrConflict
	}
	return nil
}

func secretaryOperationHash(input domain.SecretaryOperationSubmit) string {
	raw, _ := json.Marshal(input)
	return domain.SearchContentHash(string(raw))
}

func cloneSecretaryOperation(value domain.SecretaryOperation) domain.SecretaryOperation {
	raw, _ := json.Marshal(value)
	var result domain.SecretaryOperation
	_ = json.Unmarshal(raw, &result)
	return result
}

func secretaryOperationTargets(
	operation domain.SecretaryOperation,
	input domain.SecretaryOperationSubmit,
) ([]domain.SecretaryOperationTarget, error) {
	if err := validateSecretaryOperationSubmitFields(input); err != nil {
		return nil, err
	}
	return secretarySelectOperationTargets(operation, input)
}

func validateSecretaryOperationSubmitFields(input domain.SecretaryOperationSubmit) error {
	if strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 256 ||
		!utf8.ValidString(
			input.RequestID,
		) || strings.ContainsRune(input.RequestID, 0) || input.ExpectedRevision < 1 ||
		len(input.Body) > 64*1024 || len(input.Title) > 4096 ||
		len(input.CoverageMessageIDs) > 512 || !utf8.ValidString(input.Title) ||
		!utf8.ValidString(
			input.Body,
		) || strings.ContainsRune(input.Title, 0) || strings.ContainsRune(input.Body, 0) {
		return ErrConflict
	}
	if _, err := domain.NormalizeSecretaryInstruction(input.RunInstruction); err != nil {
		return err
	}
	return nil
}

func secretarySelectOperationTargets(
	operation domain.SecretaryOperation,
	input domain.SecretaryOperationSubmit,
) ([]domain.SecretaryOperationTarget, error) {
	if operation.Kind == "human_edit" {
		if len(input.MessageIDs)+len(input.CoverageMessageIDs) != 0 {
			return nil, ErrConflict
		}
		return nil, nil
	}
	selected := map[string]bool{}
	for _, id := range input.MessageIDs {
		if id == "" || selected[id] {
			return nil, ErrConflict
		}
		selected[id] = true
	}
	if input.MessageIDs != nil && len(selected) == 0 {
		return nil, ErrConflict
	}
	var targets []domain.SecretaryOperationTarget
	for _, target := range operation.Targets {
		if input.MessageIDs == nil || selected[target.MessageID] {
			targets = append(targets, target)
			delete(selected, target.MessageID)
		}
	}
	if len(selected) > 0 || len(targets) == 0 {
		return nil, ErrConflict
	}
	return targets, nil
}

func secretaryOperationPreview(doc domain.SearchDocument) string {
	text, _ := domain.SecretaryMessageView(doc.Body, doc.Role)
	if len(text) > 1024 {
		end := 1024
		for !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end] + "..."
	}
	return text
}

func secretaryOperationSummaryRequest(
	operation domain.SecretaryOperation,
	input domain.SecretaryOperationSubmit,
) (domain.PublishSummaryRequest, error) {
	request := domain.PublishSummaryRequest{
		SummaryID:   "summary_manual_" + operation.OperationID,
		SubjectType: "fragment", SubjectID: secretarySubject(operation.SourceID),
		RequestID: "operation:" + operation.OperationID,
		Title:     input.Title, Body: input.Body,
	}
	if operation.Kind == "human_edit" {
		if operation.Summary == nil || input.SummaryID != operation.Summary.SummaryID ||
			input.ExpectedSummaryRevision != operation.Summary.Revision {
			return request, domain.ErrSummaryVersionMismatch
		}
		previous := operation.Summary
		request.SummaryID, request.ExpectedRevision = previous.SummaryID, previous.Revision
		request.SubjectType, request.SubjectID = previous.SubjectType, previous.SubjectID
		request.Coverage = append([]domain.MessageVersionRef(nil), previous.Coverage...)
		request.SummarySources = append([]domain.SummaryRef(nil), previous.SummarySources...)
	} else {
		if input.SummaryID != "" || input.ExpectedSummaryRevision != 0 {
			return request, ErrConflict
		}
		coverage := input.CoverageMessageIDs
		if coverage == nil && len(operation.Targets) > 0 {
			coverage = []string{operation.Targets[0].MessageID}
		}
		available := map[string]domain.MessageVersionRef{}
		for _, target := range operation.Targets {
			available[target.MessageID] = target.MessageRef()
		}
		seen := map[string]bool{}
		for _, id := range coverage {
			ref, ok := available[id]
			if !ok || seen[id] {
				return request, ErrConflict
			}
			seen[id] = true
			request.Coverage = append(request.Coverage, ref)
		}
	}
	if strings.TrimSpace(input.Title) == "" || !utf8.ValidString(input.Body) ||
		!utf8.ValidString(input.Title) {
		return request, ErrConflict
	}
	if err := request.Validate(); err != nil {
		return request, err
	}
	_, err := domain.EncodeSecretaryPublication(domain.SecretaryBatchPublication{
		RequestID: request.RequestID,
		Outputs:   []domain.SecretarySummaryOutput{{Request: request}},
	})
	if err != nil {
		return request, errors.Join(ErrConflict, err)
	}
	return request, err
}

func secretaryOperationVersionError(id string) error {
	return fmt.Errorf("message %s changed; refresh the selected range: %w", id,
		domain.ErrSourceVersionUnavailable)
}

func secretaryOperationMessageMatches(
	job domain.SecretaryJob,
	target domain.SecretaryOperationTarget,
	current domain.SearchDocument,
) bool {
	return current.SourceID == target.MessageID && current.OwnerUserID == job.OwnerUserID &&
		current.AgentID == job.SourceAgentID && current.SessionID == job.SourceSessionID &&
		current.Role == target.Role && current.ContentHash == target.ContentHash &&
		current.CanonicalizationVersion == target.CanonicalizationVersion
}

func summaryAuthorKind(kind string) string {
	if kind == "" {
		return "legacy_unknown"
	}
	return kind
}

func fmtSecretaryReplacementRequired() error {
	return fmt.Errorf("active Secretary work requires confirm_replace: %w", ErrConflict)
}
