package storage

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPathStoreReplaceReleasesKeyLock(t *testing.T) {
	p := NewPathStore(t.TempDir()).(*pathstore)

	if _, err := p.Replace(context.Background(), "repo/pkg.bin", strings.NewReader("data"), func(int64) error {
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	p.locksMu.Lock()
	defer p.locksMu.Unlock()
	if len(p.locks) != 0 {
		t.Fatalf("key lock registry retained %d entries after replacement", len(p.locks))
	}
}

func TestPathStoreReplaceHonorsCancellationWhileWaitingForKey(t *testing.T) {
	p := NewPathStore(t.TempDir()).(*pathstore)
	unlock := p.lockKey("repo/pkg.bin")
	ctx, cancel := context.WithCancel(context.Background())
	var committed atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := p.Replace(ctx, "repo/pkg.bin", strings.NewReader("data"), func(int64) error {
			committed.Store(true)
			return nil
		})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		p.locksMu.Lock()
		refs := p.locks["repo/pkg.bin"].refs
		p.locksMu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			unlock()
			t.Fatal("Replace did not begin waiting for the key lock")
		}
		runtime.Gosched()
	}
	cancel()
	unlock()

	if err := <-done; err == nil {
		t.Fatal("Replace succeeded after its context was canceled")
	}
	if committed.Load() {
		t.Fatal("commit callback ran after cancellation")
	}
}
