package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrConflict    = errors.New("email already registered")
	ErrCredentials = errors.New("invalid email or password")
)

const sessionPrefix = "nb_session_"

type User struct {
	ID           string    `json:"id"`
	ClientID     string    `json:"client_id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	PasswordHash []byte    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        string
	UserID    string
	Digest    []byte
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type Login struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        User      `json:"user"`
}

type AccountPrincipal struct {
	User      User   `json:"user"`
	SessionID string `json:"-"`
}

type AccountRepository interface {
	// CreateAccount persists the user and its client in a single transaction.
	CreateAccount(context.Context, User, Client) error
	FindUserByEmail(context.Context, string) (User, error)
	CreateSession(context.Context, Session) error
	FindSession(context.Context, string) (Session, User, error)
	RevokeSession(context.Context, string, string, time.Time) error
}

type AccountService struct {
	repo          AccountRepository
	pepper        []byte
	ttl           time.Duration
	dummyHash     []byte
	passwordSlots chan struct{}
	now           func() time.Time
}

func NewAccountService(repo AccountRepository, pepper []byte, ttl time.Duration) (*AccountService, error) {
	if len(pepper) < 32 || ttl <= 0 {
		return nil, errors.New("account authentication requires a pepper of at least 32 bytes and positive session TTL")
	}
	dummy := make([]byte, 32)
	if _, err := rand.Read(dummy); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword(dummy, bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &AccountService{repo: repo, pepper: append([]byte(nil), pepper...), ttl: ttl, dummyHash: hash, passwordSlots: make(chan struct{}, 4), now: time.Now}, nil
}

func (s *AccountService) Register(ctx context.Context, name, email, password string) (User, error) {
	name = strings.TrimSpace(name)
	email, ok := normalizeEmail(email)
	if !validName(name) || !ok || !utf8.ValidString(password) || len(password) < 12 || len(password) > 72 {
		return User{}, fmt.Errorf("%w: provide a name of 1 to 100 characters, a valid email, and a password of 12 to 72 bytes", ErrInvalidInput)
	}
	if err := s.acquirePasswordSlot(ctx); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	s.releasePasswordSlot()
	if err != nil {
		return User{}, err
	}
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	userID, err := randomID()
	if err != nil {
		return User{}, err
	}
	clientID, err := randomID()
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	user := User{ID: userID, ClientID: clientID, Name: name, Email: email, PasswordHash: hash, CreatedAt: now}
	client := Client{ID: clientID, Name: name, CreatedAt: now}
	if err := s.repo.CreateAccount(ctx, user, client); err != nil {
		return User{}, err
	}
	// Do not return the hash even to callers of the service.
	user.PasswordHash = nil
	return user, nil
}

func (s *AccountService) Login(ctx context.Context, email, password string) (Login, error) {
	email, ok := normalizeEmail(email)
	if !ok || len(password) == 0 || len(password) > 72 {
		return Login{}, ErrCredentials
	}
	user, err := s.repo.FindUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Login{}, err
	}
	found := err == nil
	hash := s.dummyHash
	if found {
		hash = user.PasswordHash
	}
	if err := s.acquirePasswordSlot(ctx); err != nil {
		return Login{}, err
	}
	compareErr := bcrypt.CompareHashAndPassword(hash, []byte(password))
	s.releasePasswordSlot()
	if err := ctx.Err(); err != nil {
		return Login{}, err
	}
	if !found || compareErr != nil {
		return Login{}, ErrCredentials
	}
	id, err := randomID()
	if err != nil {
		return Login{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Login{}, err
	}
	token := sessionPrefix + id + "." + base64.RawURLEncoding.EncodeToString(secret)
	now := s.now().UTC()
	session := Session{ID: id, UserID: user.ID, Digest: s.digest(token), CreatedAt: now, ExpiresAt: now.Add(s.ttl)}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return Login{}, err
	}
	user.PasswordHash = nil
	return Login{AccessToken: token, TokenType: "Bearer", ExpiresAt: session.ExpiresAt, User: user}, nil
}

func (s *AccountService) AuthenticateSession(ctx context.Context, token string) (AccountPrincipal, error) {
	// Token formats have separate prefixes; API keys cannot act as login sessions.
	id, ok := parseTokenWithPrefix(token, sessionPrefix)
	if !ok {
		return AccountPrincipal{}, ErrUnauthorized
	}
	session, user, err := s.repo.FindSession(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return AccountPrincipal{}, ErrUnauthorized
	}
	if err != nil {
		return AccountPrincipal{}, err
	}
	match := subtle.ConstantTimeCompare(session.Digest, s.digest(token)) == 1
	if !match || session.RevokedAt != nil || !s.now().Before(session.ExpiresAt) {
		return AccountPrincipal{}, ErrUnauthorized
	}
	user.PasswordHash = nil
	return AccountPrincipal{User: user, SessionID: session.ID}, nil
}

func (s *AccountService) Logout(ctx context.Context, principal AccountPrincipal) error {
	if !validID(principal.User.ID) || !validID(principal.SessionID) {
		return ErrUnauthorized
	}
	return s.repo.RevokeSession(ctx, principal.User.ID, principal.SessionID, s.now().UTC())
}

func (s *AccountService) digest(token string) []byte {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte(token))
	return mac.Sum(nil)
}

func (s *AccountService) acquirePasswordSlot(ctx context.Context) error {
	select {
	case s.passwordSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *AccountService) releasePasswordSlot() { <-s.passwordSlots }

func normalizeEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 254 || !utf8.ValidString(value) {
		return "", false
	}
	address, err := mail.ParseAddress(value)
	return value, err == nil && address.Name == "" && address.Address == value && strings.Contains(value, "@")
}
