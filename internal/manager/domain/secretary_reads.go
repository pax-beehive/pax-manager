package domain

// SecretaryReadMessage records the exact text exposed by a trusted tool read.
type SecretaryReadMessage struct {
	Ref         MessageVersionRef
	Text        string
	View        string
	ReadReceipt string
}

// SecretaryToolReads is task-local read provenance, not source-job progress.
type SecretaryToolReads struct {
	Messages  []SecretaryReadMessage
	Summaries []SummaryRef
}
