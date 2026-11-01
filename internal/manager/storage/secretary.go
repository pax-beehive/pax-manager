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
	for _, id := range work.RemovedMessageIDs {
		refs = append(refs, "removed:"+id)
	}
	if work.ResetRequired {
		refs = append(refs, "reset:"+work.ResetReason)
	}
	metadata, _ := json.Marshal(work.SourceMetadata)
	refs = append(refs, "metadata:"+domain.SearchContentHash(string(metadata)))
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
		work.TargetSummaryID = secretarySummaryID(work.OwnerUserID, work.SourceID)
	}
}

func validateSecretaryPublication(
	work domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) error {
	_, err := secretaryComposePublication(work, request)
	return err
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
