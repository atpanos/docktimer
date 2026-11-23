package auth

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is the minimum accepted password length.
const MinPasswordLength = 10

// bcryptCost is deliberately high enough to be expensive for attackers while
// still fast enough for an interactive login on a small server.
const bcryptCost = 12

// ErrWeakPassword is returned when a password does not meet the policy.
var ErrWeakPassword = errors.New("password does not meet the policy")

// ValidatePassword checks the password policy. It returns a German message
// suitable for the UI.
func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if len(pw) > 72 {
		return fmt.Errorf("%w: at most 72 characters", ErrWeakPassword)
	}
	if strings.TrimSpace(pw) == "" {
		return fmt.Errorf("%w: must not consist only of whitespace", ErrWeakPassword)
	}
	return nil
}

// HashPassword hashes a password with bcrypt.
func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword compares a plaintext password with a bcrypt hash in constant
// time relative to the hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
