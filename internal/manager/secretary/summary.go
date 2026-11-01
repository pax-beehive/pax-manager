package secretary

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const maxSummaryStateBytes = 64 * 1024

type generatedEvidence struct {
	MessageID string `json:"message_id"`
	Quote     string `json:"quote"`
}

type generatedClaim struct {
	Text     string              `json:"text"`
	Kind     string              `json:"kind,omitempty"`
	Basis    string              `json:"basis,omitempty"`
	State    string              `json:"state,omitempty"`
	Evidence []generatedEvidence `json:"evidence,omitempty"`
}

type generatedSummary struct {
	Title          string                     `json:"title"`
	Body           string                     `json:"body"`
	Claims         []generatedClaim           `json:"claims,omitempty"`
	RelatedSources []domain.MessageVersionRef `json:"related_sources,omitempty"`
}

func decodeGeneratedSummary(output string) (generatedSummary, error) {
	var generated generatedSummary
	if len(output) > 1024*1024 {
		return generated, errors.New("secretary response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&generated); err != nil {
		return generated, errors.New("secretary response must match the summary JSON schema")
	}
	if decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(generated.Body) == "" ||
		len(
			generated.Title,
		) > 4096 || len(generated.Claims) > 100 || len(generated.RelatedSources) > 50 {
		return generated, errors.New("secretary response has invalid summary content")
	}
	seen := make(map[string]bool, len(generated.RelatedSources))
	for _, ref := range generated.RelatedSources {
		if !ref.Valid() || ref.ReadReceipt == "" || seen[ref.Identity()] {
			return generated, errors.New(
				"related sources require unique exact versions and read receipts",
			)
		}
		seen[ref.Identity()] = true
	}
	return generated, nil
}

// RequestedRelatedSources selects additional sources without trusting their receipts.
func RequestedRelatedSources(output string) ([]domain.MessageVersionRef, error) {
	generated, err := decodeGeneratedSummary(output)
	return generated.RelatedSources, err
}

func BuildPublication(
	job domain.SecretaryJob,
	output string,
) (domain.PublishSummaryRequest, error) {
	return BuildPublicationWithReads(job, output, domain.SecretaryToolReads{})
}

func BuildPublicationWithReads(
	job domain.SecretaryJob, output string, reads domain.SecretaryToolReads,
) (domain.PublishSummaryRequest, error) {
	if err := validateMessages(job); err != nil {
		return domain.PublishSummaryRequest{}, err
	}
	if _, err := currentPromptState(job); err != nil {
		return domain.PublishSummaryRequest{}, err
	}
	generated, err := decodeGeneratedSummary(output)
	if err != nil {
		return domain.PublishSummaryRequest{}, err
	}
	request := publicationBase(job)
	request.Title = strings.TrimSpace(generated.Title)
	request.Body = strings.TrimSpace(generated.Body)
	seen := make(map[string]bool)
	for _, message := range job.Messages {
		ref := messageReference(message)
		request.Coverage = append(request.Coverage, ref)
		seen[ref.Identity()] = true
	}
	for _, ref := range generated.RelatedSources {
		if !hasTrustedRead(ref, reads.Messages) {
			return domain.PublishSummaryRequest{}, errors.New(
				"related source was not exposed by a verified read",
			)
		}
		if !seen[ref.Identity()] {
			request.Coverage = append(request.Coverage, ref)
			seen[ref.Identity()] = true
		}
	}
	request.Claims, err = bindRollingClaims(
		generated.Claims,
		job,
		generated.RelatedSources,
		reads.Messages,
	)
	if err != nil {
		return domain.PublishSummaryRequest{}, err
	}
	state := summaryPromptState{Title: request.Title, Body: request.Body, Claims: request.Claims}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > maxSummaryStateBytes-256 {
		return domain.PublishSummaryRequest{}, errors.New(
			"secretary summary state exceeds 64 KiB; return a more concise summary",
		)
	}
	// The store adds unchanged baseline coverage and validates the final source graph.
	return request, nil
}

func publicationBase(job domain.SecretaryJob) domain.PublishSummaryRequest {
	request := domain.PublishSummaryRequest{
		SummaryID: job.TargetSummaryID, SubjectType: "fragment", SubjectID: "secretary:session:" + job.SourceID,
		ExpectedRevision: job.ExpectedRevision, RequestID: job.RunID,
		Coverage: make([]domain.MessageVersionRef, 0, len(job.Messages)),
	}
	if request.SummaryID == "" {
		request.SummaryID = domain.SecretarySummaryID(job.OwnerUserID, job.SourceID)
	}
	return request
}

// BuildEmptyPublication retires a reset source without exposing an obsolete summary.
func BuildEmptyPublication(job domain.SecretaryJob) domain.PublishSummaryRequest {
	request := publicationBase(job)
	request.Title = "Session summary"
	request.Body = "No readable source messages were available in this update."
	return request
}

func messageReference(message domain.SearchDocument) domain.MessageVersionRef {
	return domain.MessageVersionRef{
		MessageID: message.SourceID, ContentHash: message.ContentHash,
		CanonicalizationVersion: message.CanonicalizationVersion,
	}
}

func hasTrustedRead(ref domain.MessageVersionRef, reads []domain.SecretaryReadMessage) bool {
	for _, read := range reads {
		if read.Ref.Identity() == ref.Identity() && read.ReadReceipt == ref.ReadReceipt {
			return true
		}
	}
	return false
}
