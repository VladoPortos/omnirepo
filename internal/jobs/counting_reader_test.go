package jobs_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/jobs"
)

// zeroThenDataReader returns (0, nil) on the first call, then (n, io.EOF)
// with the payload bytes on the second. This models a slow-path HTTP body
// that sometimes emits a zero-byte "keepalive" read before delivering
// payload.
type zeroThenDataReader struct {
	data      []byte
	delivered bool
	yielded   bool
}

func (z *zeroThenDataReader) Read(p []byte) (int, error) {
	if !z.yielded {
		z.yielded = true
		return 0, nil
	}
	if z.delivered {
		return 0, io.EOF
	}
	z.delivered = true
	n := copy(p, z.data)
	return n, io.EOF
}

// errReader returns (n, err) on a single read. Used to assert OnRead fires
// when a partial read is paired with a non-nil error.
type errReader struct {
	n   int
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	if e.n > len(p) {
		e.n = len(p)
	}
	for i := 0; i < e.n; i++ {
		p[i] = 'x'
	}
	return e.n, e.err
}

func TestCountingReader_SkipsZeroByteReads(t *testing.T) {
	src := &zeroThenDataReader{data: []byte("hello")}
	var calls []int
	cr := &jobs.CountingReader{R: src, OnRead: func(n int) { calls = append(calls, n) }}
	buf := make([]byte, 16)
	for {
		n, err := cr.Read(buf)
		_ = n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected read err: %v", err)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("OnRead calls = %d (%v); want exactly 1 (the 5-byte payload)", len(calls), calls)
	}
	if calls[0] != 5 {
		t.Fatalf("OnRead[0] = %d; want 5", calls[0])
	}
}

func TestCountingReader_ForwardsAllBytes(t *testing.T) {
	payload := strings.Repeat("a", 200)
	src := bytes.NewBufferString(payload)
	var total int
	cr := &jobs.CountingReader{R: src, OnRead: func(n int) { total += n }}
	n, err := io.Copy(io.Discard, cr)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 200 {
		t.Fatalf("copy returned %d bytes; want 200", n)
	}
	if total != 200 {
		t.Fatalf("OnRead total = %d; want 200", total)
	}
}

func TestCountingReader_ForwardsError(t *testing.T) {
	customErr := errors.New("boom")
	src := &errReader{n: 3, err: customErr}
	var calls []int
	cr := &jobs.CountingReader{R: src, OnRead: func(n int) { calls = append(calls, n) }}
	buf := make([]byte, 16)
	n, err := cr.Read(buf)
	if n != 3 {
		t.Fatalf("n = %d; want 3", n)
	}
	if !errors.Is(err, customErr) {
		t.Fatalf("err = %v; want %v", err, customErr)
	}
	if len(calls) != 1 || calls[0] != 3 {
		t.Fatalf("OnRead calls = %v; want [3] (fired once with n=3 alongside error)", calls)
	}
}

func TestCountingReader_NilCallbackDoesNotPanic(t *testing.T) {
	src := bytes.NewBufferString("payload")
	cr := &jobs.CountingReader{R: src} // OnRead nil
	n, _ := io.Copy(io.Discard, cr)
	if n != 7 {
		t.Fatalf("n = %d; want 7", n)
	}
}
