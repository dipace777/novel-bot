// testsuite runs required tests against automatically provisioned dependencies.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"novel-bot/internal/testutil"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	suite := flag.String("suite", "integration", "integration or e2e")
	external := flag.Bool("external", false, "use explicitly supplied TEST_DATABASE_URL and TEST_REDIS_URL (integration only)")
	flag.Parse()
	if flag.NArg() != 0 || (*suite != "integration" && *suite != "e2e") || (*external && *suite != "integration") {
		return errors.New("invalid suite/options")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return errors.New("run from the repository root")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dir := filepath.Join(root, "test-results", *suite)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Remove stale logs for this suite only, before dependency startup can fail.
	if err := os.RemoveAll(filepath.Join(dir, "logs")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "tests.jsonl")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	redactor := &testutil.Infrastructure{}
	dbURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if *suite == "integration" {
		if *external {
			if os.Getenv("CI") != "" {
				return errors.New("CI must use isolated containers")
			}
			if dbURL == "" || redisURL == "" {
				return errors.New("external mode requires both test URLs")
			}
			redactor.AddSecret(dbURL)
			redactor.AddSecret(redisURL)
			for _, raw := range []string{dbURL, redisURL} {
				if parsed, err := url.Parse(raw); err == nil && parsed.User != nil {
					if password, ok := parsed.User.Password(); ok {
						redactor.AddSecret(password)
					}
				}
			}
		} else {
			redactor, err = testutil.Start(ctx, filepath.Join(dir, "logs"))
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, redactor.Close()) }()
			dbURL, redisURL = redactor.DatabaseURL, redactor.RedisURL
		}
	}
	required, err := requiredTests(root, *suite)
	if err != nil {
		return err
	}
	if len(required) == 0 {
		return errors.New("no required tests discovered")
	}
	report, err := os.OpenFile(filepath.Join(dir, "tests.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer report.Close()
	packages := map[string]bool{}
	names := []string{}
	for key := range required {
		parts := strings.Split(key, "::")
		packages[parts[0]] = true
		names = append(names, regexp.QuoteMeta(parts[1]))
	}
	sort.Strings(names)
	args := []string{"test", "-race", "-count=1", "-json", "-timeout=10m", "-run", "^(" + strings.Join(names, "|") + ")$"}
	if *suite == "e2e" {
		args = append(args, "-tags=e2e")
	}
	pkgs := []string{}
	for pkg := range packages {
		pkgs = append(pkgs, "./"+strings.TrimPrefix(pkg, "novel-bot/"))
	}
	sort.Strings(pkgs)
	args = append(args, pkgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	configureTestProcess(cmd)
	settings := map[string]string{"TEST_DATABASE_URL": dbURL, "TEST_REDIS_URL": redisURL, "TEST_CHROMIUM_PATH": "", "TEST_API_BINARY": "", "TEST_WORKER_BINARY": "", "TEST_LOADTEST_BINARY": "", "TEST_REPO_ROOT": root, "TEST_ARTIFACT_DIR": dir}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if _, ok := settings[key]; !ok {
			cmd.Env = append(cmd.Env, v)
		}
	}
	for k, v := range settings {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	evidenceErr := collect(stdout, report, os.Stdout, required, redactor.Redact)
	err = errors.Join(cmd.Wait(), evidenceErr)
	if err != nil && *suite == "integration" && !*external {
		err = errors.Join(err, redactor.SaveLogs(filepath.Join(dir, "logs")))
	}
	return err
}

// Discover required cases from source so newly added cases cannot silently skip,
// and each declared case must produce passing evidence in this run.
func requiredTests(root, suite string) (map[string]bool, error) {
	patterns := []string{"internal/storage/postgres/*integration_test.go", "internal/storage/redis/*integration_test.go", "internal/httpapi/cluster_integration_test.go"}
	if suite == "e2e" {
		patterns = []string{"tests/e2e/*_test.go"}
	}
	result := map[string]bool{}
	for _, pattern := range patterns {
		files, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(root, filepath.Dir(file))
			if err != nil {
				return nil, err
			}
			for _, decl := range parsed.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				// Native-browser tests remain available via the documented explicit
				// environment flow; image-based e2e covers this behavior in CI.
				if suite == "integration" && fn.Name.Name == "TestRedisChromiumSessionAcrossReplicas" {
					continue
				}
				result["novel-bot/"+filepath.ToSlash(rel)+"::"+fn.Name.Name] = true
			}
		}
	}
	return result, nil
}

type event struct{ Action, Package, Test, Output string }

func collect(reader io.Reader, report, output io.Writer, required map[string]bool, redact func(string) string) error {
	passed := map[string]bool{}
	var errs []error
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			errs = append(errs, fmt.Errorf("invalid test evidence: %w", err))
			continue
		}
		if _, err := fmt.Fprintln(report, redact(line)); err != nil {
			errs = append(errs, err)
		}
		if e.Output != "" {
			fmt.Fprint(output, redact(e.Output))
		}
		key := e.Package + "::" + e.Test
		if e.Action == "skip" {
			errs = append(errs, fmt.Errorf("required suite skipped %s", key))
		}
		if e.Action == "pass" && required[key] {
			passed[key] = true
		}
	}
	if err := scanner.Err(); err != nil {
		errs = append(errs, err)
		// Drain the pipe so a malformed/oversized event cannot deadlock cmd.Wait.
		_, _ = io.Copy(io.Discard, reader)
	}
	for key := range required {
		if !passed[key] {
			errs = append(errs, fmt.Errorf("missing passing evidence for %s", key))
		}
	}
	return errors.Join(errs...)
}
