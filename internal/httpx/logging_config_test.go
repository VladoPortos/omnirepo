package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/config"
)

func TestConfigureLoggingAppliesToApplicationLogs(t *testing.T) {
	oldLogger, oldStderr := slog.Default(), os.Stderr
	defer func() { slog.SetDefault(oldLogger); os.Stderr = oldStderr }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	os.Stderr = w
	var cfg config.Config
	cfg.Log.Level = "warn"
	cfg.Log.Format = "json"
	ConfigureLogging(cfg)
	slog.Info("hidden")
	slog.Warn("application.warning", "detail", "visible")
	_ = w.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hidden") {
		t.Fatal("application ignored configured level")
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("not JSON: %s (%v)", raw, err)
	}
	if row["msg"] != "application.warning" || row["detail"] != "visible" {
		t.Fatalf("record=%v", row)
	}
}
