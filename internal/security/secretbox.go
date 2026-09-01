package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type SecretBox struct {
	aead cipher.AEAD
}

func LoadOrCreateMasterKey(dataDir, configured string) ([]byte, error) {
	if configured != "" {
		key, err := base64.StdEncoding.DecodeString(configured)
		if err != nil || len(key) != 32 {
			return nil, errors.New("configured master key must be base64 encoding of 32 bytes")
		}
		return key, nil
	}
	path := filepath.Join(dataDir, "master.key")
	if data, err := os.ReadFile(path); err == nil {
		key, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(key) != 32 {
			return nil, errors.New("stored master key is invalid")
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return nil, fmt.Errorf("install master key: %w", err)
	}
	return key, nil
}

func NewSecretBox(key []byte) (*SecretBox, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

func (b *SecretBox) Seal(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := b.aead.Seal(nil, nonce, []byte(plaintext), []byte("hostpin:v1"))
	payload := append(nonce, ciphertext...)
	return "v1:" + base64.RawStdEncoding.EncodeToString(payload), nil
}

func (b *SecretBox) Open(encoded string) (string, error) {
	version, raw, ok := strings.Cut(encoded, ":")
	if !ok || version != "v1" {
		return "", errors.New("unsupported encrypted secret version")
	}
	payload, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(payload) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", errors.New("invalid encrypted secret")
	}
	nonce := payload[:b.aead.NonceSize()]
	plaintext, err := b.aead.Open(nil, nonce, payload[b.aead.NonceSize():], []byte("hostpin:v1"))
	if err != nil {
		return "", errors.New("decrypt secret")
	}
	return string(plaintext), nil
}
