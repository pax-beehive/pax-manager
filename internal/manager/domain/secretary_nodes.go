package domain

import (
	"context"
	"strings"
)

const SecretaryNodePolicyVersion = "summary-nodes-v1"

const SecretaryEmptySummaryBody = "No supported content remains in this summary."

type SecretarySummaryOutput struct {
	ProposalKey string                `json:"proposal_key,omitempty"`
	Request     PublishSummaryRequest `json:"request"`
}

type SecretaryMessageOutcome struct {
	MessageID   string   `json:"message_id"`
	Action      string   `json:"action"`
	SummaryKeys []string `json:"summary_keys,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type SecretaryBatchPublication struct {
	RequestID string                    `json:"request_id"`
	Outputs   []SecretarySummaryOutput  `json:"outputs"`
	Outcomes  []SecretaryMessageOutcome `json:"outcomes"`
}

type SecretarySummaryPage struct {
	Items      []SummaryVersion `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type SecretaryNodeStore interface {
	PublishSecretaryBatch(
		context.Context,
		SecretaryJob,
		SecretaryBatchPublication,
	) ([]SummaryVersion, error)
	ListSecretaryNodeSummaries(
		context.Context,
		string,
		string,
		SummaryListQuery,
	) (SecretarySummaryPage, error)
}

type SummaryRevisionStore interface {
	ListSummaryRevisions(context.Context, string, string, SummaryListQuery) (SummaryListPage, error)
}

func SecretaryNodeID(batchID, proposalKey string) string {
	return "summary_node_" + strings.TrimPrefix(SearchContentHash(batchID+"\x00"+proposalKey), "sha256:")[:32]
}
