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
	Docker    Docker          `koanf:"docker"`
	GC        GC              `koanf:"gc"`
	AirGap    AirGapConfig    `koanf:"air_gap"`
	Log       LogConfig       `koanf:"log"`
	Regen     RegenConfig     `koanf:"regen"`
	Sync      SyncConfig      `koanf:"sync"`
	Signing   SigningConfig   `koanf:"signing"`
	Repos     ReposConfig     `koanf:"repos"`
}

// ReposConfig carries per-protocol repo knobs that are not tied to a single
// repo row in the DB. Phase 4 Plan 03 introduces `repos.git.max_push_bytes`
// as the server-wide fallback used when a row has NULL in
// repos.git_max_push_bytes.
type ReposConfig struct {
	Git GitReposConfig `koanf:"git"`
}

// GitReposConfig is the Phase 4 Plan 03 git repo knob set.
//   - MaxPushBytes: per-install default cap on a single Smart-HTTP push in
//     bytes (D-35). 0 → apply 500 MiB default at load time; negative is a
//     validation error. Per-repo override lives in repos.git_max_push_bytes
//     (migration 017_git_extensions).
type GitReposConfig struct {
	MaxPushBytes int64 `koanf:"max_push_bytes"`
}

// RegenConfig tunes the per-repo metadata regeneration coalescer (D-35,
// Phase 03 Plan 01). DebounceMs is how long Kick() calls are collapsed
// into one regen invocation; MaxWaitMs is the absolute ceiling from the
// first Kick so continuous writes cannot starve the regen goroutine.
type RegenConfig struct {
	DebounceMs int `koanf:"debounce_ms"`
	MaxWaitMs  int `koanf:"max_wait_ms"`
}

// SyncConfig tunes the per-sync-job upstream worker pool (Phase 03 pull-
// external plans). MaxParallelDownloadsPerJob bounds concurrent blob
// fetches inside a single sync_jobs row. UpstreamHTTPTimeout is the
// per-request deadline for any call the sync worker makes to an
// upstream registry.
type SyncConfig struct {
	MaxParallelDownloadsPerJob int           `koanf:"max_parallel_downloads_per_job"`
	UpstreamHTTPTimeout        time.Duration `koanf:"upstream_http_timeout"`
}

// SigningConfig tunes the per-repo OpenPGP signing key generation
// (Phase 03 Plan 01, D-01). GPGKeyBits is the RSA key size handed to
// internal/crypto/pgpsign.GenerateRepoKey. 4096 is the air-gap default
// per D-35; operators can reduce for test installs.
type SigningConfig struct {
	GPGKeyBits int `koanf:"gpg_key_bits"`
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

// GC carries the Phase 02-12 garbage-collection knobs (D-44):
//   - TrashRetentionDays: trash entries older than this are hard-deleted.
//     Default 7.
//   - BlobQuiescenceSeconds: docker_blobs rows with ref_count==0 must have
//     been untouched for at least this long before they're eligible for
//     sweep. Default 3600 (1 hour) — covers the longest realistic chunked
//     upload window per D-03.
type GC struct {
	TrashRetentionDays    int `koanf:"trash_retention_days"`
	BlobQuiescenceSeconds int `koanf:"blob_quiescence_seconds"`
}

// Docker carries the Phase 02-05 OCI/Docker knobs:
//   - JWTTTLSeconds: /v2/token Bearer lifetime (D-06). Default 3600.
//   - UploadSessionTTLSeconds: chunked-blob-upload session lifetime used by
//     Phase 02-06. Default 3600.
type Docker struct {
	JWTTTLSeconds           int `koanf:"jwt_ttl_seconds"`
	UploadSessionTTLSeconds int `koanf:"upload_session_ttl_seconds"`
}

type ServerConfig struct {
	HTTPPort          int      `koanf:"http_port"`
	HTTPSPort         int      `koanf:"https_port"`
	Hostname          string   `koanf:"hostname"`
	ExternalHostnames []string `koanf:"external_hostnames"`
	// GitBackend selects the Git Smart-HTTP backend implementation.
	// Phase 4 Plan 03 / D-26: "gogit" (pure Go, go-git v6) is the default;
	// "gitkit" shells out to the `git` binary as the documented fallback.
	// Validator rejects any other value with a clear error.
	GitBackend string         `koanf:"git_backend"`
	Timeouts   ServerTimeouts `koanf:"timeouts"`
}

// ServerTimeouts configures defensive timeouts on the http.Server listeners.
// Slowloris/header-drip protection needs ReadHeaderTimeout; long-running
// uploads (OCI blob pushes, RPM/deb/pypi/raw uploads, Git packs, S3 PUT) mean
// ReadTimeout and WriteTimeout must stay unset/0 or they'll starve legitimate
// traffic. IdleTimeout reaps leaked keep-alive sockets.
type ServerTimeouts struct {
	ReadHeader time.Duration `koanf:"read_header"`
	Idle       time.Duration `koanf:"idle"`
	// Read/Write left unset intentionally — see struct doc.
	Read  time.Duration `koanf:"read"`
	Write time.Duration `koanf:"write"`
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
	AutoScanDefault   bool          `koanf:"auto_scan_default"`
	DBWarnAgeDays     int           `koanf:"db_warn_age_days"`
	SeverityCacheTTL  time.Duration `koanf:"severity_cache_ttl"`
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
			GitBackend:        "gogit",
			Timeouts: ServerTimeouts{
				ReadHeader: 30 * time.Second,
				Idle:       120 * time.Second,
				// Read/Write = 0 (unlimited) — artifact uploads can run minutes.
			},
		},
		Repos: ReposConfig{
			Git: GitReposConfig{
				MaxPushBytes: 524288000, // 500 MiB default per-install git push cap (D-35)
			},
		},
		DataRoot: "/var/lib/omnirepo",
		Auth: AuthConfig{
			SessionTTL:   12 * time.Hour,
			DockerJWTTTL: 60 * time.Minute,
			SigV4Skew:    15 * time.Minute,
		},
		Scan: ScanConfig{
			AutoScanDefault:  true,
			DBWarnAgeDays:    7,
			SeverityCacheTTL: 30 * time.Second,
		},
		Trivy: Trivy{
			BinaryPath: "/usr/local/bin/trivy",
			DBPath:     "/var/lib/omnirepo/trivy/db",
			// Trivy resolves its DB at <--cache-dir>/db/. CachePath must be
			// the parent of DBPath so the layout admin_trivy writes aligns
			// with what the runner reads. See TestTrivyDefaults_CachePathContainsDBSubdir.
			CachePath: "/var/lib/omnirepo/trivy",
		},
		Jobs: Jobs{
			SyncWorkers:          4,
			ScanWorkers:          2,
			PollInterval:         2 * time.Second,
			ShutdownGraceSeconds: 30,
		},
		Docker: Docker{
			JWTTTLSeconds:           3600,
			UploadSessionTTLSeconds: 3600,
		},
		GC: GC{
			TrashRetentionDays:    7,
			BlobQuiescenceSeconds: 3600,
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
		Regen: RegenConfig{
			DebounceMs: 2000,
			MaxWaitMs:  30000,
		},
		Sync: SyncConfig{
			MaxParallelDownloadsPerJob: 4,
			UpstreamHTTPTimeout:        60 * time.Second,
		},
		Signing: SigningConfig{
			GPGKeyBits: 4096,
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
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate performs post-unmarshal structural validation and applies
// fallback defaults for fields whose zero value is a sentinel meaning
// "inherit default" (e.g. max_push_bytes=0). Returns a typed error for
// the caller to surface.
//
// Phase 4 Plan 03 scope: git_backend allowed values, non-negative
// max_push_bytes, and the 0 → 500 MiB fallback.
func (cfg *Config) Validate() error {
	switch cfg.Server.GitBackend {
	case "gogit", "gitkit":
		// ok
	case "":
		cfg.Server.GitBackend = "gogit" // apply default if missing after merge
	default:
		return fmt.Errorf("config: server.git_backend %q invalid (want gogit|gitkit)", cfg.Server.GitBackend)
	}
	if cfg.Repos.Git.MaxPushBytes < 0 {
		return fmt.Errorf("config: repos.git.max_push_bytes must be >= 0 (got %d)", cfg.Repos.Git.MaxPushBytes)
	}
	if cfg.Repos.Git.MaxPushBytes == 0 {
		cfg.Repos.Git.MaxPushBytes = 524288000 // 500 MiB default (D-35)
	}
	return nil
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
