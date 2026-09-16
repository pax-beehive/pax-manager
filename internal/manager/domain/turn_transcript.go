package domain

import "context"

// TurnTranscriptStore reads a single business turn, including all of its rows.
// SessionSeq orders rows; updates to a row retain their original sequence.
type TurnTranscriptStore interface {
	ListTurnTranscript(context.Context, string, string, string) ([]MessageWithParts, error)
}
