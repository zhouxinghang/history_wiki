package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const tokenBytes = 32

func NewToken() (raw string, digest [sha256.Size]byte, err error) {
	value := make([]byte, tokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", digest, fmt.Errorf("generate secure token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(value)
	return raw, sha256.Sum256([]byte(raw)), nil
}

func TokenDigest(raw string) [sha256.Size]byte {
	return sha256.Sum256([]byte(raw))
}

func NewID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}
