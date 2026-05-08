//go:build bench

// Package gitbench implements the TEST-07 memory benchmark: a child-process
// omnirepo serving a 200 MB bare Git repo is cloned while VmRSS is sampled
// at 50 ms intervals. The hard gate asserts peak_rss < 3 * repo_bytes.
//
// Both the gogit and gitkit backends are exercised; only gogit is a hard gate
// (D-43). Results are written to .bench/git-results.json (D-44).
package gitbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// benchResult is the JSON artifact shape per D-44.
type benchResult struct {
	Timestamp    string `json:"timestamp"`
	GoVersion    string `json:"go_version"`
	Backend      string `json:"backend"`
	PeakRSSBytes int64  `json:"peak_rss_bytes"`
	RepoBytes    int64  `json:"repo_bytes"`
	Ratio        float64 `json:"ratio"`
	DurationMs   int64  `json:"duration_ms"`
}

func TestGitMemoryBench(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only bench (D-42): /proc/<pid>/status not available")
	}

	// 1. Build omnirepo binary.
	binDir := t.TempDir()
	omnirepoPath := filepath.Join(binDir, "omnirepo")
	t.Log("building omnirepo binary...")
	buildCmd := exec.Command("go", "build", "-mod=vendor", "-o", omnirepoPath, "./cmd/omnirepo")
	buildCmd.Dir = projectRoot(t)
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	// 2. Generate the fixture if not already present.
	fixtureDir := filepath.Join(projectRoot(t), ".bench", "git-fixture")
	bareRepoPath := filepath.Join(fixtureDir, "big.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		t.Log("generating 200 MB fixture (first run)...")
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		genCmd := exec.Command("go", "run", "-tags=generator", "-mod=vendor",
			"./test/bench/gitgen", "-out", bareRepoPath, "-seed", "42")
		genCmd.Dir = projectRoot(t)
		genCmd.Stdout = os.Stderr
		genCmd.Stderr = os.Stderr
		if err := genCmd.Run(); err != nil {
			t.Fatalf("gitgen: %v", err)
		}
	}

	repoBytes := duBytes(t, bareRepoPath)
	t.Logf("fixture repo size: %d bytes (%.1f MB)", repoBytes, float64(repoBytes)/(1024*1024))

	var results []benchResult

	for _, backend := range []string{"gogit", "gitkit"} {
		t.Run(backend, func(t *testing.T) {
			result := runBenchForBackend(t, omnirepoPath, bareRepoPath, repoBytes, backend)
			results = append(results, result)
		})
	}

	// Write JSON artifact.
	writeJSONResults(t, results)
}

func runBenchForBackend(t *testing.T, omnirepoPath, bareRepoPath string, repoBytes int64, backend string) benchResult {
	t.Helper()

	dataRoot := t.TempDir()
	configDir := filepath.Join(dataRoot, "config")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Bootstrap JSON: create a project + git repo pointing at the fixture.
	bootstrapJSON := fmt.Sprintf(`{
		"schema_version": 1,
		"super_admin": {"login":"admin","email":"admin@example.com","password":"bench-password-12345"},
		"users": [],
		"projects": [{"name":"bench","members":[]}],
		"repos": [{"project":"bench","type":"git","name":"big","public_read":true}],
		"api_keys": []
	}`)
	bsPath := filepath.Join(configDir, "bootstrap.json")
	if err := os.WriteFile(bsPath, []byte(bootstrapJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// The fixture bare repo gets symlinked into the data root AFTER
	// bootstrap completes — see step "swap empty bare for fixture" below.
	// Pre-symlinking here would collide with the bootstrap repo-create hook
	// (Phase 3 composedRepoCreateHook calls gitpkg.InitBare → go-git
	// PlainInit → ErrTargetDirNotEmpty when the path is non-empty).
	linkPath := filepath.Join(dataRoot, "repos", "bench", "git", "big.git")

	// Write a minimal config YAML.
	cfgYAML := fmt.Sprintf(`data_root: %s
bootstrap:
  path: %s
server:
  http_port: 0
  https_port: 0
  git_backend: %s
  external_hostnames: ["localhost"]
`, dataRoot, bsPath, backend)
	cfgPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Start omnirepo as a child process.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, omnirepoPath, "serve", "--config", cfgPath)
	cmd.Env = append(os.Environ(),
		"OMNIREPO_SERVER_GIT_BACKEND="+backend,
		"OMNIREPO_DATA_ROOT="+dataRoot,
		"OMNIREPO_BOOTSTRAP_PATH="+bsPath,
	)

	// Capture stderr to find the listen port.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start omnirepo (%s): %v", backend, err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()

	pid := cmd.Process.Pid
	t.Logf("omnirepo pid=%d backend=%s", pid, backend)

	// Read stderr to discover the HTTP port (simpler than HTTPS for git CLI).
	httpPort := discoverPort(t, stderrPipe, 30*time.Second, "http.listen")
	t.Logf("omnirepo HTTP port: %d", httpPort)

	// Wait for the server to be healthy. Bootstrap (and therefore the
	// repo-create hook that InitBares an empty bare at linkPath) runs
	// before the HTTP listeners accept; a 200 from /healthz means we can
	// swap the empty bare for the fixture symlink without racing.
	healthClient := &http.Client{Timeout: 10 * time.Second}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", httpPort)
	waitHealthy(t, healthURL, healthClient, 30*time.Second)

	// Swap the bootstrap-initialised empty bare for a symlink to the
	// 200 MB fixture. The git protocol handler is stateless — it opens
	// the bare at request time via gogit.PlainOpen, so substituting the
	// directory between bootstrap and the first git request is safe.
	// git_refs in the DB still says HEAD → refs/heads/main, but the
	// metadata table is not consulted by the upload-pack flow; clone
	// reads refs straight off disk.
	if err := os.RemoveAll(linkPath); err != nil {
		t.Fatalf("remove bootstrap-initialised bare %q: %v", linkPath, err)
	}
	if err := os.Symlink(bareRepoPath, linkPath); err != nil {
		t.Fatalf("symlink fixture into data root: %v", err)
	}

	// Start RSS sampler.
	samplerCtx, samplerCancel := context.WithCancel(context.Background())
	defer samplerCancel()
	rssCh := StartSampler(samplerCtx, pid, 50*time.Millisecond)

	// Run git clone.
	cloneDst := t.TempDir()
	cloneURL := fmt.Sprintf("http://admin:bench-password-12345@127.0.0.1:%d/git/bench/big.git", httpPort)
	t.Logf("cloning %s -> %s", fmt.Sprintf("http://127.0.0.1:%d/git/bench/big.git", httpPort), cloneDst)

	start := time.Now()
	cloneCmd := exec.Command("git", "clone", cloneURL, filepath.Join(cloneDst, "clone"))
	cloneCmd.Stdout = os.Stderr
	cloneCmd.Stderr = os.Stderr
	if err := cloneCmd.Run(); err != nil {
		t.Fatalf("git clone failed: %v", err)
	}
	duration := time.Since(start)
	t.Logf("clone completed in %s", duration)

	// Stop sampler and collect peak.
	samplerCancel()
	var peak int64
	for s := range rssCh {
		if s.VmRSS > peak {
			peak = s.VmRSS
		}
	}

	ratio := float64(peak) / float64(repoBytes)
	t.Logf("backend=%s peak_rss=%d repo_bytes=%d ratio=%.2f", backend, peak, repoBytes, ratio)

	// Hard gate for gogit (D-41, D-43).
	if backend == "gogit" && ratio >= 3.0 {
		t.Fatalf("TEST-07 HARD GATE FAILED: peak_rss=%d, repo_bytes=%d, ratio=%.2f >= 3.0",
			peak, repoBytes, ratio)
	}

	return benchResult{
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		GoVersion:    runtime.Version(),
		Backend:      backend,
		PeakRSSBytes: peak,
		RepoBytes:    repoBytes,
		Ratio:        ratio,
		DurationMs:   duration.Milliseconds(),
	}
}

// waitHealthy polls the URL until 200 or timeout.
func waitHealthy(t *testing.T, url string, client *http.Client, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not respond 200 within %s", url, timeout)
}

// discoverPort reads stderr output looking for the HTTPS listen address.
// Omnirepo logs something like: "https.listen addr=:PORT" or similar.
func discoverPort(t *testing.T, r io.Reader, timeout time.Duration, marker string) int {
	t.Helper()
	buf := make([]byte, 0, 16384)
	tmp := make([]byte, 1024)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			// Look for port in log output. Common patterns:
			// "https.listen" "addr=:PORT" or "https_addr" "127.0.0.1:PORT"
			lines := strings.Split(string(buf), "\n")
			for _, line := range lines {
				port := extractPort(line, marker)
				if port > 0 {
					// Keep draining in background so the pipe doesn't block.
					go func() {
						for {
							_, err := r.Read(tmp)
							if err != nil {
								return
							}
						}
					}()
					return port
				}
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("could not discover HTTPS port from stderr within %s; output so far: %s",
		timeout, string(buf))
	return 0
}

// extractPort attempts to find a port from a slog text log line matching the marker.
// Expected format: "... msg=<marker> addr=[::]:PORT" or "addr=0.0.0.0:PORT"
// or "addr=:PORT" or "addr=127.0.0.1:PORT".
func extractPort(line string, marker string) int {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, strings.ToLower(marker)) {
		return 0
	}
	// Find "addr=" and extract the port after the last colon.
	idx := strings.Index(line, "addr=")
	if idx < 0 {
		return 0
	}
	addrVal := line[idx+5:]
	// Trim at next space or end.
	if sp := strings.IndexByte(addrVal, ' '); sp >= 0 {
		addrVal = addrVal[:sp]
	}
	// The port is after the last colon.
	lastColon := strings.LastIndex(addrVal, ":")
	if lastColon < 0 {
		return 0
	}
	portStr := addrVal[lastColon+1:]
	port := 0
	for _, c := range portStr {
		if c < '0' || c > '9' {
			return 0
		}
		port = port*10 + int(c-'0')
	}
	if port > 0 && port < 65536 {
		return port
	}
	return 0
}

// duBytes returns the total size in bytes of the directory tree at path,
// matching `du -sb` behavior.
func duBytes(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("du %s: %v", path, err)
	}
	return total
}

// writeJSONResults writes the bench results to .bench/git-results.json.
// If the file already exists, it reads the existing entries and appends,
// keeping the last 20 runs.
func writeJSONResults(t *testing.T, results []benchResult) {
	t.Helper()
	outDir := filepath.Join(projectRoot(t), ".bench")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir .bench: %v", err)
	}
	outPath := filepath.Join(outDir, "git-results.json")

	// Read existing entries.
	var existing []benchResult
	if data, err := os.ReadFile(outPath); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	// Append new results.
	existing = append(existing, results...)

	// Truncate to last 20 entries.
	if len(existing) > 20 {
		existing = existing[len(existing)-20:]
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", outPath, err)
	}
	t.Logf("results written to %s", outPath)
}

// projectRoot returns the repo root by walking up from the test file.
func projectRoot(t *testing.T) string {
	t.Helper()
	// We know the test lives at test/bench/git/ — walk up 3 levels.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// But under `go test`, the working dir is the package dir.
	// Walk up until we find go.mod.
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}
