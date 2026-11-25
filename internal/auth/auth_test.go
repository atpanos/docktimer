package auth

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := HashPassword("a-very-good-password")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "ein-sehr") {
		t.Fatal("hash must not contain the plaintext")
	}
	if !CheckPassword(hash, "a-very-good-password") {
		t.Error("valid password rejected")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Error("invalid password accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePassword("kurz"); err == nil {
		t.Error("short password accepted")
	}
	if err := ValidatePassword(strings.Repeat("a", 73)); err == nil {
		t.Error("oversized password accepted")
	}
	if err := ValidatePassword(strings.Repeat(" ", 12)); err == nil {
		t.Error("whitespace-only password accepted")
	}
	if err := ValidatePassword("0123456789"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
}

func TestCSRFTokenBinding(t *testing.T) {
	c := NewCSRF("secret-value")
	nonce := c.NewNonce()
	token := c.Token(nonce, "session-1")

	if !c.Validate(token, nonce, "session-1") {
		t.Fatal("valid token rejected")
	}
	if c.Validate(token, nonce, "session-2") {
		t.Error("token must be bound to the session")
	}
	if c.Validate(token, c.NewNonce(), "session-1") {
		t.Error("token must be bound to the nonce")
	}
	if c.Validate("", nonce, "session-1") {
		t.Error("empty token accepted")
	}
	if c.Validate(token+"x", nonce, "session-1") {
		t.Error("tampered token accepted")
	}
}

func TestLimiterBlocksAfterMax(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("ip"); !ok {
			t.Fatalf("attempt %d blocked too early", i+1)
		}
	}
	ok, retry := l.Allow("ip")
	if ok {
		t.Fatal("limiter did not block")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("unexpected retry duration %v", retry)
	}

	// A different key is independent.
	if ok, _ := l.Allow("other"); !ok {
		t.Error("other key blocked")
	}

	// After the window the counter resets.
	now = now.Add(2 * time.Minute)
	if ok, _ := l.Allow("ip"); !ok {
		t.Error("limiter did not reset")
	}
}

func TestLimiterRelease(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("first attempt blocked")
	}
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("expected block")
	}
	l.Release("k")
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("release did not free the reservation")
	}
	l.Reset("k")
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("reset did not clear the bucket")
	}
}

func TestLimiterConcurrentAdmissionsRespectMax(t *testing.T) {
	const (
		max      = 5
		requests = 100
	)
	l := NewLimiter(max, time.Minute)

	var (
		wg       sync.WaitGroup
		admitted atomic.Int32
	)
	start := make(chan struct{})
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, _ := l.Allow("same-key"); ok {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := admitted.Load(); got != max {
		t.Fatalf("admitted %d concurrent attempts, want %d", got, max)
	}
}

func TestValidateUsername(t *testing.T) {
	for _, name := range []string{"admin", "a_b", "user.name", "user-name", "abc"} {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("%q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", "ab", "-admin", "admin!", strings.Repeat("a", 33)} {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestHashTokenStable(t *testing.T) {
	a := HashToken("abc")
	b := HashToken("abc")
	if a != b || len(a) != 64 {
		t.Fatalf("unexpected hash: %q %q", a, b)
	}
	if HashToken("abd") == a {
		t.Fatal("different tokens must hash differently")
	}
}
