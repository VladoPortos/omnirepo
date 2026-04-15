package scan

import (
	"context"
	"errors"
	"sync"
)

// ErrNothingQueued is returned by a FakeRunner when a test pops a result but
// none was queued. Tests should always Queue before invoking.
var ErrNothingQueued = errors.New("fake runner: nothing queued")

type fakeReply struct {
	res Result
	err error
}

// FakeRunner is a queue-backed Runner test double (D-21). Tests enqueue
// canned results with QueueImage / QueueFilesystem / QueueSBOM; each call
// to Image / Filesystem / SBOM pops the next entry.
type FakeRunner struct {
	mu    sync.Mutex
	image []fakeReply
	fs    []fakeReply
	sbom  []error
}

// NewFakeRunner returns an empty FakeRunner.
func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

// QueueImage enqueues a reply for the next Image call.
func (f *FakeRunner) QueueImage(r Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.image = append(f.image, fakeReply{r, err})
}

// QueueFilesystem enqueues a reply for the next Filesystem call.
func (f *FakeRunner) QueueFilesystem(r Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fs = append(f.fs, fakeReply{r, err})
}

// QueueSBOM enqueues a reply for the next SBOM call.
func (f *FakeRunner) QueueSBOM(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sbom = append(f.sbom, err)
}

// Image pops the next queued Image reply.
func (f *FakeRunner) Image(_ context.Context, _ string) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.image) == 0 {
		return Result{}, ErrNothingQueued
	}
	r := f.image[0]
	f.image = f.image[1:]
	return r.res, r.err
}

// Filesystem pops the next queued Filesystem reply.
func (f *FakeRunner) Filesystem(_ context.Context, _ string) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fs) == 0 {
		return Result{}, ErrNothingQueued
	}
	r := f.fs[0]
	f.fs = f.fs[1:]
	return r.res, r.err
}

// SBOM pops the next queued SBOM reply.
func (f *FakeRunner) SBOM(_ context.Context, _ string, _ SBOMFormat, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sbom) == 0 {
		return ErrNothingQueued
	}
	err := f.sbom[0]
	f.sbom = f.sbom[1:]
	return err
}
