package storage_test

import (
	"sync"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestLocksSamePointerForSameKey(t *testing.T) {
	l := storage.NewLocks()
	k := storage.RepoKey{Project: "acme", Type: "docker", Repo: "x"}
	m1 := l.For(k)
	m2 := l.For(k)
	if m1 != m2 {
		t.Fatalf("expected same *sync.Mutex pointer, got %p vs %p", m1, m2)
	}
}

func TestLocksDifferentPointersForDifferentKeys(t *testing.T) {
	l := storage.NewLocks()
	a := l.For(storage.RepoKey{Project: "acme", Type: "docker", Repo: "x"})
	b := l.For(storage.RepoKey{Project: "acme", Type: "rpm", Repo: "x"})
	if a == b {
		t.Fatalf("expected different pointers, got %p for both", a)
	}
}

// TestLocksSameKeySerialize asserts two goroutines contending on the same key
// take roughly 2× as long as two goroutines on different keys (serialization
// signal). Uses 60ms critical sections so CI noise doesn't fail us.
func TestLocksSameKeySerialize(t *testing.T) {
	l := storage.NewLocks()
	crit := 60 * time.Millisecond

	run := func(keys [2]storage.RepoKey) time.Duration {
		var wg sync.WaitGroup
		start := time.Now()
		for i := 0; i < 2; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				mu := l.For(keys[i])
				mu.Lock()
				time.Sleep(crit)
				mu.Unlock()
			}()
		}
		wg.Wait()
		return time.Since(start)
	}

	sameKey := storage.RepoKey{Project: "p", Type: "docker", Repo: "r"}
	sameDur := run([2]storage.RepoKey{sameKey, sameKey})
	diffDur := run([2]storage.RepoKey{
		{Project: "p", Type: "docker", Repo: "r1"},
		{Project: "p", Type: "docker", Repo: "r2"},
	})

	// sameDur should be ≥ 1.5 × diffDur (ample slack for scheduling jitter).
	if sameDur < diffDur*3/2 {
		t.Fatalf("same-key did not serialize: sameDur=%v diffDur=%v", sameDur, diffDur)
	}
}
