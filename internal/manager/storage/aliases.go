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
type Node = domain.Node
type Agent = domain.Agent
type AgentSession = domain.AgentSession
type MailboxMessage = domain.MailboxMessage
type RegisterNodeRequest = domain.RegisterNodeRequest
type RegisterAgentRequest = domain.RegisterAgentRequest
type UserAPIKey = domain.UserAPIKey
type NodeStatusReport = domain.NodeStatusReport
type AgentStatusInput = domain.AgentStatusInput
type AgentStatusReport = domain.AgentStatusReport
type SessionStatusInput = domain.SessionStatusInput
type FileChange = domain.FileChange
type CreateMailboxRequest = domain.CreateMailboxRequest
type MessageResultRequest = domain.MessageResultRequest
type MarkDeliveredRequest = domain.MarkDeliveredRequest
type CreateOutboundMessageRequest = domain.CreateOutboundMessageRequest
type CreateAgentRequest = domain.CreateAgentRequest
type CreateSessionRequest = domain.CreateSessionRequest
type MailboxPull = domain.MailboxPull
type MailboxFilter = domain.MailboxFilter
type Message = domain.Message
type MessagePart = domain.MessagePart
type TransportFrame = domain.TransportFrame

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
