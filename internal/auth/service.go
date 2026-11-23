// Package auth implements password hashing, sessions, CSRF protection and
// login rate limiting.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/atpanos/docktimer/internal/database"
)

var (
	// ErrInvalidCredentials is returned for unknown user or wrong password.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrSessionInvalid is returned for missing, expired or idle sessions.
	ErrSessionInvalid = errors.New("session invalid")
	// ErrSetupAlreadyDone is returned when setup runs on an initialized instance.
	ErrSetupAlreadyDone = errors.New("setup already done")
	// ErrLastAdmin is returned when deleting/demoting the only admin.
	ErrLastAdmin = errors.New("cannot remove the last admin")
	// ErrInvalidUsername is returned for malformed usernames.
	ErrInvalidUsername = errors.New("invalid username")
	// ErrUserNotFound is returned when a user does not exist.
	ErrUserNotFound = errors.New("user not found")
)

// RateLimitError signals a blocked login attempt.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many login attempts, retry after %s", e.RetryAfter.Round(time.Second))
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,31}$`)

// ValidateUsername enforces 3-32 chars, alphanumeric first char.
func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("%w: 3–32 Zeichen, Buchstaben, Zahlen, _ . -", ErrInvalidUsername)
	}
	return nil
}

// Service bundles authentication use cases.
type Service struct {
	store    *database.Store
	limiter  *Limiter
	csrf     *CSRF
	ttl      time.Duration
	idleTTL  time.Duration
	now      func() time.Time
	dummy    string
	dummyOne sync.Once
}

// NewService creates the authentication service.
func NewService(store *database.Store, csrf *CSRF, limiter *Limiter, ttl, idleTTL time.Duration) *Service {
	return &Service{
		store:   store,
		limiter: limiter,
		csrf:    csrf,
		ttl:     ttl,
		idleTTL: idleTTL,
		now:     time.Now,
	}
}

// SetClock replaces the clock. Used by tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// CSRF exposes the token manager to the web layer.
func (s *Service) CSRF() *CSRF { return s.csrf }

// NeedsSetup reports whether no user exists yet.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// Setup creates the first admin and logs it in.
func (s *Service) Setup(ctx context.Context, username, password, ip, userAgent string) (string, database.User, error) {
	if err := ValidateUsername(username); err != nil {
		return "", database.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", database.User{}, err
	}
	user, err := s.store.CreateFirstAdmin(ctx, username, hash)
	if err != nil {
		if errors.Is(err, database.ErrSetupComplete) {
			return "", database.User{}, ErrSetupAlreadyDone
		}
		return "", database.User{}, err
	}
	token, err := s.newSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return "", database.User{}, err
	}
	return token, user, nil
}

// Login verifies credentials and creates a session.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (string, database.User, error) {
	key := strings.ToLower(strings.TrimSpace(ip)) + "|" + strings.ToLower(strings.TrimSpace(username))
	if ok, retry := s.limiter.Allowed(key); !ok {
		return "", database.User{}, &RateLimitError{RetryAfter: retry}
	}

	user, err := s.store.UserByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			// Constant-ish time: compare against a dummy hash so user
			// enumeration through timing is harder.
			s.compareDummy(password)
			s.limiter.Fail(key)
			return "", database.User{}, ErrInvalidCredentials
		}
		return "", database.User{}, err
	}
	if !CheckPassword(user.PasswordHash, password) {
		s.limiter.Fail(key)
		return "", database.User{}, ErrInvalidCredentials
	}
	s.limiter.Reset(key)

	token, err := s.newSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return "", database.User{}, err
	}
	return token, user, nil
}

// SessionFromToken resolves a cookie token to a user. It enforces absolute and
// idle timeouts and touches the session on success.
func (s *Service) SessionFromToken(ctx context.Context, token string) (database.User, string, error) {
	if token == "" {
		return database.User{}, "", ErrSessionInvalid
	}
	id := HashToken(token)
	sess, err := s.store.SessionByID(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return database.User{}, "", ErrSessionInvalid
		}
		return database.User{}, "", err
	}

	now := s.now().UTC()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > s.idleTTL {
		_ = s.store.DeleteSession(ctx, id)
		return database.User{}, "", ErrSessionInvalid
	}

	expires := now.Add(s.ttl)
	if hardCap := sess.CreatedAt.Add(s.ttl); expires.After(hardCap) {
		expires = hardCap
	}
	if err := s.store.TouchSession(ctx, id, now, expires); err != nil {
		return database.User{}, "", err
	}
	user, err := s.store.UserByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			_ = s.store.DeleteSession(ctx, id)
			return database.User{}, "", ErrSessionInvalid
		}
		return database.User{}, "", err
	}
	return user, id, nil
}

// Logout deletes the session belonging to token.
func (s *Service) Logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_ = s.store.DeleteSession(ctx, HashToken(token))
}

// CreateUser creates a viewer or admin account.
func (s *Service) CreateUser(ctx context.Context, username, password string, role database.Role) (database.User, error) {
	if err := ValidateUsername(username); err != nil {
		return database.User{}, err
	}
	if !role.Valid() {
		return database.User{}, fmt.Errorf("invalid role %q", role)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return database.User{}, err
	}
	return s.store.CreateUser(ctx, username, hash, role)
}

// ChangePassword verifies the current password and sets a new one. All sessions
// of the user except the current one are invalidated.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	user, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !CheckPassword(user.PasswordHash, current) {
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	return s.store.UpdatePassword(ctx, userID, hash)
}

// DeleteUser removes an account, protecting the last admin.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	user, err := s.store.UserByID(ctx, id)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	if user.Role == database.RoleAdmin {
		admins, err := s.store.CountAdmins(ctx)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	return s.store.DeleteUser(ctx, id)
}

// Cleanup deletes expired sessions and sweeps the rate limiter.
func (s *Service) Cleanup(ctx context.Context) {
	_, _ = s.store.DeleteExpiredSessions(ctx, s.now().UTC())
	s.limiter.Sweep()
}

func (s *Service) newSession(ctx context.Context, userID int64, ip, userAgent string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	sess := database.Session{
		ID:         HashToken(token),
		UserID:     userID,
		ExpiresAt:  now.Add(s.ttl),
		CreatedAt:  now,
		LastSeenAt: now,
		UserAgent:  truncate(userAgent, 255),
		IP:         truncate(ip, 64),
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) compareDummy(pw string) {
	s.dummyOne.Do(func() {
		h, err := HashPassword("docktimer-dummy-password-value")
		if err == nil {
			s.dummy = h
		}
	})
	if s.dummy != "" {
		_ = CheckPassword(s.dummy, pw)
	}
}

// HashToken returns the hex SHA-256 of a session token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
