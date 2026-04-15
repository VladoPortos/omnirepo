package storage

import "sync"

// RepoKey identifies one repository-scoped lock group. Two repos with the
// same project + type + repo triple share a mutex; any differing field
// yields an independent mutex.
type RepoKey struct {
	Project string
	Type    string
	Repo    string
}

// Locks is the per-repo mutex map (D-32). Returned mutexes are cached so
// two For(k) calls with equal k return the same *sync.Mutex pointer — that
// pointer-equality is the observable contract.
type Locks interface {
	For(k RepoKey) *sync.Mutex
}

type repoLocks struct {
	m sync.Map // RepoKey -> *sync.Mutex
}

// NewLocks returns a fresh Locks instance. Safe for concurrent use.
func NewLocks() Locks { return &repoLocks{} }

func (l *repoLocks) For(k RepoKey) *sync.Mutex {
	if v, ok := l.m.Load(k); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := l.m.LoadOrStore(k, mu)
	return actual.(*sync.Mutex)
}
