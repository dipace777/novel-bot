package browser

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Reuse the test executable as a deterministic Chromium-shaped child process.
func TestMain(m *testing.M) {
	if mode := os.Getenv("NOVELBOT_TEST_BROWSER"); mode != "" {
		if mode == "exit" {
			os.Exit(1)
		}
		if mode == "ready" {
			for _, arg := range os.Args[1:] {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					_ = os.WriteFile(strings.TrimPrefix(arg, "--user-data-dir=")+"/DevToolsActivePort", []byte("9222\n/devtools/browser/test\n"), 0600)
				}
			}
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

func TestLauncherStopsProcessAndRemovesProfile(t *testing.T) {
	t.Setenv("NOVELBOT_TEST_BROWSER", "ready")
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	profiles := t.TempDir()
	launcher, err := NewChromium(path, profiles)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	browser, err := launcher.Launch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if browser.Endpoint() != "ws://127.0.0.1:9222/devtools/browser/test" {
		t.Fatal(browser.Endpoint())
	}
	if err := browser.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := browser.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(profiles)
	if err != nil || len(entries) != 0 {
		t.Fatalf("profile leaked: %v, %v", entries, err)
	}
}

func TestFailedAndTimedOutLaunchCleanup(t *testing.T) {
	for _, mode := range []string{"exit", "hang"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("NOVELBOT_TEST_BROWSER", mode)
			path, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			profiles := t.TempDir()
			launcher, err := NewChromium(path, profiles)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if _, err := launcher.Launch(ctx); err == nil {
				t.Fatal("unready process accepted")
			} else if mode == "hang" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(profiles)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed launch profile leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestDebuggingEndpointValidation(t *testing.T) {
	for _, value := range []string{"", "0\n/devtools/browser/test", "65536\n/devtools/browser/test", "1234\nhttp://external", "1234\n/devtools/browser/test?x", "1234\n/devtools/browser/test\nextra"} {
		if _, err := parseEndpoint([]byte(value)); err == nil {
			t.Fatalf("invalid endpoint accepted: %q", value)
		}
	}
}
