package domain

import (
	"context"
	"time"
)

const SecretaryOperationTargetLimit = 4096

type SecretaryOperationPrepare struct {
	SourceID   string   `json:"source_id"`
	Kind       string   `json:"kind"`
	Scope      string   `json:"scope"`
	MessageIDs []string `json:"message_ids,omitempty"`
	SummaryID  string   `json:"summary_id,omitempty"`
}

type SecretaryOperationTarget struct {
	MessageID               string `json:"message_id"`
	ContentHash             string `json:"content_hash"`
	CanonicalizationVersion string `json:"canonicalization_version"`
	ChangeRevision          int64  `json:"change_revision"`
	Role                    string `json:"role"`
	Preview                 string `json:"preview"`
}

func (t SecretaryOperationTarget) MessageRef() MessageVersionRef {
	return MessageVersionRef{MessageID: t.MessageID, ContentHash: t.ContentHash,
		CanonicalizationVersion: t.CanonicalizationVersion}
}

type SecretaryOperation struct {
	OperationID  string                        `json:"operation_id"`
	SourceID     string                        `json:"source_id"`
	Kind         string                        `json:"kind"`
	Status       string                        `json:"status"`
	Revision     int64                         `json:"revision"`
	Targets      []SecretaryOperationTarget    `json:"targets"`
	Instructions *SecretaryInstructionSnapshot `json:"instructions"`
	BaselineRefs []SummaryRef                  `json:"baseline_refs"`
	Summary      *SummaryVersion               `json:"summary,omitempty"`
	ResultRefs   []SummaryRef                  `json:"result_refs"`
	Error        string                        `json:"error,omitempty"`
	CreatedAt    time.Time                     `json:"created_at"`
	UpdatedAt    time.Time                     `json:"updated_at"`
}

type SecretaryOperationSubmit struct {
	RequestID               string   `json:"request_id"`
	ExpectedRevision        int64    `json:"expected_revision"`
	MessageIDs              []string `json:"message_ids,omitempty"`
	CoverageMessageIDs      []string `json:"coverage_message_ids,omitempty"`
	RunInstruction          string   `json:"run_instruction,omitempty"`
	Title                   string   `json:"title,omitempty"`
	Body                    string   `json:"body,omitempty"`
	SummaryID               string   `json:"summary_id,omitempty"`
	ExpectedSummaryRevision int64    `json:"expected_summary_revision,omitempty"`
	ConfirmReplace          bool     `json:"confirm_replace"`
}

type SecretaryOperationStore interface {
	PrepareSecretaryOperation(
		context.Context,
		string,
		SecretaryOperationPrepare,
	) (SecretaryOperation, error)
	GetSecretaryOperation(context.Context, string, string) (SecretaryOperation, error)
	SubmitSecretaryOperation(
		context.Context,
		string,
		string,
		SecretaryOperationSubmit,
	) (SecretaryOperation, error)
}
