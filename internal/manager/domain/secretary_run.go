package domain

import (
	"context"
	"time"
)

// An accepted receipt confirms durable queueing, not execution or publication.
type SecretaryRunReceipt struct {
	RequestID  string    `json:"request_id"`
	State      string    `json:"state"`
	AcceptedAt time.Time `json:"accepted_at"`
}

type SecretaryRunStore interface {
	RequestSecretaryRun(context.Context, string, string) (SecretaryRunReceipt, error)
}

type SecretaryRunQueueStore interface {
	AdvanceSecretaryRuns(context.Context, int) (bool, error)
}

func ValidSecretaryRunRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			ch == '_' || ch == '-' || ch == '.' || ch == ':' {
			continue
		}
		return false
	}
	return true
}
