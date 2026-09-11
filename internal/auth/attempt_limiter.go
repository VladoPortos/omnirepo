package auth

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	defaultAttemptBurst         = 10
	defaultAttemptRefill        = 6 * time.Second
	defaultAttemptMaxPeers      = 4096
	defaultAttemptMaxConcurrent = 4
)

// AttemptLimiterConfig configures password-authentication pressure controls.
// One token is restored per RefillInterval, up to Burst.
type AttemptLimiterConfig struct {
	Burst          int
	RefillInterval time.Duration
	MaxPeers       int
	MaxConcurrent  int
	Clock          func() time.Time
}

type attemptPeer struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

// AttemptLimiter bounds both password attempts per socket peer and total
// concurrent expensive password operations. It is safe for concurrent use.
type AttemptLimiter struct {
	mu             sync.Mutex
	burst          int
	refillInterval time.Duration
	maxPeers       int
	clock          func() time.Time
	peers          map[string]*attemptPeer
	slots          chan struct{}
}

// AttemptPermit owns one global password-work slot. Release is idempotent so
// middleware safety defers and authentication code may both release it.
type AttemptPermit struct {
	once    sync.Once
	release func()
}

func (p *AttemptPermit) Release() {
	if p == nil {
		return
	}
	p.once.Do(p.release)
}

func NewAttemptLimiter(cfg AttemptLimiterConfig) *AttemptLimiter {
	if cfg.Burst <= 0 {
		cfg.Burst = defaultAttemptBurst
	}
	if cfg.RefillInterval <= 0 {
		cfg.RefillInterval = defaultAttemptRefill
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = defaultAttemptMaxPeers
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultAttemptMaxConcurrent
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &AttemptLimiter{
		burst:          cfg.Burst,
		refillInterval: cfg.RefillInterval,
		maxPeers:       cfg.MaxPeers,
		clock:          cfg.Clock,
		peers:          make(map[string]*attemptPeer),
		slots:          make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Acquire consumes one peer token and reserves one global work slot. The
// returned retry duration is positive when the attempt is rejected.
func (l *AttemptLimiter) Acquire(peer string) (*AttemptPermit, time.Duration, bool) {
	if l == nil {
		return &AttemptPermit{release: func() {}}, 0, true
	}
	now := l.clock().UTC()
	key := normalizeAttemptPeer(peer)

	l.mu.Lock()
	entry := l.peers[key]
	if entry == nil {
		if len(l.peers) >= l.maxPeers {
			l.evictOldestLocked()
		}
		entry = &attemptPeer{tokens: float64(l.burst), updated: now}
		l.peers[key] = entry
	}
	elapsed := now.Sub(entry.updated)
	if elapsed > 0 {
		entry.tokens += float64(elapsed) / float64(l.refillInterval)
		if entry.tokens > float64(l.burst) {
			entry.tokens = float64(l.burst)
		}
		entry.updated = now
	}
	entry.lastSeen = now
	if entry.tokens < 1 {
		missing := 1 - entry.tokens
		retry := time.Duration(missing * float64(l.refillInterval))
		if retry < time.Second {
			retry = time.Second
		}
		l.mu.Unlock()
		return nil, retry, false
	}
	entry.tokens--
	l.mu.Unlock()

	select {
	case l.slots <- struct{}{}:
		return &AttemptPermit{release: func() { <-l.slots }}, 0, true
	default:
		return nil, time.Second, false
	}
}

func (l *AttemptLimiter) PeerCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.peers)
}

func (l *AttemptLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.peers {
		if oldestKey == "" || entry.lastSeen.Before(oldest) {
			oldestKey, oldest = key, entry.lastSeen
		}
	}
	if oldestKey != "" {
		delete(l.peers, oldestKey)
	}
}

func normalizeAttemptPeer(peer string) string {
	peer = strings.TrimSpace(peer)
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	peer = strings.Trim(peer, "[]")
	if peer == "" {
		return "unknown"
	}
	return strings.ToLower(peer)
}
