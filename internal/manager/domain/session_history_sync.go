package domain

import "context"

// SessionHistorySyncStore serves lightweight heads and bounded turn pages.
type SessionHistorySyncStore interface {
	LatestSessionMessage(context.Context, string, string) (Message, error)
	ListTurnSummaryPage(
		context.Context,
		string,
		string,
		string,
		int64,
		int64,
		int,
	) (MessageHistoryPage, error)
}
