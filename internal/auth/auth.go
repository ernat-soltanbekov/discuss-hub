// Package auth handles identity and expiring, single-device sessions.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"github.com/gofrs/uuid/v5"
	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

const SessionLifetime = 24 * time.Hour

var (
	ErrCredentials   = errors.New("email or password is incorrect")
	ErrEmailTaken    = errors.New("this email is already registered")
	ErrUsernameTaken = errors.New("this username is already taken")
	ErrInvalid       = errors.New("use a valid email, a 3–32 character username (letters, numbers, _, -, .), and an 8–72 byte password")
	usernamePattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)
)

type User struct{ ID, Email, Username string }
type Service struct {
	store     *db.Store
	cost      int
	dummyHash []byte
}

func New(store *db.Store, cost int) (*Service, error) {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, fmt.Errorf("invalid bcrypt cost")
	}
	// Unknown emails still perform bcrypt work; they do not expose a cheap
	// timing path that is absent for registered accounts.
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy-password-never-an-account"), cost)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, cost: cost, dummyHash: hash}, nil
}

func validEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email && len(email) <= 254 && !strings.ContainsAny(email, "\r\n\x00")
}

func (s *Service) Register(ctx context.Context, email, username, password string) (User, error) {
	email, username = strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(username)
	if !validEmail(email) || !usernamePattern.MatchString(username) || len(password) < 8 || len(password) > 72 || strings.TrimSpace(password) == "" {
		return User{}, ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return User{}, err
	}
	id, err := uuid.NewV4()
	if err != nil {
		return User{}, err
	}
	user := User{ID: id.String(), Email: email, Username: username}
	_, err = s.store.SQL.ExecContext(ctx, "INSERT INTO users(id,email,username,password_hash,created_at) VALUES(?,?,?,?,?)", user.ID, email, username, string(hash), time.Now().Unix())
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			if strings.Contains(sqliteErr.Error(), "users.email") {
				return User{}, ErrEmailTaken
			}
			if strings.Contains(sqliteErr.Error(), "users.username") {
				return User{}, ErrUsernameTaken
			}
		}
		return User{}, err
	}
	return user, nil
}

func (s *Service) Login(ctx context.Context, email, password string) (string, time.Time, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > 254 || len(password) == 0 || len(password) > 72 {
		return "", time.Time{}, ErrCredentials
	}
	var userID, hash string
	err := s.store.SQL.QueryRowContext(ctx, "SELECT id,password_hash FROM users WHERE email=?", email).Scan(&userID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return "", time.Time{}, ErrCredentials
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return "", time.Time{}, ErrCredentials
		}
		return "", time.Time{}, fmt.Errorf("stored password hash: %w", err)
	}
	id, err := uuid.NewV4()
	if err != nil {
		return "", time.Time{}, err
	}
	token := id.String()
	expiry := time.Now().Add(SessionLifetime).UTC().Truncate(time.Second)
	// user_id is the primary key: simultaneous logins still leave ONE session.
	_, err = s.store.SQL.ExecContext(ctx, `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES(?,?,?)
        ON CONFLICT(user_id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at`, userID, TokenHash(token), expiry.Unix())
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiry, nil
}

// TokenHash prevents a database read from yielding ready-to-use session cookies.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) User(ctx context.Context, token string) (*User, error) {
	parsed, err := uuid.FromString(token)
	if err != nil || parsed.Version() != 4 {
		return nil, nil
	}
	var user User
	err = s.store.SQL.QueryRowContext(ctx, `SELECT u.id,u.email,u.username FROM users u
        JOIN sessions s ON s.user_id=u.id WHERE s.token_hash=? AND s.expires_at>?`, TokenHash(token), time.Now().Unix()).Scan(&user.ID, &user.Email, &user.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.store.SQL.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", TokenHash(token))
	return err
}

func (s *Service) Prune(ctx context.Context) error {
	_, err := s.store.SQL.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=?", time.Now().Unix())
	return err
}
