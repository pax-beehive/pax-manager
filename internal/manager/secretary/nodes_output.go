package secretary

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type generatedNodeSummary struct {
	Action           string                     `json:"action"`
	ProposalKey      string                     `json:"proposal_key,omitempty"`
	SummaryID        string                     `json:"summary_id,omitempty"`
	ExpectedRevision *int64                     `json:"expected_revision,omitempty"`
	Title            string                     `json:"title"`
	Body             string                     `json:"body"`
	Claims           []generatedClaim           `json:"claims,omitempty"`
	SourceMessageIDs []string                   `json:"source_message_ids"`
	RelatedSources   []domain.MessageVersionRef `json:"related_sources,omitempty"`
}

type generatedNodeOutcome struct {
	MessageID   string   `json:"message_id"`
	Action      string   `json:"action"`
	SummaryKeys []string `json:"summary_keys,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type generatedNodeBatch struct {
	Summaries []generatedNodeSummary `json:"summaries"`
	Outcomes  []generatedNodeOutcome `json:"outcomes"`
}

func decodeGeneratedNodeBatch(output string) (generatedNodeBatch, error) {
	return decodeGeneratedNodeBatchForPolicy(output, "")
}

func decodeGeneratedNodeBatchForPolicy(output, policy string) (generatedNodeBatch, error) {
	var generated generatedNodeBatch
	if !domain.SecretaryOutputPolicySupported(policy) {
		return generated, generatedOutputError("schema", "secretary output policy is unsupported")
	}
	if len(output) > 1024*1024 {
		return generated, generatedOutputError("schema", "secretary response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(output))
	decoder.DisallowUnknownFields()
	generated, err := decodeNodeBatchJSON(decoder, domain.SecretarySourceOnlyOutput(policy))
	if err != nil {
		return generated, generatedOutputError(
			"schema",
			"secretary response must match the summary batch JSON schema",
		)
	}
	if decoder.Decode(new(any)) != io.EOF || generated.Summaries == nil ||
		generated.Outcomes == nil {
		return generated, generatedOutputError(
			"schema",
			"secretary response requires summaries and outcomes arrays",
		)
	}
	keys := map[string]bool{}
	claims, evidence := 0, 0
	for _, summary := range generated.Summaries {
		key, err := validateGeneratedNodeSummary(summary)
		if err != nil {
			return generated, err
		}
		if keys[key] {
			return generated, generatedOutputError(
				"schema",
				"secretary summary keys must be unique within the batch",
			)
		}
		keys[key] = true
		claims += len(summary.Claims)
		for _, claim := range summary.Claims {
			evidence += len(claim.Evidence)
		}
	}
	if claims > 100 || evidence > 512 {
		return generated, generatedOutputError(
			"schema",
			"secretary batch exceeds the claim or evidence limit",
		)
	}
	return generated, nil
}

func decodeNodeBatchJSON(decoder *json.Decoder, sourceOnly bool) (generatedNodeBatch, error) {
	var generated generatedNodeBatch
	if !sourceOnly {
		err := decoder.Decode(&generated)
		return generated, err
	}
	var projected struct {
		Summaries []struct {
			generatedNodeSummary
			Claims json.RawMessage `json:"claims,omitempty"`
		} `json:"summaries"`
		Outcomes []generatedNodeOutcome `json:"outcomes"`
	}
	if err := decoder.Decode(&projected); err != nil {
		return generated, err
	}
	generated.Outcomes = projected.Outcomes
	if projected.Summaries != nil {
		generated.Summaries = make([]generatedNodeSummary, 0, len(projected.Summaries))
		for _, summary := range projected.Summaries {
			generated.Summaries = append(generated.Summaries, summary.generatedNodeSummary)
		}
	}
	return generated, nil
}

func validateGeneratedNodeSummary(summary generatedNodeSummary) (string, error) {
	key := summary.SummaryID
	switch summary.Action {
	case "create":
		key = summary.ProposalKey
		if strings.TrimSpace(key) == "" || len(key) > 128 || summary.SummaryID != "" ||
			summary.ExpectedRevision != nil {
			return "", generatedOutputError(
				"schema",
				"new summaries require only a stable proposal_key",
			)
		}
	case "revise":
		if strings.TrimSpace(key) == "" || summary.ProposalKey != "" ||
			summary.ExpectedRevision == nil ||
			*summary.ExpectedRevision <= 0 {
			return "", generatedOutputError(
				"schema",
				"summary revisions require summary_id and expected_revision",
			)
		}
	default:
		return "", generatedOutputError(
			"schema",
			"only create and revise summary actions are supported",
		)
	}
	if strings.TrimSpace(summary.Body) == "" || len(summary.Title) > 4096 ||
		len(summary.RelatedSources) > 50 {
		return "", generatedOutputError("schema", "secretary response has invalid summary content")
	}
	seen := map[string]bool{}
	for _, id := range summary.SourceMessageIDs {
		if strings.TrimSpace(id) == "" || seen[id] {
			return "", generatedOutputError("schema", "summary sources require unique message IDs")
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, ref := range summary.RelatedSources {
		if !ref.Valid() || ref.ReadReceipt == "" || seen[ref.Identity()] {
			return "", generatedOutputError(
				"schema",
				"related sources require unique exact versions and read receipts",
			)
		}
		seen[ref.Identity()] = true
	}
	return key, nil
}

// RequestedNodeSourceReads returns only explicit proof-bearing source reads.
func RequestedNodeSourceReads(
	job domain.SecretaryJob,
	output string,
) ([]domain.MessageVersionRef, error) {
	generated, err := decodeGeneratedNodeBatchForPolicy(output, job.OutputPolicyVersion)
	if err != nil {
		return nil, err
	}
	var refs []domain.MessageVersionRef
	seen := map[string]bool{}
	for _, summary := range generated.Summaries {
		encoded, encodeErr := json.Marshal(
			generatedSummary{
				Body:           summary.Body,
				Claims:         summary.Claims,
				RelatedSources: summary.RelatedSources,
			},
		)
		if encodeErr != nil {
			return nil, encodeErr
		}
		selected, readErr := RequestedSourceReads(job, string(encoded))
		if readErr != nil {
			return nil, readErr
		}
		for _, ref := range selected {
			key := ref.Identity() + "\x00" + ref.ReadReceipt
			if !seen[key] {
				refs = append(refs, ref)
				seen[key] = true
				if len(refs) > 512 {
					return nil, generatedOutputError(
						"schema",
						"secretary batch exceeds the verified source read limit",
					)
				}
			}
		}
	}
	return refs, nil
}
