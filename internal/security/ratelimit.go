package security

import (
	"sync"
	"time"
)

type enrollmentAttempt struct {
	failures    []time.Time
	lockedUntil time.Time
}

type EnrollmentLimiter struct {
	mu          sync.Mutex
	byIP        map[string]*enrollmentAttempt
	global      []time.Time
	pausedUntil time.Time
}

func NewEnrollmentLimiter() *EnrollmentLimiter {
	return &EnrollmentLimiter{byIP: make(map[string]*enrollmentAttempt)}
}

func (l *EnrollmentLimiter) Allow(ip string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup(now)
	if now.Before(l.pausedUntil) {
		return false, l.pausedUntil.Sub(now)
	}
	attempt := l.byIP[ip]
	if attempt != nil && now.Before(attempt.lockedUntil) {
		return false, attempt.lockedUntil.Sub(now)
	}
	return true, 0
}

func (l *EnrollmentLimiter) Failure(ip string, now time.Time) (paused bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup(now)
	attempt := l.byIP[ip]
	if attempt == nil {
		attempt = &enrollmentAttempt{}
		l.byIP[ip] = attempt
	}
	attempt.failures = append(attempt.failures, now)
	l.global = append(l.global, now)
	if len(attempt.failures) >= 5 {
		attempt.lockedUntil = now.Add(30 * time.Minute)
	}
	if len(l.global) >= 100 {
		if now.Before(l.pausedUntil) {
			return false
		}
		l.pausedUntil = now.Add(15 * time.Minute)
		return true
	}
	return false
}

func (l *EnrollmentLimiter) Success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

func (l *EnrollmentLimiter) cleanup(now time.Time) {
	window := now.Add(-10 * time.Minute)
	l.global = retainAfter(l.global, window)
	for ip, attempt := range l.byIP {
		attempt.failures = retainAfter(attempt.failures, window)
		if len(attempt.failures) == 0 && now.After(attempt.lockedUntil) {
			delete(l.byIP, ip)
		}
	}
}

func retainAfter(input []time.Time, cutoff time.Time) []time.Time {
	index := 0
	for index < len(input) && input[index].Before(cutoff) {
		index++
	}
	return input[index:]
}
