package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type SecretIssuer interface {
	New(prefix string) (string, error)
	Hash(secret string) string
	Prefix(secret string) string
}

type Secrets struct{}

func (Secrets) New(prefix string) (string, error) {
	return NewSecret(prefix)
}

func (Secrets) Hash(secret string) string {
	return HashSecret(secret)
}

func (Secrets) Prefix(secret string) string {
	return KeyPrefix(secret)
}

func NewSecret(prefix string) (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf[:]), nil
}

func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func KeyPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}

func BearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(strings.ToLower(header), strings.ToLower(prefix)) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
