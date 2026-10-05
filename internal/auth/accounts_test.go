package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (m *memoryRepository) CreateAccount(_ context.Context, user User, client Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.users[user.Email]; ok {
		return ErrConflict
	}
	m.users[user.Email] = user
	m.clients[client.ID] = client
	return nil
}
func (m *memoryRepository) FindUserByEmail(_ context.Context, email string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return User{}, m.err
	}
	user, ok := m.users[email]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}
func (m *memoryRepository) CreateSession(_ context.Context, session Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sessions[session.ID] = session
	return nil
}
func (m *memoryRepository) FindSession(_ context.Context, id string) (Session, User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return Session{}, User{}, m.err
	}
	session, ok := m.sessions[id]
	if !ok {
		return Session{}, User{}, ErrNotFound
	}
	for _, user := range m.users {
		if user.ID == session.UserID {
			return session, user, nil
		}
	}
	return Session{}, User{}, ErrNotFound
}
func (m *memoryRepository) RevokeSession(_ context.Context, userID, id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	session, ok := m.sessions[id]
	if !ok || session.UserID != userID {
		return ErrNotFound
	}
	if session.RevokedAt == nil {
		session.RevokedAt = &now
	}
	m.sessions[id] = session
	return nil
}

func testAccounts(t *testing.T) (*AccountService, *memoryRepository) {
	t.Helper()
	repo := &memoryRepository{clients: map[string]Client{}, keys: map[string]Key{}, users: map[string]User{}, sessions: map[string]Session{}}
	s, err := NewAccountService(repo, []byte(strings.Repeat("p", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) }
	return s, repo
}

func TestAccountLifecycle(t *testing.T) {
	s, repo := testAccounts(t)
	ctx := context.Background()
	const password = "correct horse battery"
	user, err := s.Register(ctx, " My App ", " Owner@Example.com ", password)
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "owner@example.com" || user.Name != "My App" || user.PasswordHash != nil {
		t.Fatalf("invalid returned user: %+v", user)
	}
	stored := repo.users[user.Email]
	if err := bcrypt.CompareHashAndPassword(stored.PasswordHash, []byte(password)); err != nil {
		t.Fatal("password hash invalid:", err)
	}
	if _, ok := repo.clients[user.ClientID]; !ok {
		t.Fatal("registration did not create a client")
	}
	login, err := s.Login(ctx, "OWNER@example.com", password)
	if err != nil {
		t.Fatal(err)
	}
	if login.TokenType != "Bearer" || login.User.ID != user.ID || login.User.PasswordHash != nil {
		t.Fatal("invalid login response")
	}
	encoded, err := json.Marshal(login)
	if err != nil || strings.Contains(string(encoded), "password") {
		t.Fatal("login leaked password data")
	}
	p, err := s.AuthenticateSession(ctx, login.AccessToken)
	if err != nil || p.User.ID != user.ID || p.User.PasswordHash != nil {
		t.Fatalf("session auth: %+v %v", p, err)
	}
	if len(repo.sessions[p.SessionID].Digest) != 32 {
		t.Fatal("session token not hashed")
	}
	keys, err := NewService(repo, s.pepper)
	if err != nil {
		t.Fatal(err)
	}
	keys.now = s.now
	issued, err := keys.Issue(ctx, p.User.ClientID, "application key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(ctx, issued.APIKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("API key accepted as session")
	}
	if _, err := keys.Authenticate(ctx, login.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("session accepted as API key")
	}
	if err := s.Logout(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, p); err != nil {
		t.Fatal("logout should be idempotent at the service layer:", err)
	}
	if _, err := s.AuthenticateSession(ctx, login.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logged out session accepted")
	}
	if _, err := keys.Authenticate(ctx, issued.APIKey); err != nil {
		t.Fatal("logout should leave API keys usable:", err)
	}
	login, err = s.Login(ctx, user.Email, password)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return login.ExpiresAt }
	if _, err := s.AuthenticateSession(ctx, login.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("session accepted at expiry")
	}
}

func TestAccountValidationAndCredentials(t *testing.T) {
	s, repo := testAccounts(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, email, password string }{
		{"", "a@example.com", "long enough password"},
		{"App", "invalid", "long enough password"},
		{"App", "Name <a@example.com>", "long enough password"},
		{"App", "a@example.com", "short"},
		{"App", "a@example.com", strings.Repeat("x", 73)},
	} {
		if _, err := s.Register(ctx, tc.name, tc.email, tc.password); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid registration: %v", err)
		}
	}
	if len(repo.clients) != 0 || len(repo.users) != 0 {
		t.Fatal("invalid registrations persisted")
	}
	if _, err := s.Register(ctx, "App", "a@example.com", "long enough password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, "Other", "A@example.com", "another valid password"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate email: %v", err)
	}
	if len(repo.clients) != 1 {
		t.Fatal("duplicate registration created an orphan client")
	}
	for _, tc := range []struct{ email, password string }{
		{"a@example.com", "wrong password"}, {"unknown@example.com", "long enough password"},
		{"invalid", "long enough password"}, {"a@example.com", strings.Repeat("x", 73)},
	} {
		if _, err := s.Login(ctx, tc.email, tc.password); !errors.Is(err, ErrCredentials) {
			t.Fatalf("credentials: %v", err)
		}
	}
	if len(repo.sessions) != 0 {
		t.Fatal("failed login created a session")
	}
}

func TestSessionTamperingAndStorageFailure(t *testing.T) {
	s, repo := testAccounts(t)
	ctx := context.Background()
	user, err := s.Register(ctx, "App", "a@example.com", "long enough password")
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.Login(ctx, user.Email, "long enough password")
	if err != nil {
		t.Fatal(err)
	}
	id, ok := parseTokenWithPrefix(login.AccessToken, sessionPrefix)
	if !ok {
		t.Fatal("session token format invalid")
	}
	for _, token := range []string{"", "garbage", login.AccessToken + "x", sessionPrefix + id + "." + strings.Repeat("A", 43), sessionPrefix + strings.Repeat("f", 32) + "." + strings.Repeat("A", 43)} {
		if _, err := s.AuthenticateSession(ctx, token); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("tampered session accepted: %v", err)
		}
	}
	originalPepper := append([]byte(nil), s.pepper...)
	s.pepper[0] ^= 1
	if _, err := s.AuthenticateSession(ctx, login.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("wrong pepper accepted")
	}
	s.pepper = originalPepper
	repo.err = errors.New("storage unavailable")
	if _, err := s.AuthenticateSession(ctx, login.AccessToken); !errors.Is(err, repo.err) {
		t.Fatalf("storage error hidden: %v", err)
	}
	if result, err := s.Login(ctx, user.Email, "long enough password"); !errors.Is(err, repo.err) || result.AccessToken != "" {
		t.Fatal("failed login leaked token")
	}
}

func TestKeyRevocationOwnership(t *testing.T) {
	s, _, owner := testService(t)
	ctx := context.Background()
	other, err := s.CreateClient(ctx, "Other")
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.Issue(ctx, owner.ID, "key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, other.ID, key.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant revoked key: %v", err)
	}
	if _, err := s.Authenticate(ctx, key.APIKey); err != nil {
		t.Fatal("key changed after unauthorized revoke:", err)
	}
}
