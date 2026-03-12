package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var rawBase64 = base64.RawURLEncoding

func NewOpaque(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return prefix + rawBase64.EncodeToString(buf), nil
}

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func NewID(prefix string) string {
	trimmed := strings.TrimSuffix(prefix, "_")
	return trimmed + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
