package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const AgentTokenPrefix = "hp_a_"
const APIKeyPrefix = "hp_k_"
const ShareTokenPrefix = "hp_s_"

func RandomURLToken(bytes int) (string, error) {
	if bytes < 16 {
		bytes = 16
	}
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func NewAPIKeyToken() (token, tokenID, hash string, err error) {
	id, err := RandomURLToken(9)
	if err != nil {
		return "", "", "", err
	}
	secret, err := RandomURLToken(32)
	if err != nil {
		return "", "", "", err
	}
	return APIKeyPrefix + id + "." + secret, id, HashToken(secret), nil
}

func ParseAPIKeyToken(token string) (tokenID, hash string, err error) {
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return "", "", errors.New("invalid API key prefix")
	}
	id, secret, ok := strings.Cut(strings.TrimPrefix(token, APIKeyPrefix), ".")
	if !ok || len(id) < 8 || len(secret) < 32 {
		return "", "", errors.New("invalid API key")
	}
	return id, HashToken(secret), nil
}

func NewShareToken() (string, string, error) {
	secret, err := RandomURLToken(32)
	if err != nil {
		return "", "", err
	}
	token := ShareTokenPrefix + secret
	return token, HashToken(token), nil
}

func NewAgentToken() (token, tokenID, hash string, err error) {
	id, err := RandomURLToken(9)
	if err != nil {
		return "", "", "", err
	}
	secret, err := RandomURLToken(32)
	if err != nil {
		return "", "", "", err
	}
	return AgentTokenPrefix + id + "." + secret, id, HashToken(secret), nil
}

func ParseAgentToken(token string) (tokenID, hash string, err error) {
	if !strings.HasPrefix(token, AgentTokenPrefix) {
		return "", "", errors.New("invalid agent token prefix")
	}
	id, secret, ok := strings.Cut(strings.TrimPrefix(token, AgentTokenPrefix), ".")
	if !ok || len(id) < 8 || len(secret) < 32 {
		return "", "", errors.New("invalid agent token")
	}
	return id, HashToken(secret), nil
}

func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
