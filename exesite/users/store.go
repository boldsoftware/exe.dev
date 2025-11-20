// Package users provides the data layer for login, authentication, user, boxes, and related functionality.
package users

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	_ "embed"
)

//go:embed schema.sql
var Schema string

// Errors
var (
	// ErrExpired is returned when a temporary resource has expired.
	ErrExpired  = fmt.Errorf("expired")
	ErrInvalid  = fmt.Errorf("invalid")
	ErrNotFound = fmt.Errorf("not found")
	ErrMerge    = fmt.Errorf("merge")
)

type Store struct {
	db *sqlite.DB
}

func New(db *sqlite.DB) *Store {
	return &Store{db: db}
}

type Login struct {
	UserID    string // The logged-in user's unique ID.
	Email     string // The logged-in user's email, if verified.
	PublicKey ssh.PublicKey
	LastUsed  time.Time // When the key was last used. Zero if new.
}

func (l *Login) IsVerified() bool {
	return l.Email != ""
}

// PendingEmailVerification describes a pending email verification flow.
// Callers can use it to display which email/key pair will be linked.
type PendingEmailVerification struct {
	Code      string
	Email     string
	PublicKey ssh.PublicKey
	CreatedAt time.Time
	ExpiresAt time.Time
}

type EmailVerificationStats struct {
	Total   int // Number of expired and unexpired verifications
	Expired int // Number of expired verifications.
}

// LoginSSH logs in or creates a user using the given SSH public key,
// returning the associated User, or an error if any.
//
// It may attempt to create a new user with an userID that collides with an
// existing userID. In this case, it aborts and returns an error.
func (s *Store) LoginSSH(ctx context.Context, key ssh.PublicKey) (*Login, error) {
	const q = `
		-- If key is new, create a new user for it.
		INSERT INTO users (id)
		SELECT @makeID WHERE NOT EXISTS (
			SELECT 1 FROM ssh_keys WHERE public_key = @public_key
		);


		-- If key is new, associate it with the new user;
		-- otherwise, update its last_used_at timestamp.
		INSERT INTO ssh_keys (user_id, public_key, created_at, last_used_at)
		SELECT @makeID, @public_key, now(), now()
		ON CONFLICT(public_key) DO UPDATE SET last_used_at = now();

		
		-- Return the logged-in user.
		SELECT u.id, COALESCE(u.email, ''), ssh.last_used_at
		FROM users u JOIN ssh_keys ssh ON ssh.user_id = u.id
		WHERE ssh.public_key = @public_key;
	`
	for row := range s.queryRW(ctx, q,
		sql.Named("makeID", makeID("user")),
		sql.Named("public_key", func() []byte {
			if key == nil {
				return nil
			}
			return ssh.MarshalAuthorizedKey(key)
		}()),
	) {
		u := &Login{PublicKey: key}
		err := row.Scan(
			&u.UserID,
			&u.Email,
			&u.LastUsed,
		)
		if err != nil {
			var me *msqlite.Error
			if errors.As(err, &me) {
				switch me.Code() {
				case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
					if parseConstraintName(me) == "users.id" {
						return nil, fmt.Errorf("unfortunate user.id collision: %w", err)
					}
				}
			}
			return nil, err
		}
		return u, nil
	}

	panic("unreachable")
}

// StartEmailVerification initiates the email verification process
// using the given email.
// If key is non-nil, it associates the verification with the given SSH public key; otherwise, only a user ID is used.
func (s *Store) StartEmailVerification(ctx context.Context, email string, key ssh.PublicKey) (code string, _ error) {
	code = rand.Text()[:8]
	const q = `
		-- Remove any existing verifications for this email/key pair,
		-- or any expired verifications.
		DELETE FROM email_verifications
		WHERE
			-- Invalidate existing verifications for this email/key pair.
			(email = @email AND public_key IS @public_key)

			-- While we're at it, remove any and all expired verifications.
			OR expires_at <= now();

		-- Insert the new verification.
		INSERT INTO email_verifications (code, email, public_key, created_at, expires_at)
		VALUES (@code, @email, @public_key, now(), datetime(now(), '+15 minutes'));
	`
	err := s.db.Exec(ctx, q,
		sql.Named("code", code),
		sql.Named("email", email),
		sql.Named("public_key", func() []byte {
			if key == nil {
				return nil
			}
			return ssh.MarshalAuthorizedKey(key)
		}()),
	)
	if err != nil {
		var me *msqlite.Error
		if errors.As(err, &me) {
			switch me.Code() {
			case sqlite3.SQLITE_CONSTRAINT_CHECK:
				if parseConstraintName(me) == "check_valid_email_format" {
					return "", fmt.Errorf("%w email: %s", ErrInvalid, email)
				}
			case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
				// Unrecognized public key.
				return "", fmt.Errorf("unrecognized public key")
			}
		}
		return "", err
	}
	return code, nil
}

// ReviewEmailVerification returns details about a pending verification code.
// It returns ErrExpired if the code does not exist or has expired.
func (s *Store) ReviewEmailVerification(ctx context.Context, code string) (*PendingEmailVerification, error) {
	const q = `
		SELECT code, email, public_key, created_at, expires_at
		FROM email_verifications
		WHERE code = @code AND expires_at > now()
	`
	for row := range s.queryRO(ctx, q,
		sql.Named("code", code),
	) {
		var v PendingEmailVerification
		err := row.Scan(
			&v.Code,
			&v.Email,
			&v.PublicKey,
			&v.CreatedAt,
			&v.ExpiresAt,
		)
		if err != nil {
			return nil, err
		}
		return &v, nil
	}
	return nil, ErrExpired
}

// CompleteEmailVerification verifies the email associated with the given code.
func (s *Store) CompleteEmailVerification(ctx context.Context, code string) (*Login, error) {
	const q = `
		DROP TABLE IF EXISTS target;
		CREATE TEMP TABLE target AS
		SELECT
			COALESCE(
				(SELECT id FROM users WHERE email = v.email),
				(SELECT user_id FROM ssh_keys WHERE public_key = v.public_key),
				@makeID
			) AS user_id,
			v.email AS email,
			v.public_key AS public_key
		FROM email_verifications v
		WHERE code = @code AND expires_at > now();

		DELETE FROM email_verifications
		WHERE code = @code;

		INSERT OR IGNORE INTO users (id)
		SELECT user_id FROM target;

		UPDATE users
		SET email = (SELECT email FROM target)
		WHERE id = (SELECT user_id FROM target);

		UPDATE ssh_keys
		SET user_id = (SELECT user_id FROM target)
		WHERE public_key = (SELECT public_key FROM target);

		SELECT t.user_id, u.email, t.public_key, sk.last_used_at
		FROM target t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN ssh_keys sk ON sk.public_key = t.public_key
		LIMIT 1
	`
	for row := range s.queryRW(ctx, q,
		sql.Named("makeID", makeID("user")),
		sql.Named("code", code),
	) {
		var u Login
		if err := row.Scan(
			&u.UserID,
			&u.Email,
			&u.PublicKey,
			&u.LastUsed,
		); err != nil {
			return nil, err
		}
		return &u, nil
	}
	return nil, ErrExpired
}

// EmailVerificationStats returns counts about pending email verifications.
func (s *Store) EmailVerificationStats(ctx context.Context) (*EmailVerificationStats, error) {
	const q = `
		SELECT
			(SELECT COUNT(*) FROM email_verifications),
			(SELECT COUNT(*) FROM email_verifications WHERE expires_at <= now())
	`
	var stats EmailVerificationStats
	for row := range s.queryRO(ctx, q) {
		if err := row.Scan(
			&stats.Total,
			&stats.Expired,
		); err != nil {
			return nil, err
		}
		return &stats, nil
	}

	panic("unreachable")
}

func makeID(kind string) string {
	if kind == "" {
		panic("kind required")
	}
	return fmt.Sprintf("%s_%s", kind, rand.Text()[:8])
}
