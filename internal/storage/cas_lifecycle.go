package storage

import (
	"context"
	"sync"
)

// CASLifecycle serializes a digest's promotion and removal, including the
// metadata transaction that decides whether GC may unlink it. The context is
// passed to nested CAS operations so they share the caller's lock. Callbacks
// must not dispatch work using that context after returning.
func CASLifecycle(ctx context.Context, digest string, fn func(context.Context) error) error {
	ctx, unlock := lockCASLifecycle(ctx, digest)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

func lockCASLifecycle(ctx context.Context, digest string) (context.Context, func()) {
	if held, _ := ctx.Value(casLifecycleKey{}).(string); held == digest {
		return ctx, func() {}
	}
	casLifecycleMu.Lock()
	l := casLifecycleLocks[digest]
	if l == nil {
		l = &casLifecycleLock{}
		casLifecycleLocks[digest] = l
	}
	l.users++
	casLifecycleMu.Unlock()
	l.mu.Lock()
	unlock := func() {
		l.mu.Unlock()
		casLifecycleMu.Lock()
		l.users--
		if l.users == 0 {
			delete(casLifecycleLocks, digest)
		}
		casLifecycleMu.Unlock()
	}
	return context.WithValue(ctx, casLifecycleKey{}, digest), unlock
}

type casLifecycleKey struct{}
type casLifecycleLock struct {
	mu    sync.Mutex
	users int
}

var casLifecycleMu sync.Mutex
var casLifecycleLocks = make(map[string]*casLifecycleLock)
