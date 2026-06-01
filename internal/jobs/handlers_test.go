package jobs_test

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/jobs"
)

// TestHandlersTypeIsMap asserts the public shape stays map[string]Handler
// so downstream plans can use literal syntax. This is a compile-time
// guard more than a runtime one.
func TestHandlersTypeIsMap(t *testing.T) {
	h := jobs.Handlers{
		"k1": func(ctx context.Context, j *jobs.JobView) error { return nil },
	}
	if _, ok := h["k1"]; !ok {
		t.Fatal("Handlers map access failed")
	}
	if _, ok := h["missing"]; ok {
		t.Fatal("missing key should not be ok")
	}
}
