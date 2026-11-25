package auth

import (
	"sync"
	"time"
)

// Limiter is a fixed-window rate limiter for login attempts. It is intentionally
// in-memory: a restart clears the counters, which is acceptable for a
// single-instance home-server application.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	max     int
	window  time.Duration
	now     func() time.Time
}

type bucket struct {
	count int
	reset time.Time
}

// NewLimiter creates a limiter that allows max attempts per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		max:     max,
		window:  window,
		now:     time.Now,
	}
}

// Allow reports whether another attempt is allowed and, if not, how long the
// caller should wait. An allowed attempt is reserved atomically, so concurrent
// callers cannot exceed max. The reservation counts as a failed attempt unless
// the caller calls Reset (success) or Release (aborted before a decision).
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &bucket{count: 1, reset: now.Add(l.window)}
		return true, 0
	}
	if b.count < l.max {
		b.count++
		return true, 0
	}
	return false, b.reset.Sub(now)
}

// Release gives back a reservation that did not reach a credential decision,
// e.g. because of a database error. It must not be used for failed logins.
func (l *Limiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok || b.count <= 0 {
		return
	}
	b.count--
	if b.count == 0 {
		delete(l.buckets, key)
	}
}

// Reset clears the counter for a key, e.g. after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// Sweep removes expired buckets. Call it periodically to bound memory use.
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if now.After(b.reset) {
			delete(l.buckets, k)
		}
	}
}
