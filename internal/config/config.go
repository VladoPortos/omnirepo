// Package config loads OmniRepo configuration from YAML + environment.
//
// Search order (D-05):
//  1. flagPath (non-empty) — must exist, error if missing
//  2. $OMNIREPO_CONFIG — silently ignored if missing
//  3. /var/lib/omnirepo/config/omnirepo.yaml — silently ignored if missing
//  4. built-in defaults (D-07)
//
// Environment overrides take precedence over YAML. Env var syntax:
//
//	OMNIREPO_SERVER__HTTP_PORT=9000 -> server.http_port=9000
//
// (double underscore is the nesting separator; prefix is stripped; keys lowercased)
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	envprov "github.com/knadh/koanf/providers/env/v2"
	fileprov "github.com/knadh/koanf/providers/file"
	structsprov "github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// Config is the top-level OmniRepo configuration schema (D-06).
// All struct-tag keys match the YAML layout exactly.
type Config struct {
	Server    ServerConfig    `koanf:"server"`
	DataRoot  string          `koanf:"data_root"`
	TLS       TLSConfig       `koanf:"tls"`
	Bootstrap BootstrapConfig `koanf:"bootstrap"`
	Auth      AuthConfig      `koanf:"auth"`
	Scan      ScanConfig      `koanf:"scan"`
	Trivy     Trivy           `koanf:"trivy"`
	Jobs      Jobs            `koanf:"jobs"`
	AirGap    AirGapConfig    `koanf:"air_gap"`
	Log       LogConfig       `koanf:"log"`
}

// Trivy holds subprocess driver paths (D-44). Runtime air-gap is enforced in
// internal/scan; these are just file locations.
type Trivy struct {
	BinaryPath string `koanf:"binary_path"`
	DBPath     string `koanf:"db_path"`
	CachePath  string `koanf:"cache_path"`
}

// Jobs tunes the two-pool job runner (D-14..D-20, D-44). SyncWorkers /
// ScanWorkers are the worker-goroutine counts; PollInterval is the
// dispatcher's re-poll cadence (kick-channel short-circuits it);
// ShutdownGraceSeconds is the drain deadline on SIGTERM.
type Jobs struct {
	SyncWorkers          int           `koanf:"sync_workers"`
	ScanWorkers          int           `koanf:"scan_workers"`
	PollInterval         time.Duration `koanf:"poll_interval"`
	ShutdownGraceSeconds int           `koanf:"shutdown_grace_seconds"`
}

type ServerConfig struct {
	HTTPPort          int      `koanf:"http_port"`
	HTTPSPort         int      `koanf:"https_port"`
	Hostname          string   `koanf:"hostname"`
	ExternalHostnames []string `koanf:"external_hostnames"`
}

type TLSConfig struct {
	CertPath string `koanf:"cert_path"`
	KeyPath  string `koanf:"key_path"`
}

type BootstrapConfig struct {
	Path string `koanf:"path"`
}

type AuthConfig struct {
	SessionTTL   time.Duration `koanf:"session_ttl"`
	DockerJWTTTL time.Duration `koanf:"docker_jwt_ttl"`
	SigV4Skew    time.Duration `koanf:"sigv4_skew"`
}

type ScanConfig struct {
	AutoScanDefault bool `koanf:"auto_scan_default"`
	DBWarnAgeDays   int  `koanf:"db_warn_age_days"`
}

type AirGapConfig struct {
	AllowExternalActions bool `koanf:"allow_external_actions"`
}

type LogConfig struct {
	Level           string `koanf:"level"`
	Format          string `koanf:"format"`
	AuditMaxSizeMiB int    `koanf:"audit_max_size_mb"`
	AuditKeep       int    `koanf:"audit_keep"`
}

// defaultConfigPath is the well-known location OmniRepo reads config from when
// no flag or env override is set. Exposed as a var so tests can override.
var defaultConfigPath = "/var/lib/omnirepo/config/omnirepo.yaml"

// Defaults returns the D-07 default configuration.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			HTTPPort:          8080,
			HTTPSPort:         8443,
			ExternalHostnames: []string{},
		},
		DataRoot: "/var/lib/omnirepo",
		Auth: AuthConfig{
			SessionTTL:   12 * time.Hour,
			DockerJWTTTL: 60 * time.Minute,
			SigV4Skew:    15 * time.Minute,
		},
		Scan: ScanConfig{
			AutoScanDefault: true,
			DBWarnAgeDays:   7,
		},
		Trivy: Trivy{
			BinaryPath: "/usr/local/bin/trivy",
			DBPath:     "/var/lib/omnirepo/trivy/db",
			CachePath:  "/var/lib/omnirepo/trivy/cache",
		},
		Jobs: Jobs{
			SyncWorkers:          4,
			ScanWorkers:          2,
			PollInterval:         2 * time.Second,
			ShutdownGraceSeconds: 30,
		},
		AirGap: AirGapConfig{
			AllowExternalActions: true,
		},
		Log: LogConfig{
			Level:           "info",
			Format:          "json",
			AuditMaxSizeMiB: 100,
			AuditKeep:       10,
		},
	}
}

// Load resolves configuration following the D-05 search order.
// Precedence (lowest → highest): defaults → YAML file (if found) → env vars.
func Load(flagPath string) (Config, error) {
	k := koanf.New(".")

	// 1. Seed with defaults.
	if err := k.Load(structsprov.Provider(Defaults(), "koanf"), nil); err != nil {
		return Config{}, fmt.Errorf("config: load defaults: %w", err)
	}

	// 2. Resolve file path.
	path, mustExist, err := resolvePath(flagPath)
	if err != nil {
		return Config{}, err
	}
	if path != "" {
		if _, statErr := os.Stat(path); statErr != nil {
			if mustExist {
				return Config{}, fmt.Errorf("config: cannot read %q: %w", path, statErr)
			}
			// env or default path missing → silent skip
		} else {
			if err := k.Load(fileprov.Provider(path), yaml.Parser()); err != nil {
				return Config{}, fmt.Errorf("config: parse %q: %w", path, err)
			}
		}
	}

	// 3. Env overrides. OMNIREPO_SERVER__HTTP_PORT -> server.http_port
	envOpt := envprov.Opt{
		Prefix: "OMNIREPO_",
		TransformFunc: func(key, value string) (string, any) {
			// Skip the reserved OMNIREPO_CONFIG var (used for file path, not config keys).
			if key == "OMNIREPO_CONFIG" {
				return "", nil
			}
			k := strings.TrimPrefix(key, "OMNIREPO_")
			k = strings.ReplaceAll(k, "__", ".")
			k = strings.ToLower(k)
			return k, value
		},
	}
	if err := k.Load(envprov.Provider(".", envOpt), nil); err != nil {
		return Config{}, fmt.Errorf("config: load env: %w", err)
	}

	// 4. Unmarshal into Config. koanf honours `time.Duration` via mapstructure's DecodeHook.
	var cfg Config
	unmarshalConf := koanf.UnmarshalConf{
		Tag:           "koanf",
		DecoderConfig: newDecoderConfig(&cfg),
	}
	if err := k.UnmarshalWithConf("", &cfg, unmarshalConf); err != nil {
		return Config{}, fmt.Errorf("config: unmarshal: %w", err)
	}
	return cfg, nil
}

// resolvePath returns the chosen config file path and whether its absence is a
// fatal error (only the flagPath case is fatal when missing).
func resolvePath(flagPath string) (path string, mustExist bool, err error) {
	if flagPath != "" {
		return flagPath, true, nil
	}
	if env := os.Getenv("OMNIREPO_CONFIG"); env != "" {
		return env, false, nil
	}
	return defaultConfigPath, false, nil
}

// ErrInvalidConfig is returned when configuration fails structural validation.
// (Reserved for future use; the loader currently surfaces koanf/YAML errors directly.)
var ErrInvalidConfig = errors.New("config: invalid")
