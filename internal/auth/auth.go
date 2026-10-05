// Package auth implements application identity and the API key lifecycle.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrUnauthorized = errors.New("invalid authentication credential")
	ErrNotFound     = errors.New("not found")
	ErrInvalidInput = errors.New("invalid input")
)

const keyPrefix = "nb_key_"

type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Key struct {
	ID        string     `json:"id"`
	ClientID  string     `json:"client_id"`
	Name      string     `json:"name"`
	Digest    []byte     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type IssuedKey struct {
	Key    Key    `json:"key"`
	APIKey string `json:"api_key"`
}

// Principal is the tenant identity used to authorize future session operations.
type Principal struct {
	ClientID string `json:"client_id"`
	KeyID    string `json:"key_id"`
}

// Repository implementations must be safe for concurrent use.
type Repository interface {
	CreateClient(context.Context, Client) error
	CreateKey(context.Context, Key) error
	FindKey(context.Context, string) (Key, error)
	ListKeys(context.Context, string) ([]Key, error)
	RevokeKey(context.Context, string, string, time.Time) error
}

type Service struct {
	repo   Repository
	pepper []byte
	now    func() time.Time
}

func NewService(repo Repository, pepper []byte) (*Service, error) {
	if len(pepper) < 32 {
		return nil, fmt.Errorf("API key pepper must contain at least 32 bytes")
	}
	return &Service{repo: repo, pepper: append([]byte(nil), pepper...), now: time.Now}, nil
}

func (s *Service) CreateClient(ctx context.Context, name string) (Client, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return Client{}, fmt.Errorf("%w: client name must contain 1 to 100 characters", ErrInvalidInput)
	}
	id, err := randomID()
	if err != nil {
		return Client{}, err
	}
	client := Client{ID: id, Name: name, CreatedAt: s.now().UTC()}
	if err := s.repo.CreateClient(ctx, client); err != nil {
		return Client{}, err
	}
	return client, nil
}

func (s *Service) Issue(ctx context.Context, clientID, name string, ttl time.Duration) (IssuedKey, error) {
	name = strings.TrimSpace(name)
	if !validID(clientID) || !validName(name) || ttl <= 0 {
		return IssuedKey{}, fmt.Errorf("%w: provide a client ID, a name of 1 to 100 characters, and a positive TTL", ErrInvalidInput)
	}
	id, err := randomID()
	if err != nil {
		return IssuedKey{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return IssuedKey{}, err
	}
	token := keyPrefix + id + "." + base64.RawURLEncoding.EncodeToString(secret)
	now := s.now().UTC()
	key := Key{ID: id, ClientID: clientID, Name: name, Digest: s.digest(token), CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.repo.CreateKey(ctx, key); err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{Key: key, APIKey: token}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	id, ok := parseToken(token)
	if !ok {
		return Principal{}, ErrUnauthorized
	}
	key, err := s.repo.FindKey(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, fmt.Errorf("find API key: %w", err)
	}
	match := subtle.ConstantTimeCompare(key.Digest, s.digest(token)) == 1
	if !match || key.RevokedAt != nil || !s.now().Before(key.ExpiresAt) {
		return Principal{}, ErrUnauthorized
	}
	return Principal{ClientID: key.ClientID, KeyID: key.ID}, nil
}

func (s *Service) List(ctx context.Context, clientID string) ([]Key, error) {
	if !validID(clientID) {
		return nil, fmt.Errorf("%w: invalid client ID", ErrInvalidInput)
	}
	return s.repo.ListKeys(ctx, clientID)
}

func (s *Service) Revoke(ctx context.Context, clientID, id string) error {
	if !validID(clientID) || !validID(id) {
		return fmt.Errorf("%w: invalid client or key ID", ErrInvalidInput)
	}
	return s.repo.RevokeKey(ctx, clientID, id, s.now().UTC())
}

func (s *Service) digest(token string) []byte {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write([]byte(token))
	return mac.Sum(nil)
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) >= 1 && utf8.RuneCountInString(name) <= 100
}

func validID(id string) bool {
	if len(id) != 32 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func parseToken(token string) (string, bool) {
	return parseTokenWithPrefix(token, keyPrefix)
}

func parseTokenWithPrefix(token, prefix string) (string, bool) {
	if len(token) != len(prefix)+32+1+43 || !strings.HasPrefix(token, prefix) {
		return "", false
	}
	id, secret, ok := strings.Cut(strings.TrimPrefix(token, prefix), ".")
	if !ok || !validID(id) {
		return "", false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	return id, err == nil && len(b) == 32
}
