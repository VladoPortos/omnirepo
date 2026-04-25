package config_test

// Plan 02-04 Task 2 — cfg.S3.MultipartRetention defaults + validation.
//
// The boot-time orphan-multipart sweep + the on-demand admin sweep endpoint
// (`POST /api/v1/admin/maintenance/sweep-multipart`) read this knob to
// compute the cutoff. Default 24h matches AWS's S3 multipart-upload age
// guideline and the legacy hardcoded 24h sweeper used pre-Plan 02-04.

import (
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/config"
)

func TestS3MultipartRetention_DefaultsTo24h(t *testing.T) {
	d := config.Defaults()
	if d.S3.MultipartRetention != 24*time.Hour {
		t.Errorf("S3.MultipartRetention = %s, want 24h", d.S3.MultipartRetention)
	}
}

func TestS3MultipartRetention_EnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_S3__MULTIPART_RETENTION", "12h")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.S3.MultipartRetention != 12*time.Hour {
		t.Errorf("S3.MultipartRetention = %s, want 12h (env override)", cfg.S3.MultipartRetention)
	}
}

func TestS3MultipartRetention_RejectsNegative(t *testing.T) {
	cfg := config.Defaults()
	cfg.S3.MultipartRetention = -time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate(): want error for negative retention, got nil")
	} else if !strings.Contains(err.Error(), "multipart_retention") {
		t.Errorf("error %q does not mention multipart_retention", err.Error())
	}
}
