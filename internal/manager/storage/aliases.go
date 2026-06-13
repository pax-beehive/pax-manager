package storage

import (
	"encoding/json"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	ErrNotFound     = domain.ErrNotFound
	ErrUnauthorized = domain.ErrUnauthorized
	ErrConflict     = domain.ErrConflict
)

type User = domain.User
type UserPrincipal = domain.UserPrincipal
type Agent = domain.Agent
type AgentSession = domain.AgentSession
type MailboxMessage = domain.MailboxMessage
type RegisterAgentRequest = domain.RegisterAgentRequest
type UserAPIKey = domain.UserAPIKey
type AgentStatusReport = domain.AgentStatusReport
type CreateMailboxRequest = domain.CreateMailboxRequest
type MessageResultRequest = domain.MessageResultRequest
type MailboxPull = domain.MailboxPull
type MailboxFilter = domain.MailboxFilter

func newSecret(prefix string) (string, error) {
	return auth.NewSecret(prefix)
}

func defaultMessageType(v string) string {
	return domain.DefaultMessageType(v)
}

func expiresAt(now time.Time, messageType string) *time.Time {
	return domain.ExpiresAt(now, messageType)
}

func mailboxPayload(req CreateMailboxRequest) (json.RawMessage, error) {
	return domain.MailboxPayload(req)
}

func normalizeEmail(email string) string {
	return domain.NormalizeEmail(email)
}

func canAccessOwner(principal UserPrincipal, ownerUserID string) bool {
	return domain.CanAccessOwner(principal, ownerUserID)
}
