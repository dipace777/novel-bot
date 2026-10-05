package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only tests use an in-memory repository; production always uses PostgreSQL.
type memoryRepository struct {
	mu       sync.Mutex
	clients  map[string]Client
	keys     map[string]Key
	users    map[string]User
	sessions map[string]Session
	err      error
}

func (m *memoryRepository) CreateClient(_ context.Context, c Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.clients[c.ID] = c
	return m.err
}
func (m *memoryRepository) CreateKey(_ context.Context, k Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.clients[k.ClientID]; !ok {
		return ErrNotFound
	}
	m.keys[k.ID] = k
	return nil
}
func (m *memoryRepository) FindKey(_ context.Context, id string) (Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return Key{}, m.err
	}
	k, ok := m.keys[id]
	if !ok {
		return Key{}, ErrNotFound
	}
	return k, nil
}
func (m *memoryRepository) ListKeys(_ context.Context, clientID string) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]Key, 0)
	for _, k := range m.keys {
		if k.ClientID == clientID {
			keys = append(keys, k)
		}
	}
	return keys, m.err
}
func (m *memoryRepository) RevokeKey(_ context.Context, clientID, id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	k, ok := m.keys[id]
	if !ok || k.ClientID != clientID {
		return ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = &now
	}
	m.keys[id] = k
	return m.err
}

func testService(t *testing.T) (*Service, *memoryRepository, Client) {
	t.Helper()
	repo := &memoryRepository{clients: make(map[string]Client), keys: make(map[string]Key)}
	s, err := NewService(repo, []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	client, err := s.CreateClient(context.Background(), "test app")
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, client
}

func TestKeyLifecycle(t *testing.T) {
	s, repo, client := testService(t)
	ctx := context.Background()
	issued, err := s.Issue(ctx, client.ID, "production", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.Key.Digest) != 32 || strings.Contains(string(issued.Key.Digest), issued.APIKey) {
		t.Fatal("key must store only a digest")
	}
	encoded, err := json.Marshal(issued.Key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "digest") || strings.Contains(string(encoded), issued.APIKey) {
		t.Fatal("key metadata leaked credentials")
	}
	p, err := s.Authenticate(ctx, issued.APIKey)
	if err != nil || p.ClientID != client.ID || p.KeyID != issued.Key.ID {
		t.Fatalf("authenticate: %+v, %v", p, err)
	}
	keys, err := s.List(ctx, client.ID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list: %+v, %v", keys, err)
	}
	if err := s.Revoke(ctx, client.ID, issued.Key.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, client.ID, issued.Key.ID); err != nil {
		t.Fatal("revocation must be idempotent:", err)
	}
	if _, err := s.Authenticate(ctx, issued.APIKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked key accepted: %v", err)
	}
	if repo.keys[issued.Key.ID].RevokedAt == nil {
		t.Fatal("revocation was not persisted")
	}
}

func TestExpiredAndTamperedKeys(t *testing.T) {
	s, _, client := testService(t)
	ctx := context.Background()
	issued, err := s.Issue(ctx, client.ID, "key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Issue(ctx, client.ID, "other", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongSecret, _ := strings.Cut(other.APIKey, ".")
	for _, token := range []string{"", "garbage", issued.APIKey + "x", keyPrefix + issued.Key.ID + "." + wrongSecret, keyPrefix + strings.Repeat("a", 32) + "." + wrongSecret} {
		if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("invalid token accepted: %v", err)
		}
	}
	s.now = func() time.Time { return issued.Key.ExpiresAt }
	if _, err := s.Authenticate(ctx, issued.APIKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("key accepted at exact expiry: %v", err)
	}
}

func TestPepperAndStorageFailures(t *testing.T) {
	s, repo, client := testService(t)
	ctx := context.Background()
	if _, err := NewService(repo, []byte("short")); err == nil {
		t.Fatal("short pepper accepted")
	}
	issued, err := s.Issue(ctx, client.ID, "key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewService(repo, []byte(strings.Repeat("q", 32)))
	if err != nil {
		t.Fatal(err)
	}
	other.now = s.now
	if _, err := other.Authenticate(ctx, issued.APIKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("wrong pepper accepted")
	}
	repo.err = errors.New("storage unavailable")
	if _, err := s.Authenticate(ctx, issued.APIKey); !errors.Is(err, repo.err) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("storage error was hidden: %v", err)
	}
	if result, err := s.Issue(ctx, client.ID, "key", time.Hour); err == nil || result.APIKey != "" {
		t.Fatal("issuance failure returned a secret")
	}
}

func TestValidationAndTenantIdentity(t *testing.T) {
	s, _, client := testService(t)
	ctx := context.Background()
	for _, name := range []string{"", "  ", strings.Repeat("x", 101)} {
		if _, err := s.CreateClient(ctx, name); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad name accepted: %v", err)
		}
	}
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := s.Issue(ctx, client.ID, "key", ttl); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad TTL accepted: %v", err)
		}
	}
	if _, err := s.Issue(ctx, "bad", "key", time.Hour); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid client ID accepted")
	}
	if _, err := s.Issue(ctx, strings.Repeat("a", 32), "key", time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown client accepted")
	}
	other, err := s.CreateClient(ctx, "second app")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Client{client, other} {
		key, err := s.Issue(ctx, c.ID, "key", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.Authenticate(ctx, key.APIKey)
		if err != nil || p.ClientID != c.ID {
			t.Fatalf("wrong tenant identity: %+v, %v", p, err)
		}
	}
	keys, err := s.List(ctx, other.ID)
	if err != nil || len(keys) != 1 || keys[0].ClientID != other.ID {
		t.Fatalf("list crossed tenant boundaries: %+v, %v", keys, err)
	}
}

func TestConcurrentAuthentication(t *testing.T) {
	s, _, client := testService(t)
	issued, err := s.Issue(context.Background(), client.ID, "key", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 1000 {
		wg.Go(func() {
			p, err := s.Authenticate(context.Background(), issued.APIKey)
			if err != nil || p.ClientID != client.ID {
				t.Errorf("authenticate: %+v, %v", p, err)
			}
		})
	}
	wg.Wait()
}
