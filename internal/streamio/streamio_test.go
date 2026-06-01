package streamio_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/streamio"
)

// fakeReader returns the configured chunks in order, then the configured err.
// Used to exercise the io-error pass-through path of ReadAllLimited.
type fakeReader struct {
	chunks [][]byte
	err    error
	idx    int
}

func (f *fakeReader) Read(p []byte) (int, error) {
	if f.idx < len(f.chunks) {
		n := copy(p, f.chunks[f.idx])
		f.idx++
		return n, nil
	}
	if f.err != nil {
		return 0, f.err
	}
	return 0, io.EOF
}

func TestReadAllLimited_UnderLimit(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte(strings.Repeat("a", 100)))
	got, err := streamio.ReadAllLimited(src, 200, streamio.ErrArtifactTooLarge)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("expected 100 bytes, got %d", len(got))
	}
}

func TestReadAllLimited_AtLimit(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte(strings.Repeat("a", 200)))
	got, err := streamio.ReadAllLimited(src, 200, streamio.ErrArtifactTooLarge)
	if err != nil {
		t.Fatalf("expected nil error at exact limit, got %v", err)
	}
	if len(got) != 200 {
		t.Fatalf("expected 200 bytes at exact limit, got %d", len(got))
	}
}

func TestReadAllLimited_OverLimit(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte(strings.Repeat("a", 201)))
	got, err := streamio.ReadAllLimited(src, 200, streamio.ErrArtifactTooLarge)
	if got != nil {
		t.Fatalf("expected nil bytes on over-limit, got %d bytes", len(got))
	}
	if !errors.Is(err, streamio.ErrArtifactTooLarge) {
		t.Fatalf("expected ErrArtifactTooLarge, got %v", err)
	}
}

func TestReadAllLimited_WayOver(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte(strings.Repeat("a", 1000)))
	got, err := streamio.ReadAllLimited(src, 200, streamio.ErrArtifactTooLarge)
	if got != nil {
		t.Fatalf("expected nil bytes on way-over-limit, got %d bytes", len(got))
	}
	if !errors.Is(err, streamio.ErrArtifactTooLarge) {
		t.Fatalf("expected ErrArtifactTooLarge, got %v", err)
	}
}

func TestReadAllLimited_ZeroBytes(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader(nil)
	got, err := streamio.ReadAllLimited(src, 200, streamio.ErrArtifactTooLarge)
	if err != nil {
		t.Fatalf("expected nil error on empty input, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 bytes, got %d", len(got))
	}
}

func TestReadAllLimited_CustomSentinel(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte(strings.Repeat("a", 201)))
	_, err := streamio.ReadAllLimited(src, 200, streamio.ErrMetadataTooLarge)
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge, got %v", err)
	}
	if errors.Is(err, streamio.ErrArtifactTooLarge) {
		t.Fatalf("did not expect ErrArtifactTooLarge, got %v", err)
	}
}

func TestReadAllLimited_IOError(t *testing.T) {
	t.Parallel()
	r := &fakeReader{
		chunks: [][]byte{[]byte("partial")},
		err:    io.ErrUnexpectedEOF,
	}
	got, err := streamio.ReadAllLimited(r, 200, streamio.ErrArtifactTooLarge)
	if got != nil {
		t.Fatalf("expected nil bytes on io error, got %d bytes", len(got))
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected wrapped io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestReadAllLimited_MaxZero(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte("abc"))
	got, err := streamio.ReadAllLimited(src, 0, streamio.ErrArtifactTooLarge)
	if got != nil {
		t.Fatalf("expected nil bytes when max=0, got %d bytes", len(got))
	}
	if err == nil {
		t.Fatal("expected error when max=0, got nil")
	}
}

func TestReadAllLimited_MaxNegative(t *testing.T) {
	t.Parallel()
	src := bytes.NewReader([]byte("abc"))
	got, err := streamio.ReadAllLimited(src, -1, streamio.ErrArtifactTooLarge)
	if got != nil {
		t.Fatalf("expected nil bytes when max<0, got %d bytes", len(got))
	}
	if err == nil {
		t.Fatal("expected error when max=-1, got nil")
	}
}
