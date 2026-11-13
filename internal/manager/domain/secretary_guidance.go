package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SecretaryGuidanceUser        = "user"
	SecretaryGuidanceSession     = "session"
	SecretaryInstructionMaxBytes = 8192
	SecretaryInstructionMaxRunes = 4096
)

type SecretaryGuidance struct {
	OwnerUserID string    `json:"owner_user_id"`
	Scope       string    `json:"scope"`
	SourceID    string    `json:"source_id,omitempty"`
	Revision    int64     `json:"revision"`
	Instruction string    `json:"instruction"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SecretaryGuidanceUpdate struct {
	Instruction      string `json:"instruction"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// A snapshot belongs to the complete logical operation, including retries.
type SecretaryInstructionSnapshot struct {
	User    SecretaryGuidance `json:"user"`
	Session SecretaryGuidance `json:"session"`
	Run     string            `json:"run,omitempty"`
}

type SecretaryGuidanceStore interface {
	GetSecretaryGuidance(context.Context, string, string, string) (SecretaryGuidance, error)
	UpdateSecretaryGuidance(
		context.Context,
		string,
		string,
		string,
		SecretaryGuidanceUpdate,
	) (SecretaryGuidance, error)
}

func ValidateSecretaryGuidanceScope(scope, source string) error {
	if scope == SecretaryGuidanceUser && source == "" {
		return nil
	}
	if scope == SecretaryGuidanceSession {
		id, err := strconv.ParseInt(source, 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == source {
			return nil
		}
	}
	return fmt.Errorf("invalid secretary guidance scope or source: %w", ErrConflict)
}

func NormalizeSecretaryInstruction(instruction string) (string, error) {
	if !utf8.ValidString(instruction) || strings.ContainsRune(instruction, 0) ||
		len(instruction) > SecretaryInstructionMaxBytes ||
		utf8.RuneCountInString(instruction) > SecretaryInstructionMaxRunes {
		return "", fmt.Errorf("secretary instruction exceeds text limits: %w", ErrConflict)
	}
	return strings.TrimSpace(instruction), nil
}

func (s SecretaryInstructionSnapshot) Validate(owner, source string) error {
	for _, item := range []struct {
		guidance      SecretaryGuidance
		scope, source string
	}{
		{s.User, SecretaryGuidanceUser, ""}, {s.Session, SecretaryGuidanceSession, source},
	} {
		g := item.guidance
		if g.OwnerUserID != owner || g.Scope != item.scope || g.SourceID != item.source ||
			g.Revision < 0 ||
			g.Revision == 0 && g.Instruction != "" {
			return fmt.Errorf("secretary instruction snapshot is unavailable: %w", ErrConflict)
		}
		if _, err := NormalizeSecretaryInstruction(g.Instruction); err != nil {
			return err
		}
	}
	_, err := NormalizeSecretaryInstruction(s.Run)
	return err
}
