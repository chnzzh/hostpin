package security

import (
	"testing"
	"time"
)

func TestArgonHashAndValidation(t *testing.T) {
	original := DefaultArgonParams
	DefaultArgonParams = ArgonParams{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	defer func() { DefaultArgonParams = original }()
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyHash(hash, "correct horse battery staple") {
		t.Fatal("correct password was rejected")
	}
	if VerifyHash(hash, "wrong") || VerifyHash("malformed", "wrong") {
		t.Fatal("invalid password or malformed hash was accepted")
	}
	if !IsWeakPIN("123456") || IsWeakPIN("Fleet9Pin") {
		t.Fatal("weak PIN classification is incorrect")
	}
}

func TestEnrollmentLimiter(t *testing.T) {
	limiter := NewEnrollmentLimiter()
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 5; index++ {
		limiter.Failure("192.0.2.1", now.Add(time.Duration(index)*time.Second))
	}
	if allowed, retry := limiter.Allow("192.0.2.1", now.Add(10*time.Second)); allowed || retry < 29*time.Minute {
		t.Fatalf("expected per-IP lock, got allowed=%v retry=%s", allowed, retry)
	}
	if allowed, _ := limiter.Allow("192.0.2.2", now.Add(10*time.Second)); !allowed {
		t.Fatal("unrelated IP was incorrectly locked")
	}
	limiter.Success("192.0.2.1")
	if allowed, _ := limiter.Allow("192.0.2.1", now.Add(11*time.Second)); !allowed {
		t.Fatal("successful enrollment did not clear the lock")
	}
}

func TestEnrollmentLimiterGlobalCircuitBreaker(t *testing.T) {
	limiter := NewEnrollmentLimiter()
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	opened := 0
	for index := 0; index < 100; index++ {
		if limiter.Failure(string(rune(index+1)), now.Add(time.Duration(index)*time.Millisecond)) {
			opened++
		}
	}
	if opened != 1 {
		t.Fatalf("global circuit breaker opened %d times", opened)
	}
	if limiter.Failure("another-source", now.Add(time.Second)) {
		t.Fatal("an already-open circuit breaker emitted another transition")
	}
	if allowed, retry := limiter.Allow("clean-source", now.Add(time.Second)); allowed || retry < 14*time.Minute {
		t.Fatalf("global circuit breaker did not block enrollment: allowed=%v retry=%s", allowed, retry)
	}
}
