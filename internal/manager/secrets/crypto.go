package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

const envMasterKey = "SECRET_VAULT_MASTER_KEY"

type Cipher struct {
	key   []byte
	keyID string
}

type EncryptedValue struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

func NewCipherFromEnv() (*Cipher, error) {
	raw := os.Getenv(envMasterKey)
	if raw == "" {
		sum := sha256.Sum256([]byte("pax-manager-secret-vault-dev-key"))
		return &Cipher{key: sum[:], keyID: "dev"}, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err == nil && len(decoded) == 32 {
		return &Cipher{key: decoded, keyID: "env:" + shortHash(decoded)}, nil
	}
	sum := sha256.Sum256([]byte(raw))
	return &Cipher{key: sum[:], keyID: "env:" + shortHash(sum[:])}, nil
}

func (c *Cipher) Encrypt(plaintext string, aad []byte) (EncryptedValue, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return EncryptedValue{}, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return EncryptedValue{}, fmt.Errorf("create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return EncryptedValue{}, fmt.Errorf("generate nonce: %w", err)
	}
	return EncryptedValue{
		Ciphertext: gcm.Seal(nil, nonce, []byte(plaintext), aad),
		Nonce:      nonce,
		KeyID:      c.keyID,
	}, nil
}

func (c *Cipher) Decrypt(value EncryptedValue, aad []byte) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, value.Nonce, value.Ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plaintext), nil
}

func shortHash(value []byte) string {
	sum := sha256.Sum256(value)
	return base64.RawURLEncoding.EncodeToString(sum[:6])
}
