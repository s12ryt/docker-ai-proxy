package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

var ErrInitialAdminExists = errors.New("initial admin already exists")

// User represents a system user.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// NormalizeUsername trims and lowercases the username.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// CreateUser inserts a new user. Username is always normalized before insert.
func (s *Store) CreateUser(ctx context.Context, u User) (User, error) {
	return s.createUser(ctx, s.db, u)
}

type sqlExecerQuerier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) createUser(ctx context.Context, q sqlExecerQuerier, u User) (User, error) {
	u.Username = NormalizeUsername(u.Username)
	if u.Username == "" {
		return User{}, fmt.Errorf("username is required")
	}
	if len(u.Username) > 64 {
		return User{}, fmt.Errorf("username must be at most 64 characters")
	}
	if u.PasswordHash == "" {
		return User{}, fmt.Errorf("password hash is required")
	}
	if u.Role == "" {
		u.Role = RoleUser
	}
	if u.Role != RoleAdmin && u.Role != RoleUser {
		return User{}, fmt.Errorf("invalid role: %s", u.Role)
	}

	now := time.Now().UnixMilli()
	if s.dialect.name == "postgres" {
		query := s.dialect.rebind(`INSERT INTO users(username, password_hash, role, disabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`)
		if err := q.QueryRowContext(ctx, query, u.Username, u.PasswordHash, u.Role, u.Disabled, now, now).Scan(&u.ID); err != nil {
			return User{}, err
		}
	} else {
		query := s.dialect.rebind(`INSERT INTO users(username, password_hash, role, disabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`)
		res, err := q.ExecContext(ctx, query, u.Username, u.PasswordHash, u.Role, u.Disabled, now, now)
		if err != nil {
			return User{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return User{}, err
		}
		u.ID = id
	}
	u.CreatedAt = time.UnixMilli(now)
	u.UpdatedAt = time.UnixMilli(now)
	return u, nil
}

// FindUserByUsername finds a user by username after normalization.
func (s *Store) FindUserByUsername(ctx context.Context, username string) (User, error) {
	query := s.dialect.rebind(`SELECT id, username, password_hash, role, disabled, created_at, updated_at, last_login_at FROM users WHERE username = ?`)
	row := s.db.QueryRowContext(ctx, query, NormalizeUsername(username))
	return scanUser(row)
}

// FindUserByID finds a user by ID.
func (s *Store) FindUserByID(ctx context.Context, id int64) (User, error) {
	query := s.dialect.rebind(`SELECT id, username, password_hash, role, disabled, created_at, updated_at, last_login_at FROM users WHERE id = ?`)
	row := s.db.QueryRowContext(ctx, query, id)
	return scanUser(row)
}

// CountUsers returns the total number of users.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	return s.countUsers(ctx, s.db)
}

func (s *Store) countUsers(ctx context.Context, q sqlExecerQuerier) (int64, error) {
	var count int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&count)
	return count, err
}

// UpdateUserLastLogin updates the last_login_at timestamp.
func (s *Store) UpdateUserLastLogin(ctx context.Context, id int64) error {
	query := s.dialect.rebind(`UPDATE users SET last_login_at = ? WHERE id = ?`)
	_, err := s.db.ExecContext(ctx, query, time.Now().UnixMilli(), id)
	return err
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(row userScanner) (User, error) {
	var u User
	var lastLogin sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Disabled, &createdAt, &updatedAt, &lastLogin)
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = time.UnixMilli(createdAt)
	u.UpdatedAt = time.UnixMilli(updatedAt)
	if lastLogin.Valid {
		t := time.UnixMilli(lastLogin.Int64)
		u.LastLoginAt = &t
	}
	return u, nil
}
