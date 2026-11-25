package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CountUsers returns the number of accounts. It is used to decide whether the
// setup wizard must be shown.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CountAdmins returns the number of admin accounts.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM users WHERE role = ?`, RoleAdmin).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// CreateUser inserts a user and returns it with its ID and timestamps.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, role Role) (User, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		username, passwordHash, role, formatTime(now), formatTime(now))
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("create user id: %w", err)
	}
	return User{ID: id, Username: username, PasswordHash: passwordHash, Role: role, CreatedAt: now, UpdatedAt: now}, nil
}

// ErrUsernameTaken is returned when a username already exists (case-insensitive).
var ErrUsernameTaken = errors.New("username already taken")

const userColumns = `id, username, password_hash, role, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var role, created, updated string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.Role = Role(role)
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

// ErrSetupComplete is returned when the initial admin already exists.
var ErrSetupComplete = errors.New("setup already completed")

// CreateFirstAdmin creates the initial admin account atomically. It fails with
// ErrSetupComplete if any user already exists.
func (s *Store) CreateFirstAdmin(ctx context.Context, username, passwordHash string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, fmt.Errorf("begin setup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n); err != nil {
		return User{}, fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return User{}, ErrSetupComplete
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		username, passwordHash, RoleAdmin, formatTime(now), formatTime(now))
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("create admin: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("create admin id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, fmt.Errorf("commit setup: %w", err)
	}
	return User{ID: id, Username: username, PasswordHash: passwordHash, Role: RoleAdmin, CreatedAt: now, UpdatedAt: now}, nil
}

// UserByUsername fetches a user by its case-insensitive username.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("user by username: %w", err)
	}
	return u, nil
}

// UserByID fetches a user by ID.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("user by id: %w", err)
	}
	return u, nil
}

// ListUsers returns all users ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdatePassword replaces the password hash of a user.
func (s *Store) UpdatePassword(ctx context.Context, id int64, hash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrLastAdmin is returned when deleting the only admin account.
var ErrLastAdmin = errors.New("cannot delete the last admin")

// DeleteUser removes a user. Sessions are removed by the foreign key cascade.
// The last-admin check and the delete are a single statement, so two concurrent
// deletions can never remove every admin.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM users
		 WHERE id = ?
		   AND (role <> ? OR (SELECT COUNT(1) FROM users WHERE role = ?) > 1)`,
		id, RoleAdmin, RoleAdmin)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}

	// Nothing was deleted: distinguish a missing user from the last admin.
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, id).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("check user after delete: %w", err)
	}
	if Role(role) == RoleAdmin {
		return ErrLastAdmin
	}
	return ErrNotFound
}
