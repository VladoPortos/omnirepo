package scan_test

import (
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/scan"
)

func TestSeverityCache_GetSetInvalidate(t *testing.T) {
	c := scan.NewSeverityCache(time.Minute)
	if _, ok := c.Get(1, "docker", "sha256:abc"); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Set(1, "docker", "sha256:abc", scan.CacheEntry{
		Blocked: true, Severity: "critical", CVECount: 3, ScanID: 42,
	})
	got, ok := c.Get(1, "docker", "sha256:abc")
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if !got.Blocked || got.Severity != "critical" || got.CVECount != 3 || got.ScanID != 42 {
		t.Fatalf("entry round-trip wrong: %+v", got)
	}
	c.Invalidate(1, "docker", "sha256:abc")
	if _, ok := c.Get(1, "docker", "sha256:abc"); ok {
		t.Fatal("expected miss after Invalidate")
	}
}

func TestSeverityCache_TTLExpiry(t *testing.T) {
	c := scan.NewSeverityCache(20 * time.Millisecond)
	c.Set(7, "raw", "/foo", scan.CacheEntry{Blocked: false})
	if _, ok := c.Get(7, "raw", "/foo"); !ok {
		t.Fatal("immediate Get failed")
	}
	time.Sleep(35 * time.Millisecond)
	if _, ok := c.Get(7, "raw", "/foo"); ok {
		t.Fatal("expected miss after TTL")
	}
}

func TestSeverityCache_KeyDistinguishesArtifacts(t *testing.T) {
	c := scan.NewSeverityCache(time.Minute)
	c.Set(1, "docker", "sha256:a", scan.CacheEntry{Blocked: true, Severity: "high"})
	c.Set(1, "docker", "sha256:b", scan.CacheEntry{Blocked: false})
	c.Set(2, "docker", "sha256:a", scan.CacheEntry{Blocked: false})
	c.Set(1, "raw", "sha256:a", scan.CacheEntry{Blocked: true, Severity: "low"})
	if e, ok := c.Get(1, "docker", "sha256:a"); !ok || e.Severity != "high" {
		t.Fatalf("key 1/docker/a wrong: %+v ok=%v", e, ok)
	}
	if e, ok := c.Get(1, "docker", "sha256:b"); !ok || e.Blocked {
		t.Fatalf("key 1/docker/b wrong: %+v ok=%v", e, ok)
	}
	if e, ok := c.Get(2, "docker", "sha256:a"); !ok || e.Blocked {
		t.Fatalf("key 2/docker/a wrong: %+v ok=%v", e, ok)
	}
	if e, ok := c.Get(1, "raw", "sha256:a"); !ok || e.Severity != "low" {
		t.Fatalf("key 1/raw/a wrong: %+v ok=%v", e, ok)
	}
}

