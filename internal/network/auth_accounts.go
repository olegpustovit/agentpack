package network

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	Email              string `json:"email"`
	Role               string `json:"role"`
	Disabled           bool   `json:"disabled"`
	SessionsValidAfter int64  `json:"sessions_valid_after"`
	CreatedAt          int64  `json:"created_at"`
}

var errInvalidLogin = errors.New("invalid email or password")

func ResetOwner(ctx context.Context, store *state.Store, home string) error {
	if err := state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		for _, table := range []string{"auth_sessions", "oauth_tokens", "users"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reset gateway owner: %w", err)
	}
	for _, name := range []string{"root-password", "setup-token"} {
		if err := os.Remove(filepath.Join(home, "network", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy account file: %w", err)
		}
	}
	return nil
}

// A missing account still pays for one default-cost comparison.
const dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func normalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 254 {
		return "", errors.New("email must contain 1 to 254 bytes")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || len(address.Address) > 254 {
		return "", errors.New("enter an email address without a display name")
	}
	return strings.ToLower(address.Address), nil
}

func validPassword(password string) error {
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must contain 12 to 72 bytes")
	}
	return nil
}

func passwordHash(password string) ([]byte, error) {
	if err := validPassword(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return hash, nil
}

// claimOwner uses an already-computed hash so a legacy claim can preserve it.
func claimOwner(ctx context.Context, store *state.Store, email string, hash []byte) (bool, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return false, err
	}
	if _, err := bcrypt.Cost(hash); err != nil {
		return false, errors.New("invalid password hash")
	}
	result, err := store.DB.ExecContext(ctx, `INSERT INTO users(email,password_hash,role,created_at)
SELECT ?,?,'owner',? WHERE NOT EXISTS(SELECT 1 FROM users)`, email, string(hash), time.Now().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("claim gateway owner: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func AddUser(ctx context.Context, store *state.Store, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	result, err := store.DB.ExecContext(ctx, `INSERT INTO users(email,password_hash,role,created_at)
SELECT ?,?,'member',? WHERE EXISTS(SELECT 1 FROM users)`, email, string(hash), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("add user: %w", err)
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		err = errors.New("claim the gateway before adding users")
	}
	return err
}

func SetPassword(ctx context.Context, store *state.Store, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	return state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		if err := revokeUserSessions(ctx, tx, email, time.Now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET password_hash=? WHERE email=?", string(hash), email)
		return err
	})
}

func DisableUser(ctx context.Context, store *state.Store, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	return state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		if err := revokeUserSessions(ctx, tx, email, time.Now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET disabled=1 WHERE email=?", email)
		return err
	})
}

func revokeUserSessions(ctx context.Context, tx *sql.Tx, email string, now time.Time) error {
	// Advance past existing tokens even when several operations share a clock tick.
	result, err := tx.ExecContext(ctx, `UPDATE users SET sessions_valid_after=max(?,sessions_valid_after+1,
COALESCE((SELECT max(created_at)+1 FROM auth_sessions WHERE email=?),0)) WHERE email=?`, now.UnixMilli(), email, email)
	if err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("user not found")
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE email=?", email)
	return err
}

func ListUsers(ctx context.Context, store *state.Store) ([]User, error) {
	rows, err := store.Reader.QueryContext(ctx, "SELECT email,role,disabled,sessions_valid_after,created_at FROM users ORDER BY email")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.Email, &user.Role, &user.Disabled, &user.SessionsValidAfter, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func verifyLogin(ctx context.Context, store *state.Store, email, password string) (User, error) {
	var user User
	hash := dummyPasswordHash
	mailbox, invalidEmail := normalizeEmail(email)
	var lookup error
	if invalidEmail == nil {
		lookup = store.Reader.QueryRowContext(ctx, "SELECT email,password_hash,role,disabled,sessions_valid_after,created_at FROM users WHERE email=?", mailbox).Scan(&user.Email, &hash, &user.Role, &user.Disabled, &user.SessionsValidAfter, &user.CreatedAt)
	}
	if invalidEmail != nil || errors.Is(lookup, sql.ErrNoRows) {
		hash = dummyPasswordHash
	}
	invalidHash := false
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		hash, invalidHash = dummyPasswordHash, true
	}
	candidate := password
	if len(candidate) > 72 {
		candidate = candidate[:72]
	}
	compared := bcrypt.CompareHashAndPassword([]byte(hash), []byte(candidate))
	if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
		return User{}, fmt.Errorf("read login account: %w", lookup)
	}
	if invalidEmail != nil || lookup != nil || invalidHash || user.Email == "" || user.Disabled || validPassword(password) != nil || compared != nil {
		return User{}, errInvalidLogin
	}
	return user, nil
}
