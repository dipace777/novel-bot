// Package testutil provisions isolated test infrastructure. Production code must
// not import it. Containers are shared by a suite; tests still isolate their data.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	docker "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	tc "github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Keep these manifest digests aligned with compose.yaml.
const PostgresImage = "postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"
const RedisImage = "redis:8-alpine@sha256:3811787313eba226a2ef38658c6ccb91cd5e110edc89c37767de373120a0e5a0"

type NamedContainer struct {
	Name      string
	Container tc.Container
}

type Infrastructure struct {
	Network                                                      *tc.DockerNetwork
	Containers                                                   []NamedContainer
	DatabaseURL, RedisURL, InternalDatabaseURL, InternalRedisURL string
	mu                                                           sync.Mutex
	secrets                                                      []string
}

func Secret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (s *Infrastructure) AddSecret(secret string) {
	if secret == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = append(s.secrets, secret)
}

func (s *Infrastructure) Redact(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}

func Start(ctx context.Context, logsDir string) (_ *Infrastructure, err error) {
	// Reject an unavailable explicit daemon configuration before Testcontainers
	// can silently fall back to another local socket.
	if os.Getenv("DOCKER_HOST") != "" {
		cli, err := client.New(client.FromEnv)
		if err != nil {
			return nil, fmt.Errorf("configure Docker client: %w", err)
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = cli.Ping(probe, client.PingOptions{})
		cancel()
		cli.Close()
		if err != nil {
			return nil, fmt.Errorf("configured DOCKER_HOST is unavailable: %w", err)
		}
	}
	s := &Infrastructure{}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.SaveLogs(logsDir))
			err = errors.Join(err, s.Close())
		}
	}()
	s.Network, err = tcnetwork.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create isolated test network: %w", err)
	}
	password := Secret()
	s.AddSecret(password)
	p, err := s.Run(ctx, "postgres", tc.ContainerRequest{
		Image: PostgresImage, Env: map[string]string{"POSTGRES_USER": "novelbot", "POSTGRES_PASSWORD": password, "POSTGRES_DB": "novelbot"},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor:   wait.ForAll(wait.ForLog("database system is ready to accept connections").WithOccurrence(2), wait.ForListeningPort("5432/tcp")).WithDeadline(90 * time.Second),
	})
	if err != nil {
		return nil, err
	}
	r, err := s.Run(ctx, "redis", tc.ContainerRequest{
		Image: RedisImage, Cmd: []string{"redis-server", "--maxmemory-policy", "noeviction"}, ExposedPorts: []string{"6379/tcp"},
		WaitingFor: wait.ForAll(wait.ForLog("Ready to accept connections"), wait.ForListeningPort("6379/tcp")).WithDeadline(60 * time.Second),
	})
	if err != nil {
		return nil, err
	}
	dbEndpoint, err := p.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return nil, err
	}
	redisEndpoint, err := r.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		return nil, err
	}
	s.DatabaseURL = "postgres://novelbot:" + password + "@" + dbEndpoint + "/novelbot?sslmode=disable"
	s.RedisURL = "redis://" + redisEndpoint + "/0"
	s.InternalDatabaseURL = "postgres://novelbot:" + password + "@postgres:5432/novelbot?sslmode=disable"
	s.InternalRedisURL = "redis://redis:6379/0"
	return s, nil
}

func (s *Infrastructure) Run(ctx context.Context, name string, req tc.ContainerRequest) (tc.Container, error) {
	req.Networks = []string{s.Network.Name}
	req.NetworkAliases = map[string][]string{s.Network.Name: {name}}
	modifier := req.HostConfigModifier
	req.HostConfigModifier = func(h *docker.HostConfig) {
		if modifier != nil {
			modifier(h)
		}
		// Test endpoints bind only to loopback and receive dynamically mapped ports.
		h.PortBindings = network.PortMap{}
		for _, port := range req.ExposedPorts {
			h.PortBindings[network.MustParsePort(port)] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1")}}
		}
	}
	c, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{ContainerRequest: req, Started: true})
	if c != nil {
		s.Containers = append(s.Containers, NamedContainer{name, c})
	}
	if err != nil {
		return nil, fmt.Errorf("start %s container: %s", name, s.Redact(err.Error()))
	}
	return c, nil
}

func (s *Infrastructure) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var errs []error
	for i := len(s.Containers) - 1; i >= 0; i-- {
		if err := s.Containers[i].Container.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %s", s.Containers[i].Name, s.Redact(err.Error())))
		}
	}
	if s.Network != nil {
		errs = append(errs, s.Network.Remove(ctx))
	}
	return errors.Join(errs...)
}

// SaveLogs never inspects container environments, and redacts suite credentials.
func (s *Infrastructure) SaveLogs(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var errs []error
	for _, c := range s.Containers {
		logs, err := c.Container.Logs(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(logs, 8<<20))
		logs.Close()
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, c.Name+".log"), []byte(s.Redact(string(data))), 0600)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// WorkerHostConfig uses the same sandbox/resource constraints as Compose.
func WorkerHostConfig(profile []byte) func(*docker.HostConfig) {
	return func(h *docker.HostConfig) {
		init := true
		pids := int64(2048)
		h.Init = &init
		h.CapDrop = []string{"ALL"}
		h.SecurityOpt = []string{"no-new-privileges:true", "seccomp=" + string(profile)}
		h.ShmSize = 512 << 20
		h.Resources = docker.Resources{Memory: 4 << 30, MemorySwap: 4 << 30, NanoCPUs: 4e9, PidsLimit: &pids}
	}
}

func Address(ctx context.Context, c tc.Container, port string) (string, error) {
	host, err := c.Host(ctx)
	if err != nil {
		return "", err
	}
	p, err := c.MappedPort(ctx, port)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort(host, p.Port()), nil
}
