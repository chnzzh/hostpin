package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

type ArgonParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var DefaultArgonParams = ArgonParams{
	Memory: 64 * 1024, Iterations: 3, Parallelism: uint8(min(runtime.NumCPU(), 2)),
	SaltLength: 16, KeyLength: 32,
}

var PINArgonParams = ArgonParams{
	Memory: 32 * 1024, Iterations: 3, Parallelism: 1, SaltLength: 16, KeyLength: 32,
}

func HashPassword(password string) (string, error) {
	return hashArgon(password, DefaultArgonParams)
}

func HashPIN(pin string) (string, error) {
	return hashArgon(pin, PINArgonParams)
}

func hashArgon(value string, params ArgonParams) (string, error) {
	if value == "" {
		return "", errors.New("value must not be empty")
	}
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(value), salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", params.Memory, params.Iterations,
		params.Parallelism, base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyHash(encoded, value string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return false
	}
	params := ArgonParams{}
	for _, item := range strings.Split(parts[2], ",") {
		key, raw, ok := strings.Cut(item, "=")
		if !ok {
			return false
		}
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return false
		}
		switch key {
		case "m":
			params.Memory = uint32(value)
		case "t":
			params.Iterations = uint32(value)
		case "p":
			params.Parallelism = uint8(value)
		}
	}
	if params.Memory < 8*1024 || params.Memory > 512*1024 || params.Iterations == 0 || params.Iterations > 10 || params.Parallelism == 0 || params.Parallelism > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	wanted, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(wanted) < 16 || len(wanted) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(value), salt, params.Iterations, params.Memory, params.Parallelism, uint32(len(wanted)))
	return subtle.ConstantTimeCompare(actual, wanted) == 1
}

func IsWeakPIN(pin string) bool {
	if len(pin) < 8 {
		return true
	}
	var hasLetter, hasDigit bool
	for _, r := range pin {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	return !(hasLetter && hasDigit)
}
