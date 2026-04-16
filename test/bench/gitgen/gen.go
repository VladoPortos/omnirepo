//go:build bench || generator

// Command gitgen produces a deterministic bare Git repository from a fixed
// PRNG seed for the TEST-07 memory benchmark. The output is byte-identical
// across runs on the same Go version + platform when the seed is unchanged.
//
// Usage:
//
//	go run -tags=generator ./test/bench/gitgen -out /tmp/big.git -seed 42
//
// Determinism guarantees:
//   - All blob content is derived from a seeded math/rand source.
//   - Author/committer name, email, and timestamps are fixed per seed.
//   - The git CLI is invoked with GIT_COMMITTER_DATE and GIT_AUTHOR_DATE
//     environment overrides so pack hashing is reproducible.
//   - The final bare repo is created via `git clone --bare` from a
//     temporary non-bare repo so pack layout is canonical.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	seedFlag := flag.Int64("seed", 42, "PRNG seed for deterministic blob content")
	outFlag := flag.String("out", "", "output bare-repo path (required)")
	commitsFlag := flag.Int("commits", 800, "number of commits to generate")
	flag.Parse()

	if *outFlag == "" {
		fmt.Fprintln(os.Stderr, "error: -out is required")
		os.Exit(1)
	}

	if err := generate(*outFlag, *seedFlag, *commitsFlag); err != nil {
		fmt.Fprintf(os.Stderr, "gitgen: %v\n", err)
		os.Exit(1)
	}
}

func generate(outPath string, seed int64, commits int) error {
	rng := rand.New(rand.NewSource(seed))
	baseTime := time.Unix(seed, 0).UTC()

	// Work in a temp directory; create a non-bare repo, commit N times,
	// then clone --bare to outPath.
	tmpDir, err := os.MkdirTemp("", "gitgen-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	workRepo := filepath.Join(tmpDir, "work")
	if err := os.MkdirAll(workRepo, 0o755); err != nil {
		return err
	}

	// Init repo with deterministic config.
	if err := gitCmd(workRepo, nil, "init", "-b", "main"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := gitCmd(workRepo, nil, "config", "user.name", "Bench Generator"); err != nil {
		return err
	}
	if err := gitCmd(workRepo, nil, "config", "user.email", "bench@example.com"); err != nil {
		return err
	}
	// Disable GPG signing if globally enabled.
	if err := gitCmd(workRepo, nil, "config", "commit.gpgsign", "false"); err != nil {
		return err
	}

	// Each commit adds 1-5 blobs of 200-400 KB each.
	// 800 commits * avg 3 blobs * avg 300 KB = ~720 MB uncompressed.
	// Git packing compresses PRNG output ~3-4x, yielding ~180-220 MB packed.
	for i := 0; i < commits; i++ {
		commitTime := baseTime.Add(time.Duration(i) * time.Second)
		dateStr := commitTime.Format(time.RFC3339)

		nBlobs := 1 + rng.Intn(5)
		for j := 0; j < nBlobs; j++ {
			// Deterministic file path: dir<i%10>/file_<i>_<j>.bin
			dir := fmt.Sprintf("dir%d", i%10)
			fname := fmt.Sprintf("file_%04d_%d.bin", i, j)
			fpath := filepath.Join(workRepo, dir, fname)
			if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
				return err
			}
			// 200-400 KB of PRNG bytes.
			size := 200*1024 + rng.Intn(200*1024)
			blob := make([]byte, size)
			rng.Read(blob)
			if err := os.WriteFile(fpath, blob, 0o644); err != nil {
				return err
			}
		}

		env := []string{
			"GIT_COMMITTER_DATE=" + dateStr,
			"GIT_AUTHOR_DATE=" + dateStr,
			"GIT_COMMITTER_NAME=Bench Generator",
			"GIT_COMMITTER_EMAIL=bench@example.com",
			"GIT_AUTHOR_NAME=Bench Generator",
			"GIT_AUTHOR_EMAIL=bench@example.com",
		}
		if err := gitCmd(workRepo, env, "add", "-A"); err != nil {
			return fmt.Errorf("git add (commit %d): %w", i, err)
		}
		msg := fmt.Sprintf("commit %d", i)
		if err := gitCmd(workRepo, env, "commit", "-m", msg, "--no-gpg-sign"); err != nil {
			return fmt.Errorf("git commit %d: %w", i, err)
		}

		if (i+1)%100 == 0 {
			fmt.Fprintf(os.Stderr, "gitgen: %d/%d commits\n", i+1, commits)
		}
	}

	// Repack aggressively so the pack layout is canonical.
	if err := gitCmd(workRepo, nil, "gc", "--aggressive"); err != nil {
		return fmt.Errorf("git gc: %w", err)
	}

	// Remove output path if it exists, then clone --bare.
	_ = os.RemoveAll(outPath)
	if err := gitCmd(tmpDir, nil, "clone", "--bare", workRepo, outPath); err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}

	fmt.Fprintf(os.Stderr, "gitgen: bare repo written to %s\n", outPath)
	return nil
}

// gitCmd runs git with the given args in the specified directory.
// Extra env vars are appended to the current process environment.
func gitCmd(dir string, extraEnv []string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd.Run()
}
