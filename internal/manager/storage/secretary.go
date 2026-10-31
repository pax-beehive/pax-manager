package storage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretarySubject(sourceID string) string { return "secretary:session:" + sourceID }

func secretaryRunID(work domain.SecretaryJob) string {
	refs := make([]string, 0, len(work.Messages))
	for _, message := range work.Messages {
		refs = append(
			refs,
			message.SourceID+":"+message.CanonicalizationVersion+":"+message.ContentHash,
		)
	}
	sort.Strings(refs)
	return "secretary:" + strings.TrimPrefix(
		domain.SearchContentHash(work.OwnerUserID+":"+work.SourceID+":"+
			strconv.FormatInt(
				work.SettingsRevision,
				10,
			)+":"+strconv.FormatInt(work.RetryGeneration, 10)+":"+
			strconv.FormatInt(work.ExpectedRevision, 10)+":"+strings.Join(refs, "|")),
		"sha256:",
	)
}

func secretaryPrepareJob(work *domain.SecretaryJob) {
	work.RunID = secretaryRunID(*work)
	if work.TargetSummaryID == "" {
		base := *work
		base.RetryGeneration = 0
		work.TargetSummaryID = secretaryRunID(base)
	}
}

func validateSecretaryPublication(
	work domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) error {
	if len(work.Messages) == 0 || request.SummaryID != work.TargetSummaryID ||
		request.RequestID != work.RunID ||
		request.ExpectedRevision != work.ExpectedRevision ||
		request.SubjectType != "fragment" ||
		request.SubjectID != secretarySubject(work.SourceID) ||
		len(request.Coverage) != len(work.Messages) {
		return ErrConflict
	}
	if err := request.Validate(); err != nil {
		return err
	}
	covered := map[string]bool{}
	for _, ref := range request.Coverage {
		covered[ref.Identity()] = true
	}
	for _, message := range work.Messages {
		ref := domain.MessageVersionRef{
			MessageID:               message.SourceID,
			ContentHash:             message.ContentHash,
			CanonicalizationVersion: message.CanonicalizationVersion,
		}
		if !covered[ref.Identity()] {
			return ErrConflict
		}
	}
	return nil
}

func secretaryRequestHash(request domain.PublishSummaryRequest) string {
	raw, _ := json.Marshal(summaryWithoutReceipts(request))
	return domain.SearchContentHash(string(raw))
}

func secretaryErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToValidUTF8(err.Error(), "?")
	if len(text) > 1000 {
		text = text[:1000]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

func secretaryWithinBounds(documents []domain.SearchDocument) error {
	size := 0
	for _, doc := range documents {
		size += len(doc.Body)
	}
	if len(documents) > domain.SecretaryMaxMessages || size > domain.SecretaryMaxBytes {
		return fmt.Errorf("secretary source exceeds bounded batch: %w", ErrConflict)
	}
	return nil
}
