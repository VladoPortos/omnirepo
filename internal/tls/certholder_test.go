package tls

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func freshPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	certPEM, keyPEM, err := GenerateSelfSigned([]string{"h"}, time.Hour, 2048)
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	return certPEM, keyPEM
}

func TestCertHolderEmptyGet(t *testing.T) {
	h := NewCertHolder()
	_, err := h.Get(nil)
	if err == nil {
		t.Fatalf("expected error on empty holder")
	}
	if !strings.Contains(err.Error(), "no certificate loaded") {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.Loaded() {
		t.Fatalf("Loaded() should be false before Swap")
	}
}

func TestCertHolderSwapAndGet(t *testing.T) {
	certPEM, keyPEM := freshPair(t)
	h := NewCertHolder()
	if err := h.Swap(certPEM, keyPEM); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	got, err := h.Get(nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Leaf == nil {
		t.Fatalf("nil cert returned")
	}
}

func TestCertHolderPublicKeyMismatch(t *testing.T) {
	certPEM, _ := freshPair(t)
	_, otherKey := freshPair(t)
	h := NewCertHolder()
	err := h.Swap(certPEM, otherKey)
	if err == nil {
		t.Fatalf("expected public key mismatch error")
	}
	// tls.X509KeyPair reports something containing "public key" or "mismatch".
	msg := err.Error()
	if !(strings.Contains(msg, "public key") || strings.Contains(msg, "mismatch")) {
		t.Fatalf("unexpected error shape: %v", err)
	}
}

func TestCertHolderConcurrentSwapAndGet(t *testing.T) {
	certPEM, keyPEM := freshPair(t)
	h := NewCertHolder()
	if err := h.Swap(certPEM, keyPEM); err != nil {
		t.Fatalf("Swap: %v", err)
	}

	certPEM2, keyPEM2 := freshPair(t)

	var wg sync.WaitGroup
	iterations := 1000
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c, err := h.Get(nil)
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			if c == nil {
				t.Errorf("nil cert under load")
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			var err error
			if i%2 == 0 {
				err = h.Swap(certPEM, keyPEM)
			} else {
				err = h.Swap(certPEM2, keyPEM2)
			}
			if err != nil {
				t.Errorf("Swap: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
