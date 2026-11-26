package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateSession stores a hashed session ID.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, expires_at, created_at, last_seen_at, user_agent, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, formatTime(sess.ExpiresAt), formatTime(sess.CreatedAt),
		formatTime(sess.LastSeenAt), sess.UserAgent, sess.IP)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionByID returns a session by its hashed ID.
func (s *Store) SessionByID(ctx context.Context, id string) (Session, error) {
	var sess Session
	var expires, created, seen string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, expires_at, created_at, last_seen_at, user_agent, ip
		 FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.UserID, &expires, &created, &seen, &sess.UserAgent, &sess.IP)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("session by id: %w", err)
	}
	sess.ExpiresAt = parseTime(expires)
	sess.CreatedAt = parseTime(created)
	sess.LastSeenAt = parseTime(seen)
	return sess, nil
}

// TouchSession extends a session's idle timestamp and expiry.
func (s *Store) TouchSession(ctx context.Context, id string, seenAt, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		formatTime(seenAt), formatTime(expiresAt), id)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteSession removes a single session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions removes all sessions of a user.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// DeleteOtherSessions removes all sessions of a user except keepID.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID int64, keepID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID); err != nil {
		return fmt.Errorf("delete other sessions: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that are expired at now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
