package testutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsRedactAllRegisteredCredentials(t *testing.T) {
	s := &Infrastructure{}
	s.AddSecret("database-password")
	s.AddSecret("private-token")
	s.AddSecret("")
	result := s.Redact("postgres://user:database-password@postgres; Bearer private-token")
	if strings.Contains(result, "database-password") || strings.Contains(result, "private-token") || !strings.Contains(result, "postgres://user:") {
		t.Fatalf("credential redaction failed: %s", result)
	}
}

func TestConfiguredUnavailableDockerFailsWithoutFallback(t *testing.T) {
	// macOS's default test temp path can exceed the Unix socket path limit.
	dir, err := os.MkdirTemp("/tmp", "nb-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(dir, "missing.sock"))
	_, err = Start(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "DOCKER_HOST is unavailable") {
		t.Fatalf("unavailable explicit Docker host accepted: %v", err)
	}
}
